package data

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	"go-wind-admin/pkg/localdeps/go-crud/pagination"
	"go-wind-admin/pkg/localdeps/go-utils/trans"

	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"
	"go-wind-admin/app/admin/service/tests/testutil"
)

// setUserCursorSecretForTest 为游标分页测试设置签名密钥。
// 生产环境由启动装配从 AK/SK 主密钥派生后设置（见 cmd/server/wiring_ent.go）；
// 未设置时游标一律 fail-closed，因此测试必须显式设置。
func setUserCursorSecretForTest(t *testing.T) {
	t.Helper()
	previous := pagination.CursorSecret()
	pagination.SetCursorSecret([]byte("user-cursor-sqlite-test-secret----"))
	t.Cleanup(func() { pagination.SetCursorSecret(previous) })
}

// createCursorUsersForTest 造 n 条可区分的 user 记录，返回按创建顺序（即 id 升序）的 ID。
func createCursorUsersForTest(t *testing.T, repo *userRepo, ctx context.Context, n int, username func(i int) string, nickname string) []uint32 {
	t.Helper()

	ids := make([]uint32, 0, n)
	for i := range n {
		created, err := repo.Create(ctx, &identityV1.CreateUserRequest{
			Data: &identityV1.User{
				Username: trans.Ptr(username(i)),
				Nickname: trans.Ptr(nickname),
			},
		})
		require.NoError(t, err)
		ids = append(ids, created.GetId())
	}
	return ids
}

// flippingLastChar 翻转字符串最后一个字符，用于制造"被篡改的游标"。
func flippingLastChar(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	b[len(b)-1] = b[len(b)-1] ^ 1
	return string(b)
}

