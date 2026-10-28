package data

import (
	"context"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
)

func (c *NetworkClient) ListPlatformEIPs(ctx context.Context, actor string, r *networkv1.ListPlatformEIPsRequest) (*networkv1.ListPlatformEIPsResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.ListPlatformEIPsResponse, error) {
		return c.platformEIPs.ListPlatformEIPs(ctx, r)
	})
}
func (c *NetworkClient) GetPlatformEIP(ctx context.Context, actor string, r *networkv1.GetPlatformEIPRequest) (*networkv1.GetPlatformEIPResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.GetPlatformEIPResponse, error) {
		return c.platformEIPs.GetPlatformEIP(ctx, r)
	})
}
func (c *NetworkClient) ReleasePlatformEIP(ctx context.Context, actor string, r *networkv1.ReleasePlatformEIPRequest) (*networkv1.ReleasePlatformEIPResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.ReleasePlatformEIPResponse, error) {
		return c.platformEIPs.ReleasePlatformEIP(ctx, r)
	})
}
