//go:build quota_pg

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	session "github.com/zhangzhe-ctrl/ani-session-gateway/api/gen/session/v1"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	api "go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Only Resource and SG protocol boundaries are controlled. Current authorization,
// tenant mapping, admission, transactions, charges, and delete fence use real Gov.
type vmPGPeer struct {
	vmResolvePeer
	id, replayKey       string
	canonical           []byte
	changes, targets    int
	currentVersion      int64
	dnsReads            int
	dnsTenant, dnsActor string
}

func (p *vmPGPeer) Get(context.Context, string, string, string) (*vm.VirtualMachine, error) {
	version := p.currentVersion
	if version == 0 {
		version = 1
	}
	return &vm.VirtualMachine{Id: p.id, Version: version}, nil
}
func (p *vmPGPeer) Read(_ context.Context, tenant, actor, action, _, _ string) (proto.Message, error) {
	if action != "dns-defaults" {
		return nil, status.Error(codes.Unimplemented, "unexpected read")
	}
	p.dnsReads++
	p.dnsTenant, p.dnsActor = tenant, actor
	return &vm.DNSDefaults{Nameservers: []string{"223.5.5.5"}}, nil
}
func (p *vmPGPeer) Delete(context.Context, string, string, *vm.AcceptDeleteRequest) (*vm.Acceptance, error) {
	return nil, status.Error(codes.Aborted, "VM_VERSION_CONFLICT")
}
func (p *vmPGPeer) Lookup(_ context.Context, _, _ string, in *vm.LookupCommandReplayRequest) (*vm.Operation, error) {
	if in.IdempotencyKey != p.replayKey {
		return nil, status.Error(codes.NotFound, "VM_NOT_FOUND")
	}
	if !bytes.Equal(in.CanonicalIntent, p.canonical) {
		return nil, status.Error(codes.AlreadyExists, "VM_IDEMPOTENCY_CONFLICT")
	}
	return &vm.Operation{Id: "11111111-1111-4111-8111-111111111111", VmId: p.id, Action: in.Action, IntentVersion: 4, State: "succeeded"}, nil
}

type vmPGSessions struct {
	key     string
	created int
}

