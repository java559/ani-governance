package data

import (
	"context"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"time"

	"entgo.io/ent/dialect/sql"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"

	"go-wind-admin/pkg/localdeps/go-utils/copierutil"
	"go-wind-admin/pkg/localdeps/go-utils/mapper"
	"go-wind-admin/pkg/localdeps/go-utils/timeutil"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/predicate"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaaccount"
	"go-wind-admin/app/admin/service/internal/data/ent/quotacharge"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	"go-wind-admin/app/admin/service/internal/data/ent/quotareleasereceipt"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"

	appViewer "go-wind-admin/pkg/entgo/viewer"

	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
)

type TenantRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
	log       *bLogger.Helper

	mapper               *mapper.CopierMapper[identityV1.Tenant, ent.Tenant]
	statusConverter      *mapper.EnumTypeConverter[identityV1.Tenant_Status, tenant.Status]
	typeConverter        *mapper.EnumTypeConverter[identityV1.Tenant_Type, tenant.Type]
	auditStatusConverter *mapper.EnumTypeConverter[identityV1.Tenant_AuditStatus, tenant.AuditStatus]

	repository *entCrud.Repository[
		ent.TenantQuery, ent.TenantSelect,
		ent.TenantCreate, ent.TenantCreateBulk,
		ent.TenantUpdate, ent.TenantUpdateOne,
		ent.TenantDelete,
		predicate.Tenant,
		identityV1.Tenant, ent.Tenant,
	]
}

func NewTenantRepo(ctx *bootstrap.Context, entClient *entCrud.EntClient[*ent.Client]) *TenantRepo {
	repo := &TenantRepo{
		log:                  ctx.NewLoggerHelper("tenant/repo/admin-service"),
		entClient:            entClient,
		mapper:               mapper.NewCopierMapper[identityV1.Tenant, ent.Tenant](),
		statusConverter:      mapper.NewEnumTypeConverter[identityV1.Tenant_Status, tenant.Status](identityV1.Tenant_Status_name, identityV1.Tenant_Status_value),
		typeConverter:        mapper.NewEnumTypeConverter[identityV1.Tenant_Type, tenant.Type](identityV1.Tenant_Type_name, identityV1.Tenant_Type_value),
		auditStatusConverter: mapper.NewEnumTypeConverter[identityV1.Tenant_AuditStatus, tenant.AuditStatus](identityV1.Tenant_AuditStatus_name, identityV1.Tenant_AuditStatus_value),
	}

	repo.init()

	return repo
}

func (r *TenantRepo) init() {
	r.repository = entCrud.NewRepository[
		ent.TenantQuery, ent.TenantSelect,
		ent.TenantCreate, ent.TenantCreateBulk,
		ent.TenantUpdate, ent.TenantUpdateOne,
		ent.TenantDelete,
		predicate.Tenant,
		identityV1.Tenant, ent.Tenant,
	](r.mapper)

	r.mapper.AppendConverters(copierutil.NewTimeStringConverterPair())
	r.mapper.AppendConverters(copierutil.NewTimeTimestamppbConverterPair())

	r.mapper.AppendConverters(r.statusConverter.NewConverterPair())
	r.mapper.AppendConverters(r.typeConverter.NewConverterPair())
	r.mapper.AppendConverters(r.auditStatusConverter.NewConverterPair())
}

func (r *TenantRepo) Count(ctx context.Context, req *paginationV1.PagingRequest) (int, error) {
	builder := r.entClient.Client().Tenant.Query()

	whereSelectors, _, _ := r.repository.BuildListSelectorWithPaging(builder, req)
	if len(whereSelectors) != 0 {
		builder.Modify(whereSelectors...)
	}

	count, err := builder.Count(ctx)
	if err != nil {
		r.log.Errorf(ctx, "query tenant count failed: %s", err.Error())
		return 0, identityV1.ErrorInternalServerError("query count failed")
	}

	return count, nil
}

func (r *TenantRepo) List(ctx context.Context, req *paginationV1.PagingRequest) (*identityV1.ListTenantResponse, error) {
	if req == nil {
		return nil, identityV1.ErrorBadRequest("invalid parameter")
	}

	builder := r.entClient.Client().Tenant.Query()

	ret, err := r.repository.ListWithPaging(ctx, builder, builder.Clone(), req)
	if err != nil {
		return nil, err
	}
	if ret == nil {
		return &identityV1.ListTenantResponse{Total: 0, Items: nil}, nil
	}

	return &identityV1.ListTenantResponse{
		Total: ret.Total,
		Items: ret.Items,
	}, nil
}

