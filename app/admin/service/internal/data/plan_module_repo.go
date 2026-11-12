package data

import (
	"context"
	"time"

	"entgo.io/ent/dialect/sql"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"

	"go-wind-admin/pkg/localdeps/go-utils/copierutil"
	"go-wind-admin/pkg/localdeps/go-utils/mapper"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/predicate"
	tenantEntity "go-wind-admin/app/admin/service/internal/data/ent/tenant"

	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

type PlanModuleRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
	log       *bLogger.Helper

	mapper     *mapper.CopierMapper[identityV1.PlanModule, ent.PlanModule]
	moduleConv *mapper.EnumTypeConverter[identityV1.Module, planmodule.Module]

	repository *entCrud.Repository[
		ent.PlanModuleQuery, ent.PlanModuleSelect,
		ent.PlanModuleCreate, ent.PlanModuleCreateBulk,
		ent.PlanModuleUpdate, ent.PlanModuleUpdateOne,
		ent.PlanModuleDelete,
		predicate.PlanModule,
		identityV1.PlanModule, ent.PlanModule,
	]
}

func NewPlanModuleRepo(
	ctx *bootstrap.Context,
	entClient *entCrud.EntClient[*ent.Client],
) *PlanModuleRepo {
	repo := &PlanModuleRepo{
		log:       ctx.NewLoggerHelper("plan-module/repo/admin-service"),
		entClient: entClient,
		mapper:    mapper.NewCopierMapper[identityV1.PlanModule, ent.PlanModule](),
		moduleConv: mapper.NewEnumTypeConverter[identityV1.Module, planmodule.Module](
			identityV1.Module_name, identityV1.Module_value,
		),
	}

	repo.init()

	return repo
}

func (r *PlanModuleRepo) init() {
	r.repository = entCrud.NewRepository[
		ent.PlanModuleQuery, ent.PlanModuleSelect,
		ent.PlanModuleCreate, ent.PlanModuleCreateBulk,
		ent.PlanModuleUpdate, ent.PlanModuleUpdateOne,
		ent.PlanModuleDelete,
		predicate.PlanModule,
		identityV1.PlanModule, ent.PlanModule,
	](r.mapper)

	r.mapper.AppendConverters(copierutil.NewTimeStringConverterPair())
	r.mapper.AppendConverters(copierutil.NewTimeTimestamppbConverterPair())

	r.mapper.AppendConverters(r.moduleConv.NewConverterPair())
}

func (r *PlanModuleRepo) Count(ctx context.Context, whereCond []func(s *sql.Selector)) (int, error) {
	builder := r.entClient.Client().PlanModule.Query()
	if len(whereCond) != 0 {
		builder.Modify(whereCond...)
	}

	count, err := builder.Count(ctx)
	if err != nil {
		r.log.Errorf(ctx, "query count failed: %s", err.Error())
		return 0, identityV1.ErrorInternalServerError("query count failed")
	}

	return count, nil
}

func (r *PlanModuleRepo) List(ctx context.Context, req *paginationV1.PagingRequest) (*identityV1.ListPlanModuleResponse, error) {
	if req == nil {
		return nil, identityV1.ErrorBadRequest("invalid parameter")
	}

	builder := r.entClient.Client().PlanModule.Query().WithPlan()

	whereSelectors, _, err := r.repository.BuildListSelectorWithPaging(builder, req)
	if err != nil {
		r.log.Errorf(ctx, "parse list param error [%s]", err.Error())
		return nil, identityV1.ErrorBadRequest("invalid query parameter")
	}

	entities, err := builder.All(ctx)
	if err != nil {
		r.log.Errorf(ctx, "query plan module list failed: %s", err.Error())
		return nil, identityV1.ErrorInternalServerError("query plan module list failed")
	}

	dtos := make([]*identityV1.PlanModule, 0, len(entities))
	for _, entity := range entities {
		dto := r.mapper.ToDTO(entity)
		if entity.Edges.Plan != nil {
			dto.PlanId = &entity.Edges.Plan.ID
		}
		dtos = append(dtos, dto)
	}

	count, err := r.Count(ctx, whereSelectors)
	if err != nil {
		return nil, err
	}

	return &identityV1.ListPlanModuleResponse{
		Total: uint64(count),
		Items: dtos,
	}, nil
}

