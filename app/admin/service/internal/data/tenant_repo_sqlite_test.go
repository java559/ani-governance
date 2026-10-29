package data

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	"go-wind-admin/pkg/localdeps/go-utils/mapper"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/tests/testutil"
)

// newTenantRepoSqlite 用 enttest helper 构造一个可直接做 CRUD 的 TenantRepo。
// 白盒构造逐字段复刻 NewTenantRepo 的 mapper/converter 初始化，
// 仅将 log 换为 NopLogger、entClient 换为 SQLite 内存库测试 client。
func newTenantRepoSqlite(t *testing.T) *TenantRepo {
	t.Helper()
	entClient := testutil.NewEntClientForTest(t)
	repo := &TenantRepo{
		entClient: entClient,
		log:       bLogger.NewHelper(bLogger.NopLogger()),
		mapper:    mapper.NewCopierMapper[identityV1.Tenant, ent.Tenant](),
		statusConverter: mapper.NewEnumTypeConverter[identityV1.Tenant_Status, tenant.Status](
			identityV1.Tenant_Status_name,
			identityV1.Tenant_Status_value,
		),
		typeConverter: mapper.NewEnumTypeConverter[identityV1.Tenant_Type, tenant.Type](
			identityV1.Tenant_Type_name,
			identityV1.Tenant_Type_value,
		),
		auditStatusConverter: mapper.NewEnumTypeConverter[identityV1.Tenant_AuditStatus, tenant.AuditStatus](
			identityV1.Tenant_AuditStatus_name,
			identityV1.Tenant_AuditStatus_value,
		),
	}

	repo.init()

	return repo
}

// TestTenantRepoSqlite_Create 端到端验证 TenantRepo.Create：
// 经 mapper/converter → ent builder → SQLite 完整链路写入一条租户记录，
// 再用 ent client 直查（System viewer）确认记录与各字段确实落库。
func TestTenantRepoSqlite_Create(t *testing.T) {
	repo := newTenantRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())

	// TenantRepo.Create 的真实签名直接接收 Tenant DTO（无 Request 包装）
	_, err := repo.Create(ctx, &identityV1.Tenant{
		Name:        trans.Ptr("sqlite测试租户A"),
		Code:        trans.Ptr(fmt.Sprintf("TENANT_SQLITE_%d", 1001)),
		Domain:      trans.Ptr("tenant-a.example.com"),
		Industry:    trans.Ptr("software"),
		Remark:      trans.Ptr("集成测试写入"),
		Status:      identityV1.Tenant_ON.Enum(),
		Type:        identityV1.Tenant_TRIAL.Enum(),
		AuditStatus: identityV1.Tenant_PENDING.Enum(),
	})
	require.NoError(t, err, "通过 repo.Create 写入 SQLite 应成功")

	rows, err := repo.entClient.Client().Tenant.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1, "SQLite 中应有 1 条 tenant 记录")
	require.Equal(t, "sqlite测试租户A", *rows[0].Name, "name 应按 Create 载荷落库")
	require.Equal(t, fmt.Sprintf("TENANT_SQLITE_%d", 1001), *rows[0].Code, "code 应按 Create 载荷落库")
	require.NotNil(t, rows[0].Status, "status 枚举应经转换器落库")
	require.Equal(t, tenant.StatusOn, *rows[0].Status, "proto Tenant_ON 应映射为 ent StatusOn")
	require.NotNil(t, rows[0].Type, "type 枚举应经转换器落库")
	require.Equal(t, tenant.TypeTrial, *rows[0].Type, "proto Tenant_TRIAL 应映射为 ent TypeTrial")
	require.NotNil(t, rows[0].AuditStatus, "audit_status 枚举应经转换器落库")
	require.Equal(t, tenant.AuditStatusPending, *rows[0].AuditStatus, "proto Tenant_PENDING 应映射为 ent AuditStatusPending")
}