func (r *TenantRepo) IsExist(ctx context.Context, id uint32) (bool, error) {
	exist, err := r.entClient.Client().Tenant.Query().
		Where(tenant.IDEQ(id)).
		Exist(ctx)
	if err != nil {
		r.log.Errorf(ctx, "query exist failed: %s", err.Error())
		return false, identityV1.ErrorInternalServerError("query exist failed")
	}
	return exist, nil
}

func (r *TenantRepo) Get(ctx context.Context, req *identityV1.GetTenantRequest) (*identityV1.Tenant, error) {
	if req == nil {
		return nil, identityV1.ErrorBadRequest("invalid parameter")
	}

	builder := r.entClient.Client().Tenant.Query()

	var whereCond []func(s *sql.Selector)
	switch req.QueryBy.(type) {
	default:
	case *identityV1.GetTenantRequest_Id:
		whereCond = append(whereCond, tenant.IDEQ(req.GetId()))

	case *identityV1.GetTenantRequest_Code:
		whereCond = append(whereCond, tenant.CodeEQ(req.GetCode()))

	case *identityV1.GetTenantRequest_Name:
		whereCond = append(whereCond, tenant.NameEQ(req.GetName()))
	}

	dto, err := r.repository.Get(ctx, builder, req.GetViewMask(), whereCond...)
	if err != nil {
		return nil, err
	}

	return dto, err
}

func (r *TenantRepo) BeginTx(ctx context.Context) (tx *ent.Tx, cleanup func(), err error) {
	tx, err = r.entClient.Client().Tx(ctx)
	if err != nil {
		r.log.Errorf(ctx, "start transaction failed: %s", err.Error())
		return nil, nil, identityV1.ErrorInternalServerError("start transaction failed")
	}

	cleanup = func() {
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
	}

	return tx, cleanup, nil
}

// plan_id 是可空的 sys_plans 外键。proto 的 optional uint32 在“未选择套餐”时
// 会以 0 显式到达这里，直接落库会触发 FK 23503；读取侧已把 0 与 NULL 同义
// 视为无套餐，写入侧统一归一化为 NULL。
func normalizePlanID(planID *uint32) *uint32 {
	if planID != nil && *planID == 0 {
		return nil
	}
	return planID
}

