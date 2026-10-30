package data

import (
	"context"

	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// All platform lifecycle stubs share the managed Resource mTLS connection and
// the exclusive platform delegation rebuilt by platformCall. No retry is added.

func (c *NetworkClient) CreateVirtualSwitch(ctx context.Context, actor string, req *networkv1.CreateVirtualSwitchRequest) (*networkv1.CreateVirtualSwitchResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.CreateVirtualSwitchResponse, error) {
		return c.platform.CreateVirtualSwitch(ctx, req)
	})
}

func (c *NetworkClient) GetVirtualSwitch(ctx context.Context, actor string, req *networkv1.GetVirtualSwitchRequest) (*networkv1.GetVirtualSwitchResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.GetVirtualSwitchResponse, error) {
		return c.platform.GetVirtualSwitch(ctx, req)
	})
}

func (c *NetworkClient) ListVirtualSwitches(ctx context.Context, actor string, req *networkv1.ListVirtualSwitchesRequest) (*networkv1.ListVirtualSwitchesResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.ListVirtualSwitchesResponse, error) {
		return c.platform.ListVirtualSwitches(ctx, req)
	})
}

func (c *NetworkClient) DeleteVirtualSwitch(ctx context.Context, actor string, req *networkv1.DeleteVirtualSwitchRequest) (*networkv1.DeleteVirtualSwitchResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.DeleteVirtualSwitchResponse, error) {
		return c.platform.DeleteVirtualSwitch(ctx, req)
	})
}

func (c *NetworkClient) CreateDefaultVPCEgress(ctx context.Context, actor string, req *networkv1.CreateDefaultVPCEgressRequest) (*networkv1.CreateDefaultVPCEgressResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.CreateDefaultVPCEgressResponse, error) {
		if c.defaultEgress == nil {
			return nil, status.Error(codes.Unavailable, "network client unavailable")
		}
		return c.defaultEgress.CreateDefaultVPCEgress(ctx, req)
	})
}

func (c *NetworkClient) GetDefaultVPCEgress(ctx context.Context, actor string, req *networkv1.GetDefaultVPCEgressRequest) (*networkv1.GetDefaultVPCEgressResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.GetDefaultVPCEgressResponse, error) {
		if c.defaultEgress == nil {
			return nil, status.Error(codes.Unavailable, "network client unavailable")
		}
		return c.defaultEgress.GetDefaultVPCEgress(ctx, req)
	})
}
func (c *NetworkClient) GetCurrentDefaultVPCEgress(ctx context.Context, actor string, req *networkv1.GetCurrentDefaultVPCEgressRequest) (*networkv1.GetCurrentDefaultVPCEgressResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.GetCurrentDefaultVPCEgressResponse, error) {
		if c.defaultEgress == nil {
			return nil, status.Error(codes.Unavailable, "network client unavailable")
		}
		return c.defaultEgress.GetCurrentDefaultVPCEgress(ctx, req)
	})
}

func (c *NetworkClient) DeleteDefaultVPCEgress(ctx context.Context, actor string, req *networkv1.DeleteDefaultVPCEgressRequest) (*networkv1.DeleteDefaultVPCEgressResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.DeleteDefaultVPCEgressResponse, error) {
		if c.defaultEgress == nil {
			return nil, status.Error(codes.Unavailable, "network client unavailable")
		}
		return c.defaultEgress.DeleteDefaultVPCEgress(ctx, req)
	})
}
