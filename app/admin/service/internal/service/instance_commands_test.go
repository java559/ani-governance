package service

import (
	"context"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	command "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/command/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

type instanceReplayPeer struct {
	InstanceOwnerClient
	lookupCalls int
	reply       *res.OperationResponse
}

func (p *instanceReplayPeer) Lookup(_ context.Context, tenant, actor string, in *res.LookupCommandReplayRequest) (*res.OperationResponse, error) {
	p.lookupCalls++
	if p.reply == nil {
		return nil, status.Error(codes.NotFound, "INSTANCE_NOT_FOUND")
	}
	return p.reply, nil
}

type instanceCommandAuthorization struct {
	denied bool
	path   string
}

func (a *instanceCommandAuthorization) Authorize(_ context.Context, _, _ uint32, path, method string) error {
	a.path = path
	if a.denied {
		return data.ErrActionAuthorizationDenied
	}
	return nil
}
func TestInstanceCommandCurrentAuthorizationPrecedesReplayAndClosedGates(t *testing.T) {
	id, key := uuid.NewString(), uuid.NewString()
	peer := &instanceReplayPeer{reply: &res.OperationResponse{DurableOwnerAck: &command.DurableOwnerAck{OperationId: uuid.NewString(), ResourceId: id, Accepted: true}}}
	authorization := &instanceCommandAuthorization{denied: true}
	svc := NewInstanceService(nil, NewInstanceGpuBinding(peer, authorization, "cluster"), instancePresetTenantResolver{}, nil, nil)
	request := &view.ChangeInstanceRequest{Data: &view.ChangeInstanceData{InstanceId: id, IdempotencyKey: key, ExpectedVersion: 7}}
	_, err := svc.RestartInstance(instancePresetContext(7), request)
	require.Error(t, err)
	require.Zero(t, peer.lookupCalls)
	authorization.denied = false
	// All admission flags are closed and no ledger exists: replay must finish first.
	svc.binding.newIntentsEnabled = false
	svc.binding.restartEnabled = false
	got, err := svc.RestartInstance(instancePresetContext(7), request)
	require.NoError(t, err)
	require.True(t, got.Replayed)
	require.Equal(t, 1, peer.lookupCalls)
	require.Equal(t, data.InstanceRestartPath, authorization.path)
	// Attach replay needs the attach authority only, and survives PVC disappearance.
	got, err = svc.AttachInstancePVC(instancePresetContext(7), &view.AttachInstancePVCRequest{Data: &view.AttachInstancePVCData{InstanceId: id, IdempotencyKey: key, ExpectedVersion: 7, PvcName: "gone-pvc", MountPath: "/datasets", RestartIfRunning: true}})
	require.NoError(t, err)
	require.True(t, got.Replayed)
	require.Equal(t, data.InstanceAttachPVCPath, authorization.path)
}
func TestInstanceDeleteRejectClassifierIsDefiniteAndExact(t *testing.T) {
	require.True(t, instanceDeleteVersionRejected(status.Error(codes.FailedPrecondition, "INSTANCE_VERSION_CONFLICT")))
	for _, err := range []error{status.Error(codes.DeadlineExceeded, "INSTANCE_VERSION_CONFLICT"), status.Error(codes.Unavailable, "INSTANCE_VERSION_CONFLICT"), status.Error(codes.Aborted, "INSTANCE_VERSION_CONFLICT"), status.Error(codes.FailedPrecondition, "VM_VERSION_CONFLICT"), status.Error(codes.FailedPrecondition, "INSTANCE_STATE_CONFLICT")} {
		require.False(t, instanceDeleteVersionRejected(err))
	}
}
