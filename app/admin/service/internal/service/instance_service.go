package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"strconv"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	terminal "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/terminal/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	sessionpb "github.com/zhangzhe-ctrl/ani-session-gateway/api/gen/session/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	quota "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const InstanceCreateAction = "/instance.v1.InstanceService/AcceptCreate"
const InstanceDeleteAction = "/instance.v1.InstanceService/AcceptDelete"
const InstanceOwner = "ani-resource-service"

type InstanceOwnerClient interface {
	Resolve(context.Context, string, string, *res.CreateInstanceData) (*res.CreateBusiness, error)
	Create(context.Context, *res.AcceptCreateRequest) (*res.OperationResponse, error)
	Delete(context.Context, *res.AcceptDeleteRequest) (*res.OperationResponse, error)
	Change(context.Context, string, string, *res.ChangeInstanceRequest, bool) (*res.OperationResponse, error)
	Get(context.Context, string, string, string) (*res.Instance, error)
	List(context.Context, string, string, int32, string) (*res.ListInstancesResponse, error)
	Operation(context.Context, string, string, string) (*res.Operation, error)
	Presets(context.Context, string, string) (*res.ListPresetsResponse, error)
	Target(context.Context, string, string, string, int64) (*terminal.TerminalTarget, error)
}
type InstanceActionAuthorizer interface {
	Authorize(context.Context, uint32, uint32, string, string) error
}
type InstanceGpuBinding struct {
	client        InstanceOwnerClient
	authorization InstanceActionAuthorizer
}

