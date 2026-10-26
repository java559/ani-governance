package service

import (
	"context"
	"encoding/json"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	session "github.com/zhangzhe-ctrl/ani-session-gateway/api/gen/session/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"strconv"
	"strings"
	"time"
)

type VMSessionClient interface {
	CreateSession(context.Context, *session.CreateSessionRequest) (*session.CreateSessionResponse, error)
	GetSessionReplay(context.Context, *session.GetSessionReplayRequest) (*session.GetSessionReplayResponse, error)
}
type VMService struct {
	admin.UnimplementedVMServiceServer
	acceptance *GpuAcceptance
	binding    *VMBinding
	resolver   ResourceTenantResolver
	ledger     *data.QuotaLedgerRepo
	sessions   VMSessionClient
}

func NewVMService(a *GpuAcceptance, b *VMBinding, r ResourceTenantResolver, l *data.QuotaLedgerRepo, s VMSessionClient) *VMService {
	return &VMService{acceptance: a, binding: b, resolver: r, ledger: l, sessions: s}
}
func (s *VMService) scope(ctx context.Context, action string) (string, string, error) {
	p, err := gpuPrincipal(ctx)
	if err != nil {
		return "", "", err
	}
	route, ok := data.VMActionRoute(action)
	if !ok || s.binding == nil || s.binding.authorization == nil {
		return "", "", kratoserrors.Forbidden("VM_SCOPE_DENIED", "VM action denied")
	}
	if err = vmAuthorizationError(s.binding.authorization.Authorize(ctx, p.TenantID, p.ID, route.Path, route.Method)); err != nil {
		return "", "", err
	}
	if s.binding.client == nil || s.resolver == nil || s.ledger == nil {
		return "", "", kratoserrors.ServiceUnavailable("VM_ADAPTER_UNAVAILABLE", "VM owner unavailable")
	}
	tenant, err := s.resolver.ResourceTenantID(ctx, p.TenantID)
	return tenant, "governance:user:" + strconv.FormatUint(uint64(p.ID), 10), err
}
func vmInvalid() error { return kratoserrors.BadRequest("VM_INVALID_REQUEST", "invalid VM request") }
func vmNotFound(err error) bool {
	return status.Code(err) == codes.NotFound || kratoserrors.Code(err) == 404
}
func vmWire(in, out proto.Message) error {
	if in == nil || !in.ProtoReflect().IsValid() {
		return kratoserrors.ServiceUnavailable("VM_INVALID_RESPONSE", "VM owner returned no result")
	}
	raw, err := protojson.Marshal(in)
	if err != nil {
		return err
	}
	// Read-only BFF DTOs intentionally omit source PVCs and trusted native fields.
	return (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, out)
}
func vmSummary(spec *vm.CreateSpec, out *view.VirtualMachine) {
	if spec == nil || out == nil {
		return
	}
	if spec.Compute != nil {
		out.PresetId = spec.Compute.PresetId
		out.CpuMillis = spec.Compute.CpuMillis
		out.MemoryMib = spec.Compute.MemoryMib
	}
	if spec.Dns != nil {
		out.Dns = &view.VMFrozenDNS{Mode: spec.Dns.Mode, Nameservers: append([]string(nil), spec.Dns.Nameservers...)}
	}
	if spec.Intent != nil {
		out.SystemDiskSizeGb = spec.Intent.SystemDiskSizeGb
		out.SourceKind = spec.Intent.SourceKind
		out.MediaKey = spec.Intent.MediaKey
	}
}
func (s *VMService) CreateVM(ctx context.Context, r *view.CreateVMRequest) (*view.VMAcceptance, error) {
	if r == nil || r.Data == nil || s.acceptance == nil {
		return nil, vmInvalid()
	}
	d := r.Data
	intent := &vm.CreateVMData{Name: d.Name, PresetId: d.PresetId, SourceKind: d.SourceKind, MediaKey: d.MediaKey, SystemDiskSizeGb: d.SystemDiskSizeGb, StartAfterCreate: d.StartAfterCreate}
	if d.Dns != nil {
		var err error
		intent.Dns, err = vm.NormalizeDNSSelection(&vm.DNSSelection{Mode: d.Dns.Mode, Nameservers: d.Dns.Nameservers})
		if err != nil {
			return nil, vmInvalid()
		}
	}
	if d.Network != nil {
		intent.Network = &vm.NetworkSelection{VpcId: d.Network.VpcId, SubnetId: d.Network.SubnetId}
	}
	for _, disk := range d.DataDisks {
		if disk == nil {
			return nil, vmInvalid()
		}
		intent.DataDisks = append(intent.DataDisks, &vm.InitialDataDisk{ClientDiskKey: disk.ClientDiskKey, Name: disk.Name, SizeGb: disk.SizeGb})
	}
	if d.GuestInitialization != nil {
		initialization := &vm.GuestInitialization{Method: "CLOUD_INIT_NOCLOUD", Username: d.GuestInitialization.Username, SshPublicKey: d.GuestInitialization.SshPublicKey, PasswordSet: d.GuestInitialization.Password != ""}
		intent.GuestInitialization = initialization
		if initialization.PasswordSet {
			if s.binding == nil || s.binding.guestCipher == nil {
				return nil, kratoserrors.ServiceUnavailable("VM_DEPENDENCY_NOT_READY", "VM guest credential encryption unavailable")
			}
			raw, e := json.Marshal(vmGuestCredentialPayload{Password: d.GuestInitialization.Password})
			if e != nil {
				return nil, vmInvalid()
			}
			defer clear(raw)
			initialization.CredentialIntentDigest, e = s.binding.guestCipher.IntentDigest(raw)
			if e != nil {
				return nil, vmInvalid()
			}
			ctx = context.WithValue(ctx, vmGuestCredentialContextKey{}, raw)
		}
	}
	result, err := s.acceptance.AcceptGpuCreate(ctx, d.IdempotencyKey, nil, intent)
	if err != nil {
		return nil, err
	}
	return &view.VMAcceptance{OperationId: result.OperationID, ResourceId: result.ResourceID, Replayed: result.Replayed, Result: "ACCEPTED"}, nil
}
func (s *VMService) DeleteVM(ctx context.Context, r *view.ChangeVMRequest) (*view.VMAcceptance, error) {
	if r == nil || !validVMChange(r.Data, "delete") || s.acceptance == nil {
		return nil, vmInvalid()
	}
	var replayed bool
	result, err := s.acceptance.acceptGpuDeleteWithReplay(ctx, r.Data.IdempotencyKey, r.Data.VmId, r.Data.ExpectedVersion, &replayed)
	if err != nil {
		return nil, err
	}
	return &view.VMAcceptance{OperationId: result.DeleteOperationId, ResourceId: result.ResourceId, Result: result.Result, Replayed: replayed}, nil
}
func (s *VMService) GetVM(ctx context.Context, r *view.GetVMRequest) (*view.VirtualMachine, error) {
	if r == nil || !canonicalCatalogUUID(r.VmId) {
		return nil, vmInvalid()
	}
	tenant, actor, err := s.scope(ctx, "get")
	if err != nil {
		return nil, err
	}
	result, err := s.binding.client.Get(ctx, tenant, actor, r.VmId)
	if vmNotFound(err) {
		p, e := gpuPrincipal(ctx)
		if e != nil {
			return nil, e
		}
		original, e := s.ledger.GetCreateOperationByResource(ctx, p.TenantID, InstanceOwner, r.VmId)
		if e != nil {
			return nil, err
		}
		if original.Action != commandcontract.VMCreateAction || original.ResourceTenantID != tenant {
			return nil, err
		}
		if original.DispatchState == quotaoperation.DispatchStateAcked {
			return nil, err
		}
		frozen, e := commandcontract.DecodeVMCanonical([]byte(original.CanonicalRequest))
		if e != nil {
			return nil, e
		}
		spec, e := vm.DecodeCreateSpec(frozen.CreateSpec)
		if e != nil {
			return nil, e
		}
		deleting, e := s.ledger.HasAcceptedVMDelete(ctx, p.TenantID, r.VmId)
		if e != nil {
			return nil, e
		}
		reason := "WAITING_FOR_OWNER"
		if original.LastErrorCode != nil {
			reason = *original.LastErrorCode
		}
		out := &view.VirtualMachine{Id: r.VmId, Name: spec.Intent.Name, OsFamily: spec.Media.OsFamily, OsVersion: spec.Media.OsVersion, ObservedState: "unknown", DesiredState: "pending", Reason: reason, LastOperationId: original.OperationID, Closing: deleting}
		if original.DispatchState == quotaoperation.DispatchStateCanceledUnsent {
			out.DesiredState = "deleted"
			out.ObservedState = "canceled"
			out.Reason = "CANCELED_BEFORE_DISPATCH"
		}
		vmSummary(spec, out)
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if result == nil || result.Id != r.VmId {
		return nil, kratoserrors.ServiceUnavailable("VM_INVALID_RESPONSE", "VM identity mismatch")
	}
	out := new(view.VirtualMachine)
	err = vmWire(result, out)
	if err == nil {
		vmSummary(result.FrozenSpec, out)
	}
	return out, err
}
func (s *VMService) ListVMs(ctx context.Context, r *view.ListVMsRequest) (*view.ListVMsResponse, error) {
	if r == nil {
		return nil, vmInvalid()
	}
	tenant, actor, err := s.scope(ctx, "list")
	if err != nil {
		return nil, err
	}
	page, size, err := resolveComputePage(r.Page, r.PageSize)
	if err != nil {
		return nil, err
	}
	result, err := s.binding.client.List(ctx, tenant, actor, page, size)
	if err != nil {
		return nil, err
	}
	out := new(view.ListVMsResponse)
	err = vmWire(result, out)
	if err == nil {
		for i, item := range result.Items {
			if item != nil && i < len(out.Items) {
				vmSummary(item.FrozenSpec, out.Items[i])
			}
		}
	}
	return out, err
}
func (s *VMService) GetVMOperation(ctx context.Context, r *view.GetOperationRequest) (*view.VMOperation, error) {
	if r == nil || !canonicalCatalogUUID(r.VmId) || !canonicalCatalogUUID(r.OperationId) {
		return nil, vmInvalid()
	}
	tenant, actor, err := s.scope(ctx, "operation")
	if err != nil {
		return nil, err
	}
	p, err := gpuPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	pending, err := s.ledger.GetOperationForUser(ctx, p.TenantID, r.OperationId)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if pending != nil {
		definition, ok := commandcontract.LookupAction(pending.OwnerService, pending.Action)
		if !ok || definition.ResourceKind != commandcontract.ResourceKindVM || pending.ResourceID != r.VmId || pending.ResourceTenantID != tenant {
			return nil, kratoserrors.NotFound("VM_NOT_FOUND", "operation not found")
		}
		if _, err = commandcontract.DecodeVMCanonical([]byte(pending.CanonicalRequest)); err != nil {
			return nil, err
		}
		if pending.DispatchState != quotaoperation.DispatchStateAcked {
			reason := "WAITING_FOR_OWNER"
			if pending.LastErrorCode != nil {
				reason = *pending.LastErrorCode
			}
			state := strings.ToLower(string(pending.DispatchState))
			if pending.DispatchState == quotaoperation.DispatchStateRejected {
				state = "failed"
			}
			return &view.VMOperation{Id: pending.OperationID, VmId: pending.ResourceID, Action: definition.Phase, State: state, Reason: reason}, nil
		}
	}
	result, err := s.binding.client.Operation(ctx, tenant, actor, r.VmId, r.OperationId)
	if err != nil {
		return nil, err
	}
	out := new(view.VMOperation)
	err = vmWire(result, out)
	return out, err
}
func (s *VMService) canOperate(ctx context.Context, id string) error {
	p, err := gpuPrincipal(ctx)
	if err != nil {
		return err
	}
	deleting, err := s.ledger.HasAcceptedVMDelete(ctx, p.TenantID, id)
	if ent.IsNotFound(err) {
		return kratoserrors.NotFound("VM_NOT_FOUND", "VM not found")
	}
	if err != nil {
		return err
	}
	if deleting {
		return kratoserrors.Conflict("VM_OPERATION_CONFLICT", "VM final deletion already accepted")
	}
	return nil
}
func validVMChange(d *view.ChangeVMData, action string) bool {
	if d == nil || !canonicalCatalogUUID(d.VmId) || !canonicalCatalogUUID(d.IdempotencyKey) || d.ExpectedVersion < 1 {
		return false
	}
	if d.DestructiveConfirmation && action != "force-stop" && action != "restore-snapshot" {
		return false
	}
	if d.GuestPreparedConfirmation && action != "detach-disk" {
		return false
	}
	if d.DiskId != "" && action != "attach-disk" && action != "detach-disk" && action != "delete-disk" {
		return false
	}
	if (d.Name != "" || d.SizeGb != 0) && action != "create-disk" && action != "create-snapshot" {
		return false
	}
	if d.SizeGb != 0 && action != "create-disk" {
		return false
	}
	if d.Slot != "" && action != "set-media" && action != "eject-media" {
		return false
	}
	if d.MediaKey != "" && action != "set-media" {
		return false
	}
	if d.BootDevice != "" && action != "set-boot" {
		return false
	}
	if d.SnapshotId != "" && action != "delete-snapshot" && action != "restore-snapshot" {
		return false
	}
	if d.PreviewDigest != "" && action != "restore-snapshot" {
		return false
	}
	return true
}
func vmChangeIntent(tenant, action string, d *view.ChangeVMData) proto.Message {
	base := &vm.ChangeVMRequest{TenantId: tenant, VmId: d.VmId, IdempotencyKey: d.IdempotencyKey, ExpectedVersion: d.ExpectedVersion}
	switch action {
	case "start", "stop", "restart":
		return base
	case "force-stop":
		return &vm.ForceStopRequest{TenantId: tenant, VmId: d.VmId, IdempotencyKey: d.IdempotencyKey, ExpectedVersion: d.ExpectedVersion, DestructiveConfirmation: d.DestructiveConfirmation}
	case "create-disk":
		return &vm.CreateDiskRequest{TenantId: tenant, VmId: d.VmId, IdempotencyKey: d.IdempotencyKey, ExpectedVersion: d.ExpectedVersion, Name: d.Name, SizeGb: d.SizeGb}
	case "attach-disk", "detach-disk", "delete-disk":
		return &vm.DiskChangeRequest{TenantId: tenant, VmId: d.VmId, IdempotencyKey: d.IdempotencyKey, ExpectedVersion: d.ExpectedVersion, DiskId: d.DiskId, GuestPreparedConfirmation: d.GuestPreparedConfirmation}
	case "set-media":
		return &vm.SetMediaRequest{TenantId: tenant, VmId: d.VmId, IdempotencyKey: d.IdempotencyKey, ExpectedVersion: d.ExpectedVersion, Slot: d.Slot, MediaKey: d.MediaKey}
	case "eject-media":
		return &vm.EjectMediaRequest{TenantId: tenant, VmId: d.VmId, IdempotencyKey: d.IdempotencyKey, ExpectedVersion: d.ExpectedVersion, Slot: d.Slot}
	case "set-boot":
		return &vm.SetBootDeviceRequest{TenantId: tenant, VmId: d.VmId, IdempotencyKey: d.IdempotencyKey, ExpectedVersion: d.ExpectedVersion, BootDevice: d.BootDevice}
	case "create-snapshot":
		return &vm.SnapshotCreateRequest{TenantId: tenant, VmId: d.VmId, IdempotencyKey: d.IdempotencyKey, ExpectedVersion: d.ExpectedVersion, Name: d.Name}
	case "delete-snapshot":
		return &vm.SnapshotChangeRequest{TenantId: tenant, VmId: d.VmId, IdempotencyKey: d.IdempotencyKey, ExpectedVersion: d.ExpectedVersion, SnapshotId: d.SnapshotId}
	case "restore-snapshot":
		return &vm.RestoreSnapshotRequest{TenantId: tenant, VmId: d.VmId, IdempotencyKey: d.IdempotencyKey, ExpectedVersion: d.ExpectedVersion, SnapshotId: d.SnapshotId, PreviewDigest: d.PreviewDigest, DestructiveConfirmation: d.DestructiveConfirmation}
	}
	return nil
}
func (s *VMService) change(ctx context.Context, r *view.ChangeVMRequest, action string) (*view.VMOperation, error) {
	if r == nil || !validVMChange(r.Data, action) {
		return nil, vmInvalid()
	}
	tenant, actor, err := s.scope(ctx, action)
	if err != nil {
		return nil, err
	}
	d := r.Data
	intent := vmChangeIntent(tenant, action, d)
	if intent == nil {
		return nil, vmInvalid()
	}
	canonical, err := vm.CanonicalCommandIntent(intent)
	if err != nil {
		return nil, err
	}
	// A pure lookup preserves the original accepted command even after DELETE.
	result, err := s.binding.client.Lookup(ctx, tenant, actor, &vm.LookupCommandReplayRequest{TenantId: tenant, VmId: d.VmId, Action: action, IdempotencyKey: d.IdempotencyKey, CanonicalIntent: canonical})
	if err != nil && !vmNotFound(err) {
		return nil, err
	}
	if vmNotFound(err) {
		if err = s.canOperate(ctx, d.VmId); err != nil {
			return nil, err
		}
		result, err = s.binding.client.Change(ctx, tenant, actor, action, intent)
		if err != nil {
			return nil, err
		}
	}
	if result == nil || result.VmId != d.VmId || result.Id == "" {
		return nil, kratoserrors.ServiceUnavailable("VM_INVALID_RESPONSE", "VM operation identity mismatch")
	}
	out := new(view.VMOperation)
	err = vmWire(result, out)
	return out, err
}
func (s *VMService) StartVM(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "start")
}
func (s *VMService) StopVM(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "stop")
}
func (s *VMService) RestartVM(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "restart")
}
func (s *VMService) ForceStopVM(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "force-stop")
}
func (s *VMService) CreateVMDisk(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "create-disk")
}
func (s *VMService) AttachVMDisk(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "attach-disk")
}
func (s *VMService) DetachVMDisk(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "detach-disk")
}
func (s *VMService) DeleteVMDisk(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "delete-disk")
}
func (s *VMService) SetVMMedia(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "set-media")
}
func (s *VMService) EjectVMMedia(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "eject-media")
}
func (s *VMService) SetVMBootDevice(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "set-boot")
}
func (s *VMService) CreateVMSnapshot(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "create-snapshot")
}
func (s *VMService) DeleteVMSnapshot(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "delete-snapshot")
}
func (s *VMService) RestoreVMSnapshot(c context.Context, r *view.ChangeVMRequest) (*view.VMOperation, error) {
	return s.change(c, r, "restore-snapshot")
}
func (s *VMService) read(ctx context.Context, action, id, snapshot string, out proto.Message) error {
	if action != "media" && action != "dns-defaults" && !canonicalCatalogUUID(id) {
		return vmInvalid()
	}
	tenant, actor, err := s.scope(ctx, action)
	if err != nil {
		return err
	}
	result, err := s.binding.client.Read(ctx, tenant, actor, action, id, snapshot)
	if err != nil {
		return err
	}
	return vmWire(result, out)
}
func (s *VMService) ListVMDisks(c context.Context, r *view.GetVMRequest) (*view.ListDisksResponse, error) {
	if r == nil {
		return nil, vmInvalid()
	}
	out := new(view.ListDisksResponse)
	err := s.read(c, "disks", r.VmId, "", out)
	return out, err
}
func (s *VMService) GetVMDNSDefaults(c context.Context, r *view.GetDNSDefaultsRequest) (*view.DNSDefaults, error) {
	if r == nil {
		return nil, vmInvalid()
	}
	out := new(view.DNSDefaults)
	err := s.read(c, "dns-defaults", "", "", out)
	return out, err
}
func (s *VMService) ListVMMedia(c context.Context, r *view.ListMediaRequest) (*view.ListMediaResponse, error) {
	if r == nil {
		return nil, vmInvalid()
	}
	out := new(view.ListMediaResponse)
	err := s.read(c, "media", "", "", out)
	return out, err
}
func (s *VMService) ListVMSnapshots(c context.Context, r *view.GetVMRequest) (*view.ListSnapshotsResponse, error) {
	if r == nil {
		return nil, vmInvalid()
	}
	out := new(view.ListSnapshotsResponse)
	err := s.read(c, "snapshots", r.VmId, "", out)
	return out, err
}
func (s *VMService) GetVMSnapshot(c context.Context, r *view.GetSnapshotRequest) (*view.VMSnapshot, error) {
	if r == nil || !canonicalCatalogUUID(r.SnapshotId) {
		return nil, vmInvalid()
	}
	out := new(view.VMSnapshot)
	err := s.read(c, "snapshot", r.VmId, r.SnapshotId, out)
	return out, err
}
func (s *VMService) PreviewVMRestore(c context.Context, r *view.ChangeVMRequest) (*view.VMRestorePreview, error) {
	if r == nil || r.Data == nil || !canonicalCatalogUUID(r.Data.SnapshotId) {
		return nil, vmInvalid()
	}
	out := new(view.VMRestorePreview)
	err := s.read(c, "preview-restore", r.Data.VmId, r.Data.SnapshotId, out)
	return out, err
}
func (s *VMService) CreateVMConsoleSession(ctx context.Context, r *view.ChangeVMRequest) (*view.VMConsoleSession, error) {
	if r == nil || !validVMChange(r.Data, "console") {
		return nil, vmInvalid()
	}
	tenant, actor, err := s.scope(ctx, "console")
	if err != nil {
		return nil, err
	}
	if s.sessions == nil {
		return nil, kratoserrors.ServiceUnavailable("VM_SESSION_UNAVAILABLE", "VM console unavailable")
	}
	d := r.Data
	principal := &session.Principal{TenantId: tenant, SubjectId: actor}
	replay, err := s.sessions.GetSessionReplay(ctx, &session.GetSessionReplayRequest{IdempotencyKey: d.IdempotencyKey, Principal: principal, InstanceId: d.VmId, ExpectedVersion: d.ExpectedVersion})
	if err == nil {
		if replay == nil || !replay.Replayed || replay.SessionId == "" || replay.ConnectUrl == "" || replay.ExpiresAt == nil {
			return nil, kratoserrors.ServiceUnavailable("VM_SESSION_UNAVAILABLE", "invalid console replay")
		}
		return &view.VMConsoleSession{SessionId: replay.SessionId, WsUrl: replay.ConnectUrl, ExpiresAt: replay.ExpiresAt.AsTime().UTC().Format(time.RFC3339Nano)}, nil
	}
	if !vmNotFound(err) {
		return nil, err
	}
	if err = s.canOperate(ctx, d.VmId); err != nil {
		return nil, err
	}
	target, err := s.binding.client.Target(ctx, tenant, actor, d.VmId, d.ExpectedVersion)
	if err != nil {
		return nil, err
	}
	if target == nil || target.TenantId != tenant || target.VmId != d.VmId || target.Version != d.ExpectedVersion || target.ClusterId != s.binding.cluster || target.Namespace == "" || target.NamespaceUid == "" || target.VmName == "" || target.VmUid == "" || target.VmiName == "" || target.VmiUid == "" {
		return nil, kratoserrors.Conflict("VM_CONSOLE_TARGET_CHANGED", "VM console target changed")
	}
	// Recheck the durable Governance fence immediately before signing a new intent.
	if err = s.canOperate(ctx, d.VmId); err != nil {
		return nil, err
	}
	reply, err := s.sessions.CreateSession(ctx, &session.CreateSessionRequest{RequestId: uuid.NewString(), IdempotencyKey: d.IdempotencyKey, Principal: principal, Target: &session.Target{InstanceId: d.VmId, WorkloadName: target.VmName, WorkloadKind: session.WorkloadKind_WORKLOAD_KIND_VM, VmBinding: target}, Mode: &session.CreateSessionRequest_VmConsole{VmConsole: &session.VMConsoleOptions{Protocol: session.VMConsoleOptions_PROTOCOL_VNC}}})
	if err != nil {
		return nil, err
	}
	if reply == nil || reply.SessionId == "" || reply.ConnectUrl == "" || reply.ExpiresAt == nil {
		return nil, kratoserrors.ServiceUnavailable("VM_SESSION_UNAVAILABLE", "invalid console response")
	}
	return &view.VMConsoleSession{SessionId: reply.SessionId, WsUrl: reply.ConnectUrl, ExpiresAt: reply.ExpiresAt.AsTime().UTC().Format(time.RFC3339Nano)}, nil
}
