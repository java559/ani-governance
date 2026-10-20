package data

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	pb "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type ComputeClient struct{ client pb.ComputePresetServiceClient }

func computeCall[T any](c *ComputeClient, ctx context.Context, tenant, actor string, invoke func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.client == nil {
		return zero, status.Error(codes.Unavailable, "COMPUTE_ADAPTER_UNAVAILABLE")
	}
	if !validResourceIdentity(tenant, actor) {
		return zero, status.Error(codes.Unauthenticated, "INVALID_COMPUTE_CALLER")
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call = metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return invoke(call)
}

func validComputeUsage(usage pb.Usage) bool {
	return usage == pb.Usage_CONTAINER_INSTANCE || usage == pb.Usage_TRAINING || usage == pb.Usage_INFERENCE
}

func (c *ComputeClient) ListPresets(ctx context.Context, tenant, actor string, request *pb.ListPresetsRequest) (*pb.ListPresetsResponse, error) {
	if request == nil || request.TenantId != tenant || !validComputeUsage(request.Usage) {
		return nil, status.Error(codes.InvalidArgument, "INVALID_COMPUTE_REQUEST")
	}
	return computeCall(c, ctx, tenant, actor, func(call context.Context) (*pb.ListPresetsResponse, error) {
		return c.client.ListPresets(call, request)
	})
}

// ResolveComputePreset is a read-only candidate call for registered consumers.
// Container creation continues to call only InstanceClient.Resolve.
func (c *ComputeClient) ResolveComputePreset(ctx context.Context, tenant, actor string, request *pb.ResolveComputePresetRequest) (*pb.ComputeSelection, error) {
	if request == nil || request.TenantId != tenant || !validComputeUsage(request.Usage) {
		return nil, status.Error(codes.InvalidArgument, "INVALID_COMPUTE_REQUEST")
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	selection, err := computeCall(c, call, tenant, actor, func(call context.Context) (*pb.ComputeSelection, error) {
		return c.client.ResolveComputePreset(call, request)
	})
	if err != nil {
		return nil, err
	}
	if pb.ValidateSelection(selection) != nil || selection.PresetId != request.PresetId {
		return nil, status.Error(codes.Unavailable, "COMPUTE_INVALID_RESPONSE")
	}
	return selection, nil
}

const ComputeAdminPresetsPath = "/api/v1/admin/compute-presets"
const ComputeAdminPresetPath = "/api/v1/admin/compute-presets/{id}"
const ComputeAdminPresetEnabledPath = "/api/v1/admin/compute-presets/{id}:set-enabled"

func computePlatformCall[T any](c *ComputeClient, ctx context.Context, actor string, invoke func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.client == nil {
		return zero, status.Error(codes.Unavailable, "COMPUTE_ADAPTER_UNAVAILABLE")
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(actor, "governance:user:"), 10, 32)
	if err != nil || id == 0 || actor != "governance:user:"+strconv.FormatUint(id, 10) {
		return zero, status.Error(codes.Unauthenticated, "INVALID_COMPUTE_CALLER")
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call = metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-scope", "platform", "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return invoke(call)
}
func (c *ComputeClient) AdminListPresets(ctx context.Context, actor string, r *pb.AdminListPresetsRequest) (*pb.ListPresetsResponse, error) {
	return computePlatformCall(c, ctx, actor, func(call context.Context) (*pb.ListPresetsResponse, error) { return c.client.AdminListPresets(call, r) })
}
func (c *ComputeClient) AdminGetPreset(ctx context.Context, actor string, r *pb.AdminGetPresetRequest) (*pb.Preset, error) {
	return computePlatformCall(c, ctx, actor, func(call context.Context) (*pb.Preset, error) { return c.client.AdminGetPreset(call, r) })
}
func (c *ComputeClient) AdminCreatePreset(ctx context.Context, actor string, r *pb.AdminCreatePresetRequest) (*pb.Preset, error) {
	return computePlatformCall(c, ctx, actor, func(call context.Context) (*pb.Preset, error) { return c.client.AdminCreatePreset(call, r) })
}
func (c *ComputeClient) AdminUpdatePreset(ctx context.Context, actor string, r *pb.AdminUpdatePresetRequest) (*pb.Preset, error) {
	return computePlatformCall(c, ctx, actor, func(call context.Context) (*pb.Preset, error) { return c.client.AdminUpdatePreset(call, r) })
}
func (c *ComputeClient) AdminSetPresetEnabled(ctx context.Context, actor string, r *pb.AdminSetPresetEnabledRequest) (*pb.Preset, error) {
	return computePlatformCall(c, ctx, actor, func(call context.Context) (*pb.Preset, error) { return c.client.AdminSetPresetEnabled(call, r) })
}
func (c *ComputeClient) AdminDeletePreset(ctx context.Context, actor string, r *pb.AdminDeletePresetRequest) (*emptypb.Empty, error) {
	return computePlatformCall(c, ctx, actor, func(call context.Context) (*emptypb.Empty, error) { return c.client.AdminDeletePreset(call, r) })
}