func (p *vmPGSessions) GetSessionReplay(_ context.Context, in *session.GetSessionReplayRequest) (*session.GetSessionReplayResponse, error) {
	if in.IdempotencyKey != p.key {
		return nil, status.Error(codes.NotFound, "SESSION_NOT_FOUND")
	}
	return &session.GetSessionReplayResponse{SessionId: "original-session", ConnectUrl: "wss://console.invalid/original", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Replayed: true}, nil
}
func (p *vmPGSessions) CreateSession(context.Context, *session.CreateSessionRequest) (*session.CreateSessionResponse, error) {
	p.created++
	return nil, status.Error(codes.Internal, "unexpected new session")
}
func TestVMPostgresUnmeteredDeleteFenceAndOriginalReplay(t *testing.T) {
	t.Setenv("ANI_VM_V1_WRITES", "true")
	client := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, client)
	sys := appViewer.NewSystemViewerContext(context.Background())
	bctx := testutil.NewBootstrapContext(nil)
	fixture := uuid.NewString()
	plan, err := client.Client().Plan.Create().SetName("vm-" + fixture).Save(sys)
	require.NoError(t, err)
	tenant, err := client.Client().Tenant.Create().SetName("vm-" + fixture).SetCode("vm-" + fixture).SetPlanID(plan.ID).Save(sys)
	require.NoError(t, err)
	require.NoError(t, client.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.ModuleVirtualMachine).Exec(sys))
	operator, err := client.Client().User.Create().SetTenantID(tenant.ID).SetUsername("vm-" + fixture).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	actorRole, err := client.Client().Role.Create().SetTenantID(tenant.ID).SetName("vm-" + fixture).SetCode("tenant:vm:" + fixture).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	require.NoError(t, client.Client().UserRole.Create().SetTenantID(tenant.ID).SetUserID(operator.ID).SetRoleID(actorRole.ID).SetStatus(userrole.StatusActive).Exec(sys))
	perm, err := client.Client().Permission.Create().SetName("VM fixture").SetCode("vm:fixture:" + fixture).SetStatus(permission.StatusOn).Save(sys)
	require.NoError(t, err)
	grant, err := client.Client().RolePermission.Create().SetTenantID(tenant.ID).SetRoleID(actorRole.ID).SetPermissionID(perm.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
	require.NoError(t, err)
	for _, action := range []string{"create", "delete", "get", "operation", "start", "stop", "restart", "force-stop", "create-disk", "attach-disk", "detach-disk", "delete-disk", "set-media", "eject-media", "set-boot", "create-snapshot", "delete-snapshot", "restore-snapshot", "console"} {
		route, ok := data.VMActionRoute(action)
		require.True(t, ok)
		entry, e := client.Client().Api.Query().Where(api.PathEQ(route.Path), api.MethodEQ(route.Method), api.ModuleEQ("VMService"), api.ScopeEQ(api.ScopeAdmin)).Only(sys)
		if ent.IsNotFound(e) {
			entry, e = client.Client().Api.Create().SetModule("VMService").SetPath(route.Path).SetMethod(route.Method).SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleVirtualMachine).SetStatus(api.StatusOn).Save(sys)
		}
		require.NoError(t, e)
		require.NoError(t, client.Client().PermissionApi.Create().SetPermissionID(perm.ID).SetAPIID(entry.ID).Exec(sys))
	}
	request := viewer.WithContext(context.Background(), appViewer.NewUserViewer(uint64(operator.ID), uint64(tenant.ID), 0, "", []viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}}))
	request = auth.NewPrincipalContext(request, &auth.Principal{Type: auth.SubjectUser, ID: operator.ID, TenantID: tenant.ID})
	spec := vmFixtureSpec(t)
	peer := &vmPGPeer{vmResolvePeer: vmResolvePeer{spec: spec}}
	binding := NewVMBinding(peer, data.NewVMAuthorizationRepo(client), spec.ClusterId)
	registry := NewQuotaAdapterRegistry()
	require.NoError(t, registry.Register(NewResourceDispatchAdapter(nil, binding)))
	ledger := data.NewQuotaLedgerRepo(bctx, client)
	resolver := data.NewTenantRepo(bctx, client)
	acceptance, err := NewGpuAcceptance(ledger, registry, resolver, nil, binding, nil)
	require.NoError(t, err)
	sessions := &vmPGSessions{key: uuid.NewString()}
	service := NewVMService(acceptance, binding, resolver, ledger, sessions)
	key := uuid.NewString()
	// DNS discovery is not allowed until its exact route is cataloged and granted.
	_, err = service.GetVMDNSDefaults(request, &view.GetDNSDefaultsRequest{})
	require.Error(t, err)
	require.Zero(t, peer.dnsReads)
	dnsAPI, err := client.Client().Api.Query().Where(api.PathEQ(data.VMDNSDefaultsPath), api.MethodEQ("GET"), api.ModuleEQ("VMService"), api.ScopeEQ(api.ScopeAdmin)).Only(sys)
	if ent.IsNotFound(err) {
		dnsAPI, err = client.Client().Api.Create().SetModule("VMService").SetPath(data.VMDNSDefaultsPath).SetMethod("GET").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleVirtualMachine).SetStatus(api.StatusOn).Save(sys)
	}
	require.NoError(t, err)
	require.NoError(t, client.Client().PermissionApi.Create().SetPermissionID(perm.ID).SetAPIID(dnsAPI.ID).Exec(sys))
	defaults, err := service.GetVMDNSDefaults(request, &view.GetDNSDefaultsRequest{})
	require.NoError(t, err)
	require.Equal(t, []string{"223.5.5.5"}, defaults.Nameservers)
	require.Equal(t, tenant.ResourceTenantID, peer.dnsTenant)
	require.Equal(t, "governance:user:"+strconv.FormatUint(uint64(operator.ID), 10), peer.dnsActor)
	spec.Intent.Dns = &vm.DNSSelection{Mode: vm.DNSCustom, Nameservers: []string{"223.5.5.5", "2001:4860:4860::8888"}}
	spec.Dns = &vm.FrozenDNS{Mode: vm.DNSCustom, Nameservers: append([]string(nil), spec.Intent.Dns.Nameservers...)}
	created, err := service.CreateVM(request, &view.CreateVMRequest{Data: &view.CreateVMData{
		IdempotencyKey: key, Name: spec.Intent.Name, PresetId: spec.Intent.PresetId, SourceKind: spec.Intent.SourceKind, MediaKey: spec.Intent.MediaKey, SystemDiskSizeGb: spec.Intent.SystemDiskSizeGb, StartAfterCreate: spec.Intent.StartAfterCreate,
		Network: &view.VMNetworkSelection{VpcId: spec.Intent.Network.VpcId, SubnetId: spec.Intent.Network.SubnetId},
		Dns:     &view.VMDNSSelection{Mode: vm.DNSCustom, Nameservers: []string{"223.5.5.5", "2001:4860:4860:0:0:0:0:8888", "223.5.5.5"}},
	}})
	require.NoError(t, err)
	require.False(t, created.Replayed)
	// The public request and its canonical normalized form replay the same acceptance.
	first, err := acceptance.AcceptGpuCreate(request, key, nil, spec.Intent)
	require.NoError(t, err)
	require.True(t, first.Replayed)
	require.Equal(t, created.OperationId, first.OperationID)
	require.Equal(t, created.ResourceId, first.ResourceID)
	peer.id = first.ResourceID
	require.Empty(t, first.ChargeIDs)
	require.Equal(t, 1, peer.calls)
	require.Zero(t, client.Client().QuotaAccount.Query().CountX(sys))
	require.Zero(t, client.Client().QuotaCharge.Query().CountX(sys), "VM requires no CPU/memory/disk account")
	replay, err := acceptance.AcceptGpuCreate(request, key, nil, spec.Intent)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, first.OperationID, replay.OperationID)
	require.Equal(t, 1, peer.calls)
	changedDNS := proto.Clone(spec.Intent).(*vm.CreateVMData)
	changedDNS.Dns.Nameservers[0] = "1.1.1.1"
	_, err = acceptance.AcceptGpuCreate(request, key, nil, changedDNS)
	require.Error(t, err, "DNS changes cannot replay the original create key")
	require.Equal(t, 1, peer.calls, "conflicting intent must not resolve a new spec")
	require.NoError(t, client.Client().RolePermission.UpdateOneID(grant.ID).SetStatus(rolepermission.StatusOff).Exec(sys))
	_, err = service.GetVMDNSDefaults(request, &view.GetDNSDefaultsRequest{})
	require.Error(t, err, "DNS discovery still requires current route permission")
	require.Equal(t, 1, peer.dnsReads)
	_, err = acceptance.AcceptGpuCreate(request, key, nil, spec.Intent)
	require.Error(t, err, "replay still requires current authorization")
	require.NoError(t, client.Client().RolePermission.UpdateOneID(grant.ID).SetStatus(rolepermission.StatusOn).Exec(sys))
	pending, err := service.GetVMOperation(request, &view.GetOperationRequest{VmId: first.ResourceID, OperationId: first.OperationID})
	require.NoError(t, err)
	require.Equal(t, "queued", pending.State)
	require.Zero(t, pending.IntentVersion)
	claimed, err := ledger.ClaimDispatchable(request, "vm-pg-boundary", time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	ack, _ := json.Marshal(map[string]any{"operation_id": first.OperationID, "resource_id": first.ResourceID, "accepted": true})
	ok, err := ledger.AckDispatched(request, tenant.ID, first.OperationID, claimed[0].LeaseGeneration, string(ack))
	require.NoError(t, err)
	require.True(t, ok)
	deleteKey := uuid.NewString()
	deleted, err := acceptance.AcceptGpuDeleteVersion(request, deleteKey, first.ResourceID, 1)
	require.NoError(t, err)
	require.Equal(t, "QUEUED_FOR_OWNER", deleted.Result)
	blocked, err := ledger.HasAcceptedVMDelete(request, tenant.ID, first.ResourceID)
	require.NoError(t, err)
	require.True(t, blocked, "queued unsent DELETE is already a fence")
	originalKey := uuid.NewString()
	peer.replayKey = originalKey
	direct := &vm.ChangeVMRequest{TenantId: tenant.ResourceTenantID, VmId: first.ResourceID, IdempotencyKey: originalKey, ExpectedVersion: 1}
	peer.canonical, err = vm.CanonicalCommandIntent(direct)
	require.NoError(t, err)
	original, err := service.StartVM(request, &view.ChangeVMRequest{Data: &view.ChangeVMData{VmId: first.ResourceID, IdempotencyKey: originalKey, ExpectedVersion: 1}})
	require.NoError(t, err)
	require.Equal(t, int64(4), original.IntentVersion, "original request replay ignores current version")
	_, err = service.StartVM(request, &view.ChangeVMRequest{Data: &view.ChangeVMData{VmId: first.ResourceID, IdempotencyKey: originalKey, ExpectedVersion: 2}})
	require.Error(t, err, "changed expected_version is a conflicting intent")
	for _, action := range []string{"start", "stop", "restart", "force-stop", "create-disk", "attach-disk", "detach-disk", "delete-disk", "set-media", "eject-media", "set-boot", "create-snapshot", "delete-snapshot", "restore-snapshot"} {
		_, err = service.change(request, &view.ChangeVMRequest{Data: &view.ChangeVMData{VmId: first.ResourceID, IdempotencyKey: uuid.NewString(), ExpectedVersion: 1}}, action)
		require.Error(t, err, action)
	}
	console, err := service.CreateVMConsoleSession(request, &view.ChangeVMRequest{Data: &view.ChangeVMData{VmId: first.ResourceID, IdempotencyKey: sessions.key, ExpectedVersion: 1}})
	require.NoError(t, err)
	require.Equal(t, "original-session", console.SessionId)
	_, err = service.CreateVMConsoleSession(request, &view.ChangeVMRequest{Data: &view.ChangeVMData{VmId: first.ResourceID, IdempotencyKey: uuid.NewString(), ExpectedVersion: 1}})
	require.Error(t, err)
	require.Zero(t, sessions.created)
	again, err := acceptance.AcceptGpuDeleteVersion(request, deleteKey, first.ResourceID, 1)
	require.NoError(t, err)
	require.Equal(t, deleted.DeleteOperationId, again.DeleteOperationId)
	bffReplay, err := service.DeleteVM(request, &view.ChangeVMRequest{Data: &view.ChangeVMData{VmId: first.ResourceID, IdempotencyKey: deleteKey, ExpectedVersion: 1}})
	require.NoError(t, err)
	require.True(t, bffReplay.Replayed)
	require.Equal(t, deleted.DeleteOperationId, bffReplay.OperationId)
	_, err = acceptance.AcceptGpuDeleteVersion(request, deleteKey, first.ResourceID, 2)
	require.Error(t, err)
	deletion, err := ledger.GetOperationForUser(request, tenant.ID, deleted.DeleteOperationId)
	require.NoError(t, err)
	require.Equal(t, uint32(1), deletion.InstanceContractVersion, "VM uses only legacy placeholder")
	require.Equal(t, commandcontract.VMDeleteAction, deletion.Action)
	require.Equal(t, strconv.FormatUint(uint64(operator.ID), 10), deletion.ActorID)
	// An in-flight request can win Resource's version before a queued DELETE.
	// Only an explicit pre-admission rejection terminates that delete fence.
	claimed, err = ledger.ClaimDispatchable(request, "vm-delete-version", time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	NewQuotaDispatchWorker(bctx, ledger, registry).dispatchOne(request, &claimed[0])
	failed, err := service.GetVMOperation(request, &view.GetOperationRequest{VmId: first.ResourceID, OperationId: deleted.DeleteOperationId})
	require.NoError(t, err)
	require.Equal(t, "failed", failed.State)
	require.Equal(t, "VM_VERSION_CONFLICT", failed.Reason)
	blocked, err = ledger.HasAcceptedVMDelete(request, tenant.ID, first.ResourceID)
	require.NoError(t, err)
	require.False(t, blocked)
	rejected, err := acceptance.AcceptGpuDeleteVersion(request, deleteKey, first.ResourceID, 1)
	require.NoError(t, err)
	require.Equal(t, deleted.DeleteOperationId, rejected.DeleteOperationId)
	require.Equal(t, "REJECTED_VERSION_CONFLICT", rejected.Result)
	peer.currentVersion = 2
	var wait sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := acceptance.AcceptGpuDeleteVersion(request, uuid.NewString(), first.ResourceID, 2)
			outcomes <- err
		}()
	}
	wait.Wait()
	close(outcomes)
	success := 0
	conflict := 0
	for outcome := range outcomes {
		if outcome == nil {
			success++
		} else {
			conflict++
		}
	}
	require.Equal(t, 1, success, "one active VM DELETE under the tenant transaction lock")
	require.Equal(t, 1, conflict)
	originalDelete, err := ledger.GetOperationForUser(request, tenant.ID, deleted.DeleteOperationId)
	require.NoError(t, err)
	frozenDelete, err := commandcontract.DecodeVMCanonical([]byte(originalDelete.CanonicalRequest))
	require.NoError(t, err)
	require.Equal(t, int64(1), frozenDelete.DeleteExpectedVersion, "rejection never overwrites the original version")
	require.Zero(t, client.Client().QuotaAccount.Query().CountX(sys))
	require.Zero(t, client.Client().QuotaCharge.Query().CountX(sys))
	require.NoError(t, client.Client().Close())
	testVMGuestDurableRestartPostgres(t, request)
}
