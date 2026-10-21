package service

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"go-wind-admin/pkg/middleware/auth"
)

func TestAcceleratorPublicViewsAreStructurallyRedacted(t *testing.T) {
	ref := &acc.GpuUsageRef{TenantId: "secret-tenant", OwnerService: InstanceOwner, ResourceId: "resource", CreateOperationId: "create"}
	binding := &acc.GpuBinding{BindingId: "binding", VerifiedUsageRef: ref, Pod: &acc.PodRef{ClusterId: "secret-cluster", Namespace: "secret-namespace", Name: "secret-pod", Uid: "secret-uid", ContainerName: "workspace"}, PhysicalDeviceId: "secret-device", State: acc.AllocationState_ACTIVE, MemoryMib: 6144, CoreLimitPercent: 25, SourceBaselineDigest: "secret-baseline", SourceMemoryBlockMib: 1024, EvidenceRef: "secret-evidence", Observation: &acc.Observation{Complete: true, SnapshotRef: "secret-watermark", ObservedAt: "2026-09-23T10:00:00Z"}}
	raw, e := protojson.Marshal(wireAcceleratorBinding(binding))
	require.NoError(t, e)
	require.NotContains(t, string(raw), "secret-")
	require.Contains(t, string(raw), "workspace")
	require.NotContains(t, string(raw), InstanceOwner)
	require.Contains(t, string(raw), `"6144"`)
	projection := &acc.GpuUsageProjection{Ref: ref, Revision: 2, State: acc.UsageState_ENDED, SourceFactRef: "secret-fact", PayloadDigest: "secret-payload", ProfileId: "profile", ProfileVersion: 1, ExecutionDigest: "secret-execution", BaselineId: "secret-baseline"}
	raw, e = protojson.Marshal(wireAcceleratorUsage(projection))
	require.NoError(t, e)
	require.NotContains(t, string(raw), "secret-")
	require.Contains(t, string(raw), "ENDED")
	capacity := wireAcceleratorCapacity(&acc.CapacityView{Devices: []*acc.DeviceCapacity{{DeviceId: "secret-device"}}, Observation: &acc.Observation{Complete: false, SnapshotRef: "secret-watermark"}}, false)
	require.Empty(t, capacity.Devices)
	require.False(t, capacity.Observation.Complete)
}

func TestAcceleratorErrorMappingKeepsReasonAndDropsRawContent(t *testing.T) {
	for code, want := range map[codes.Code]int32{codes.Unauthenticated: 503, codes.PermissionDenied: 503, codes.NotFound: 404, codes.InvalidArgument: 400, codes.AlreadyExists: 409, codes.Aborted: 409, codes.FailedPrecondition: 412, codes.Unavailable: 503, codes.DeadlineExceeded: 504, codes.Internal: 503, codes.Unknown: 503} {
		st, e := status.New(code, "secret-dsn provider-raw token").WithDetails(&errdetails.ErrorInfo{Reason: "SOURCE_UNKNOWN", Domain: "accelerator"})
		require.NoError(t, e)
		mapped := errors.FromError(mapAcceleratorError(st.Err()))
		require.Equal(t, want, mapped.Code)
		if code == codes.Unauthenticated || code == codes.PermissionDenied {
			require.Equal(t, "ACCELERATOR_SERVICE_ACCESS_DENIED", mapped.Reason)
		} else {
			require.Equal(t, "SOURCE_UNKNOWN", mapped.Reason)
		}
		require.NotContains(t, mapped.Message, "secret")
	}
	st, e := status.New(codes.Internal, "raw").WithDetails(&errdetails.ErrorInfo{Reason: "postgres://secret"})
	require.NoError(t, e)
	require.Equal(t, "ACCELERATOR_ERROR", errors.FromError(mapAcceleratorError(st.Err())).Reason)
}

func TestAcceleratorPrincipalRequiresExplicitScopeAndJWT(t *testing.T) {
	_, e := acceleratorPrincipal(context.Background(), false)
	require.EqualValues(t, 401, errors.FromError(e).Code)
	for _, p := range []*auth.Principal{{Type: auth.SubjectAPIKey, ID: 1, TenantID: 1}, {Type: auth.SubjectUser, ID: 1, TenantID: 0}, {Type: auth.SubjectUser, ID: 0, TenantID: 1}} {
		_, e = acceleratorPrincipal(auth.NewPrincipalContext(context.Background(), p), false)
		require.Error(t, e)
	}
	_, e = acceleratorPrincipal(auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 1, TenantID: 1}), true)
	require.Error(t, e)
}