// TestUserRepoSqlite_ListCursorPaging 验证游标分页的基本合同：
// 默认按唯一 ID 降序、每页 limit 条、末页 next_cursor 为空、
// 翻页不重不漏，且 total 是"当前条件下的总数"，不随游标窗口变化。
func TestUserRepoSqlite_ListCursorPaging(t *testing.T) {
	repo := newUserRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	ids := createCursorUsersForTest(t, repo, ctx, 5,
		func(i int) string { return fmt.Sprintf("cursorpage_user_%d", i) }, "分页用户")

	req := &paginationV1.PagingRequest{Limit: trans.Ptr(uint32(2))}
	var got []uint32
	for page := 1; ; page++ {
		require.LessOrEqual(t, page, 5, "翻页应当在有限页内结束")

		resp, err := repo.List(ctx, req)
		require.NoError(t, err)
		require.Equal(t, uint64(5), resp.GetTotal(), "total 必须是当前筛选条件下的总数，不受游标窗口影响")
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

	require.Len(t, got, 5, "翻页应恰好覆盖 5 条记录，不重不漏")

	want := make([]uint32, len(ids))
	for i, id := range ids {
		want[len(ids)-1-i] = id
	}
	require.Equal(t, want, got, "未指定排序时应按唯一 ID 降序")
}

// TestUserRepoSqlite_ListCursorSameSortValueNoSkip 验证排序值相同的多行
// （全部同昵称）靠唯一 ID 兜底仍不重不漏：这是键集分页最容易出错的地方。
func TestUserRepoSqlite_ListCursorSameSortValueNoSkip(t *testing.T) {
	repo := newUserRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	ids := createCursorUsersForTest(t, repo, ctx, 5,
		func(i int) string { return fmt.Sprintf("cursorsame_user_%d", i) }, "同名同昵称")

	sorting := []*paginationV1.Sorting{{Field: "nickname", Direction: paginationV1.Sorting_ASC}}
	req := &paginationV1.PagingRequest{Limit: trans.Ptr(uint32(2)), Sorting: sorting}

	var got []uint32
	for page := 1; ; page++ {
		require.LessOrEqual(t, page, 5, "翻页应当在有限页内结束")

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

	require.Equal(t, ids, got, "同值排序应回退到 ID 升序兜底，覆盖全部记录且不重复")
}

// TestUserRepoSqlite_ListCursorTotalIgnoresFilterWindow 验证 total 只受筛选影响：
// 有筛选时 total 是筛选后的总数，翻到第二页仍是同一个值。
func TestUserRepoSqlite_ListCursorTotalIgnoresFilterWindow(t *testing.T) {
	repo := newUserRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	createCursorUsersForTest(t, repo, ctx, 3,
		func(i int) string { return fmt.Sprintf("cursortotal_match_%d", i) }, "命中")
	createCursorUsersForTest(t, repo, ctx, 2,
		func(i int) string { return fmt.Sprintf("cursortotal_other_%d", i) }, "未命中")

	query := `{"username__contains":"cursortotal_match"}`
	req := &paginationV1.PagingRequest{
		Limit:         trans.Ptr(uint32(2)),
		FilteringType: &paginationV1.PagingRequest_Query{Query: query},
	}

	first, err := repo.List(ctx, req)
	require.NoError(t, err)
	require.Equal(t, uint64(3), first.GetTotal(), "total 应为筛选后的条数")
	require.Len(t, first.GetItems(), 2)
	require.NotEmpty(t, first.GetNextCursor())

	second, err := repo.List(ctx, &paginationV1.PagingRequest{
		Limit:         trans.Ptr(uint32(2)),
		FilteringType: &paginationV1.PagingRequest_Query{Query: query},
		Cursor:        trans.Ptr(first.GetNextCursor()),
	})
	require.NoError(t, err)
	require.Equal(t, uint64(3), second.GetTotal(), "翻页后 total 不变")
	require.Len(t, second.GetItems(), 1)
	require.Empty(t, second.GetNextCursor(), "末页不得返回下一页游标")
}

// TestUserRepoSqlite_ListCursorRejectsInvalidRequest 验证非法入参明确返回 400：
// limit 越界、游标被篡改、游标与筛选/排序不匹配、cursor 与 page 同时出现。
func TestUserRepoSqlite_ListCursorRejectsInvalidRequest(t *testing.T) {
	repo := newUserRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	createCursorUsersForTest(t, repo, ctx, 3,
		func(i int) string { return fmt.Sprintf("cursorreject_user_%d", i) }, "拒绝用例")

	// limit 越界：不截断，直接拒绝
	for _, limit := range []uint32{0, 101} {
		_, err := repo.List(ctx, &paginationV1.PagingRequest{Limit: trans.Ptr(limit)})
		require.Truef(t, identityV1.IsBadRequest(err), "limit=%d 应当返回 400，实际 %v", limit, err)
	}

	// cursor 与 page/pageSize 互斥
	_, err := repo.List(ctx, &paginationV1.PagingRequest{
		Page:     trans.Ptr(uint32(1)),
		PageSize: trans.Ptr(uint32(20)),
		Cursor:   trans.Ptr("c1.whatever.signature"),
	})
	require.True(t, identityV1.IsBadRequest(err), "cursor 与 page 同时出现应当返回 400，实际 %v", err)

	// 取得一个合法游标后逐项制造不匹配
	first, err := repo.List(ctx, &paginationV1.PagingRequest{Limit: trans.Ptr(uint32(1))})
	require.NoError(t, err)
	cursor := first.GetNextCursor()
	require.NotEmpty(t, cursor)

	_, err = repo.List(ctx, &paginationV1.PagingRequest{
		Limit:  trans.Ptr(uint32(1)),
		Cursor: trans.Ptr(flippingLastChar(cursor)),
	})
	require.True(t, identityV1.IsBadRequest(err), "被篡改的游标应当返回 400，实际 %v", err)

	_, err = repo.List(ctx, &paginationV1.PagingRequest{
		Limit:         trans.Ptr(uint32(1)),
		FilteringType: &paginationV1.PagingRequest_Query{Query: `{"username__contains":"cursorreject"}`},
		Cursor:        trans.Ptr(cursor),
	})
	require.True(t, identityV1.IsBadRequest(err), "游标与筛选不匹配应当返回 400，实际 %v", err)

	_, err = repo.List(ctx, &paginationV1.PagingRequest{
		Limit:   trans.Ptr(uint32(1)),
		Sorting: []*paginationV1.Sorting{{Field: "nickname", Direction: paginationV1.Sorting_ASC}},
		Cursor:  trans.Ptr(cursor),
	})
	require.True(t, identityV1.IsBadRequest(err), "游标与排序不匹配应当返回 400，实际 %v", err)

	// 不支持的排序列同样 fail-closed，避免返回顺序与游标窗口不一致的数据
	_, err = repo.List(ctx, &paginationV1.PagingRequest{
		Limit:   trans.Ptr(uint32(1)),
		Sorting: []*paginationV1.Sorting{{Field: "description", Direction: paginationV1.Sorting_ASC}},
	})
	require.True(t, identityV1.IsBadRequest(err), "不支持的排序列应当返回 400，实际 %v", err)
}

// TestUserRepoSqlite_ListCursorDoesNotRequireWindowParams 验证不传任何分页参数时
// 走游标分页且默认 limit=20：这是 /api/v1/admin/users 改为 limit+cursor 后的默认行为。
func TestUserRepoSqlite_ListCursorDoesNotRequireWindowParams(t *testing.T) {
	repo := newUserRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	createCursorUsersForTest(t, repo, ctx, 3,
		func(i int) string { return fmt.Sprintf("cursordefault_user_%d", i) }, "默认分页")

	resp, err := repo.List(ctx, &paginationV1.PagingRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetItems(), 3)
	require.Equal(t, uint64(3), resp.GetTotal())
	require.Empty(t, resp.GetNextCursor(), "不足默认页长时不应返回下一页游标")
}

// TestUserRepoSqlite_ListCursorEmptyRelationFilterFailsClosed 验证 §3.2：
// 关系筛选（如 role_ids）在短路返回空集之前，limit 越界与游标校验必须先行执行。
// 否则筛一个没有成员的角色再传 limit=101 或 cursor=bogus 会返回成功空列表，
// 而不是约定的 400——参数校验被静默跳过。
func TestUserRepoSqlite_ListCursorEmptyRelationFilterFailsClosed(t *testing.T) {
	repo := newUserRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	// 造若干用户，但都不关联角色 999（关系筛选命空集）
	createCursorUsersForTest(t, repo, ctx, 2,
		func(i int) string { return fmt.Sprintf("cursoremptyrel_user_%d", i) }, "空关系筛选")

	emptyQuery := `{"role_ids__in":[999]}`

	// 先确认该筛选确实会短路为空集
	empty, err := repo.List(ctx, &paginationV1.PagingRequest{
		Limit:         trans.Ptr(uint32(20)),
		FilteringType: &paginationV1.PagingRequest_Query{Query: emptyQuery},
	})
	require.NoError(t, err)
	require.Zero(t, empty.GetTotal(), "关联角色 999 的用户应为空")

	// limit 越界：必须 400，而不是成功空列表
	_, err = repo.List(ctx, &paginationV1.PagingRequest{
		Limit:         trans.Ptr(uint32(101)),
		FilteringType: &paginationV1.PagingRequest_Query{Query: emptyQuery},
	})
	require.True(t, identityV1.IsBadRequest(err), "空关系筛选 + limit=101 应返回 400，实际 %v", err)

	// 游标非空但无法解码：必须 400
	_, err = repo.List(ctx, &paginationV1.PagingRequest{
		Limit:         trans.Ptr(uint32(20)),
		Cursor:        trans.Ptr("c1.bogus.signature"),
		FilteringType: &paginationV1.PagingRequest_Query{Query: emptyQuery},
	})
	require.True(t, identityV1.IsBadRequest(err), "空关系筛选 + cursor=bogus 应返回 400，实际 %v", err)
}

// TestUserRepoSqlite_ListCursorNoPagingCursorConflict 验证 §3.2：
// 互斥检查移出 noPaging 分支——noPaging 与 cursor 同时出现同样应拒绝，
// 否则客户端以为在翻页、服务端却忽略了游标。
func TestUserRepoSqlite_ListCursorNoPagingCursorConflict(t *testing.T) {
	repo := newUserRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)
	createCursorUsersForTest(t, repo, ctx, 1,
		func(i int) string { return fmt.Sprintf("cursornopaging_user_%d", i) }, "noPaging 冲突")

	_, err := repo.List(ctx, &paginationV1.PagingRequest{
		NoPaging: trans.Ptr(true),
		Cursor:   trans.Ptr("c1.whatever.signature"),
	})
	require.True(t, identityV1.IsBadRequest(err), "noPaging 与 cursor 同时出现应返回 400，实际 %v", err)
}

// TestUserRepoSqlite_ListCursorFieldMaskKeepsRelationFill 验证 §3.3：
// fieldMask 不含 id（只含其它真实列）时，关联回填依赖的实体 ID 必须保留到回填
// 完成再裁剪，因此有关联的用户仍返回非空 role_ids；而 id 作为输出仍被裁掉。
func TestUserRepoSqlite_ListCursorFieldMaskKeepsRelationFill(t *testing.T) {
	repo := newUserRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	ids := createCursorUsersForTest(t, repo, ctx, 1,
		func(i int) string { return fmt.Sprintf("cursormaskrel_user_%d", i) }, "掩码关联用户")

	// 给该用户分配一个角色关联，供回填读取
	require.NoError(t, repo.AssignUserRoles(ctx, ids[0], []*permissionV1.UserRole{
		{UserId: trans.Ptr(ids[0]), RoleId: trans.Ptr(uint32(2201)), Status: trans.Ptr(permissionV1.UserRole_ACTIVE)},
	}))

	resp, err := repo.List(ctx, &paginationV1.PagingRequest{
		Limit:     trans.Ptr(uint32(1)),
		FieldMask: &fieldmaskpb.FieldMask{Paths: []string{"username"}},
	})
	require.NoError(t, err)
	require.Len(t, resp.GetItems(), 1)
	item := resp.GetItems()[0]
	require.Zero(t, item.GetId(), "id 不在 fieldMask 内，输出应被裁掉")
	require.NotEmpty(t, item.GetUsername(), "mask 内的真实列应正常返回")
	require.Equal(t, []uint32{2201}, item.GetRoleIds(),
		"fieldMask 不含 id 时，关联回填仍应返回非空 role_ids")
}

// TestUserRepoSqlite_ListCursorMultiFieldQueryPaging 验证 §3.1：
// 多字段 query（Go map 遍历顺序随机）在两页之间应保持同一指纹，
// 原样回传服务端生成的 next_cursor 不得被误判为篡改。
func TestUserRepoSqlite_ListCursorMultiFieldQueryPaging(t *testing.T) {
	repo := newUserRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	createCursorUsersForTest(t, repo, ctx, 5,
		func(i int) string { return fmt.Sprintf("cursormulti_user_%d", i) }, "多字段筛选")
	// 另造不命中的记录，确保筛选确有作用
	createCursorUsersForTest(t, repo, ctx, 2,
		func(i int) string { return fmt.Sprintf("cursormulti_other_%d", i) }, "未命中")

	query := `{"username__contains":"cursormulti_user","nickname":"多字段筛选"}`
	req := &paginationV1.PagingRequest{
		Limit:         trans.Ptr(uint32(2)),
		FilteringType: &paginationV1.PagingRequest_Query{Query: query},
	}

	var got []uint32
	for page := 1; ; page++ {
		require.LessOrEqual(t, page, 4, "翻页应当在有限页内结束")

		resp, err := repo.List(ctx, req)
		require.NoErrorf(t, err, "第 %d 页不应因游标指纹不稳定被拒", page)
		require.Equal(t, uint64(5), resp.GetTotal(), "total 应为筛选后的条数")
		for _, item := range resp.GetItems() {
			got = append(got, item.GetId())
		}
		if resp.GetNextCursor() == "" {
			break
		}
		req = &paginationV1.PagingRequest{
			Limit:         trans.Ptr(uint32(2)),
			FilteringType: &paginationV1.PagingRequest_Query{Query: query},
			Cursor:        trans.Ptr(resp.GetNextCursor()),
		}
	}
	require.Len(t, got, 5, "多字段筛选下应完整翻页，不重不漏")
}

// 说明（未在 SQLite 上断言的部分）：时间列（created_at 等）的键集翻页刻意不在本文件断言，
// 原因已实证：modernc.org/sqlite 把 time.Time 列存成 Go 的时间字符串（形如
// "2026-10-10 14:50:09.4129805 +0800 CST m=+0.058844801"，含单调时钟后缀），
// 列上比较退化为整串文本比较：参数里无法复现 m=+... 后缀，于是"同值判定"恒为 false，
// 与末行同一时刻的行会被静默跳过。这是测试夹具驱动（SQLite）的存储形态问题，
// 生产配置的驱动是 postgres（见 configs/data.yaml），时间列按绝对时刻比较，
// 该路径由部署后的真实环境用例覆盖，另在 entgo 包用 parseCursorSortValue 的单测覆盖编码/解码。

// TestUserRepoSqlite_ListCursorNullableSortCoversNullRows 验证可空排序列跨页时不漏行：
// sys_users 的绝大多数列可空，排序方向决定 NULL 位置（降序 NULL 在前、升序 NULL 在后），
// 游标落在 NULL 区段时键集条件必须继续推进，否则整个 NULL 区段会被静默跳过。
func TestUserRepoSqlite_ListCursorNullableSortCoversNullRows(t *testing.T) {
	repo := newUserRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	// u1 昵称为空串（非 NULL）、u2 有值、u3/u4 为 NULL
	nicknames := []*string{trans.Ptr(""), trans.Ptr("b"), nil, nil}
	ids := make([]uint32, 0, len(nicknames))
	for i, nickname := range nicknames {
		created, err := repo.Create(ctx, &identityV1.CreateUserRequest{Data: &identityV1.User{
			Username: trans.Ptr(fmt.Sprintf("cursornull_user_%d", i)),
			Nickname: nickname,
		}})
		require.NoError(t, err)
		ids = append(ids, created.GetId())
	}

	paginate := func(direction paginationV1.Sorting_Direction) []uint32 {
		t.Helper()
		sorting := []*paginationV1.Sorting{{Field: "nickname", Direction: direction}}
		var got []uint32
		req := &paginationV1.PagingRequest{Limit: trans.Ptr(uint32(2)), Sorting: sorting}
		for page := 1; ; page++ {
			require.LessOrEqual(t, page, 5, "翻页应当在有限页内结束")

			resp, err := repo.List(ctx, req)
			require.NoError(t, err)
			for _, item := range resp.GetItems() {
				got = append(got, item.GetId())
			}
			if resp.GetNextCursor() == "" {
				return got
			}
			req = &paginationV1.PagingRequest{
				Limit:   trans.Ptr(uint32(2)),
				Sorting: sorting,
				Cursor:  trans.Ptr(resp.GetNextCursor()),
			}
		}
	}

	require.Equal(t, []uint32{ids[0], ids[1], ids[2], ids[3]}, paginate(paginationV1.Sorting_ASC),
		"升序应为 非NULL 升序 → NULL(id 升序)，覆盖全部记录")
	require.Equal(t, []uint32{ids[3], ids[2], ids[1], ids[0]}, paginate(paginationV1.Sorting_DESC),
		"降序应为 NULL(id 降序) → 非NULL 降序，覆盖全部记录")
}

// TestUserRepoSqlite_ListCursorFieldMaskKeepsCursorGeneratable 验证 fieldMask
// 只裁剪输出：被 mask 排除的唯一 ID 在响应里为空，但服务端仍能据此生成游标，
// 且第二页取到的是另一条记录（说明游标不是由空 ID 生成的）。
func TestUserRepoSqlite_ListCursorFieldMaskKeepsCursorGeneratable(t *testing.T) {
	repo := newUserRepoSqlite(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	setUserCursorSecretForTest(t)

	createCursorUsersForTest(t, repo, ctx, 3,
		func(i int) string { return fmt.Sprintf("cursormask_user_%d", i) }, "掩码用户")

	mask := func() *paginationV1.PagingRequest {
		return &paginationV1.PagingRequest{
			Limit:     trans.Ptr(uint32(1)),
			FieldMask: &fieldmaskpb.FieldMask{Paths: []string{"username"}},
		}
	}

	first, err := repo.List(ctx, mask())
	require.NoError(t, err)
	require.Len(t, first.GetItems(), 1)
	require.NotEmpty(t, first.GetItems()[0].GetUsername(), "被 mask 保留的字段应正常返回")
	require.Zero(t, first.GetItems()[0].GetId(), "唯一 ID 不在 fieldMask 内，输出应被裁掉")
	require.NotEmpty(t, first.GetNextCursor(), "游标必须仍可生成（内部读取了排序列与唯一 ID）")

	second, err := repo.List(ctx, &paginationV1.PagingRequest{
		Limit:     trans.Ptr(uint32(1)),
		FieldMask: &fieldmaskpb.FieldMask{Paths: []string{"username"}},
		Cursor:    trans.Ptr(first.GetNextCursor()),
	})
	require.NoError(t, err)
	require.Len(t, second.GetItems(), 1)
	require.NotEqual(t, first.GetItems()[0].GetUsername(), second.GetItems()[0].GetUsername(),
		"第二页应取到下一条记录，说明游标未退化")
}
