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
	"google.golang.org/protobuf/proto"

	"go-wind-admin/pkg/middleware/auth"
)

func TestAcceleratorCurrentViewsPreservePresenceAndRedactSourceEvidence(t *testing.T) {
	device := wireAcceleratorDevice(&acc.PhysicalDevice{DeviceId: "device", DeviceUid: "Serial-ABC", DeviceKind: acc.DeviceKind_PHYSICAL_DEVICE, AdapterId: "hygon-hcu-v1", MemoryCapacityBytes: proto.Uint64(0), Observation: &acc.Observation{Complete: true, SnapshotRef: "secret-watermark"}})
	raw, e := protojson.Marshal(device)
	require.NoError(t, e)
	require.NotContains(t, string(raw), "secret-")
	require.Contains(t, string(raw), `"memory_capacity_bytes":"0"`)
	require.NotNil(t, device.MemoryCapacityBytes)
	require.Nil(t, device.ComputeUnits)
	capacity := wireAcceleratorProfileCapacity(&acc.ProfileCapacity{ProfileId: "profile", ProfileVersion: 1, AllocationType: acc.AllocationType_FIXED_PARTITION, Basis: acc.CapacityBasis_BOUND_POD_REQUEST, Granularity: acc.CapacityGranularity_NODE_RESOURCE, NominalUnits: proto.Int64(0), Totals: &acc.CapacityTotals{TotalUnits: proto.Int64(0)}, Details: []*acc.CapacityDetail{{Totals: &acc.CapacityTotals{AllocatedUnits: proto.Int64(0)}}}})
	require.NotNil(t, capacity.NominalUnits)
	require.EqualValues(t, 0, *capacity.NominalUnits)
	require.Nil(t, capacity.EstimatedAvailableUnits)
	require.NotNil(t, capacity.Details[0].Totals.AllocatedUnits)
	require.Nil(t, capacity.Details[0].Totals.TotalUnits)
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
	denied, err := status.New(codes.PermissionDenied, "private details").WithDetails(&errdetails.ErrorInfo{Reason: "CLUSTER_REGISTRATION_NOT_AUTHORIZED", Domain: "accelerator"})
	require.NoError(t, err)
	mapped := errors.FromError(mapAcceleratorError(denied.Err()))
	require.EqualValues(t, 403, mapped.Code)
	require.Equal(t, "CLUSTER_REGISTRATION_NOT_AUTHORIZED", mapped.Reason)
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
