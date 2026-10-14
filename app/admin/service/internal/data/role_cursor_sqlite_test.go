package data

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	"go-wind-admin/pkg/localdeps/go-utils/trans"

	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/tests/testutil"
)

// createCursorRolesForTest 造 n 条可区分的角色，按给定 sort_order 落库，返回按创建顺序（id 升序）的 ID。
func createCursorRolesForTest(t *testing.T, repo *RoleRepo, ctx context.Context, sortOrders []uint32) []uint32 {
	t.Helper()
	ids := make([]uint32, 0, len(sortOrders))
	for i, order := range sortOrders {
		require.NoError(t, repo.Create(ctx, &permissionV1.CreateRoleRequest{
			Data: &permissionV1.Role{
				Name:      trans.Ptr("cursor 角色"),
				Code:      trans.Ptr("ROLE_CURSOR_" + string(rune('a'+i))),
				Status:    permissionV1.Role_ON.Enum(),
				Type:      permissionV1.Role_TENANT.Enum(),
				SortOrder: trans.Ptr(order),
			},
		}))
		roles, err := repo.entClient.Client().Role.Query().All(ctx)
		require.NoError(t, err)
		ids = append(ids, uint32(roles[len(roles)-1].ID))
	}
	return ids
}

// TestRoleRepoSqlite_ListCursorPaging 验证 /api/v1/admin/roles 游标分页：
// 未指定排序时按 sort_order 升序、同值靠唯一 ID 升序兜底、翻页不重不漏、
// 末页 next_cursor 为空、total 为当前条件下的总数。
func TestRoleRepoSqlite_ListCursorPaging(t *testing.T) {
	repo := newRoleRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	// sort_order 升序应为 r1(1) → r2(2) → r3(3)；并制造 r2/r3 同值以验证 ID 兜底
	ids := createCursorRolesForTest(t, repo, ctx, []uint32{1, 2, 2})

	var got []uint32
	req := &paginationV1.PagingRequest{Limit: trans.Ptr(uint32(2))}
	for page := 1; ; page++ {
		require.LessOrEqual(t, page, 3, "翻页应当在有限页内结束")

		resp, err := repo.List(ctx, req)
		require.NoError(t, err)
		require.Equal(t, uint64(3), resp.GetTotal(), "total 必须是当前条件下的总数，不受游标窗口影响")
		require.LessOrEqual(t, len(resp.GetItems()), 2, "单页不得超出 limit")

		for _, item := range resp.GetItems() {
			got = append(got, item.GetId())
		}
		if resp.GetNextCursor() == "" {
			break
		}
		req = &paginationV1.PagingRequest{
			Limit:  trans.Ptr(uint32(2)),
			Cursor: trans.Ptr(resp.GetNextCursor()),
		}
	}

	require.Len(t, got, 3, "翻页应恰好覆盖 3 条记录，不重不漏")
	require.Equal(t, []uint32{ids[0], ids[1], ids[2]}, got,
		"未指定排序时应按 sort_order 升序，同值靠唯一 ID 升序兜底")
}

// TestRoleRepoSqlite_ListCursorHonoursExplicitSort 验证显式指定排序时尊重方向：
// 按 id 降序取到的顺序应与升序相反，且连续翻页不重不漏。
func TestRoleRepoSqlite_ListCursorHonoursExplicitSort(t *testing.T) {
	repo := newRoleRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	ids := createCursorRolesForTest(t, repo, ctx, []uint32{1, 2, 3})

	sorting := []*paginationV1.Sorting{{Field: "id", Direction: paginationV1.Sorting_DESC}}
	var got []uint32
	req := &paginationV1.PagingRequest{Limit: trans.Ptr(uint32(2)), Sorting: sorting}
	for page := 1; ; page++ {
		require.LessOrEqual(t, page, 3, "翻页应当在有限页内结束")

		resp, err := repo.List(ctx, req)
		require.NoError(t, err)
		for _, item := range resp.GetItems() {
			got = append(got, item.GetId())
		}
		if resp.GetNextCursor() == "" {
			break
		}
		req = &paginationV1.PagingRequest{
			Limit:   trans.Ptr(uint32(2)),
			Sorting: sorting,
			Cursor:  trans.Ptr(resp.GetNextCursor()),
		}
	}
	require.Equal(t, []uint32{ids[2], ids[1], ids[0]}, got, "显式 id 降序应按 ID 从大到小")
}

// TestRoleRepoSqlite_ListCursorRejectsInvalidRequest 验证 roles 游标分页对非法入参
// fail-closed：limit 越界、cursor 与 page 互斥、多个排序列、白名单外排序列一律 400。
func TestRoleRepoSqlite_ListCursorRejectsInvalidRequest(t *testing.T) {
	repo := newRoleRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)
	createCursorRolesForTest(t, repo, ctx, []uint32{1, 2})

	for _, limit := range []uint32{0, 101} {
		_, err := repo.List(ctx, &paginationV1.PagingRequest{Limit: trans.Ptr(limit)})
		require.Truef(t, permissionV1.IsBadRequest(err), "limit=%d 应返回 400，实际 %v", limit, err)
	}

	_, err := repo.List(ctx, &paginationV1.PagingRequest{
		PageSize: trans.Ptr(uint32(20)),
		Cursor:   trans.Ptr("c1.bogus.signature"),
	})
	require.True(t, permissionV1.IsBadRequest(err), "cursor 与 pageSize 互斥应返回 400，实际 %v", err)

	_, err = repo.List(ctx, &paginationV1.PagingRequest{
		Sorting: []*paginationV1.Sorting{
			{Field: "id", Direction: paginationV1.Sorting_ASC},
			{Field: "name", Direction: paginationV1.Sorting_ASC},
		},
	})
	require.True(t, permissionV1.IsBadRequest(err), "多排序列无法用单列键集表达，应返回 400，实际 %v", err)

	_, err = repo.List(ctx, &paginationV1.PagingRequest{
		Sorting: []*paginationV1.Sorting{{Field: "unknown_col", Direction: paginationV1.Sorting_ASC}},
	})
	require.True(t, permissionV1.IsBadRequest(err), "白名单外排序列应返回 400，实际 %v", err)
}

// TestRoleRepoSqlite_ListCursorFillsRelations 验证 roles 游标分页仍做关联填充：
// 取页后权限 ID 列表被回填（延迟裁剪保证关联回填依赖的 ID 仍在）。
func TestRoleRepoSqlite_ListCursorFillsRelations(t *testing.T) {
	repo := newRoleRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	ids := createCursorRolesForTest(t, repo, ctx, []uint32{5})

	// 直接造一条角色权限关联（ON + ALLOW 才被 ListPermissionIDs 计入）
	_, err := repo.entClient.Client().RolePermission.Create().
		SetRoleID(ids[0]).
		SetPermissionID(4242).
		SetStatus(rolepermission.StatusOn).
		SetEffect(rolepermission.EffectAllow).
		Save(ctx)
	require.NoError(t, err)

	resp, err := repo.List(ctx, &paginationV1.PagingRequest{Limit: trans.Ptr(uint32(1))})
	require.NoError(t, err)
	require.Len(t, resp.GetItems(), 1)
	require.Equal(t, []uint32{4242}, resp.GetItems()[0].GetPermissions(),
		"游标分页取页后应完成权限关联填充")
}
