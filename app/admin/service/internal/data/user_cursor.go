package data

import (
	"context"
	"errors"
	"math"
	"strconv"
	"time"

	"go-wind-admin/pkg/localdeps/go-crud/viewer"

	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	paginationSorting "go-wind-admin/pkg/localdeps/go-crud/pagination/sorting"

	"go-wind-admin/app/admin/service/internal/data/ent"

	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
)

// /api/v1/admin/users 游标分页的资源与列约定。
const (
	// userCursorResource 是游标绑定的资源标识：不同资源的游标不可互相复用。
	userCursorResource = "identity.user"
	// userCursorIDColumn 是稳定排序所需的唯一 ID 兜底列。
	userCursorIDColumn = "id"
	// userCursorDefaultSortColumn 是未指定排序时的默认排序：按 ID 降序
	// （与前端既有 orderBy=['-id'] 的取数顺序一致，且主键本身即可支撑键集查询）。
	userCursorDefaultSortColumn = "id"
)

// userCursorSortColumns 定义允许用于游标分页的排序列，以及每列在游标里的取值类型与取值方式。
//
// 键集分页必须知道排序列的取值类型，才能把游标里的排序值还原成驱动值
// （时间列传字符串会让 PostgreSQL 报 "operator does not exist: timestamp with time zone > text"）。
// 未列出的排序字段一律 400：宁可明确拒绝，也不返回顺序与游标窗口不一致的数据。
//
// 取值一律从数据库实体（*ent.User）取：sys_users 的绝大多数列可空，实体侧是指针，
// 能如实区分 SQL NULL 与空值；DTO 侧两者都退化成零值，会让键集条件漏掉 NULL 区段。
var userCursorSortColumns = map[string]struct {
	kind  entCrud.CursorSortKind
	value func(entity *ent.User) (string, bool)
}{
	// id 是主键、恒非空，作为默认排序与唯一兜底列。
	"id": {entCrud.CursorSortUint, func(e *ent.User) (string, bool) {
		return strconv.FormatUint(uint64(e.ID), 10), false
	}},
	"tenant_id": {entCrud.CursorSortUint, func(e *ent.User) (string, bool) { return cursorUintValue(e.TenantID) }},
	"username":  {entCrud.CursorSortString, func(e *ent.User) (string, bool) { return cursorStringValue(e.Username) }},
	"nickname":  {entCrud.CursorSortString, func(e *ent.User) (string, bool) { return cursorStringValue(e.Nickname) }},
	"realname":  {entCrud.CursorSortString, func(e *ent.User) (string, bool) { return cursorStringValue(e.Realname) }},
	"email":     {entCrud.CursorSortString, func(e *ent.User) (string, bool) { return cursorStringValue(e.Email) }},
	"mobile":    {entCrud.CursorSortString, func(e *ent.User) (string, bool) { return cursorStringValue(e.Mobile) }},
	// status/gender 在库中以枚举字符串存储（"NORMAL"/"MALE"），排序与键集比较同为字符串比较，语义一致。
	"status": {entCrud.CursorSortString, func(e *ent.User) (string, bool) {
		if e.Status == nil {
			return "", true
		}
		return string(*e.Status), false
	}},
	"gender": {entCrud.CursorSortString, func(e *ent.User) (string, bool) {
		if e.Gender == nil {
			return "", true
		}
		return string(*e.Gender), false
	}},
	"created_at":    {entCrud.CursorSortTime, func(e *ent.User) (string, bool) { return cursorTimeValue(e.CreatedAt) }},
	"updated_at":    {entCrud.CursorSortTime, func(e *ent.User) (string, bool) { return cursorTimeValue(e.UpdatedAt) }},
	"last_login_at": {entCrud.CursorSortTime, func(e *ent.User) (string, bool) { return cursorTimeValue(e.LastLoginAt) }},
	"locked_until":  {entCrud.CursorSortTime, func(e *ent.User) (string, bool) { return cursorTimeValue(e.LockedUntil) }},
}

// userCursorSort 是一次游标分页已确定的排序方案。
type userCursorSort struct {
	column string
	desc   bool
	kind   entCrud.CursorSortKind
	value  func(entity *ent.User) (string, bool)
}

// cursorStringValue / cursorUintValue / cursorTimeValue 把可空列转成游标排序值，
// 第二个返回值为 true 表示该列在库中是 SQL NULL（而不是空串/零值）。
func cursorStringValue(v *string) (string, bool) {
	if v == nil {
		return "", true
	}
	return *v, false
}

func cursorUintValue(v *uint32) (string, bool) {
	if v == nil {
		return "", true
	}
	return strconv.FormatUint(uint64(*v), 10), false
}

func cursorTimeValue(v *time.Time) (string, bool) {
	if v == nil {
		return "", true
	}
	return entCrud.FormatCursorTime(*v), false
}