func (r *TenantRepo) Create(ctx context.Context, data *identityV1.Tenant) (tenant *identityV1.Tenant, err error) {
	if data == nil {
		return nil, identityV1.ErrorBadRequest("invalid parameter")
	}

	var tx *ent.Tx
	tx, err = r.entClient.Client().Tx(ctx)
	if err != nil {
		r.log.Errorf(ctx, "start transaction failed: %s", err.Error())
		return nil, identityV1.ErrorInternalServerError("start transaction failed")
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

	return r.CreateWithTx(ctx, tx, data)
}

func (r *TenantRepo) CreateWithTx(ctx context.Context, tx *ent.Tx, data *identityV1.Tenant) (*identityV1.Tenant, error) {
	if data == nil {
		return nil, identityV1.ErrorBadRequest("invalid parameter")
	}

	builder := tx.Tenant.Create().
		SetNillableName(data.Name).
		SetNillableCode(data.Code).
		SetNillableDomain(data.Domain).
		SetNillableLogoURL(data.LogoUrl).
		SetNillableRemark(data.Remark).
		SetNillableIndustry(data.Industry).
		SetNillableAdminUserID(data.AdminUserId).
		SetNillableStatus(r.statusConverter.ToEntity(data.Status)).
		SetNillableType(r.typeConverter.ToEntity(data.Type)).
		SetNillableAuditStatus(r.auditStatusConverter.ToEntity(data.AuditStatus)).
		SetNillableSubscriptionPlan(data.SubscriptionPlan).
		SetNillableExpiredAt(timeutil.TimestamppbToTime(data.ExpiredAt)).
		SetNillableSubscriptionAt(timeutil.TimestamppbToTime(data.SubscriptionAt)).
		SetNillableUnsubscribeAt(timeutil.TimestamppbToTime(data.UnsubscribeAt)).
		SetNillablePlanID(normalizePlanID(data.PlanId)).
		SetNillableCreatedBy(data.CreatedBy).
		SetCreatedAt(time.Now())

	if data.Id != nil {
		builder.SetID(data.GetId())
	}

	if ret, err := builder.Save(ctx); err != nil {
		r.log.Errorf(ctx, "insert tenant failed: %s", err.Error())
		return nil, identityV1.ErrorInternalServerError("insert tenant failed")
	} else {
		return r.mapper.ToDTO(ret), nil
	}
}

func (r *TenantRepo) Update(ctx context.Context, req *identityV1.UpdateTenantRequest) error {
	if req == nil || req.Data == nil {
		return identityV1.ErrorBadRequest("invalid parameter")
	}
	if req.GetId() == 0 {
		return identityV1.ErrorBadRequest("id is required")
	}
	for _, path := range req.GetUpdateMask().GetPaths() {
		if path == "resource_tenant_id" || path == "resourceTenantId" {
			return identityV1.ErrorBadRequest("resource tenant identity is immutable")
		}
	}

	// 如果不存在则创建
	if req.GetAllowMissing() {
		exist, err := r.IsExist(ctx, req.GetId())
		if err != nil {
			return err
		}
		if !exist {
			createReq := &identityV1.CreateTenantRequest{Data: req.Data}
			createReq.Data.CreatedBy = createReq.Data.UpdatedBy
			createReq.Data.UpdatedBy = nil
			_, err = r.Create(ctx, createReq.Data)
			return err
		}
	}

	// planId=0 表示清空套餐。归一化为 nil 后，UpdateX 的 applyUpdateNilFieldMask
	// 会在 mask 含 plan_id 时生成 plan_id = NULL；否则 Builder 会写 0 并撞外键。
	req.Data.PlanId = normalizePlanID(req.Data.PlanId)

	builder := r.entClient.Client().Tenant.Update()
	err := r.repository.UpdateX(ctx, builder, req.Data, req.GetUpdateMask(),
		func(dto *identityV1.Tenant) {
			builder.
				SetNillableName(req.Data.Name).
				SetNillableCode(req.Data.Code).
				SetNillableDomain(req.Data.Domain).
				SetNillableLogoURL(req.Data.LogoUrl).
				SetNillableRemark(req.Data.Remark).
				SetNillableIndustry(req.Data.Industry).
				SetNillableAdminUserID(req.Data.AdminUserId).
				SetNillableStatus(r.statusConverter.ToEntity(req.Data.Status)).
				SetNillableType(r.typeConverter.ToEntity(req.Data.Type)).
				SetNillableAuditStatus(r.auditStatusConverter.ToEntity(req.Data.AuditStatus)).
				SetNillableSubscriptionPlan(req.Data.SubscriptionPlan).
				SetNillableExpiredAt(timeutil.TimestamppbToTime(req.Data.ExpiredAt)).
				SetNillableSubscriptionAt(timeutil.TimestamppbToTime(req.Data.SubscriptionAt)).
				SetNillableUnsubscribeAt(timeutil.TimestamppbToTime(req.Data.UnsubscribeAt)).
				SetNillablePlanID(req.Data.PlanId).
				SetNillableUpdatedBy(req.Data.UpdatedBy).
				SetUpdatedAt(time.Now())
		},
		func(s *sql.Selector) {
			s.Where(sql.EQ(tenant.FieldID, req.GetId()))
		},
	)

	return err
}

// AssignTenantAdmin assigns an admin user to a tenant within a transaction.
func (r *TenantRepo) AssignTenantAdmin(ctx context.Context, tx *ent.Tx, tenantId uint32, userId uint32) error {
	_, err := tx.Tenant.Update().
		Where(tenant.IDEQ(tenantId)).
		SetAdminUserID(userId).
		Save(ctx)
	if err != nil {
		r.log.Errorf(ctx, "assign tenant admin failed: %s", err.Error())
		return identityV1.ErrorInternalServerError("assign tenant admin failed")
	}

	return nil
}

func (r *TenantRepo) Delete(ctx context.Context, req *identityV1.DeleteTenantRequest) error {
	if req == nil {
		return identityV1.ErrorBadRequest("invalid parameter")
	}

	// 计划 §6.12：存在配额账户或历史操作的租户不得物理删除（409 QUOTA_HISTORY_PRESENT）。
	// 在持有 tenant 锁后检查新账本；未产生新配额记录的租户保留原行为。
	if err := r.checkQuotaHistory(ctx, req.GetId()); err != nil {
		return err
	}

	if err := r.entClient.Client().Tenant.DeleteOneID(req.GetId()).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return identityV1.ErrorNotFound("tenant not found")
		}

		r.log.Errorf(ctx, "delete one data failed: %s", err.Error())

		return identityV1.ErrorInternalServerError("delete failed")
	}

	return nil
}

