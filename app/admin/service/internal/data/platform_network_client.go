package data

import (
	"context"
	"github.com/google/uuid"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"strconv"
	"strings"
)

// platformCall rebuilds exclusive platform delegation after BFF authorization.
// It never forwards incoming identity headers and never creates a tenant UUID.
func platformCall[T any](c *NetworkClient, ctx context.Context, actor string, call func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.platform == nil || c.timeout <= 0 {
		return zero, status.Error(codes.Unavailable, "network client unavailable")
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(actor, "governance:user:"), 10, 32)
	if err != nil || id == 0 || actor != "governance:user:"+strconv.FormatUint(id, 10) {
		return zero, status.Error(codes.Unauthenticated, "invalid trusted platform identity")
	}
	if err := ctx.Err(); err != nil {
		return zero, status.FromContextError(err).Err()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-scope", "platform", "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return call(ctx)
}

func (c *NetworkClient) CreateVlanNetwork(ctx context.Context, actor string, req *networkv1.CreateVlanNetworkRequest) (*networkv1.CreateVlanNetworkResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.CreateVlanNetworkResponse, error) {
		return c.platform.CreateVlanNetwork(ctx, req)
	})
}

func (c *NetworkClient) GetVlanNetwork(ctx context.Context, actor string, req *networkv1.GetVlanNetworkRequest) (*networkv1.GetVlanNetworkResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.GetVlanNetworkResponse, error) {
		return c.platform.GetVlanNetwork(ctx, req)
	})
}

func (c *NetworkClient) ListVlanNetworks(ctx context.Context, actor string, req *networkv1.ListVlanNetworksRequest) (*networkv1.ListVlanNetworksResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.ListVlanNetworksResponse, error) {
		return c.platform.ListVlanNetworks(ctx, req)
	})
}

func (c *NetworkClient) DeleteVlanNetwork(ctx context.Context, actor string, req *networkv1.DeleteVlanNetworkRequest) (*networkv1.DeleteVlanNetworkResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.DeleteVlanNetworkResponse, error) {
		return c.platform.DeleteVlanNetwork(ctx, req)
	})
}

func (c *NetworkClient) CreateEgressGateway(ctx context.Context, actor string, req *networkv1.CreateEgressGatewayRequest) (*networkv1.CreateEgressGatewayResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.CreateEgressGatewayResponse, error) {
		return c.platform.CreateEgressGateway(ctx, req)
	})
}

func (c *NetworkClient) GetEgressGateway(ctx context.Context, actor string, req *networkv1.GetEgressGatewayRequest) (*networkv1.GetEgressGatewayResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.GetEgressGatewayResponse, error) {
		return c.platform.GetEgressGateway(ctx, req)
	})
}

func (c *NetworkClient) ListEgressGateways(ctx context.Context, actor string, req *networkv1.ListEgressGatewaysRequest) (*networkv1.ListEgressGatewaysResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.ListEgressGatewaysResponse, error) {
		return c.platform.ListEgressGateways(ctx, req)
	})
}

func (c *NetworkClient) DeleteEgressGateway(ctx context.Context, actor string, req *networkv1.DeleteEgressGatewayRequest) (*networkv1.DeleteEgressGatewayResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.DeleteEgressGatewayResponse, error) {
		return c.platform.DeleteEgressGateway(ctx, req)
	})
}

func (c *NetworkClient) CreatePublicAddressPool(ctx context.Context, actor string, req *networkv1.CreatePublicAddressPoolRequest) (*networkv1.CreatePublicAddressPoolResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.CreatePublicAddressPoolResponse, error) {
		return c.platform.CreatePublicAddressPool(ctx, req)
	})
}

func (c *NetworkClient) GetPublicAddressPool(ctx context.Context, actor string, req *networkv1.GetPublicAddressPoolRequest) (*networkv1.GetPublicAddressPoolResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.GetPublicAddressPoolResponse, error) {
		return c.platform.GetPublicAddressPool(ctx, req)
	})
}

func (c *NetworkClient) ListPublicAddressPools(ctx context.Context, actor string, req *networkv1.ListPublicAddressPoolsRequest) (*networkv1.ListPublicAddressPoolsResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.ListPublicAddressPoolsResponse, error) {
		return c.platform.ListPublicAddressPools(ctx, req)
	})
}

func (c *NetworkClient) DeletePublicAddressPool(ctx context.Context, actor string, req *networkv1.DeletePublicAddressPoolRequest) (*networkv1.DeletePublicAddressPoolResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.DeletePublicAddressPoolResponse, error) {
		return c.platform.DeletePublicAddressPool(ctx, req)
	})
}