func NewInstanceGpuBinding(c InstanceOwnerClient, a InstanceActionAuthorizer) *InstanceGpuBinding {
	return &InstanceGpuBinding{c, a}
}
func (*InstanceGpuBinding) OwnerService() string { return InstanceOwner }
func (*InstanceGpuBinding) CreateAction() string { return InstanceCreateAction }
func (*InstanceGpuBinding) DeleteAction() string { return InstanceDeleteAction }
func (*InstanceGpuBinding) Actions() []string {
	return []string{InstanceCreateAction, InstanceDeleteAction}
}
func (*InstanceGpuBinding) GpuQuotaCodes() []string {
	return []string{GpuSharedQuotaCode, GpuPhysicalQuotaCode}
}
func (b *InstanceGpuBinding) AuthorizeGpu(ctx context.Context, p *auth.Principal, action, resource string) error {
	path, method := data.InstanceCreatePath, "POST"
	if action == InstanceDeleteAction {
		path, method = data.InstanceDeletePath, "DELETE"
	} else if action != InstanceCreateAction {
		return kratoserrors.Forbidden("FORBIDDEN", "instance action denied")
	}
	if p == nil || b.authorization == nil {
		return kratoserrors.Forbidden("FORBIDDEN", "instance action denied")
	}
	return instanceAuthorizationError(b.authorization.Authorize(ctx, p.TenantID, p.ID, path, method))
}
func instanceAuthorizationError(e error) error {
	if errors.Is(e, data.ErrInferenceAuthorizationDenied) {
		return kratoserrors.Forbidden("FORBIDDEN", "instance action denied")
	}
	if errors.Is(e, data.ErrInferenceAuthorizationUnavailable) {
		return kratoserrors.ServiceUnavailable("INSTANCE_AUTHORIZATION_UNAVAILABLE", "instance authorization unavailable")
	}
	return e
}
func (*InstanceGpuBinding) CanonicalGpuBusiness(m proto.Message) ([]byte, error) {
	return res.CanonicalBusinessPayload(m)
}
func (b *InstanceGpuBinding) ResolveGpuBusiness(ctx context.Context, p *auth.Principal, tenant string, m proto.Message) (*acc.GpuRequest, proto.Message, error) {
	in, ok := m.(*res.CreateInstanceData)
	if !ok || in == nil || b.client == nil {
		return nil, nil, quota.ErrorInvalidQuotaRequest("%s", "instance public data required")
	}
	resolved, e := b.client.Resolve(ctx, tenant, "governance:user:"+strconv.FormatUint(uint64(p.ID), 10), in)
	if e != nil {
		return nil, nil, e
	}
	if resolved == nil || resolved.Data == nil || resolved.Spec == nil || resolved.Gpu == nil || !proto.Equal(in, resolved.Data) {
		return nil, nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance resolution")
	}
	return resolved.Gpu, resolved, nil
}
func (*InstanceGpuBinding) ValidateGpuBusiness(_ context.Context, m proto.Message) ([]data.QuotaOccupyItem, error) {
	b, ok := m.(*res.CreateBusiness)
	if !ok || b == nil || b.Data == nil || b.Spec == nil || b.Gpu == nil || b.Gpu.Replicas != 1 || b.Gpu.DevicesPerReplica != 1 || b.Gpu.ContainerName != "workspace" || b.Spec.ClusterId != b.Gpu.ClusterId || b.Spec.Namespace == "" || b.Spec.ImageDigest == "" || b.Spec.ImageReference == "" {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid frozen instance business")
	}
	return nil, nil
}
func (b *InstanceGpuBinding) ValidateGpuDelete(ctx context.Context, p *auth.Principal, tenant, id string, version int64) error {
	if b.client == nil {
		return kratoserrors.ServiceUnavailable("INSTANCE_ADAPTER_UNAVAILABLE", "instance owner unavailable")
	}
	current, e := b.client.Get(ctx, tenant, "governance:user:"+strconv.FormatUint(uint64(p.ID), 10), id)
	if status.Code(e) == codes.NotFound && version == 1 {
		return nil
	}
	if e != nil {
		return e
	}
	if current == nil || current.Version != version {
		return kratoserrors.Conflict("INSTANCE_VERSION_CONFLICT", "instance version changed")
	}
	return nil
}
func (b *InstanceGpuBinding) Dispatch(ctx context.Context, c *QuotaDispatchCommand) ([]byte, error) {
	if b.client == nil || c == nil {
		return nil, quota.ErrorQuotaAdapterUnavailable("%s", "instance owner unavailable")
	}
	create, del, e := BuildGpuOwnerAttachments(InstanceOwner, c)
	if e != nil {
		return nil, e
	}
	frozen, e := DecodeGpuCanonical(c.CanonicalRequest)
	if e != nil {
		return nil, e
	}
	charges := []*res.OriginalQuotaCharge{}
	for _, q := range c.Charges {
		charges = append(charges, &res.OriginalQuotaCharge{ChargeId: q.ChargeID, QuotaCode: q.QuotaCode, OriginalUnits: q.ChargedUnits})
	}
	var reply *res.OperationResponse
	switch c.Action {
	case InstanceCreateAction:
		business, e := res.DecodeBusinessPayload(frozen.BusinessPayload)
		if e != nil {
			return nil, e
		}
		digest, e := res.BusinessPayloadDigest(business)
		if e != nil || digest != frozen.BusinessPayloadDigest || create == nil || del != nil || !proto.Equal(business.Gpu, frozen.GpuRequest) {
			return nil, quota.ErrorInvalidQuotaRequest("%s", "instance frozen payload mismatch")
		}
		reply, e = b.client.Create(ctx, &res.AcceptCreateRequest{TenantId: c.ResourceTenantID, OperationId: c.OperationID, Business: business, GpuOwnerAttachment: create, OriginalCharges: charges})
		if e != nil {
			return nil, e
		}
	case InstanceDeleteAction:
		if del == nil || create != nil || frozen.DeleteExpectedVersion < 1 {
			return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance delete")
		}
		reply, e = b.client.Delete(ctx, &res.AcceptDeleteRequest{TenantId: c.ResourceTenantID, OperationId: c.OperationID, GpuOwnerAttachment: del, OriginalCharges: charges, ExpectedVersion: frozen.DeleteExpectedVersion})
		if e != nil {
			return nil, e
		}
	default:
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance action")
	}
	if reply == nil || reply.DurableOwnerAck == nil || reply.DurableOwnerAck.OperationId != c.OperationID || reply.DurableOwnerAck.ResourceId != c.ResourceID || !reply.DurableOwnerAck.Accepted {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance durable ACK")
	}
	raw, e := json.Marshal(map[string]any{"operation_id": c.OperationID, "resource_id": c.ResourceID, "accepted": true})
	if e == nil {
		e = ValidateDurableOwnerAck(c, raw)
	}
	return raw, e
}

type InstanceSessionClient interface {
	CreateSession(context.Context, *sessionpb.CreateSessionRequest) (*sessionpb.CreateSessionResponse, error)
}
type InstanceService struct {
	admin.UnimplementedInstanceServiceServer
	acceptance *GpuAcceptance
	binding    *InstanceGpuBinding
	resolver   ResourceTenantResolver
	ledger     *data.QuotaLedgerRepo
	sessions   InstanceSessionClient
}

