package data

import (
	"context"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
)

func (c *InstanceClient) SetSecurityGroups(ctx context.Context, tenant, actor string, request *res.SetSecurityGroupsRequest) (*res.OperationResponse, error) {
	return instanceCall(c, ctx, tenant, actor, func(ctx context.Context) (*res.OperationResponse, error) {
		return c.client.SetSecurityGroups(ctx, request)
	})
}