func (r *PlanModuleRepo) IsExist(ctx context.Context, id uint32) (bool, error) {
	exist, err := r.entClient.Client().PlanModule.Query().
		Where(planmodule.IDEQ(id)).
		Exist(ctx)
	if err != nil {
		r.log.Errorf(ctx, "query exist failed: %s", err.Error())
		return false, identityV1.ErrorInternalServerError("query exist failed")
	}
	return exist, nil
}

func (r *PlanModuleRepo) Get(ctx context.Context, req *identityV1.GetPlanModuleRequest) (*identityV1.PlanModule, error) {
	if req == nil {
		return nil, identityV1.ErrorBadRequest("invalid parameter")
	}

	builder := r.entClient.Client().PlanModule.Query()

	var whereCond []func(s *sql.Selector)
	switch req.QueryBy.(type) {
	default:
	case *identityV1.GetPlanModuleRequest_Id:
		whereCond = append(whereCond, planmodule.IDEQ(req.GetId()))
	}

	dto, err := r.repository.Get(ctx, builder, req.GetViewMask(), whereCond...)
	if err != nil {
		return nil, err
	}

	return dto, err
}

func (r *PlanModuleRepo) Create(ctx context.Context, req *identityV1.CreatePlanModuleRequest) (err error) {
	if req == nil || req.Data == nil {
		return identityV1.ErrorBadRequest("invalid parameter")
	}

	var tx *ent.Tx
	tx, err = r.entClient.Client().Tx(ctx)
	if err != nil {
		r.log.Errorf(ctx, "start transaction failed: %s", err.Error())
		return identityV1.ErrorInternalServerError("start transaction failed")
	}
	defer func() {
		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				r.log.Errorf(ctx, "transaction rollback failed: %s", rollbackErr.Error())
			}
			return
		}
		if commitErr := tx.Commit(); commitErr != nil {
			r.log.Errorf(ctx, "transaction commit failed: %s", commitErr.Error())
			err = identityV1.ErrorInternalServerError("transaction commit failed")
		}
	}()

	builder := tx.PlanModule.Create().
		SetNillableCreatedBy(req.Data.CreatedBy).
		SetCreatedAt(time.Now())

	// module 为 proto 零值（MODULE_UNSPECIFIED）时跳过：ent schema 未声明该值，
	// SetNillableModule 会触发 ModuleValidator 失败。未指定即留空。
	if req.Data.Module != nil && *req.Data.Module != identityV1.Module_MODULE_UNSPECIFIED {
		builder.SetNillableModule(r.moduleConv.ToEntity(req.Data.Module))
	}

	// 此前守卫倒置（PlanId == nil 才设置），导致 plan_id 永远写空、
	// 套餐模块关联自始无法通过 API 建立。有值即设置。
	if req.Data.PlanId != nil {
		builder.SetPlanID(req.Data.GetPlanId())
	}

	if req.Data.Id != nil {
		builder.SetID(req.GetData().GetId())
	}

	if _, err = builder.Save(ctx); err != nil {
		r.log.Errorf(ctx, "insert plan module failed: %s", err.Error())
		return identityV1.ErrorInternalServerError("insert plan module failed")
	}

	return nil
}