func (c *NetworkClient) RecordPublicPoolVerification(ctx context.Context, actor string, req *networkv1.RecordPublicPoolVerificationRequest) (*networkv1.RecordPublicPoolVerificationResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.RecordPublicPoolVerificationResponse, error) {
		return c.platform.RecordPublicPoolVerification(ctx, req)
	})
}

func (c *NetworkClient) SetPublicPoolAllocationEnabled(ctx context.Context, actor string, req *networkv1.SetPublicPoolAllocationEnabledRequest) (*networkv1.SetPublicPoolAllocationEnabledResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.SetPublicPoolAllocationEnabledResponse, error) {
		return c.platform.SetPublicPoolAllocationEnabled(ctx, req)
	})
}

func (c *NetworkClient) SetDefaultPublicPool(ctx context.Context, actor string, req *networkv1.SetDefaultPublicPoolRequest) (*networkv1.SetDefaultPublicPoolResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.SetDefaultPublicPoolResponse, error) {
		return c.platform.SetDefaultPublicPool(ctx, req)
	})
}

func (c *NetworkClient) CreateIntranetAddressPool(ctx context.Context, actor string, req *networkv1.CreateIntranetAddressPoolRequest) (*networkv1.CreateIntranetAddressPoolResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.CreateIntranetAddressPoolResponse, error) {
		return c.platform.CreateIntranetAddressPool(ctx, req)
	})
}

func (c *NetworkClient) GetIntranetAddressPool(ctx context.Context, actor string, req *networkv1.GetIntranetAddressPoolRequest) (*networkv1.GetIntranetAddressPoolResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.GetIntranetAddressPoolResponse, error) {
		return c.platform.GetIntranetAddressPool(ctx, req)
	})
}

func (c *NetworkClient) ListIntranetAddressPools(ctx context.Context, actor string, req *networkv1.ListIntranetAddressPoolsRequest) (*networkv1.ListIntranetAddressPoolsResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.ListIntranetAddressPoolsResponse, error) {
		return c.platform.ListIntranetAddressPools(ctx, req)
	})
}

func (c *NetworkClient) DeleteIntranetAddressPool(ctx context.Context, actor string, req *networkv1.DeleteIntranetAddressPoolRequest) (*networkv1.DeleteIntranetAddressPoolResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.DeleteIntranetAddressPoolResponse, error) {
		return c.platform.DeleteIntranetAddressPool(ctx, req)
	})
}

func (c *NetworkClient) RecordIntranetPoolVerification(ctx context.Context, actor string, req *networkv1.RecordIntranetPoolVerificationRequest) (*networkv1.RecordIntranetPoolVerificationResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.RecordIntranetPoolVerificationResponse, error) {
		return c.platform.RecordIntranetPoolVerification(ctx, req)
	})
}

func (c *NetworkClient) SetIntranetPoolAllocationEnabled(ctx context.Context, actor string, req *networkv1.SetIntranetPoolAllocationEnabledRequest) (*networkv1.SetIntranetPoolAllocationEnabledResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.SetIntranetPoolAllocationEnabledResponse, error) {
		return c.platform.SetIntranetPoolAllocationEnabled(ctx, req)
	})
}

func (c *NetworkClient) SetDefaultIntranetPool(ctx context.Context, actor string, req *networkv1.SetDefaultIntranetPoolRequest) (*networkv1.SetDefaultIntranetPoolResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.SetDefaultIntranetPoolResponse, error) {
		return c.platform.SetDefaultIntranetPool(ctx, req)
	})
}

func (c *NetworkClient) GetPlatformNetworkCapabilities(ctx context.Context, actor string, req *networkv1.GetPlatformNetworkCapabilitiesRequest) (*networkv1.GetPlatformNetworkCapabilitiesResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.GetPlatformNetworkCapabilitiesResponse, error) {
		return c.platform.GetPlatformNetworkCapabilities(ctx, req)
	})
}

func (c *NetworkClient) GetPlatformOperation(ctx context.Context, actor string, req *networkv1.GetPlatformOperationRequest) (*networkv1.GetPlatformOperationResponse, error) {
	return platformCall(c, ctx, actor, func(ctx context.Context) (*networkv1.GetPlatformOperationResponse, error) {
		return c.platform.GetPlatformOperation(ctx, req)
	})
}
