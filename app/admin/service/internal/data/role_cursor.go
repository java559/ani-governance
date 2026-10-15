package data

import (
	"context"
	"errors"
	"strconv"

	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"

	"go-wind-admin/app/admin/service/internal/data/ent"

	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"
)

// /api/v1/admin/roles 游标分页的资源与列约定。
const (
	// roleCursorResource 是游标绑定的资源标识：不同资源的游标不可互相复用。
	roleCursorResource = "permission.role"
	// roleCursorIDColumn 是稳定排序所需的唯一 ID 兜底列。
	roleCursorIDColumn = "id"
	// roleCursorDefaultSortColumn 是未指定排序时的默认排序：按 sort_order 升序
	// （语义为"越小越靠前"），并以唯一 ID 升序兜底。前端后续若反馈顺序不合适再调整。
	roleCursorDefaultSortColumn = "sort_order"
)

// roleCursorSortColumns 定义允许用于游标分页的排序列、取值类型与取值方式。
//
// 未列出的排序字段一律 400：宁可明确拒绝，也不返回顺序与游标窗口不一致的数据。
// 取值一律从数据库实体（*ent.Role）取：可空列在实体侧是指针，能如实区分 SQL NULL 与空值。
var roleCursorSortColumns = map[string]struct {
	kind  entCrud.CursorSortKind
	value func(entity *ent.Role) (string, bool)
}{
	"id": {entCrud.CursorSortUint, func(e *ent.Role) (string, bool) {
		return strconv.FormatUint(uint64(e.ID), 10), false
	}},
	"tenant_id":  {entCrud.CursorSortUint, func(e *ent.Role) (string, bool) { return cursorUintValue(e.TenantID) }},
	"sort_order": {entCrud.CursorSortUint, func(e *ent.Role) (string, bool) { return cursorUintValue(e.SortOrder) }},
	"created_by": {entCrud.CursorSortUint, func(e *ent.Role) (string, bool) { return cursorUintValue(e.CreatedBy) }},
	"name":       {entCrud.CursorSortString, func(e *ent.Role) (string, bool) { return cursorStringValue(e.Name) }},
	"code":       {entCrud.CursorSortString, func(e *ent.Role) (string, bool) { return cursorStringValue(e.Code) }},
	"type": {entCrud.CursorSortString, func(e *ent.Role) (string, bool) {
		if e.Type == nil {
			return "", true
		}
		return string(*e.Type), false
	}},
	"data_scope": {entCrud.CursorSortString, func(e *ent.Role) (string, bool) {
		if e.DataScope == nil {
			return "", true
		}
		return string(*e.DataScope), false
	}},
	"status": {entCrud.CursorSortString, func(e *ent.Role) (string, bool) {
		if e.Status == nil {
			return "", true
		}
		return string(*e.Status), false
	}},
	"created_at": {entCrud.CursorSortTime, func(e *ent.Role) (string, bool) { return cursorTimeValue(e.CreatedAt) }},
	"updated_at": {entCrud.CursorSortTime, func(e *ent.Role) (string, bool) { return cursorTimeValue(e.UpdatedAt) }},
}

// roleCursorSort 是一次游标分页已确定的排序方案。
type roleCursorSort struct {
	column string
	desc   bool
	kind   entCrud.CursorSortKind
	value  func(entity *ent.Role) (string, bool)
}

// roleCursorSortColumnSet 返回允许的排序列集合。
func roleCursorSortColumnSet() map[string]struct{} {
	allowed := make(map[string]struct{}, len(roleCursorSortColumns))
	for column := range roleCursorSortColumns {
		allowed[column] = struct{}{}
	}
	return allowed
}

// resolveRoleCursorSort 解析本次请求的排序方案。
// 未指定排序时用 sort_order 升序（默认方向见 roleCursorDefaultSortColumn 说明）。
func resolveRoleCursorSort(req *paginationV1.PagingRequest) (roleCursorSort, error) {
	column, desc, err := resolveCursorSortColumn(req, roleCursorDefaultSortColumn, roleCursorSortColumnSet(), permissionV1.ErrorBadRequest)
	if err != nil {
		return roleCursorSort{}, err
	}
	// 默认排序（未显式指定排序）按升序；显式指定时尊重请求方向。
	if !hasExplicitRoleSort(req) {
		desc = false
	}
	entry := roleCursorSortColumns[column]
	return roleCursorSort{column: column, desc: desc, kind: entry.kind, value: entry.value}, nil
}

// hasExplicitRoleSort 表示请求是否显式指定了排序列。
func hasExplicitRoleSort(req *paginationV1.PagingRequest) bool {
	if req.GetOrderBy() != "" {
		return true
	}
	for _, s := range req.GetSorting() {
		if s != nil && s.GetField() != "" {
			return true
		}
	}
	return false
}

// listRolesWithCursor 走游标（键集）分页。
//
// 与 users 的差异：本接口在取页后需逐条做关联填充（权限/组织单元/字段权限），
// 因此 CursorSpec 采用延迟裁剪，待关联回填后再清除辅助列。
func (r *RoleRepo) listRolesWithCursor(ctx context.Context, req *paginationV1.PagingRequest) (*permissionV1.ListRoleResponse, error) {
	sort, err := resolveRoleCursorSort(req)
	if err != nil {
		return nil, err
	}

	filterKey := entCrud.CursorFilterKey(req, sort.column, sort.desc)

	bindingContext, tenantID, err := cursorBindingContext(ctx)
	if err != nil {
		return nil, err
	}

	builder := r.entClient.Client().Role.Query()

	ret, err := r.repository.ListWithCursor(ctx, builder, builder.Clone(), req, entCrud.CursorSpec[ent.Role]{
		Resource:         roleCursorResource,
		Context:          bindingContext,
		TenantID:         tenantID,
		FilterKey:        filterKey,
		SortColumn:       sort.column,
		Desc:             sort.desc,
		SortKind:         sort.kind,
		IDColumn:         roleCursorIDColumn,
		ExtractID:        func(entity *ent.Role) uint64 { return uint64(entity.ID) },
		ExtractSortValue: sort.value,
		// 延迟裁剪：关联填充依赖 DTO 的 ID，待填充完成后由本方法裁剪输出。
		DeferHelperTrim: true,
	})
	if err != nil {
		if errors.Is(err, entCrud.ErrInvalidCursorRequest) {
			return nil, permissionV1.ErrorBadRequest("%s", err.Error())
		}
		return nil, err
	}
	if ret == nil {
		return &permissionV1.ListRoleResponse{Total: 0, Items: nil}, nil
	}

	roleIDs := make([]uint32, 0, len(ret.Items))
	for _, item := range ret.Items {
		roleIDs = append(roleIDs, item.GetId())
	}
	fieldPermGroups, _ := r.roleFieldPermissionRepo.ListHiddenFieldGroupsByRoleIDs(ctx, roleIDs)

	for _, item := range ret.Items {
		_ = r.fillPermissionIDs(ctx, item)
		_ = r.fillOrgUnitIDs(ctx, item)
		item.FieldPermissions = fieldPermGroups[item.GetId()]
	}
	if len(ret.HelperPaths) > 0 {
		entCrud.TrimCursorHelperFields(ret.Items, ret.HelperPaths)
	}

	return &permissionV1.ListRoleResponse{
		Total:      ret.Total,
		Items:      ret.Items,
		NextCursor: ret.NextCursor,
	}, nil
}