// resolveUserCursorSort 解析本次请求的排序方案。
// 未指定排序时用默认列；只支持单一排序列（多列无法用单列键集表达）。
func resolveUserCursorSort(req *paginationV1.PagingRequest) (userCursorSort, error) {
	sortings := req.GetSorting()
	if len(sortings) == 0 {
		if orderBy := req.GetOrderBy(); orderBy != "" {
			converted, err := paginationSorting.NewOrderByStringConverter().Convert(orderBy)
			if err != nil {
				return userCursorSort{}, identityV1.ErrorBadRequest("invalid orderBy: %s", err.Error())
			}
			sortings = converted
		}
	}

	effective := make([]*paginationV1.Sorting, 0, len(sortings))
	for _, sorting := range sortings {
		if sorting == nil || sorting.GetField() == "" {
			continue
		}
		effective = append(effective, sorting)
	}
	if len(effective) > 1 {
		return userCursorSort{}, identityV1.ErrorBadRequest(
			"cursor pagination supports a single sort field, got %d", len(effective))
	}

	column, desc := userCursorDefaultSortColumn, true
	if len(effective) == 1 {
		column = effective[0].GetField()
		desc = effective[0].GetDirection() == paginationV1.Sorting_DESC
	}

	entry, ok := userCursorSortColumns[column]
	if !ok {
		return userCursorSort{}, identityV1.ErrorBadRequest(
			"unsupported sort field for cursor pagination: %s", column)
	}

	return userCursorSort{column: column, desc: desc, kind: entry.kind, value: entry.value}, nil
}

// cursorBindingContext 从可信 viewer 推导游标绑定的上下文类型与租户。
//
// 平台/系统上下文下 Ent 的租户策略放行（列表本身跨租户），必须与租户上下文区分开，
// 否则同一条游标会在两种上下文之间被误用。viewer 缺失时 fail-closed：
// 与 Ent 租户策略一致，不允许在没有身份的情况下分页。
func cursorBindingContext(ctx context.Context) (string, uint32, error) {
	vc, exist := viewer.FromContext(ctx)
	if !exist || vc == nil {
		return "", 0, identityV1.ErrorInternalServerError("viewer context missing")
	}

	if vc.IsPlatformContext() {
		return "platform", 0, nil
	}
	if vc.IsSystemContext() {
		return "system", 0, nil
	}

	tenantID := vc.TenantID()
	if tenantID == 0 || tenantID > math.MaxUint32 {
		return "", 0, identityV1.ErrorInternalServerError("viewer tenant missing")
	}
	return "tenant", uint32(tenantID), nil
}

// listWithCursor 走游标（键集）分页：首次不带 cursor，之后原样回传上一页的 next_cursor。
func (r *userRepo) listWithCursor(ctx context.Context, req *paginationV1.PagingRequest) (*identityV1.ListUserResponse, error) {
	sort, err := resolveUserCursorSort(req)
	if err != nil {
		return nil, err
	}

	// 绑定指纹必须在 applyUserListFilters 增强请求之前计算：绑定的是调用方的原始筛选，
	// 而不是展开后的用户 ID 列表，否则关联成员变动会让同一筛选下的翻页被误判为篡改。
	filterKey := entCrud.CursorFilterKey(req, sort.column, sort.desc)

	bindingContext, tenantID, err := cursorBindingContext(ctx)
	if err != nil {
		return nil, err
	}

	// 参数与游标签名校验必须前置到"空关系筛选短路"之前：否则筛选一个没有成员的
	// 角色再传 limit=101 或 cursor=bogus 会返回成功空列表，而不是约定的 400。
	// 校验与 ListWithCursor 内部一致，后者仍会重复校验（幂等）。
	if err = validateCursorRequest(req, userCursorResource, bindingContext, tenantID, filterKey, identityV1.ErrorBadRequest); err != nil {
		return nil, err
	}

	empty, err := r.applyUserListFilters(ctx, req)
	if err != nil {
		return nil, err
	}
	if empty {
		return &identityV1.ListUserResponse{Total: 0, Items: nil}, nil
	}

	builder := r.entClient.Client().User.Query()

	ret, err := r.repository.ListWithCursor(ctx, builder, builder.Clone(), req, entCrud.CursorSpec[ent.User]{
		Resource:         userCursorResource,
		Context:          bindingContext,
		TenantID:         tenantID,
		FilterKey:        filterKey,
		SortColumn:       sort.column,
		Desc:             sort.desc,
		SortKind:         sort.kind,
		IDColumn:         userCursorIDColumn,
		ExtractID:        func(entity *ent.User) uint64 { return uint64(entity.ID) },
		ExtractSortValue: sort.value,
		// 延迟裁剪：fieldMask 未含 id 时，关联回填仍需要实体 ID，
		// 因此待关联回填完成后由本方法调用 TrimCursorHelperFields 再裁剪输出。
		DeferHelperTrim: true,
	})
	if err != nil {
		// limit 越界、游标被篡改或绑定不匹配属于调用方输入问题，映射为 400。
		if errors.Is(err, entCrud.ErrInvalidCursorRequest) {
			return nil, identityV1.ErrorBadRequest("%s", err.Error())
		}
		return nil, err
	}
	if ret == nil {
		return &identityV1.ListUserResponse{Total: 0, Items: nil}, nil
	}

	resp := &identityV1.ListUserResponse{
		Total:      ret.Total,
		Items:      ret.Items,
		NextCursor: ret.NextCursor,
	}

	// 关联回填依赖 DTO 的 ID，必须在裁剪辅助列之前完成。
	r.enrichRelationIDs(ctx, resp.Items)
	if len(ret.HelperPaths) > 0 {
		entCrud.TrimCursorHelperFields(resp.Items, ret.HelperPaths)
	}

	return resp, nil
}