// TestTenantRepoSqlite_List 验证 TenantRepo.List 的分页列表与
// contains 模糊搜索过滤语义（仓规：搜索条件一律 contains，不做 EQ）。
func TestTenantRepoSqlite_List(t *testing.T) {
	repo := newTenantRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())

	// 两条带可区分标记的记录
	_, err := repo.Create(ctx, &identityV1.Tenant{
		Name: trans.Ptr("MARKERALPHA 租户"),
		Code: trans.Ptr(fmt.Sprintf("TENANT_SQLITE_%d", 2001)),
	})
	require.NoError(t, err)
	_, err = repo.Create(ctx, &identityV1.Tenant{
		Name: trans.Ptr("MARKERBETA 租户"),
		Code: trans.Ptr(fmt.Sprintf("TENANT_SQLITE_%d", 2002)),
	})
	require.NoError(t, err)

	// 无过滤：应返回全部 2 条
	all, err := repo.List(ctx, &paginationV1.PagingRequest{})
	require.NoError(t, err)
	require.Equal(t, uint64(2), all.Total, "无过滤时应统计全部 2 条")
	require.Len(t, all.Items, 2, "无过滤时应返回 2 条")
	for _, item := range all.Items {
		// 列表读视图：status/type 未显式指定、按列默认（ON/PAID）落库并经回填如实呈现；
		// audit_status 无列默认、保持 NULL → DTO 恒 nil、getter 呈 UNSPECIFIED（如实体况）。
		require.Equal(t, identityV1.Tenant_ON, item.GetStatus(), "列表读视图应回填列默认 status")
		require.Equal(t, identityV1.Tenant_PAID, item.GetType(), "列表读视图应回填列默认 type")
		require.Equal(t, identityV1.Tenant_TENANT_AUDIT_STATUS_UNSPECIFIED, item.GetAuditStatus(), "未写入且无默认的 audit_status 保持 NULL，读视图呈 UNSPECIFIED")
	}

	// contains 过滤：仅命中名称含 MARKERALPHA 的那条（LIKE %MARKERALPHA%）
	filtered, err := repo.List(ctx, &paginationV1.PagingRequest{
		FilteringType: &paginationV1.PagingRequest_Query{
			Query: `{"name__contains":"MARKERALPHA"}`,
		},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1), filtered.Total, "contains 过滤应只统计命中行")
	require.Len(t, filtered.Items, 1, "contains 过滤应只返回命中行")
	require.Contains(t, filtered.Items[0].GetName(), "MARKERALPHA", "命中行应为含标记的那条")
}

// TestTenantRepoSqlite_Get 验证 TenantRepo.Get 按主键/编码查询的命中与未命中。
func TestTenantRepoSqlite_Get(t *testing.T) {
	repo := newTenantRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())

	_, err := repo.Create(ctx, &identityV1.Tenant{
		Name:        trans.Ptr("sqlite查询租户"),
		Code:        trans.Ptr(fmt.Sprintf("TENANT_SQLITE_%d", 3001)),
		Status:      identityV1.Tenant_FREEZE.Enum(),
		Type:        identityV1.Tenant_INTERNAL.Enum(),
		AuditStatus: identityV1.Tenant_REJECTED.Enum(),
	})
	require.NoError(t, err)

	rows, err := repo.entClient.Client().Tenant.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	createdID := uint32(rows[0].ID)

	// 命中：按主键
	gotByID, err := repo.Get(ctx, &identityV1.GetTenantRequest{
		QueryBy: &identityV1.GetTenantRequest_Id{Id: createdID},
	})
	require.NoError(t, err, "按主键查询已存在记录应命中")
	require.Equal(t, "sqlite查询租户", gotByID.GetName(), "命中记录的 name 应与写入一致")
	require.Equal(t, fmt.Sprintf("TENANT_SQLITE_%d", 3001), gotByID.GetCode(), "命中记录的 code 应与写入一致")
	// 读视图：三个枚举字段（含无列默认的 audit_status）均应经回填如实呈现写入值。
	require.Equal(t, identityV1.Tenant_FREEZE, gotByID.GetStatus(), "读视图应回填 status")
	require.Equal(t, identityV1.Tenant_INTERNAL, gotByID.GetType(), "读视图应回填 type")
	require.Equal(t, identityV1.Tenant_REJECTED, gotByID.GetAuditStatus(), "读视图应回填 audit_status")

	// 命中：按编码
	gotByCode, err := repo.Get(ctx, &identityV1.GetTenantRequest{
		QueryBy: &identityV1.GetTenantRequest_Code{Code: fmt.Sprintf("TENANT_SQLITE_%d", 3001)},
	})
	require.NoError(t, err, "按编码查询已存在记录应命中")
	require.Equal(t, createdID, gotByCode.GetId(), "按编码查到的记录主键应与写入时一致")

	// 未命中：不存在的编码
	_, err = repo.Get(ctx, &identityV1.GetTenantRequest{
		QueryBy: &identityV1.GetTenantRequest_Code{Code: "TENANT_SQLITE_NOT_EXIST"},
	})
	require.Error(t, err, "查询不存在的编码应返回错误")

	// 未命中：不存在的主键
	_, err = repo.Get(ctx, &identityV1.GetTenantRequest{
		QueryBy: &identityV1.GetTenantRequest_Id{Id: 99999},
	})
	require.Error(t, err, "查询不存在的主键应返回错误")
}

