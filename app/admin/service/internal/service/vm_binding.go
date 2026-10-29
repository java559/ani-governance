package service

import (
	"bytes"
	"context"
	"encoding/json"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	console "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/console/v1"
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data"
	appcrypto "go-wind-admin/pkg/crypto"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"os"
	"strconv"
)

type VMOwnerClient interface {
	Resolve(context.Context, string, string, *vm.CreateVMData) (*vm.ResolveCreateSpecResponse, error)
	Create(context.Context, string, string, *vm.AcceptCreateRequest) (*vm.Acceptance, error)
	Delete(context.Context, string, string, *vm.AcceptDeleteRequest) (*vm.Acceptance, error)
	Get(context.Context, string, string, string) (*vm.VirtualMachine, error)
	List(context.Context, string, string, uint32, uint32) (*vm.ListVMsResponse, error)
	Operation(context.Context, string, string, string, string) (*vm.Operation, error)
	Lookup(context.Context, string, string, *vm.LookupCommandReplayRequest) (*vm.Operation, error)
	Change(context.Context, string, string, string, proto.Message) (*vm.Operation, error)
	Read(context.Context, string, string, string, string, string) (proto.Message, error)
	Target(context.Context, string, string, string, int64) (*console.VMConsoleBinding, error)
	Logs(context.Context, string, string, string, uint32, bool) (grpc.ServerStreamingClient[vm.VMLogFrame], error)
}
type VMBinding struct {
	client           VMOwnerClient
	authorization    InstanceActionAuthorizer
	cluster          string
	newCreateEnabled bool
	guestCipher      *appcrypto.VMGuestCipher
}

