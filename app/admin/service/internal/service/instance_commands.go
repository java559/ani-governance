package service

import (
	"context"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"github.com/zhangzhe-ctrl/ani-resource-service/pkg/instanceconfig"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"k8s.io/apimachinery/pkg/util/validation"
)

type instanceCommandClient interface {
	Lookup(context.Context, string, string, *res.LookupCommandReplayRequest) (*res.OperationResponse, error)
	Restart(context.Context, string, string, *res.ChangeInstanceRequest) (*res.OperationResponse, error)
	AttachPVC(context.Context, string, string, *res.AttachPVCRequest) (*res.OperationResponse, error)
	DetachPVC(context.Context, string, string, *res.DetachPVCRequest) (*res.OperationResponse, error)
	InstanceMountablePVCs(context.Context, string, string, *res.ListInstanceMountablePVCsRequest) (*res.ListMountablePVCsResponse, error)
}

func instanceUUID(s string) bool {
	id, e := uuid.Parse(s)
	return e == nil && id != uuid.Nil && id.String() == s
}

// Authorization and canonical replay precede all gates and current owner state.
func (s *InstanceService) command(ctx context.Context, path, action, id, key string, version int64, pvcName, mountPath string, readOnly, restartIfRunning bool) (*view.Acceptance, error) {
	tenant, actor, err := s.scope(ctx, path, "POST")
	if err != nil {
		return nil, err
	}
	if !instanceUUID(id) || !instanceUUID(key) || version < 1 {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid instance command")
	}
	if (action == "attach-pvc" || action == "detach-pvc") && !instanceconfig.MountPath(mountPath) || action == "attach-pvc" && len(validation.IsDNS1123Subdomain(pvcName)) != 0 {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid instance mount intent")
	}
	canonical, err := res.CanonicalCommandIntent(action, id, version, pvcName, mountPath, readOnly, restartIfRunning)
	if err != nil {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid instance command")
	}
	client, ok := s.binding.client.(instanceCommandClient)
	if !ok {
		return nil, kratoserrors.ServiceUnavailable("INSTANCE_DEPENDENCY_UNAVAILABLE", "instance command owner unavailable")
	}
	reply, err := client.Lookup(ctx, tenant, actor, &res.LookupCommandReplayRequest{TenantId: tenant, InstanceId: id, Action: action, IdempotencyKey: key, CanonicalIntent: canonical})
	if err == nil {
		return instanceCommandAcceptance(reply, id, true)
	}
	if status.Code(err) != codes.NotFound {
		return nil, err
	}
	if err = s.canOperate(ctx, id); err != nil {
		return nil, err
	}
	if !s.binding.newIntentsEnabled || action == "restart" && !s.binding.restartEnabled || (action == "attach-pvc" || action == "detach-pvc") && !s.binding.pvcMutationsEnabled {
		return nil, kratoserrors.ServiceUnavailable("INSTANCE_ACTION_UNAVAILABLE", "instance action unavailable")
	}
	switch action {
	case "start", "stop":
		reply, err = s.binding.client.Change(ctx, tenant, actor, &res.ChangeInstanceRequest{TenantId: tenant, InstanceId: id, IdempotencyKey: key, ExpectedVersion: version}, action == "start")
	case "restart":
		reply, err = client.Restart(ctx, tenant, actor, &res.ChangeInstanceRequest{TenantId: tenant, InstanceId: id, IdempotencyKey: key, ExpectedVersion: version})
	case "attach-pvc":
		reply, err = client.AttachPVC(ctx, tenant, actor, &res.AttachPVCRequest{TenantId: tenant, InstanceId: id, IdempotencyKey: key, ExpectedVersion: version, PvcName: pvcName, MountPath: mountPath, ReadOnly: readOnly, RestartIfRunning: restartIfRunning})
	case "detach-pvc":
		reply, err = client.DetachPVC(ctx, tenant, actor, &res.DetachPVCRequest{TenantId: tenant, InstanceId: id, IdempotencyKey: key, ExpectedVersion: version, MountPath: mountPath, RestartIfRunning: restartIfRunning})
	default:
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid instance action")
	}
	if err != nil {
		return nil, err
	}
	return instanceCommandAcceptance(reply, id, false)
}
func instanceCommandAcceptance(reply *res.OperationResponse, id string, replayed bool) (*view.Acceptance, error) {
	if reply == nil || reply.DurableOwnerAck == nil || !reply.DurableOwnerAck.Accepted || reply.DurableOwnerAck.ResourceId != id || !instanceUUID(reply.DurableOwnerAck.OperationId) {
		return nil, kratoserrors.ServiceUnavailable("INSTANCE_DEPENDENCY_UNAVAILABLE", "invalid instance durable ACK")
	}
	return &view.Acceptance{OperationId: reply.DurableOwnerAck.OperationId, ResourceId: id, Replayed: replayed || reply.Replayed, Result: "ACCEPTED"}, nil
}
func (s *InstanceService) RestartInstance(ctx context.Context, r *view.ChangeInstanceRequest) (*view.Acceptance, error) {
	if r == nil || r.Data == nil {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "instance data required")
	}
	d := r.Data
	return s.command(ctx, data.InstanceRestartPath, "restart", d.InstanceId, d.IdempotencyKey, d.ExpectedVersion, "", "", false, false)
}
func (s *InstanceService) AttachInstancePVC(ctx context.Context, r *view.AttachInstancePVCRequest) (*view.Acceptance, error) {
	if r == nil || r.Data == nil {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "instance data required")
	}
	d := r.Data
	return s.command(ctx, data.InstanceAttachPVCPath, "attach-pvc", d.InstanceId, d.IdempotencyKey, d.ExpectedVersion, d.PvcName, d.MountPath, d.ReadOnly, d.RestartIfRunning)
}
func (s *InstanceService) DetachInstancePVC(ctx context.Context, r *view.DetachInstancePVCRequest) (*view.Acceptance, error) {
	if r == nil || r.Data == nil {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "instance data required")
	}
	d := r.Data
	return s.command(ctx, data.InstanceDetachPVCPath, "detach-pvc", d.InstanceId, d.IdempotencyKey, d.ExpectedVersion, "", d.MountPath, false, d.RestartIfRunning)
}
func (s *InstanceService) ListInstanceMountablePVCs(ctx context.Context, r *view.ListInstanceMountablePVCsRequest) (*view.ListMountablePVCsResponse, error) {
	if r == nil || !instanceUUID(r.InstanceId) {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid instance request")
	}
	// The same attach authority permits candidates without create/start/stop grants.
	tenant, actor, err := s.scope(ctx, data.InstanceAttachPVCPath, "POST")
	if err != nil {
		return nil, err
	}
	page, size, err := resolveComputePage(r.Page, r.PageSize)
	if err != nil {
		return nil, err
	}
	client, ok := s.binding.client.(instanceCommandClient)
	if !ok {
		return nil, kratoserrors.ServiceUnavailable("INSTANCE_DEPENDENCY_UNAVAILABLE", "instance owner unavailable")
	}
	reply, err := client.InstanceMountablePVCs(ctx, tenant, actor, &res.ListInstanceMountablePVCsRequest{TenantId: tenant, InstanceId: r.InstanceId, Page: page, PageSize: size, Name: r.Name})
	if err != nil {
		return nil, err
	}
	if reply == nil {
		return nil, kratoserrors.ServiceUnavailable("INSTANCE_DEPENDENCY_UNAVAILABLE", "invalid instance candidates")
	}
	raw, err := protojson.Marshal(reply)
	if err != nil {
		return nil, err
	}
	out := new(view.ListMountablePVCsResponse)
	err = protojson.Unmarshal(raw, out)
	return out, err
}
