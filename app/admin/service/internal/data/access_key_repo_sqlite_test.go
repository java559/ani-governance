package data

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	accesskeyV1 "go-wind-admin/api/gen/go/access_key/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/accesskey"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/tests/testutil"
	appcrypto "go-wind-admin/pkg/crypto"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/middleware/auth"
)

// SQLite covers repository behavior only. The Atlas composite FK and the
// acceptance path are checked separately against the isolated PostgreSQL lab.
func TestAccessKeyRepoLifecycle(t *testing.T) {
	cipher, err := appcrypto.NewAccessKeyCipher("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	repo := NewAccessKeyRepo(testutil.NewBootstrapContext(nil), testutil.NewEntClientForTest(t), cipher)
	system := testutil.NewSystemViewerCtx(context.Background())
	tenant := func(id uint64) context.Context {
		return viewer.WithContext(context.Background(), appViewer.NewUserViewer(10, id, 0, "", []viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}}))
	}
	a, b := tenant(7), tenant(8)
	r, err := repo.entClient.Client().Role.Create().SetTenantID(7).SetName("reader").SetCode("tenant:reader").SetDescription("只读角色").SetType(role.TypeTenant).SetStatus(role.StatusOn).Save(system)
	require.NoError(t, err)
	foreign, err := repo.entClient.Client().Role.Create().SetTenantID(8).SetName("other").SetCode("tenant:other").SetType(role.TypeTenant).SetStatus(role.StatusOn).Save(system)
	require.NoError(t, err)
	dto, err := repo.Create(a, &accesskeyV1.CreateAccessKeyData{Name: trans.Ptr("test"), RoleId: trans.Ptr(r.ID)}, 7, 10, "ak-test", "sk-example")
	require.NoError(t, err)
	require.True(t, dto.GetIsActive())
	require.Equal(t, "reader", dto.GetRoleName(), "创建响应应回填绑定角色名称")
	require.Equal(t, "只读角色", dto.GetRoleDescription(), "创建响应应回填绑定角色描述")
	row, err := repo.entClient.Client().AccessKey.Get(system, dto.GetId())
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(row.SecretCiphertext, "enc:"))
	require.NotContains(t, row.SecretCiphertext, "sk-example")
	signed, err := repo.LookupSigningKey(context.Background(), "ak-test")
	require.NoError(t, err)
	require.Equal(t, uint32(7), signed.TenantID)
	require.Equal(t, "sk-example", signed.Secret)
	require.NoError(t, repo.MarkSigningKeyUsed(context.Background(), dto.GetId()))
	got, err := repo.Get(a, &accesskeyV1.GetAccessKeyRequest{KeyId: dto.GetId()})
	require.NoError(t, err)
	require.NotNil(t, got.LastUsedAt)
	out, err := protojson.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(out), "secret")
	listed, err := repo.List(a, &paginationV1.PagingRequest{})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	require.True(t, listed.Items[0].GetIsActive())
	require.Equal(t, "reader", listed.Items[0].GetRoleName(), "列表项应回填绑定角色名称")
	require.Equal(t, "只读角色", listed.Items[0].GetRoleDescription(), "列表项应回填绑定角色描述")
	_, err = repo.Get(b, &accesskeyV1.GetAccessKeyRequest{KeyId: dto.GetId()})
	require.Error(t, err)
	require.Error(t, repo.Delete(b, dto.GetId(), 8))
	_, err = repo.Create(a, &accesskeyV1.CreateAccessKeyData{Name: trans.Ptr("foreign"), RoleId: trans.Ptr(foreign.ID)}, 7, 10, "ak-other", "sk-example")
	require.Error(t, err)
	update := func(data *accesskeyV1.AccessKey, paths ...string) error {
		return repo.Update(a, &accesskeyV1.UpdateAccessKeyRequest{KeyId: dto.GetId(), Data: data, UpdateMask: &fieldmaskpb.FieldMask{Paths: paths}}, 7, 10)
	}
	require.NoError(t, update(&accesskeyV1.AccessKey{IsActive: trans.Ptr(false)}, "is_active"))
	_, err = repo.LookupSigningKey(context.Background(), "ak-test")
	require.ErrorIs(t, err, auth.ErrSigningKeyRejected)
	require.NoError(t, update(&accesskeyV1.AccessKey{IsActive: trans.Ptr(true), ExpiresAt: timestamppb.New(time.Now().Add(-time.Second))}, "is_active", "expires_at"))
	_, err = repo.LookupSigningKey(context.Background(), "ak-test")
	require.ErrorIs(t, err, auth.ErrSigningKeyRejected)
	require.NoError(t, update(&accesskeyV1.AccessKey{}, "expires_at"))
	_, err = repo.LookupSigningKey(context.Background(), "ak-test")
	require.NoError(t, err)
	_, err = repo.ResetSecret(a, dto.GetId(), 7, 10, "sk-reset")
	require.NoError(t, err)
	signed, err = repo.LookupSigningKey(context.Background(), "ak-test")
	require.NoError(t, err)
	require.Equal(t, "sk-reset", signed.Secret)
	require.Error(t, update(&accesskeyV1.AccessKey{RoleId: trans.Ptr(foreign.ID), Name: trans.Ptr("must rollback")}, "name", "role_id"))
	got, err = repo.Get(a, &accesskeyV1.GetAccessKeyRequest{KeyId: dto.GetId()})
	require.NoError(t, err)
	require.Equal(t, "test", got.GetName())
	require.NoError(t, repo.entClient.Client().Role.UpdateOneID(r.ID).SetStatus(role.StatusOff).Exec(system))
	signed, err = repo.LookupSigningKey(context.Background(), "ak-test")
	require.NoError(t, err)
	require.False(t, signed.RoleAllowed)
	require.NoError(t, repo.entClient.Client().Role.UpdateOneID(r.ID).SetStatus(role.StatusOn).Exec(system))
	require.NoError(t, repo.entClient.Client().AccessKey.UpdateOneID(dto.GetId()).SetSecretCiphertext("sk-plaintext").Exec(system))
	_, err = repo.LookupSigningKey(context.Background(), "ak-test")
	require.Error(t, err)
	require.NotErrorIs(t, err, auth.ErrSigningKeyRejected)
	require.NoError(t, repo.Delete(a, dto.GetId(), 7))
	_, err = repo.LookupSigningKey(context.Background(), "ak-test")
	require.ErrorIs(t, err, auth.ErrSigningKeyRejected)
	count, err := repo.entClient.Client().AccessKey.Query().Where(accesskey.IDEQ(dto.GetId())).Count(system)
	require.NoError(t, err)
	require.Zero(t, count)
}

