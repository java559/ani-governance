package data

import (
	"context"
	"errors"
	"strconv"

	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/accesskey"

	accesskeyV1 "go-wind-admin/api/gen/go/access_key/service/v1"
	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
)

// /api/v1/auth/api-keys 游标分页的资源与列约定。
const (
	// accessKeyCursorResource 是游标绑定的资源标识：不同资源的游标不可互相复用。
	accessKeyCursorResource = "access_key.access_key"
	// accessKeyCursorIDColumn 是稳定排序所需的唯一 ID 兜底列。
	accessKeyCursorIDColumn = "id"
	// accessKeyCursorDefaultSortColumn 是未指定排序时的默认排序：按 ID 降序
	// （主键本身即可支撑键集查询，且与新建凭证"最新在前"的直觉一致）。
	accessKeyCursorDefaultSortColumn = "id"
)

// accessKeyCursorSortColumns 定义允许用于游标分页的排序列，以及每列在游标里的取值类型与取值方式。
//
// 未列出的排序字段一律 400：宁可明确拒绝，也不返回顺序与游标窗口不一致的数据。
// 取值一律从数据库实体（*ent.AccessKey）取：可空列在实体侧是指针，能如实区分
// SQL NULL 与空值。
//
// 安全约束：白名单显式排除 secret_ciphertext（密文，字段标注 .Sensitive()），
// 不得进入排序/游标材料，否则会把密钥密文写进游标载荷。
var accessKeyCursorSortColumns = map[string]struct {
	kind  entCrud.CursorSortKind
	value func(entity *ent.AccessKey) (string, bool)
}{
	"id": {entCrud.CursorSortUint, func(e *ent.AccessKey) (string, bool) {
		return strconv.FormatUint(uint64(e.ID), 10), false
	}},
	"tenant_id": {entCrud.CursorSortUint, func(e *ent.AccessKey) (string, bool) { return cursorUintValue(e.TenantID) }},
	"role_id":   {entCrud.CursorSortUint, func(e *ent.AccessKey) (string, bool) { return strconv.FormatUint(uint64(e.RoleID), 10), false }},
	"name":      {entCrud.CursorSortString, func(e *ent.AccessKey) (string, bool) { return cursorStringValue(e.Name) }},
	"access_key": {entCrud.CursorSortString, func(e *ent.AccessKey) (string, bool) {
		return e.AccessKey, false
	}},
	"status": {entCrud.CursorSortString, func(e *ent.AccessKey) (string, bool) {
		if e.Status == nil {
			return "", true
		}
		return string(*e.Status), false
	}},
	"expires_at":   {entCrud.CursorSortTime, func(e *ent.AccessKey) (string, bool) { return cursorTimeValue(e.ExpiresAt) }},
	"last_used_at": {entCrud.CursorSortTime, func(e *ent.AccessKey) (string, bool) { return cursorTimeValue(e.LastUsedAt) }},
	"created_at":   {entCrud.CursorSortTime, func(e *ent.AccessKey) (string, bool) { return cursorTimeValue(e.CreatedAt) }},
	"updated_at":   {entCrud.CursorSortTime, func(e *ent.AccessKey) (string, bool) { return cursorTimeValue(e.UpdatedAt) }},
}

// accessKeyCursorSort 是一次游标分页已确定的排序方案。
type accessKeyCursorSort struct {
	column string
	desc   bool
	kind   entCrud.CursorSortKind
	value  func(entity *ent.AccessKey) (string, bool)
}

// accessKeyCursorSortColumnSet 返回允许的排序列集合。
func accessKeyCursorSortColumnSet() map[string]struct{} {
	allowed := make(map[string]struct{}, len(accessKeyCursorSortColumns))
	for column := range accessKeyCursorSortColumns {
		allowed[column] = struct{}{}
	}
	return allowed
}

// resolveAccessKeyCursorSort 解析本次请求的排序方案。
func resolveAccessKeyCursorSort(req *paginationV1.PagingRequest) (accessKeyCursorSort, error) {
	column, desc, err := resolveCursorSortColumn(req, accessKeyCursorDefaultSortColumn, accessKeyCursorSortColumnSet(), adminV1.ErrorBadRequest)
	if err != nil {
		return accessKeyCursorSort{}, err
	}
	entry := accessKeyCursorSortColumns[column]
	return accessKeyCursorSort{column: column, desc: desc, kind: entry.kind, value: entry.value}, nil
}

// listAccessKeysWithCursor 走游标（键集）分页。
//
// 与 users 的差异：本接口输出需把内部 ON/OFF 状态映射为公开布尔 is_active，
// 通用 mapper 做不到，因此沿用既有做法——先取一页（含唯一 ID），再用 ID 二次
// 查询实体并显式构造 DTO。
func (r *AccessKeyRepo) listAccessKeysWithCursor(ctx context.Context, req *paginationV1.PagingRequest) (*accesskeyV1.ListAccessKeyResponse, error) {
	sort, err := resolveAccessKeyCursorSort(req)
	if err != nil {
		return nil, err
	}

	filterKey := entCrud.CursorFilterKey(req, sort.column, sort.desc)

	bindingContext, tenantID, err := cursorBindingContext(ctx)
	if err != nil {
		return nil, err
	}

	builder := r.entClient.Client().AccessKey.Query()

	// 取页时强制读入 id：下方要按 ID 二次查询实体映射 is_active。
	// fieldMask 若不含 id，取页结果的 ID 会是零值，导致 IDIn(0,...) 命不中、整页为空。
	ret, err := r.repository.ListWithCursor(ctx, builder, builder.Clone(), withFieldMaskIncludingID(req), entCrud.CursorSpec[ent.AccessKey]{
		Resource:         accessKeyCursorResource,
		Context:          bindingContext,
		TenantID:         tenantID,
		FilterKey:        filterKey,
		SortColumn:       sort.column,
		Desc:             sort.desc,
		SortKind:         sort.kind,
		IDColumn:         accessKeyCursorIDColumn,
		ExtractID:        func(entity *ent.AccessKey) uint64 { return uint64(entity.ID) },
		ExtractSortValue: sort.value,
	})
	if err != nil {
		if errors.Is(err, entCrud.ErrInvalidCursorRequest) {
			return nil, adminV1.ErrorBadRequest("%s", err.Error())
		}
		return nil, err
	}

	result := &accesskeyV1.ListAccessKeyResponse{Items: []*accesskeyV1.AccessKey{}}
	if ret == nil {
		return result, nil
	}
	result.Total = ret.Total
	result.NextCursor = ret.NextCursor

	// 只取所选 ID，再二次查询实体以显式映射 ON/OFF → is_active。
	ids := make([]uint32, 0, len(ret.Items))
	for _, item := range ret.Items {
		ids = append(ids, item.GetId())
	}
	if len(ids) == 0 {
		return result, nil
	}
	entities, err := r.entClient.Client().AccessKey.Query().Where(accesskey.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, keyRepoError(err)
	}
	byID := make(map[uint32]*ent.AccessKey, len(entities))
	roleIDs := make([]uint32, 0, len(entities))
	for _, e := range entities {
		byID[e.ID] = e
		roleIDs = append(roleIDs, e.RoleID)
	}
	roles, err := r.roleBriefByIDs(ctx, roleIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if e := byID[id]; e != nil {
			result.Items = append(result.Items, accessKeyDTO(e, roles))
		}
	}
	return result, nil
}
