package data

import (
	"context"
	"strconv"

	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"

	"go-wind-admin/pkg/authorizer"
	"go-wind-admin/pkg/constants"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/permcode"
)

// AuthorizerProvider 权限数据提供者
type AuthorizerProvider struct {
	log *bLogger.Helper

	roleRepo *RoleRepo
	apiRepo  *ApiRepo
}

func NewAuthorizerProvider(
	ctx *bootstrap.Context,
	roleRepo *RoleRepo,
	apiRepo *ApiRepo,
) authorizer.Provider {
	return &AuthorizerProvider{
		log:      ctx.NewLoggerHelper("authorizer-data-provider/data/admin-service"),
		roleRepo: roleRepo,
		apiRepo:  apiRepo,
	}
}

// ProvidePolicies 提供策略数据
func (p *AuthorizerProvider) ProvidePolicies(_ context.Context) (authorizer.PermissionDataMap, error) {
	// 策略装载需要全量角色/权限数据，统一以 SystemViewer 跑，忽略调用方 ctx 的 viewer
	ctx := appViewer.NewSystemViewerContext(context.Background())

	roles, err := p.roleRepo.List(ctx, &paginationV1.PagingRequest{NoPaging: trans.Ptr(true)})
	if err != nil {
		p.log.Errorf(context.Background(), "failed to list roles: %v", err)
		return nil, err
	}

	result := make(authorizer.PermissionDataMap)
	var apiIDs []uint32
	var apis []*permissionV1.Api
	for _, role := range roles.Items {
		//p.log.Infof(context.Background(), _, "processing role: %s", role.GetCode())
		if role == nil {
			continue
		}
		if role.GetCode() == "" {
			continue
		}
		if role.GetStatus() != permissionV1.Role_ON {
			continue
		}
		if constants.IsTemplateRoleCode(role.GetCode()) {
			continue
		}

		apiIDs, err = p.roleRepo.GetRolePermissionApiIDs(ctx, role.GetId())
		if err != nil {
			p.log.Errorf(context.Background(), "failed to get role [%d] permission api ids: %v", role.GetId(), err)
			continue
		}

		// 角色未绑定任何 API 时直接跳过：GetApiByIDs 对空集合返回 400，
		// 逐角色刷 ERROR 日志纯属误导。
		if len(apiIDs) == 0 {
			continue
		}

		apis, err = p.apiRepo.GetApiByIDs(ctx, apiIDs)
		if err != nil {
			p.log.Errorf(context.Background(), "failed to list apis by ids: %v", err)
			continue
		}

		var authorizerDataArray authorizer.PermissionDataArray
		for _, api := range apis {
			if api == nil {
				continue
			}
			if api.GetStatus() != permissionV1.Api_ON {
				continue
			}
			if api.GetPath() == "" || api.GetMethod() == "" {
				continue
			}

			var data = authorizer.PermissionData{
				Domain: strconv.FormatUint(uint64(role.GetTenantId()), 10),
				// 目录路径可能来自 OpenAPI（驼峰参数名），运行时模板来自 proto（蛇形）；
				// 策略侧与请求侧都按参数段归一化，避免同一路由因参数名不同而匹配失败。
				Path:   permcode.NormalizePath(api.GetPath()),
				Method: api.GetMethod(),
			}
			authorizerDataArray = append(authorizerDataArray, data)
		}
		if len(authorizerDataArray) > 0 {
			// The same role code can exist in several tenants. Keep each domain's
			// policies; assigning here loses every earlier tenant's bindings.
			result[role.GetCode()] = append(result[role.GetCode()], authorizerDataArray...)
		}
	}

	return result, nil
}