// TestAccessKeyRepoRoleBriefMissing 验证角色缺失时的展示语义：
// 角色查不到不报错、不回填（字段缺席），且空/为 0 的入参不发查询、不产生副作用。
func TestAccessKeyRepoRoleBriefMissing(t *testing.T) {
	cipher, err := appcrypto.NewAccessKeyCipher("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	repo := NewAccessKeyRepo(testutil.NewBootstrapContext(nil), testutil.NewEntClientForTest(t), cipher)
	ctx := viewer.WithContext(context.Background(), appViewer.NewUserViewer(10, 7, 0, "", []viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}}))

	for _, ids := range [][]uint32{nil, {}, {0}} {
		briefs, err := repo.roleBriefByIDs(ctx, ids)
		require.NoError(t, err)
		require.Empty(t, briefs, "空或全 0 的入参不应回填任何角色")
	}

	briefs, err := repo.roleBriefByIDs(ctx, []uint32{99999})
	require.NoError(t, err, "角色不存在不应让读取失败")
	require.Empty(t, briefs)
	// 角色缺失时 DTO 上两个只读字段缺席，其余字段照常。
	dto := accessKeyDTO(&ent.AccessKey{ID: 1, RoleID: 99999, AccessKey: "ak-x"}, briefs)
	require.Nil(t, dto.RoleName)
	require.Nil(t, dto.RoleDescription)
	require.Equal(t, uint32(99999), dto.GetRoleId())
}
