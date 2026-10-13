package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	accesskeyV1 "go-wind-admin/api/gen/go/access_key/service/v1"
	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	"go-wind-admin/pkg/middleware/auth"
)

func TestAccessKeyServiceCRUD(t *testing.T) {
	client := testutil.NewEntClientForTest(t)
	system := testutil.NewSystemViewerCtx(context.Background())
	r, err := client.Client().Role.Create().SetTenantID(7).SetName("reader").SetCode("tenant:reader").SetType(role.TypeTenant).SetStatus(role.StatusOn).Save(system)
	require.NoError(t, err)
	ctx := viewer.WithContext(context.Background(), appViewer.NewUserViewer(10, 7, 0, "", []viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}}))
	ctx = auth.NewContext(ctx, &authenticationV1.UserTokenPayload{UserId: 10, TenantId: trans.Ptr(uint32(7))})
	svc := NewAccessKeyService(nil, newAccessKeyRepo(t, client))
	created, err := svc.Create(ctx, &accesskeyV1.CreateAccessKeyRequest{Data: &accesskeyV1.CreateAccessKeyData{Name: trans.Ptr("reader"), RoleId: trans.Ptr(r.ID)}})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(created.SecretKey, "sk-"))
	require.True(t, strings.HasPrefix(created.Data.GetAccessKey(), "ak-"))
	require.Equal(t, "reader", created.Data.GetRoleName(), "创建响应应回填绑定角色名称")
	reset, err := svc.ResetSecret(ctx, &accesskeyV1.ResetAccessKeySecretRequest{KeyId: created.Data.GetId()})
	require.NoError(t, err)
	require.NotEqual(t, created.SecretKey, reset.SecretKey)
	require.Equal(t, created.Data.GetAccessKey(), reset.Data.GetAccessKey())
	require.Equal(t, "reader", reset.Data.GetRoleName(), "重置密钥响应也应回填绑定角色名称")
	deleted, err := svc.Delete(ctx, &accesskeyV1.DeleteAccessKeyRequest{KeyId: created.Data.GetId()})
	require.NoError(t, err)
	require.Equal(t, "revoked", deleted.Status)
	machine := auth.NewContext(ctx, &authenticationV1.UserTokenPayload{UserId: 0, TenantId: trans.Ptr(uint32(7))})
	_, err = svc.Get(machine, &accesskeyV1.GetAccessKeyRequest{KeyId: created.Data.GetId()})
	require.Error(t, err)
	_, err = svc.Create(ctx, nil)
	require.Error(t, err)
}

// Create 的幂等分支：同键同意图重放不返回明文 secret_key，异意图返回 409。
func TestAccessKeyServiceCreateIdempotency(t *testing.T) {
	client := testutil.NewEntClientForTest(t)
	system := testutil.NewSystemViewerCtx(context.Background())
	r, err := client.Client().Role.Create().SetTenantID(7).SetName("reader").SetCode("tenant:reader").SetType(role.TypeTenant).SetStatus(role.StatusOn).Save(system)
	require.NoError(t, err)
	ctx := viewer.WithContext(context.Background(), appViewer.NewUserViewer(10, 7, 0, "", []viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}}))
	ctx = auth.NewContext(ctx, &authenticationV1.UserTokenPayload{UserId: 10, TenantId: trans.Ptr(uint32(7))})
	svc := NewAccessKeyService(nil, newAccessKeyRepo(t, client))

	req := func(key string) *accesskeyV1.CreateAccessKeyRequest {
		return &accesskeyV1.CreateAccessKeyRequest{Data: &accesskeyV1.CreateAccessKeyData{Name: trans.Ptr("reader"), RoleId: trans.Ptr(r.ID), IdempotencyKey: trans.Ptr(key)}}
	}
	first, err := svc.Create(ctx, req("svc-key-1"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(first.SecretKey, "sk-"))
	require.Equal(t, "reader", first.Data.GetRoleName(), "首次创建应回填绑定角色名称")

	replay, err := svc.Create(ctx, req("svc-key-1"))
	require.NoError(t, err)
	require.Equal(t, first.Data.GetId(), replay.Data.GetId(), "同键同意图应回放同一把 Key")
	require.Empty(t, replay.SecretKey, "重放不得再交付明文 SK")
	require.Equal(t, "reader", replay.Data.GetRoleName(), "幂等重放响应应与首次一致地回填角色名称")

	conflict := req("svc-key-1")
	conflict.Data.Name = trans.Ptr("renamed")
	_, err = svc.Create(ctx, conflict)
	require.Error(t, err)
	require.True(t, adminV1.IsConflict(err), "同键异意图必须映射 409 CONFLICT: %v", err)

	tooLong := req(strings.Repeat("k", 129))
	_, err = svc.Create(ctx, tooLong)
	require.Error(t, err)
	require.True(t, adminV1.IsBadRequest(err))
}
