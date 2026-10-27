package data

import (
	"context"
	identity "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

const InstanceCreatePath = "/api/v1/instances"
const InstanceDeletePath = "/api/v1/instances/{data.instance_id}"
const InstanceStartPath = "/api/v1/instances/{data.instance_id}:start"
const InstanceStopPath = "/api/v1/instances/{data.instance_id}:stop"
const InstanceMountablePVCsPath = "/api/v1/instances/mountable-pvcs"
const InstanceConfigurationPath = "/api/v1/instances/{instance_id}/configuration"
const InstanceGetPath = "/api/v1/instances/{instance_id}"
const InstanceOperationPath = "/api/v1/instance-operations/{operation_id}"
const InstanceExecPath = "/api/v1/instances/{data.instance_id}/exec"
const InstanceRuntimePath = "/api/v1/instances/{instance_id}/runtime"
const InstanceEventsPath = "/api/v1/instances/{instance_id}/events"
const InstanceLogsPath = "/api/v1/instances/{instance_id}/logs"
const InstanceMetricsQueryPath = "/api/v1/instances/{instance_id}/metrics/query"
const InstanceMetricsLatestPath = "/api/v1/instances/{instance_id}/metrics/latest"

// ObservabilityResourceTrendPath 是首页资源趋势路由。按 2026-10-27 定案挂
// INSTANCE 模块（现有套餐 plan2/plan3 均含 INSTANCE、均无 DASHBOARD），
// 权限码为 observability:resource-trend:read，经 InstanceAuthorizationRepo 走租户闸门。
const ObservabilityResourceTrendPath = "/api/v1/observability/resource-trend"

// OverviewPath 是首页资源统计卡片聚合路由；入口权限码 overview:resource-count:read
// 同样挂 INSTANCE 模块，数据源二次鉴权复用各自列表路由。
const OverviewPath = "/api/v1/overview"

// OverviewModelsPath 是首页模型卡片的二次鉴权路由（MODEL 模块）。
const OverviewModelsPath = "/api/v1/models"

func InstanceModuleRegistered() bool {
	id, ok := identity.Module_value["INSTANCE"]
	return ok && id > 0
}

type InstanceAuthorizationRepo struct{ current *ActionAuthorizationRepo }

func NewInstanceAuthorizationRepo(c *entCrud.EntClient[*ent.Client]) *InstanceAuthorizationRepo {
	return &InstanceAuthorizationRepo{NewActionAuthorizationRepo(c)}
}
func (r *InstanceAuthorizationRepo) Authorize(ctx context.Context, tenant, user uint32, path, method string) error {
	if !InstanceModuleRegistered() {
		return ErrActionAuthorizationDenied
	}
	allowed := false
	switch path {
	case InstanceCreatePath:
		allowed = method == "POST" || method == "GET"
	case InstanceDeletePath:
		allowed = method == "DELETE"
	case InstanceStartPath, InstanceStopPath, InstanceExecPath, InstanceMetricsQueryPath:
		allowed = method == "POST"
	case InstanceGetPath, InstanceOperationPath, InstanceRuntimePath, InstanceEventsPath, InstanceLogsPath, InstanceMountablePVCsPath, InstanceConfigurationPath, InstanceMetricsLatestPath:
		allowed = method == "GET"
	case ObservabilityResourceTrendPath, OverviewPath:
		allowed = method == "GET"
	}
	if !allowed || r == nil || r.current == nil {
		return ErrActionAuthorizationDenied
	}
	return r.current.authorizeRoute(ctx, tenant, user, path, "INSTANCE", method)
}

// AuthorizeSource 供首页聚合按数据源二次鉴权：只允许 GET，模块由调用方指定
// （模型 MODEL、推理 INFERENCE），未识别模块按 fail-closed 拒绝。
func (r *InstanceAuthorizationRepo) AuthorizeSource(ctx context.Context, tenant, user uint32, path, module string) error {
	switch module {
	case "MODEL", "INFERENCE":
	default:
		return ErrActionAuthorizationDenied
	}
	if r == nil || r.current == nil {
		return ErrActionAuthorizationUnavailable
	}
	return r.current.authorizeRoute(ctx, tenant, user, path, module, "GET")
}

// The GPU catalog intersection uses the caller's current ACC permission and
// entitlement as well as the independent INSTANCE entry gate.
func (r *InstanceAuthorizationRepo) AuthorizeCatalog(ctx context.Context, tenant, user uint32) error {
	if r == nil || r.current == nil {
		return ErrActionAuthorizationDenied
	}
	return r.current.authorizeRoute(ctx, tenant, user, "/api/v1/accelerator/profiles", "ACCELERATOR", "GET")
}

func (r *InstanceAuthorizationRepo) AuthorizeGpuUse(ctx context.Context, tenant, user uint32) error {
	if r == nil || r.current == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrActionAuthorizationUnavailable
	}
	return NewGpuUseAuthorizationRepo(r.current.entClient).AuthorizeGpuUse(ctx, tenant, user)
}