// TestTenantRepoSqlite_Update 验证 TenantRepo.Update 在单字段 updateMask 下
// 只更新掩码内字段，掩码外字段保持原值。
func TestTenantRepoSqlite_Update(t *testing.T) {
	repo := newTenantRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())

	_, err := repo.Create(ctx, &identityV1.Tenant{
		Name: trans.Ptr("更新前名称"),
		Code: trans.Ptr(fmt.Sprintf("TENANT_SQLITE_%d", 4001)),
	})
	require.NoError(t, err)

	rows, err := repo.entClient.Client().Tenant.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	createdID := uint32(rows[0].ID)

	err = repo.Update(ctx, &identityV1.UpdateTenantRequest{
		Id:         createdID,
		Data:       &identityV1.Tenant{Name: trans.Ptr("更新后名称")},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}},
	})
	require.NoError(t, err, "单字段掩码更新应成功")

	after, err := repo.entClient.Client().Tenant.Query().Only(ctx)
	require.NoError(t, err)
	require.Equal(t, "更新后名称", *after.Name, "掩码内字段 name 应被更新")
	require.Equal(t, fmt.Sprintf("TENANT_SQLITE_%d", 4001), *after.Code, "掩码外字段 code 应保持原值")
}

// TestTenantRepoSqlite_Create_ZeroPlanStoredAsNull 验证“不选套餐”的写入语义：
// proto 显式传 planId=0 时必须落库为 NULL，而不是写 0（plan_id 是指向
// sys_plans 的外键，0 会触发 23503）。
func TestTenantRepoSqlite_Create_ZeroPlanStoredAsNull(t *testing.T) {
	repo := newTenantRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())

	_, err := repo.Create(ctx, &identityV1.Tenant{
		Name:   trans.Ptr("无套餐租户"),
		Code:   trans.Ptr("TENANT_SQLITE_NO_PLAN"),
		PlanId: trans.Ptr(uint32(0)),
	})
	require.NoError(t, err, "planId=0 应按“无套餐”创建成功")

	row, err := repo.entClient.Client().Tenant.Query().
		Where(tenant.CodeEQ("TENANT_SQLITE_NO_PLAN")).Only(ctx)
	require.NoError(t, err)
	require.Nil(t, row.PlanID, "planId=0 必须落库为 NULL，不能写 0")
}

// TestTenantRepoSqlite_Update_ZeroPlanClearsPlan 验证编辑时清空套餐：
// planId=0 且 updateMask 含 plan_id 时应把 plan_id 置 NULL。
func TestTenantRepoSqlite_Update_ZeroPlanClearsPlan(t *testing.T) {
	repo := newTenantRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())

	plan, err := repo.entClient.Client().Plan.Create().SetName("plan-for-clear").Save(ctx)
	require.NoError(t, err)

	created, err := repo.Create(ctx, &identityV1.Tenant{
		Name:   trans.Ptr("绑定套餐租户"),
		Code:   trans.Ptr("TENANT_SQLITE_CLEAR_PLAN"),
		PlanId: &plan.ID,
	})
	require.NoError(t, err)
	require.Equal(t, plan.ID, created.GetPlanId())

	err = repo.Update(ctx, &identityV1.UpdateTenantRequest{
		Id:         created.GetId(),
		Data:       &identityV1.Tenant{PlanId: trans.Ptr(uint32(0))},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"plan_id"}},
	})
	require.NoError(t, err, "清空套餐应成功")

	row, err := repo.entClient.Client().Tenant.Query().
		Where(tenant.CodeEQ("TENANT_SQLITE_CLEAR_PLAN")).Only(ctx)
	require.NoError(t, err)
	require.Nil(t, row.PlanID, "清空套餐后 plan_id 应为 NULL")
}

// TestTenantRepoSqlite_Delete 验证 TenantRepo.Delete 删除记录后表内计数归零，
// 且删除不存在的记录返回错误。
func TestTenantRepoSqlite_Delete(t *testing.T) {
	repo := newTenantRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())

	_, err := repo.Create(ctx, &identityV1.Tenant{
		Name: trans.Ptr("待删除租户"),
		Code: trans.Ptr(fmt.Sprintf("TENANT_SQLITE_%d", 5001)),
	})
	require.NoError(t, err)

	rows, err := repo.entClient.Client().Tenant.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	createdID := uint32(rows[0].ID)

	require.NoError(t, repo.Delete(ctx, &identityV1.DeleteTenantRequest{
		QueryBy: &identityV1.DeleteTenantRequest_Id{Id: createdID},
	}), "删除已存在记录应成功")

	count, err := repo.entClient.Client().Tenant.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count, "删除后 tenant 表计数应归零")

	err = repo.Delete(ctx, &identityV1.DeleteTenantRequest{
		QueryBy: &identityV1.DeleteTenantRequest_Id{Id: 99999},
	})
	require.Error(t, err, "删除不存在的记录应返回错误")
}

