package data

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	accesskeyV1 "go-wind-admin/api/gen/go/access_key/service/v1"
	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/tests/testutil"
	appcrypto "go-wind-admin/pkg/crypto"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

// accessKeyIsBadRequest 判断错误是否为 api-keys 服务返回的 400。
func accessKeyIsBadRequest(err error) bool {
	return adminV1.IsBadRequest(err)
}

// newAccessKeyCursorFixture 造一个租户上下文下的 AccessKeyRepo 与绑定角色，
// 供 /api/v1/auth/api-keys 游标分页用例复用。
func newAccessKeyCursorFixture(t *testing.T) (*AccessKeyRepo, context.Context, uint32) {
	t.Helper()
	cipher, err := appcrypto.NewAccessKeyCipher("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	repo := NewAccessKeyRepo(testutil.NewBootstrapContext(nil), testutil.NewEntClientForTest(t), cipher)

	system := testutil.NewSystemViewerCtx(context.Background())
	r, err := repo.entClient.Client().Role.Create().
		SetTenantID(7).SetName("reader").SetCode("tenant:reader").
		SetType(role.TypeTenant).SetStatus(role.StatusOn).Save(system)
	require.NoError(t, err)

	tenantCtx := viewer.WithContext(context.Background(),
		appViewer.NewUserViewer(10, 7, 0, "", []viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}}))
	return repo, tenantCtx, r.ID
}

// createCursorAccessKeysForTest 造 n 把可区分的访问凭证，返回按创建顺序（id 升序）的 ID。
func createCursorAccessKeysForTest(t *testing.T, repo *AccessKeyRepo, ctx context.Context, roleID uint32, n int) []uint32 {
	t.Helper()
	ids := make([]uint32, 0, n)
	for i := range n {
		dto, err := repo.Create(ctx, &accesskeyV1.CreateAccessKeyData{
			Name:   trans.Ptr("cursor-key"),
			RoleId: trans.Ptr(roleID),
		}, 7, 10, "ak-cursor-"+string(rune('a'+i)), "sk-cursor")
		require.NoError(t, err)
		ids = append(ids, dto.GetId())
	}
	return ids
}

// TestAccessKeyRepoSqlite_ListCursorPaging 验证 api-keys 游标分页：
// 默认按唯一 ID 降序、每页 limit 条、末页 next_cursor 为空、翻页不重不漏、
// total 为当前条件下的总数，且 is_active 映射不回归、响应不含密钥密文。
func TestAccessKeyRepoSqlite_ListCursorPaging(t *testing.T) {
	repo, ctx, roleID := newAccessKeyCursorFixture(t)
	setUserCursorSecretForTest(t)

	ids := createCursorAccessKeysForTest(t, repo, ctx, roleID, 3)

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
			require.True(t, item.GetIsActive(), "内部 ON 状态应映射为公开 is_active=true")
			require.Equal(t, "reader", item.GetRoleName(), "游标分页项也应回填绑定角色名称")
			out, err := protojson.Marshal(item)
			require.NoError(t, err)
			require.NotContains(t, string(out), "secret", "凭证密文不得进入响应")
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
	want := []uint32{ids[2], ids[1], ids[0]}
	require.Equal(t, want, got, "未指定排序时应按唯一 ID 降序")
}

// TestAccessKeyRepoSqlite_ListCursorRejectsInvalidRequest 验证 api-keys 游标分页
// 对非法入参 fail-closed：limit 越界、cursor 与 page 互斥、不支持的排序列一律 400。
func TestAccessKeyRepoSqlite_ListCursorRejectsInvalidRequest(t *testing.T) {
	repo, ctx, roleID := newAccessKeyCursorFixture(t)
	setUserCursorSecretForTest(t)
	createCursorAccessKeysForTest(t, repo, ctx, roleID, 2)

	for _, limit := range []uint32{0, 101} {
		_, err := repo.List(ctx, &paginationV1.PagingRequest{Limit: trans.Ptr(limit)})
		require.Truef(t, accessKeyIsBadRequest(err), "limit=%d 应返回 400，实际 %v", limit, err)
	}

	_, err := repo.List(ctx, &paginationV1.PagingRequest{
		Page:   trans.Ptr(uint32(1)),
		Cursor: trans.Ptr("c1.bogus.signature"),
	})
	require.True(t, accessKeyIsBadRequest(err), "cursor 与 page 互斥应返回 400，实际 %v", err)

	_, err = repo.List(ctx, &paginationV1.PagingRequest{
		Limit:   trans.Ptr(uint32(1)),
		Sorting: []*paginationV1.Sorting{{Field: "secret_ciphertext", Direction: paginationV1.Sorting_ASC}},
	})
	require.True(t, accessKeyIsBadRequest(err), "排序列白名单外的字段（含 secret_ciphertext）应返回 400，实际 %v", err)

	// 篡改游标：翻转末位字符后原样回传必须被拒
	first, err := repo.List(ctx, &paginationV1.PagingRequest{Limit: trans.Ptr(uint32(1))})
	require.NoError(t, err)
	require.NotEmpty(t, first.GetNextCursor())
	_, err = repo.List(ctx, &paginationV1.PagingRequest{
		Limit:  trans.Ptr(uint32(1)),
		Cursor: trans.Ptr(flippingLastChar(first.GetNextCursor())),
	})
	require.True(t, accessKeyIsBadRequest(err), "被篡改的游标应返回 400，实际 %v", err)
}

