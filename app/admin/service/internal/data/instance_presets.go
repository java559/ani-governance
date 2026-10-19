package data

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	pb "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const InstanceAdminPresetsPath = "/api/v1/admin/compute-presets"
const InstanceAdminPresetPath = "/api/v1/admin/compute-presets/{id}"
const InstanceAdminPresetEnabledPath = "/api/v1/admin/compute-presets/{id}:set-enabled"

func instancePlatformCall[T any](c *InstanceClient, ctx context.Context, actor string, invoke func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.client == nil {
		return zero, status.Error(codes.Unavailable, "INSTANCE_ADAPTER_UNAVAILABLE")
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(actor, "governance:user:"), 10, 32)
	if err != nil || id == 0 || actor != "governance:user:"+strconv.FormatUint(id, 10) {
		return zero, status.Error(codes.Unauthenticated, "INVALID_INSTANCE_CALLER")
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call = metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-scope", "platform", "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return invoke(call)
}
func (c *InstanceClient) AdminListPresets(ctx context.Context, actor string, r *pb.AdminListPresetsRequest) (*pb.ListPresetsResponse, error) {
	return instancePlatformCall(c, ctx, actor, func(call context.Context) (*pb.ListPresetsResponse, error) { return c.client.AdminListPresets(call, r) })
}
func (c *InstanceClient) AdminGetPreset(ctx context.Context, actor string, r *pb.AdminGetPresetRequest) (*pb.Preset, error) {
	return instancePlatformCall(c, ctx, actor, func(call context.Context) (*pb.Preset, error) { return c.client.AdminGetPreset(call, r) })
}
func (c *InstanceClient) AdminCreatePreset(ctx context.Context, actor string, r *pb.AdminCreatePresetRequest) (*pb.Preset, error) {
	return instancePlatformCall(c, ctx, actor, func(call context.Context) (*pb.Preset, error) { return c.client.AdminCreatePreset(call, r) })
}
func (c *InstanceClient) AdminUpdatePreset(ctx context.Context, actor string, r *pb.AdminUpdatePresetRequest) (*pb.Preset, error) {
	return instancePlatformCall(c, ctx, actor, func(call context.Context) (*pb.Preset, error) { return c.client.AdminUpdatePreset(call, r) })
}
func (c *InstanceClient) AdminSetPresetEnabled(ctx context.Context, actor string, r *pb.AdminSetPresetEnabledRequest) (*pb.Preset, error) {
	return instancePlatformCall(c, ctx, actor, func(call context.Context) (*pb.Preset, error) { return c.client.AdminSetPresetEnabled(call, r) })
}
func (c *InstanceClient) AdminDeletePreset(ctx context.Context, actor string, r *pb.AdminDeletePresetRequest) (*emptypb.Empty, error) {
	return instancePlatformCall(c, ctx, actor, func(call context.Context) (*emptypb.Empty, error) { return c.client.AdminDeletePreset(call, r) })
}
