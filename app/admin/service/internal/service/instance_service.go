package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"os"
	"strconv"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	klog "github.com/go-kratos/kratos/v2/log"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	terminal "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/terminal/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	sessionpb "github.com/zhangzhe-ctrl/ani-session-gateway/api/gen/session/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	quota "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	"go-wind-admin/pkg/middleware/auth"
	"go-wind-admin/pkg/middleware/requestid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const InstanceCreateAction = "/instance.v1.InstanceService/AcceptCreate"
const InstanceDeleteAction = "/instance.v1.InstanceService/AcceptDelete"
const InstanceOwner = "ani-resource-service"

type InstanceOwnerClient interface {
	instanceCommandClient
	Resolve(context.Context, string, string, *res.CreateInstanceData, *res.SchedulingOptions) (*res.CreateSpec, error)
	Create(context.Context, *res.AcceptCreateRequest) (*res.OperationResponse, error)
	Delete(context.Context, *res.AcceptDeleteRequest) (*res.OperationResponse, error)
	Change(context.Context, string, string, *res.ChangeInstanceRequest, bool) (*res.OperationResponse, error)
	Get(context.Context, string, string, string) (*res.Instance, error)
	ResolveInstanceMetricScope(context.Context, string, string, string) (*res.InstanceMetricScope, error)
	List(context.Context, string, string, *res.ListInstancesRequest) (*res.ListInstancesResponse, error)
	Operation(context.Context, string, string, string) (*res.Operation, error)
	Target(context.Context, string, string, string, int64) (*terminal.TerminalTarget, error)
}
type InstanceActionAuthorizer interface {
	Authorize(context.Context, uint32, uint32, string, string) error
}
type InstanceGpuBinding struct {
	client              InstanceOwnerClient
	authorization       InstanceActionAuthorizer
	clusterID           string
	scheduling          *InstanceSchedulingPolicy
	newCreateEnabled    bool
	newIntentsEnabled   bool
	restartEnabled      bool
	pvcMutationsEnabled bool
}