func NewInstanceService(a *GpuAcceptance, b *InstanceGpuBinding, r ResourceTenantResolver, l *data.QuotaLedgerRepo, sessions InstanceSessionClient) *InstanceService {
	return &InstanceService{acceptance: a, binding: b, resolver: r, ledger: l, sessions: sessions}
}
func (s *InstanceService) scope(ctx context.Context, path, method string) (string, string, error) {
	p, e := gpuPrincipal(ctx)
	if e != nil {
		return "", "", e
	}
	if s.binding == nil || s.binding.authorization == nil {
		return "", "", kratoserrors.Forbidden("FORBIDDEN", "instance action denied")
	}
	if e = instanceAuthorizationError(s.binding.authorization.Authorize(ctx, p.TenantID, p.ID, path, method)); e != nil {
		return "", "", e
	}
	if s.binding.client == nil {
		return "", "", kratoserrors.ServiceUnavailable("INSTANCE_ADAPTER_UNAVAILABLE", "instance owner unavailable")
	}
	tenant, e := s.resolver.ResourceTenantID(ctx, p.TenantID)
	return tenant, "governance:user:" + strconv.FormatUint(uint64(p.ID), 10), e
}
func (s *InstanceService) CreateInstance(ctx context.Context, r *view.CreateInstanceRequest) (*view.Acceptance, error) {
	if r == nil || r.Data == nil || s.acceptance == nil {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "instance data required")
	}
	d := r.Data
	result, e := s.acceptance.AcceptGpuCreate(ctx, d.IdempotencyKey, nil, &res.CreateInstanceData{Name: d.Name, ImageId: d.ImageId, ImageScope: d.ImageScope, PresetId: d.PresetId, PresetVersion: d.PresetVersion})
	if e != nil {
		return nil, e
	}
	return &view.Acceptance{OperationId: result.OperationID, ResourceId: result.ResourceID, Replayed: result.Replayed, Result: "ACCEPTED"}, nil
}
func (s *InstanceService) DeleteInstance(ctx context.Context, r *view.ChangeInstanceRequest) (*view.Acceptance, error) {
	if r == nil || r.Data == nil || r.Data.ExpectedVersion < 1 {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "instance delete data required")
	}
	accepted, e := s.acceptance.AcceptGpuDeleteVersion(ctx, r.Data.IdempotencyKey, r.Data.InstanceId, r.Data.ExpectedVersion)
	if e != nil {
		return nil, e
	}
	return &view.Acceptance{OperationId: accepted.DeleteOperationId, ResourceId: accepted.ResourceId, Result: accepted.Result}, nil
}
func (s *InstanceService) canOperate(ctx context.Context, id string) error {
	p, e := gpuPrincipal(ctx)
	if e != nil {
		return e
	}
	op, e := s.ledger.GetCreateOperationByResource(ctx, p.TenantID, InstanceOwner, id)
	if e != nil {
		return e
	}
	_, _, hasDelete, e := s.ledger.LoadGpuProjectionSource(ctx, p.TenantID, op.OperationID)
	if e != nil {
		return e
	}
	if hasDelete {
		return kratoserrors.Conflict("INSTANCE_DELETING", "instance final deletion already accepted")
	}
	return nil
}
func (s *InstanceService) change(ctx context.Context, r *view.ChangeInstanceRequest, start bool) (*view.Acceptance, error) {
	if r == nil || r.Data == nil {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "instance data required")
	}
	path := data.InstanceStopPath
	if start {
		path = data.InstanceStartPath
	}
	tenant, actor, e := s.scope(ctx, path, "POST")
	if e != nil {
		return nil, e
	}
	d := r.Data
	if e = s.canOperate(ctx, d.InstanceId); e != nil {
		return nil, e
	}
	reply, e := s.binding.client.Change(ctx, tenant, actor, &res.ChangeInstanceRequest{TenantId: tenant, InstanceId: d.InstanceId, IdempotencyKey: d.IdempotencyKey, ExpectedVersion: d.ExpectedVersion}, start)
	if e != nil {
		return nil, e
	}
	if reply == nil || reply.DurableOwnerAck == nil || !reply.DurableOwnerAck.Accepted || reply.DurableOwnerAck.ResourceId != d.InstanceId {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance ACK")
	}
	return &view.Acceptance{OperationId: reply.DurableOwnerAck.OperationId, ResourceId: d.InstanceId, Result: "ACCEPTED"}, nil
}
func (s *InstanceService) StartInstance(ctx context.Context, r *view.ChangeInstanceRequest) (*view.Acceptance, error) {
	return s.change(ctx, r, true)
}
func (s *InstanceService) StopInstance(ctx context.Context, r *view.ChangeInstanceRequest) (*view.Acceptance, error) {
	return s.change(ctx, r, false)
}
func (s *InstanceService) GetInstance(ctx context.Context, r *view.GetInstanceRequest) (*view.Instance, error) {
	if r == nil {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "request required")
	}
	tenant, actor, e := s.scope(ctx, data.InstanceGetPath, "GET")
	if e != nil {
		return nil, e
	}
	i, e := s.binding.client.Get(ctx, tenant, actor, r.InstanceId)
	if e != nil {
		return nil, e
	}
	out := new(view.Instance)
	raw, e := protojson.Marshal(i)
	if e == nil {
		e = protojson.Unmarshal(raw, out)
	}
	return out, e
}
func (s *InstanceService) ListInstances(ctx context.Context, r *view.ListInstancesRequest) (*view.ListInstancesResponse, error) {
	if r == nil {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "request required")
	}
	tenant, actor, e := s.scope(ctx, data.InstanceCreatePath, "GET")
	if e != nil {
		return nil, e
	}
	items, e := s.binding.client.List(ctx, tenant, actor, r.PageSize, r.AfterId)
	if e != nil {
		return nil, e
	}
	out := new(view.ListInstancesResponse)
	raw, e := protojson.Marshal(items)
	if e == nil {
		e = protojson.Unmarshal(raw, out)
	}
	return out, e
}
func (s *InstanceService) GetInstanceOperation(ctx context.Context, r *view.GetOperationRequest) (*view.Operation, error) {
	if r == nil {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "request required")
	}
	tenant, actor, e := s.scope(ctx, data.InstanceOperationPath, "GET")
	if e != nil {
		return nil, e
	}
	op, e := s.binding.client.Operation(ctx, tenant, actor, r.OperationId)
	if e != nil {
		return nil, e
	}
	out := new(view.Operation)
	raw, e := protojson.Marshal(op)
	if e == nil {
		e = protojson.Unmarshal(raw, out)
	}
	return out, e
}
func (s *InstanceService) ListInstancePresets(ctx context.Context, r *view.ListPresetsRequest) (*view.ListPresetsResponse, error) {
	tenant, actor, e := s.scope(ctx, data.InstancePresetPath, "GET")
	if e != nil {
		return nil, e
	}
	items, e := s.binding.client.Presets(ctx, tenant, actor)
	if e != nil {
		return nil, e
	}
	out := new(view.ListPresetsResponse)
	for _, p := range items.Items {
		out.Items = append(out.Items, &view.Preset{Id: p.Id, Version: p.Version, Name: p.Name, Cpu: p.Cpu, Memory: p.Memory})
	}
	return out, nil
}
func (s *InstanceService) ExecInstance(ctx context.Context, r *view.ExecInstanceRequest) (*view.ExecInstanceResponse, error) {
	if r == nil || r.Data == nil {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "terminal data required")
	}
	d := r.Data
	if _, e := uuid.Parse(d.IdempotencyKey); e != nil || d.ExpectedVersion < 1 || d.Rows < 1 || d.Rows > 4096 || d.Cols < 1 || d.Cols > 4096 {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid terminal request")
	}
	tenant, actor, e := s.scope(ctx, data.InstanceExecPath, "POST")
	if e != nil {
		return nil, e
	}
	if e = s.canOperate(ctx, d.InstanceId); e != nil {
		return nil, e
	}
	if s.sessions == nil {
		return nil, kratoserrors.ServiceUnavailable("INSTANCE_SESSION_UNAVAILABLE", "instance terminal unavailable")
	}
	t, e := s.binding.client.Target(ctx, tenant, actor, d.InstanceId, d.ExpectedVersion)
	if e != nil {
		return nil, e
	}
	if t == nil || t.TenantId != tenant || t.InstanceId != d.InstanceId || t.Version != d.ExpectedVersion || t.PodUid == "" || t.NotebookUid == "" || t.Shell == "" || t.ContainerName != "workspace" {
		return nil, kratoserrors.Conflict("INSTANCE_TERMINAL_NOT_READY", "instance terminal not ready")
	}
	reply, e := s.sessions.CreateSession(ctx, &sessionpb.CreateSessionRequest{RequestId: uuid.NewString(), IdempotencyKey: d.IdempotencyKey, Principal: &sessionpb.Principal{TenantId: tenant, SubjectId: actor}, Target: &sessionpb.Target{InstanceId: d.InstanceId, WorkloadName: t.NotebookName, WorkloadKind: sessionpb.WorkloadKind_WORKLOAD_KIND_GPU_CONTAINER, InstanceBinding: &sessionpb.InstanceBinding{Version: t.Version, ClusterId: t.ClusterId, Namespace: t.Namespace, NotebookName: t.NotebookName, NotebookUid: t.NotebookUid, PodName: t.PodName, PodUid: t.PodUid, ContainerName: t.ContainerName, Shell: t.Shell}}, Mode: &sessionpb.CreateSessionRequest_Exec{Exec: &sessionpb.ExecOptions{Container: t.ContainerName, Command: []string{t.Shell}, Tty: true, Rows: d.Rows, Cols: d.Cols}}})
	if e != nil {
		return nil, e
	}
	if reply == nil || reply.SessionId == "" || reply.ConnectUrl == "" || reply.ExpiresAt == nil {
		return nil, kratoserrors.ServiceUnavailable("INSTANCE_SESSION_UNAVAILABLE", "invalid terminal reply")
	}
	return &view.ExecInstanceResponse{SessionId: reply.SessionId, WsUrl: reply.ConnectUrl, ExpiresAt: reply.ExpiresAt.AsTime().UTC().Format(time.RFC3339Nano)}, nil
}