// checkQuotaHistory 检查租户是否已有配额账本记录（账户/操作/占额/回执任一存在即拒绝）。
// 锁顺序：先 tenant FOR UPDATE 再检查，防止检查与占额并发竞态。
func (r *TenantRepo) checkQuotaHistory(ctx context.Context, tenantId uint32) error {
	if tenantId == 0 {
		return nil
	}
	sysCtx := appViewer.NewSystemViewerContext(ctx)
	tx, err := r.entClient.Client().Tx(sysCtx)
	if err != nil {
		r.log.Errorf(ctx, "delete tenant %d: start tx failed: %s", tenantId, err.Error())
		return identityV1.ErrorInternalServerError("start transaction failed")
	}
	defer func() { _ = tx.Rollback() }()

	tq := tx.Tenant.Query().Where(tenant.IDEQ(tenantId))
	if supportsRowLock(r.entClient) {
		tq = tq.ForUpdate()
	}
	if _, err = tq.Only(sysCtx); ent.IsNotFound(err) {
		return identityV1.ErrorNotFound("tenant not found")
	} else if err != nil {
		r.log.Errorf(ctx, "delete tenant %d: lock tenant failed: %s", tenantId, err.Error())
		return identityV1.ErrorInternalServerError("lock tenant failed")
	}

	for _, check := range []struct {
		name  string
		count func(context.Context) (int, error)
	}{
		{"quota account", func(c context.Context) (int, error) {
			return tx.QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(tenantId)).Count(c)
		}},
		{"quota operation", func(c context.Context) (int, error) {
			return tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenantId)).Count(c)
		}},
		{"quota charge", func(c context.Context) (int, error) {
			return tx.QuotaCharge.Query().Where(quotacharge.TenantIDEQ(tenantId)).Count(c)
		}},
		{"quota release receipt", func(c context.Context) (int, error) {
			return tx.QuotaReleaseReceipt.Query().Where(quotareleasereceipt.TenantIDEQ(tenantId)).Count(c)
		}},
	} {
		cnt, cerr := check.count(sysCtx)
		if cerr != nil {
			r.log.Errorf(ctx, "delete tenant %d: query %s failed: %s", tenantId, check.name, cerr.Error())
			return identityV1.ErrorInternalServerError("check quota history failed")
		}
		if cnt > 0 {
			return quotapb.ErrorQuotaHistoryPresent("%s", "tenant has "+check.name+" records")
		}
	}
	return nil
}

// TenantExists checks if a tenant with the given code or name exists.
func (r *TenantRepo) TenantExists(ctx context.Context, req *identityV1.TenantExistsRequest) (*identityV1.TenantExistsResponse, error) {
	// code 和 name 各自唯一（见 schema 索引），冲突检测应取 OR：
	// 此前用 AND，仅当 code 和 name 同时命中同一行才返回存在，单字段冲突被漏判。
	query := r.entClient.Client().Tenant.Query()
	predicates := []predicate.Tenant{}
	if code := req.GetCode(); code != "" {
		predicates = append(predicates, tenant.CodeEQ(code))
	}
	if name := req.GetName(); name != "" {
		predicates = append(predicates, tenant.NameEQ(name))
	}
	if len(predicates) > 0 {
		query = query.Where(tenant.Or(predicates...))
	}

	exist, err := query.Exist(ctx)
	if err != nil {
		r.log.Errorf(ctx, "query exist failed: %s", err.Error())
		return nil, identityV1.ErrorInternalServerError("query exist failed")
	}

	return &identityV1.TenantExistsResponse{
		Exist: exist,
	}, nil
}

// ListTenantsByIds gets tenants by a list of IDs.
func (r *TenantRepo) ListTenantsByIds(ctx context.Context, ids []uint32) ([]*identityV1.Tenant, error) {
	if len(ids) == 0 {
		return []*identityV1.Tenant{}, nil
	}

	entities, err := r.entClient.Client().Tenant.Query().
		Where(tenant.IDIn(ids...)).
		All(ctx)
	if err != nil {
		r.log.Errorf(ctx, "query tenant by ids failed: %s", err.Error())
		return nil, identityV1.ErrorInternalServerError("query tenant by ids failed")
	}

	dtos := make([]*identityV1.Tenant, 0, len(entities))
	for _, entity := range entities {
		dto := r.mapper.ToDTO(entity)
		dtos = append(dtos, dto)
	}

	return dtos, nil
}