func NewInstanceGpuBinding(c InstanceOwnerClient, a InstanceActionAuthorizer, clusterID string, policies ...*InstanceSchedulingPolicy) *InstanceGpuBinding {
	b := &InstanceGpuBinding{client: c, authorization: a, clusterID: clusterID, newCreateEnabled: os.Getenv("ANI_INSTANCE_CONFIG_V2_WRITES") == "true", newIntentsEnabled: os.Getenv("ANI_INSTANCE_NEW_INTENTS_ENABLED") == "true", restartEnabled: os.Getenv("ANI_INSTANCE_RESTART_ENABLED") == "true", pvcMutationsEnabled: os.Getenv("ANI_INSTANCE_PVC_MUTATIONS_ENABLED") == "true"}
	if len(policies) > 0 {
		b.scheduling = policies[0]
	}
	return b
}
func (*InstanceGpuBinding) OwnerService() string { return InstanceOwner }
func (b *InstanceGpuBinding) NewCreateEnabled() bool {
	return b != nil && b.newCreateEnabled && b.newIntentsEnabled
}
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
	if errors.Is(e, data.ErrActionAuthorizationDenied) {
		return kratoserrors.Forbidden("FORBIDDEN", "instance action denied")
	}
	if errors.Is(e, data.ErrActionAuthorizationUnavailable) {
		return kratoserrors.ServiceUnavailable("INSTANCE_AUTHORIZATION_UNAVAILABLE", "instance authorization unavailable")
	}
	return e
}
func (*InstanceGpuBinding) CanonicalGpuBusinessVersion(m proto.Message, version uint32) ([]byte, error) {
	if version != data.CurrentInstanceContractVersion {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "unsupported instance contract")
	}
	return res.CanonicalBusinessPayloadVersion(m, version)
}
func (*InstanceGpuBinding) CanonicalGpuBusiness(m proto.Message) ([]byte, error) {
	return res.CanonicalBusinessPayloadVersion(m, data.CurrentInstanceContractVersion)
}
func (b *InstanceGpuBinding) ResolveGpuBusiness(ctx context.Context, p *auth.Principal, tenant string, m proto.Message) (*acc.GpuRequest, proto.Message, error) {
	in, ok := m.(*res.CreateInstanceData)
	if !ok || in == nil || b.client == nil {
		return nil, nil, quota.ErrorInvalidQuotaRequest("%s", "instance public data required")
	}
	if b.clusterID == "" {
		return nil, nil, kratoserrors.ServiceUnavailable("INSTANCE_CLUSTER_NOT_CONFIGURED", "instance cluster is not configured")
	}
	var settings *view.InstanceSchedulingSettings
	var defaults *res.SchedulingOptions
	if b.scheduling != nil {
		var err error
		settings, err = b.scheduling.read(ctx)
		if err != nil {
			return nil, nil, err
		}
		defaults = instanceSchedulingDefaults(settings)
	}
	resolved, e := b.client.Resolve(ctx, tenant, "governance:user:"+strconv.FormatUint(uint64(p.ID), 10), in, defaults)
	if e != nil {
		return nil, nil, e
	}
	if resolved == nil || resolved.InstanceKind != "container" || resolved.Name != in.Name || resolved.ImageId != in.ImageId || resolved.ImageScope != in.ImageScope || resolved.PresetId != in.PresetId || (resolved.ComputeType == "cpu" && resolved.GpuSelection != nil) || (resolved.ComputeType == "gpu" && resolved.GpuSelection == nil) || (resolved.ComputeType != "cpu" && resolved.ComputeType != "gpu") {
		return nil, nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance resolution")
	}
	if resolved.ClusterId != b.clusterID {
		return nil, nil, kratoserrors.BadRequest("SCHEDULING_FIELD_CONFLICT", "resolved cluster differs from configured instance cluster")
	}
	if e = res.ValidateCreateSpec(resolved, false); e != nil {
		return nil, nil, instanceBusinessError(e)
	}
	if e = b.scheduling.authorizePriority(ctx, p, tenant, settings, resolved); e != nil {
		return nil, nil, e
	}
	var gpu *acc.GpuRequest
	if g := resolved.GpuSelection; g != nil {
		gpu = &acc.GpuRequest{ClusterId: g.ClusterId, PoolId: g.PoolId, ProfileId: g.ProfileId, ProfileVersion: g.ProfileVersion, Replicas: 1, DevicesPerReplica: g.DevicesPerInstance, ContainerName: "workspace"}
	}

	return gpu, resolved, nil
}
func (*InstanceGpuBinding) ValidateGpuBusiness(_ context.Context, m proto.Message) ([]data.QuotaOccupyItem, error) {
	b, ok := m.(*res.CreateSpec)
	if !ok || b == nil || b.ComputeType != "gpu" || b.GpuSelection == nil || b.GpuSelection.DevicesPerInstance < 1 || b.ClusterId != b.GpuSelection.ClusterId || b.ImageReference == "" {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid resolved instance business")
	}
	if err := res.ValidateCreateSpec(b, false); err != nil {
		return nil, instanceBusinessError(err)
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
	if data.IsInstanceCPUCanonical(c.CanonicalRequest) {
		return b.dispatchCPU(ctx, c)
	}
	frozen, e := DecodeGpuCanonical(c.CanonicalRequest)
	if e != nil {
		return nil, e
	}
	if e = ValidateGpuCharges(frozen, c.Charges); e != nil {
		return nil, e
	}
	business, e := res.DecodeBusinessPayload(frozen.BusinessPayload)
	if e != nil {
		return nil, e
	}
	if e = res.ValidateCreateSpec(business, true); e != nil {
		return nil, instanceBusinessError(e)
	}
	commandContext := &res.CommandContext{TenantId: c.ResourceTenantID, OperationId: c.OperationID, ResourceId: c.ResourceID}
	ctx = data.WithInstanceCommandActor(ctx, "governance:"+c.Actor.Type+":"+c.Actor.ID)
	var reply *res.OperationResponse
	switch c.Action {
	case InstanceCreateAction:
		if c.CreateOperationID != "" {
			return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid create origin")
		}
		commandContext.RequestDigest, e = res.CommandDigest(commandContext, business)
		if e != nil {
			return nil, e
		}
		reply, e = b.client.Create(ctx, &res.AcceptCreateRequest{Context: commandContext, Spec: business})
	case InstanceDeleteAction:
		if c.CreateOperationID == "" || frozen.DeleteExpectedVersion < 1 {
			return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance delete")
		}
		request := &res.AcceptDeleteRequest{Context: commandContext, CreateOperationId: c.CreateOperationID, ExpectedVersion: frozen.DeleteExpectedVersion, InstanceKind: business.InstanceKind, ComputeType: business.ComputeType}
		commandContext.RequestDigest, e = res.DeleteCommandDigest(request)
		if e != nil {
			return nil, e
		}
		reply, e = b.client.Delete(ctx, request)
	default:
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance action")
	}
	if e != nil {
		return nil, e
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
	catalog    InstanceCatalogConfig
	monitoring InstanceMonitoringClient
}

func NewInstanceService(a *GpuAcceptance, b *InstanceGpuBinding, r ResourceTenantResolver, l *data.QuotaLedgerRepo, sessions InstanceSessionClient, catalogs ...InstanceCatalogConfig) *InstanceService {
	s := &InstanceService{acceptance: a, binding: b, resolver: r, ledger: l, sessions: sessions}
	if len(catalogs) > 0 {
		s.catalog = catalogs[0]
		s.monitoring = catalogs[0].Monitoring
	}
	return s
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
	var scheduling *res.SchedulingOptions
	if d.Scheduling != nil {
		scheduling = &res.SchedulingOptions{PriorityClassName: d.Scheduling.PriorityClassName}
		if d.Scheduling.PodPlacement != nil {
			value := res.SchedulingOptions_PodPlacement(*d.Scheduling.PodPlacement)
			scheduling.PodPlacement = &value
		}
	}
	business := &res.CreateInstanceData{Name: d.Name, ImageId: d.ImageId, ImageScope: d.ImageScope, PresetId: d.PresetId, Scheduling: scheduling, WorkspaceMountPath: d.WorkspaceMountPath, Labels: d.Labels, Annotations: d.Annotations}
	for _, mount := range d.VolumeMounts {
		if mount == nil {
			return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid volume mount")
		}
		business.VolumeMounts = append(business.VolumeMounts, &res.VolumeMount{PvcName: mount.PvcName, MountPath: mount.MountPath, ReadOnly: mount.ReadOnly})
	}
	if d.Network != nil {
		business.Network = &res.NetworkSelection{VpcId: d.Network.VpcId, SubnetId: d.Network.SubnetId}
	}
	for _, variable := range d.Env {
		if variable == nil {
			return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid environment variable")
		}
		business.Env = append(business.Env, &res.InstanceEnvVar{Name: variable.Name, Value: variable.Value})
	}
	result, e := s.acceptance.AcceptGpuCreate(ctx, d.IdempotencyKey, nil, business)
	if e != nil {
		return nil, e
	}
	return &view.Acceptance{OperationId: result.OperationID, ResourceId: result.ResourceID, Replayed: result.Replayed, Result: "ACCEPTED"}, nil
}
func (s *InstanceService) DeleteInstance(ctx context.Context, r *view.ChangeInstanceRequest) (*view.Acceptance, error) {
	if r == nil || r.Data == nil || r.Data.ExpectedVersion < 1 {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "instance delete data required")
	}
	var replayed bool
	accepted, e := s.acceptance.acceptGpuDeleteWithReplay(ctx, r.Data.IdempotencyKey, r.Data.InstanceId, r.Data.ExpectedVersion, &replayed)
	if e != nil {
		return nil, e
	}
	return &view.Acceptance{OperationId: accepted.DeleteOperationId, ResourceId: accepted.ResourceId, Replayed: replayed, Result: accepted.Result}, nil
}

func (s *InstanceService) canOperate(ctx context.Context, id string) error {
	p, e := gpuPrincipal(ctx)
	if e != nil {
		return e
	}
	hasDelete, e := s.ledger.HasAcceptedInstanceDelete(ctx, p.TenantID, id)
	if ent.IsNotFound(e) {
		return kratoserrors.NotFound("INSTANCE_NOT_FOUND", "instance not found")
	}
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
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "instance data required")
	}
	path, action := data.InstanceStopPath, "stop"
	if start {
		path, action = data.InstanceStartPath, "start"
	}
	d := r.Data
	return s.command(ctx, path, action, d.InstanceId, d.IdempotencyKey, d.ExpectedVersion, "", "", false, false)
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
	page, size, e := resolveComputePage(r.Page, r.PageSize)
	if e != nil {
		return nil, e
	}
	items, e := s.binding.client.List(ctx, tenant, actor, &res.ListInstancesRequest{TenantId: tenant, Page: page, PageSize: size, InstanceKind: r.InstanceKind, ComputeType: r.ComputeType, ObservedState: r.ObservedState, Name: r.Name, Id: r.Id})
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
	p, e := gpuPrincipal(ctx)
	if e != nil {
		return nil, e
	}
	pending, e := s.ledger.GetOperationForUser(ctx, p.TenantID, r.OperationId)
	if e != nil && !ent.IsNotFound(e) {
		return nil, e
	}
	if pending != nil {
		if pending.OwnerService != InstanceOwner || pending.ResourceTenantID != tenant {
			return nil, quota.ErrorQuotaNotFound("%s", "operation not found")
		}
		if pending.Action == InstanceDeleteAction && pending.CreateOperationID != nil && pending.DispatchState == quotaoperation.DispatchStateRejected && pending.AckJSON == nil && pending.LastErrorCode != nil && *pending.LastErrorCode == "INSTANCE_VERSION_CONFLICT" {
			return &view.Operation{Id: pending.OperationID, InstanceId: pending.ResourceID, Action: "delete", IntentVersion: 0, State: "failed", Reason: "INSTANCE_VERSION_CONFLICT"}, nil
		}
		if data.IsInstanceCPUCanonical([]byte(pending.CanonicalRequest)) && pending.DispatchState != quotaoperation.DispatchStateAcked {
			if !data.AllowsInstanceCPUCommand(pending.OwnerService, pending.Action, pending.CanonicalRequest) {
				return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid frozen CPU instance command")
			}
			action := "create"
			if pending.CreateOperationID != nil {
				action = "delete"
			}
			reason := "WAITING_FOR_OWNER"
			if pending.LastErrorCode != nil {
				reason = *pending.LastErrorCode
			}
			if pending.DispatchState == quotaoperation.DispatchStateCanceledUnsent {
				reason = "CANCELED_BEFORE_DISPATCH"
			}
			// No Resource intent version exists before its durable acceptance.
			return &view.Operation{Id: pending.OperationID, InstanceId: pending.ResourceID, Action: action, State: string(pending.DispatchState), Reason: reason}, nil
		}
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
	requestID, _ := requestid.FromContext(ctx)
	t, e := s.binding.client.Target(ctx, tenant, actor, d.InstanceId, d.ExpectedVersion)
	if e != nil {
		klog.Errorw("event", "instance_exec_upstream_failed", "stage", "resource.target", "grpc_code", status.Code(e).String(), "request_id", requestID)
		return nil, e
	}
	if t == nil || t.TenantId != tenant || t.InstanceId != d.InstanceId || t.Version != d.ExpectedVersion || t.PodUid == "" || t.NotebookUid == "" || t.Shell == "" || t.ContainerName != "workspace" {
		return nil, kratoserrors.Conflict("INSTANCE_TERMINAL_NOT_READY", "instance terminal not ready")
	}
	current, e := s.binding.client.Get(ctx, tenant, actor, d.InstanceId)
	if e != nil {
		klog.Errorw("event", "instance_exec_upstream_failed", "stage", "resource.get", "grpc_code", status.Code(e).String(), "request_id", requestID)
		return nil, e
	}
	if current == nil || current.Id != d.InstanceId || current.Version != d.ExpectedVersion {
		return nil, kratoserrors.Conflict("INSTANCE_VERSION_CONFLICT", "instance version changed")
	}
	kind := sessionpb.WorkloadKind_WORKLOAD_KIND_GPU_CONTAINER
	if current.InstanceKind != "container" {
		return nil, kratoserrors.Conflict("INSTANCE_TERMINAL_NOT_READY", "invalid instance kind")
	}
	if current.ComputeType == "cpu" {
		kind = sessionpb.WorkloadKind_WORKLOAD_KIND_CONTAINER
	} else if current.ComputeType != "gpu" {
		return nil, kratoserrors.Conflict("INSTANCE_TERMINAL_NOT_READY", "invalid instance type")
	}
	sessionRequestID := uuid.NewString()
	reply, e := s.sessions.CreateSession(ctx, &sessionpb.CreateSessionRequest{RequestId: sessionRequestID, IdempotencyKey: d.IdempotencyKey, Principal: &sessionpb.Principal{TenantId: tenant, SubjectId: actor}, Target: &sessionpb.Target{InstanceId: d.InstanceId, WorkloadName: t.NotebookName, WorkloadKind: kind, InstanceBinding: &sessionpb.InstanceBinding{Version: t.Version, ClusterId: t.ClusterId, Namespace: t.Namespace, NotebookName: t.NotebookName, NotebookUid: t.NotebookUid, PodName: t.PodName, PodUid: t.PodUid, ContainerName: t.ContainerName, Shell: t.Shell}}, Mode: &sessionpb.CreateSessionRequest_Exec{Exec: &sessionpb.ExecOptions{Container: t.ContainerName, Command: []string{t.Shell}, Tty: true, Rows: d.Rows, Cols: d.Cols}}})
	if e != nil {
		klog.Errorw("event", "instance_exec_upstream_failed", "stage", "session_gateway.create_session", "grpc_code", status.Code(e).String(), "request_id", requestID, "session_request_id", sessionRequestID)
		return nil, e
	}
	if reply == nil || reply.SessionId == "" || reply.ConnectUrl == "" || reply.ExpiresAt == nil {
		return nil, kratoserrors.ServiceUnavailable("INSTANCE_SESSION_UNAVAILABLE", "invalid terminal reply")
	}
	return &view.ExecInstanceResponse{SessionId: reply.SessionId, WsUrl: reply.ConnectUrl, ExpiresAt: reply.ExpiresAt.AsTime().UTC().Format(time.RFC3339Nano)}, nil
}