func (r *PlanModuleRepo) Update(ctx context.Context, req *identityV1.UpdatePlanModuleRequest) (err error) {
	if req == nil || req.Data == nil {
		return identityV1.ErrorBadRequest("invalid parameter")
	}
	if req.GetId() == 0 {
		return identityV1.ErrorBadRequest("id is required")
	}

	// 如果不存在则创建
	if req.GetAllowMissing() {
		var exist bool
		exist, err = r.IsExist(ctx, req.GetId())
		if err != nil {
			return err
		}
		if !exist {
			createReq := &identityV1.CreatePlanModuleRequest{Data: req.Data}
			createReq.Data.CreatedBy = createReq.Data.UpdatedBy
			createReq.Data.UpdatedBy = nil
			return r.Create(ctx, createReq)
		}
	}

	var tx *ent.Tx
	tx, err = r.entClient.Client().Tx(ctx)
	if err != nil {
		r.log.Errorf(ctx, "start transaction failed: %s", err.Error())
		return identityV1.ErrorInternalServerError("start transaction failed")
	}
	defer func() {
		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				r.log.Errorf(ctx, "transaction rollback failed: %s", rollbackErr.Error())
			}
			return
		}
		if commitErr := tx.Commit(); commitErr != nil {
			r.log.Errorf(ctx, "transaction commit failed: %s", commitErr.Error())
			err = identityV1.ErrorInternalServerError("transaction commit failed")
		}
	}()

	builder := tx.PlanModule.UpdateOneID(req.GetId())
	_, err = r.repository.UpdateOne(ctx, builder, req.Data, req.GetUpdateMask(),
		func(dto *identityV1.PlanModule) {
			builder.
				SetNillableUpdatedBy(req.Data.UpdatedBy).
				SetUpdatedAt(time.Now())

			// module 为 proto 零值（MODULE_UNSPECIFIED）时跳过：ent schema 未声明该值，
			// SetNillableModule 会触发 ModuleValidator 失败。未指定即不更新该字段。
			if req.Data.Module != nil && *req.Data.Module != identityV1.Module_MODULE_UNSPECIFIED {
				builder.SetNillableModule(r.moduleConv.ToEntity(req.Data.Module))
			}
		},
		func(s *sql.Selector) {
			s.Where(sql.EQ(planmodule.FieldID, req.GetId()))
		},
	)
	if err != nil {
		r.log.Errorf(ctx, "update plan module failed: %s", err.Error())
		return identityV1.ErrorInternalServerError("update plan module failed")
	}

	return err
}

func (r *PlanModuleRepo) Delete(ctx context.Context, id uint32) error {
	if id == 0 {
		return identityV1.ErrorBadRequest("invalid parameter")
	}

	if err := r.entClient.Client().PlanModule.DeleteOneID(id).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return identityV1.ErrorNotFound("plan module not found")
		}

		r.log.Errorf(ctx, "delete one data failed: %s", err.Error())

		return identityV1.ErrorInternalServerError("delete failed")
	}

	return nil
}

// ListModulesByPlanId 返回指定套餐允许的模块集合。
// 用于菜单树过滤和请求拦截的白名单判定。
func (r *PlanModuleRepo) ListModulesByPlanId(ctx context.Context, planId uint32) (map[identityV1.Module]bool, error) {
	if planId == 0 {
		return nil, nil
	}
	entities, err := r.entClient.Client().PlanModule.Query().
		Where(planmodule.HasPlanWith(plan.IDEQ(planId))).
		All(ctx)
	if err != nil {
		r.log.Errorf(ctx, "query plan modules by plan id failed: %s", err.Error())
		return nil, identityV1.ErrorInternalServerError("query plan modules failed")
	}
	result := make(map[identityV1.Module]bool, len(entities))
	for _, e := range entities {
		if e.Module != nil {
			if module := mapEntModuleToProto(*e.Module); module != identityV1.Module_MODULE_UNSPECIFIED {
				result[module] = true
			}
		}
	}
	return result, nil
}

// mapEntModuleToProto 将 ent planmodule.Module 字符串枚举映射到 proto identityV1.Module。
func mapEntModuleToProto(m planmodule.Module) identityV1.Module {
	value, ok := identityV1.Module_value[string(m)]
	if !ok {
		return identityV1.Module_MODULE_UNSPECIFIED
	}
	return identityV1.Module(value)
}