// TestResourceTenantUUIDPersistence 校验 resource tenant UUID 的持久化不变量：
// 创建即生成、租户间不重复、不可通过 update_mask 改写、平台租户(0)拒绝映射，
// 以及租户闸门仍能识别 MODULE_MODEL 这个模块值。
// 原先放在 model_client_test.go 中，因 model 接入暂摘而迁到此处。
func TestResourceTenantUUIDPersistence(t *testing.T) {
	client := testutil.NewEntClientForTest(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	a, err := client.Client().Tenant.Create().SetName("a").SetCode("a").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.Client().Tenant.Create().SetName("b").SetCode("b").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a.ResourceTenantID == b.ResourceTenantID {
		t.Fatal("duplicate resource identity")
	}
	if _, err = uuid.Parse(a.ResourceTenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Client().Tenant.UpdateOneID(a.ID).SetName("renamed").Save(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := client.Client().Tenant.Get(ctx, a.ID)
	if err != nil || row.ResourceTenantID != a.ResourceTenantID {
		t.Fatalf("identity changed: %v %v", row, err)
	}
	repo := &TenantRepo{entClient: client}
	if err := repo.Update(ctx, &identityV1.UpdateTenantRequest{Id: a.ID, Data: &identityV1.Tenant{}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"resource_tenant_id"}}}); err == nil {
		t.Fatal("immutable identity mask accepted")
	}
	if got, err := repo.ResourceTenantID(ctx, a.ID); err != nil || got != a.ResourceTenantID {
		t.Fatalf("mapping: %q %v", got, err)
	}
	if _, err := repo.ResourceTenantID(ctx, 0); err == nil {
		t.Fatal("platform mapping accepted")
	}
	if _, err = client.Client().Tenant.Create().SetResourceTenantID(a.ResourceTenantID).Save(ctx); err == nil {
		t.Fatal("duplicate UUID inserted")
	}
	if mapProtoModuleToEnt(identityV1.Module_MODEL) != planmodule.ModuleModel || mapApiBusinessModuleToProto(api.BusinessModuleModel) != identityV1.Module_MODEL {
		t.Fatal("MODEL not recognized by tenant gate")
	}
}

// PlanID is an existing Ent edge column and must survive DTO reads used by navigation.
func TestTenantRepoSqlite_PlanIDRoundTrip(t *testing.T) {
	repo := newTenantRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	first, err := repo.entClient.Client().Plan.Create().SetName("first").Save(ctx)
	require.NoError(t, err)
	second, err := repo.entClient.Client().Plan.Create().SetName("second").Save(ctx)
	require.NoError(t, err)
	bound, err := repo.entClient.Client().Tenant.Create().SetName("bound").SetCode("bound-plan").SetPlanID(first.ID).Save(ctx)
	require.NoError(t, err)
	unbound, err := repo.entClient.Client().Tenant.Create().SetName("unbound").SetCode("no-plan").Save(ctx)
	require.NoError(t, err)
	get := func(id uint32) *identityV1.Tenant {
		v, err := repo.Get(ctx, &identityV1.GetTenantRequest{QueryBy: &identityV1.GetTenantRequest_Id{Id: id}})
		require.NoError(t, err)
		return v
	}
	require.Equal(t, first.ID, get(bound.ID).GetPlanId())
	require.Nil(t, get(unbound.ID).PlanId)
	listed, err := repo.List(ctx, &paginationV1.PagingRequest{NoPaging: trans.Ptr(true)})
	require.NoError(t, err)
	for _, item := range listed.Items {
		if item.GetId() == bound.ID {
			require.Equal(t, first.ID, item.GetPlanId())
		}
	}
	_, err = repo.entClient.Client().Tenant.UpdateOneID(bound.ID).SetPlanID(second.ID).Save(ctx)
	require.NoError(t, err)
	require.Equal(t, second.ID, get(bound.ID).GetPlanId())
	_, err = repo.entClient.Client().Tenant.UpdateOneID(bound.ID).ClearPlan().Save(ctx)
	require.NoError(t, err)
	require.Nil(t, get(bound.ID).PlanId)
}