// TestAccessKeyRepoSqlite_ListCursorFieldMaskKeepsPageItems 验证修复：
// fieldMask 被当作 SELECT 字段掩码，若不含 id，取页结果的 ID 会退化为零值，
// 随后"按 ID 二次查询实体映射 is_active"会命中不到任何行、整页 items 变空。
// 取页时应强制补入 id，使 items 正常返回。游标与非游标两条路径都要修好。
func TestAccessKeyRepoSqlite_ListCursorFieldMaskKeepsPageItems(t *testing.T) {
	repo, ctx, roleID := newAccessKeyCursorFixture(t)
	setUserCursorSecretForTest(t)
	createCursorAccessKeysForTest(t, repo, ctx, roleID, 3)

	mask := func() *fieldmaskpb.FieldMask { return &fieldmaskpb.FieldMask{Paths: []string{"name"}} }

	// 游标路径（limit，不带窗口参数）
	cursorResp, err := repo.List(ctx, &paginationV1.PagingRequest{
		Limit:     trans.Ptr(uint32(10)),
		FieldMask: mask(),
	})
	require.NoError(t, err)
	require.Equal(t, uint64(3), cursorResp.GetTotal())
	require.Len(t, cursorResp.GetItems(), 3, "fieldMask 不含 id 时游标路径仍应返回完整一页")
	for _, item := range cursorResp.GetItems() {
		require.NotZero(t, item.GetId(), "取页强制读入 id，items 应带可用 ID")
	}

	// 非游标路径（page/pageSize）
	pageResp, err := repo.List(ctx, &paginationV1.PagingRequest{
		Page:      trans.Ptr(uint32(1)),
		PageSize:  trans.Ptr(uint32(10)),
		FieldMask: mask(),
	})
	require.NoError(t, err)
	require.Equal(t, uint64(3), pageResp.GetTotal())
	require.Len(t, pageResp.GetItems(), 3, "fieldMask 不含 id 时非游标路径仍应返回完整一页")
}

// TestAccessKeyRepoSqlite_CursorDoesNotLeakSecretInMaterial 验证 §安全：
// secret_ciphertext 既不在响应中，也不能作为排序材料进入游标载荷（游标对客户端不透明）。
func TestAccessKeyRepoSqlite_CursorDoesNotLeakSecretInMaterial(t *testing.T) {
	repo, ctx, roleID := newAccessKeyCursorFixture(t)
	setUserCursorSecretForTest(t)
	createCursorAccessKeysForTest(t, repo, ctx, roleID, 2)

	system := testutil.NewSystemViewerCtx(context.Background())
	rows, err := repo.entClient.Client().AccessKey.Query().All(system)
	require.NoError(t, err)
	storedCiphertexts := make([]string, 0, len(rows))
	for _, e := range rows {
		storedCiphertexts = append(storedCiphertexts, e.SecretCiphertext)
	}
	require.NotEmpty(t, storedCiphertexts)

	resp, err := repo.List(ctx, &paginationV1.PagingRequest{Limit: trans.Ptr(uint32(1))})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetNextCursor(), "存在下一条时应返回游标")
	for _, ciphertext := range storedCiphertexts {
		require.NotContains(t, resp.GetNextCursor(), ciphertext,
			"游标载荷不得包含密钥密文")
	}
}