// TenantExists 报告目标租户是否存在且未删除，不检查状态与套餐。
// 平台代管的查看、停用、注销类操作使用它：即使目标租户已停用或套餐已变更，
// 也要能读到状态并回收身份。
func (r *PlanModuleRepo) TenantExists(ctx context.Context, tenantID uint32) (bool, error) {
	if r == nil || r.entClient == nil || tenantID == 0 {
		return false, nil
	}
	sysCtx := appViewer.NewSystemViewerContext(ctx)
	ok, err := r.entClient.Client().Tenant.Query().
		Where(tenantEntity.IDEQ(tenantID), tenantEntity.DeletedAtIsNil()).
		Exist(sysCtx)
	if err != nil {
		r.log.Errorf(ctx, "query tenant %d for platform image gate failed: %s", tenantID, err.Error())
		return false, identityV1.ErrorInternalServerError("query tenant failed")
	}
	return ok, nil
}

// activeTenant 返回目标租户，仅当它存在、status=ON、未过期且未删除时；
// 否则返回 nil，调用方无需区分“不存在”和“不可用”。
func (r *PlanModuleRepo) activeTenant(ctx context.Context, tenantID uint32) (*ent.Tenant, error) {
	if r == nil || r.entClient == nil || tenantID == 0 {
		return nil, nil
	}
	sysCtx := appViewer.NewSystemViewerContext(ctx)
	t, err := r.entClient.Client().Tenant.Query().
		Where(tenantEntity.IDEQ(tenantID), tenantEntity.DeletedAtIsNil()).
		WithPlan().
		Only(sysCtx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		r.log.Errorf(ctx, "query tenant %d for platform image gate failed: %s", tenantID, err.Error())
		return nil, identityV1.ErrorInternalServerError("query tenant failed")
	}
	if t.Status == nil || *t.Status != tenantEntity.StatusOn {
		return nil, nil
	}
	if t.ExpiredAt != nil && !t.ExpiredAt.IsZero() && t.ExpiredAt.Before(time.Now()) {
		return nil, nil
	}
	return t, nil
}

// TenantActive 报告代管目标租户当前是否仍可用（存在、启用、未过期、未删除）。
// 平台代管入口用它区分“目标不存在”和“目标已停用”之外的普通不可用。
func (r *PlanModuleRepo) TenantActive(ctx context.Context, tenantID uint32) (bool, error) {
	t, err := r.activeTenant(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return t != nil, nil
}

// TenantModuleEnabled 报告目标租户当前是否可用某个业务模块。
//
// 这是平台代管入口的只读前置检查：查询 tenant.plan_id -> plan_module.module，
// 同时要求租户存在、status=ON、未过期、未删除。它不改变 TenantAccessChecker 的
// 租户自助语义，只在“平台替租户开通/扩张镜像能力”时使用。
// 已停用或套餐已变更的租户在查看、停用、注销类操作中不做本检查，便于回收。
func (r *PlanModuleRepo) TenantModuleEnabled(ctx context.Context, tenantID uint32, module identityV1.Module) (bool, error) {
	entModule := mapProtoModuleToEnt(module)
	if entModule == "" {
		return false, nil
	}
	t, err := r.activeTenant(ctx, tenantID)
	if err != nil || t == nil || t.Edges.Plan == nil {
		return false, err
	}
	cnt, err := r.entClient.Client().PlanModule.Query().
		Where(
			planmodule.HasPlanWith(plan.IDEQ(t.Edges.Plan.ID)),
			planmodule.ModuleEQ(entModule),
		).
		Count(appViewer.NewSystemViewerContext(ctx))
	if err != nil {
		r.log.Errorf(ctx, "query plan module gate failed: %s", err.Error())
		return false, identityV1.ErrorInternalServerError("query plan modules failed")
	}
	return cnt > 0, nil
}