func NewVMBinding(c VMOwnerClient, a InstanceActionAuthorizer, cluster string, guestCipher ...*appcrypto.VMGuestCipher) *VMBinding {
	b := &VMBinding{client: c, authorization: a, cluster: cluster, newCreateEnabled: os.Getenv("ANI_VM_V1_WRITES") == "true"}
	if len(guestCipher) > 0 {
		b.guestCipher = guestCipher[0]
	}
	return b
}
func (*VMBinding) OwnerService() string { return InstanceOwner }
func (*VMBinding) CreateAction() string { return commandcontract.VMCreateAction }
func (*VMBinding) DeleteAction() string { return commandcontract.VMDeleteAction }
func (*VMBinding) Actions() []string {
	return []string{commandcontract.VMCreateAction, commandcontract.VMDeleteAction}
}
func (*VMBinding) GpuQuotaCodes() []string  { return nil }
func (b *VMBinding) NewCreateEnabled() bool { return b != nil && b.newCreateEnabled }
func (b *VMBinding) AuthorizeGpu(ctx context.Context, p *auth.Principal, action, resource string) error {
	if p == nil || b == nil || b.authorization == nil {
		return kratoserrors.Forbidden("VM_SCOPE_DENIED", "VM action denied")
	}
	routeName := "create"
	if action == commandcontract.VMDeleteAction {
		routeName = "delete"
	} else if action != commandcontract.VMCreateAction {
		return commandcontract.Invalid("unsupported VM action")
	}
	route, _ := data.VMActionRoute(routeName)
	return vmAuthorizationError(b.authorization.Authorize(ctx, p.TenantID, p.ID, route.Path, route.Method))
}
func vmAuthorizationError(err error) error {
	if err == data.ErrActionAuthorizationDenied {
		return kratoserrors.Forbidden("VM_SCOPE_DENIED", "VM action denied")
	}
	if err == data.ErrActionAuthorizationUnavailable {
		return kratoserrors.ServiceUnavailable("VM_AUTHORIZATION_UNAVAILABLE", "VM authorization unavailable")
	}
	return err
}
func (*VMBinding) ValidateGpuBusiness(_ context.Context, m proto.Message) ([]data.QuotaOccupyItem, error) {
	spec, ok := m.(*vm.CreateSpec)
	if !ok || vm.ValidateCreateSpec(spec) != nil {
		return nil, commandcontract.Invalid("invalid VM create spec")
	}
	return []data.QuotaOccupyItem{}, nil
}
func (*VMBinding) IntentHash(tenant, actorType, actorID string, m proto.Message) (string, error) {
	intent, ok := m.(*vm.CreateVMData)
	if !ok {
		return "", commandcontract.Invalid("invalid VM intent")
	}
	raw, err := vm.CanonicalUserIntent(intent)
	if err != nil {
		return "", err
	}
	return gpuHash(map[string]any{"owner": InstanceOwner, "action": commandcontract.VMCreateAction, "tenant_id": tenant, "actor_type": actorType, "actor_id": actorID, "intent": json.RawMessage(raw)})
}
func (b *VMBinding) PrepareManagedBusiness(ctx context.Context, p *auth.Principal, tenant string, m proto.Message) (*preparedManagedBusiness, error) {
	intent, ok := m.(*vm.CreateVMData)
	if !ok || vm.ValidateUserIntent(intent) != nil {
		return nil, commandcontract.Invalid("invalid VM intent")
	}
	if b.client == nil || b.cluster == "" {
		return nil, kratoserrors.ServiceUnavailable("VM_ADAPTER_UNAVAILABLE", "VM owner unavailable")
	}
	resolved, err := b.client.Resolve(ctx, tenant, "governance:user:"+strconv.FormatUint(uint64(p.ID), 10), intent)
	if err != nil {
		return nil, err
	}
	if resolved == nil || resolved.VmContractVersion != vm.ContractVersion || resolved.CreateSpec == nil || resolved.CreateSpec.ClusterId != b.cluster || !proto.Equal(resolved.CreateSpec.Intent, intent) {
		return nil, commandcontract.Invalid("invalid VM resolution")
	}
	digest, err := vm.CreateSpecDigest(resolved.CreateSpec)
	if err != nil || digest != resolved.VmCreateDigest || resolved.SelectionDigest != resolved.CreateSpec.Compute.SelectionDigest {
		return nil, commandcontract.Invalid("VM resolution digest mismatch")
	}
	spec, err := vm.CanonicalCreateSpec(resolved.CreateSpec)
	if err != nil {
		return nil, err
	}
	credentials, err := b.guestCredentials(ctx, resolved.CreateSpec)
	if err != nil {
		return nil, err
	}
	var frozenCredentialsCommand, frozenCredentialBinding []byte
	return &preparedManagedBusiness{instanceContractVersion: 0, freeze: func(tenant, actorType, actorID, operationID, resourceID string, plan *acc.ResolvedGpuRequest) ([]byte, error) {
		if plan != nil {
			return nil, commandcontract.Invalid("unexpected VM charge plan")
		}
		command := &commandcontract.VMCanonical{VMContractVersion: vm.ContractVersion, CreateSpec: spec}
		if len(credentials) > 0 {
			binding, e := vmGuestCredentialBinding(tenant, actorType, actorID, operationID, resourceID, digest)
			if e != nil {
				return nil, e
			}
			if len(frozenCredentialsCommand) > 0 {
				if !bytes.Equal(binding, frozenCredentialBinding) {
					return nil, commandcontract.Invalid("VM guest credential identity changed")
				}
				return bytes.Clone(frozenCredentialsCommand), nil
			}
			defer clear(credentials)
			command.GuestCredentialsCiphertext, e = b.guestCipher.Seal(binding, credentials)
			if e != nil {
				return nil, commandcontract.Invalid("VM guest credential encryption unavailable")
			}
			raw, e := commandcontract.MarshalVMCanonical(command)
			if e != nil {
				return nil, e
			}
			frozenCredentialsCommand = bytes.Clone(raw)
			frozenCredentialBinding = bytes.Clone(binding)
			return raw, nil
		}
		return commandcontract.MarshalVMCanonical(command)
	}}, nil
}
func (b *VMBinding) ValidateGpuDelete(ctx context.Context, p *auth.Principal, tenant, id string, version int64) error {
	if b.client == nil {
		return kratoserrors.ServiceUnavailable("VM_ADAPTER_UNAVAILABLE", "VM owner unavailable")
	}
	current, err := b.client.Get(ctx, tenant, "governance:user:"+strconv.FormatUint(uint64(p.ID), 10), id)
	if status.Code(err) == codes.NotFound && version == 1 {
		return nil
	}
	if err != nil {
		return err
	}
	if current == nil || current.Id != id || current.Version != version {
		return kratoserrors.Conflict("VM_VERSION_CONFLICT", "VM version changed")
	}
	return nil
}
func (b *VMBinding) Dispatch(ctx context.Context, c *QuotaDispatchCommand) ([]byte, error) {
	if b == nil || b.client == nil || c == nil {
		return nil, commandcontract.Invalid("VM owner unavailable")
	}
	facts, err := validateDispatchCommand(InstanceOwner, c)
	if err != nil {
		return nil, err
	}
	if facts.VM == nil || len(c.Charges) != 0 {
		return nil, commandcontract.Invalid("invalid VM command")
	}
	spec, err := vm.DecodeCreateSpec(facts.VM.CreateSpec)
	if err != nil {
		return nil, err
	}
	actor := "governance:" + c.Actor.Type + ":" + c.Actor.ID
	cc := &vm.CommandContext{TenantId: c.ResourceTenantID, ResourceId: c.ResourceID, OperationId: c.OperationID}
	var reply *vm.Acceptance
	switch c.Action {
	case commandcontract.VMCreateAction:
		cc.RequestDigest, err = vm.RequestDigest(spec.Intent)
		if err != nil {
			return nil, err
		}
		digest, e := vm.CreateSpecDigest(spec)
		if e != nil {
			return nil, e
		}
		request := &vm.AcceptCreateRequest{Context: cc, VmContractVersion: vm.ContractVersion, CreateSpec: spec, VmCreateDigest: digest}
		if facts.VM.GuestCredentialsCiphertext != "" {
			binding, e := vmGuestCredentialBinding(c.ResourceTenantID, c.Actor.Type, c.Actor.ID, c.OperationID, c.ResourceID, digest)
			if e != nil {
				return nil, e
			}
			raw, e := b.guestCipher.Open(binding, facts.VM.GuestCredentialsCiphertext)
			if e != nil {
				return nil, commandcontract.Invalid("VM guest credential envelope unavailable")
			}
			defer clear(raw)
			credentials, e := decodeVMGuestCredentials(raw)
			if e != nil {
				return nil, e
			}
			if e = vm.ValidateGuestCredentials(spec, credentials); e != nil {
				return nil, e
			}
			request.GuestCredentials = credentials
		}
		reply, err = b.client.Create(ctx, c.ResourceTenantID, actor, request)
	case commandcontract.VMDeleteAction:
		request := &vm.AcceptDeleteRequest{Context: cc, CreateOperationId: c.CreateOperationID, ExpectedVersion: facts.VM.DeleteExpectedVersion, VmContractVersion: vm.ContractVersion}
		cc.RequestDigest, err = vm.DeleteRequestDigest(request)
		if err != nil {
			return nil, err
		}
		reply, err = b.client.Delete(ctx, c.ResourceTenantID, actor, request)
	default:
		return nil, commandcontract.Invalid("unsupported VM action")
	}
	if err != nil {
		return nil, err
	}
	if reply == nil || !reply.Accepted || reply.OperationId != c.OperationID || reply.ResourceId != c.ResourceID {
		return nil, commandcontract.Invalid("invalid VM durable ACK")
	}
	raw, err := json.Marshal(map[string]any{"operation_id": reply.OperationId, "resource_id": reply.ResourceId, "accepted": true})
	if err != nil {
		return nil, err
	}
	if err = ValidateDurableOwnerAck(c, raw); err != nil {
		return nil, err
	}
	return bytes.Clone(raw), nil
}
