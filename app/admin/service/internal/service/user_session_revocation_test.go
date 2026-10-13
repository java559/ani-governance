package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
)

// Exercise the real user repository and Redis revocation path, including a
// status excluded by update_mask and Redis failures after the database commit.
func TestUserService_AccountSessionRevocation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    *identityV1.User_Status
		mask      []string
		delete    bool
		self      bool
		tenant    bool
		redisFail bool
		revoked   bool
	}{
		{name: "disable", status: identityV1.User_DISABLED.Enum(), mask: []string{"status"}, revoked: true},
		{name: "lock", status: identityV1.User_LOCKED.Enum(), mask: []string{"status"}, revoked: true},
		{name: "delete", delete: true, revoked: true},
		{name: "disable tenant account", tenant: true, status: identityV1.User_DISABLED.Enum(), mask: []string{"status"}, revoked: true},
		{name: "delete tenant account", tenant: true, delete: true, revoked: true},
		{name: "edit profile", mask: []string{"nickname"}},
		{name: "status excluded by mask", status: identityV1.User_DISABLED.Enum(), mask: []string{"nickname"}},
		{name: "protected deletion", delete: true, self: true},
		{name: "disable redis failure", status: identityV1.User_DISABLED.Enum(), mask: []string{"status"}, redisFail: true},
		{name: "delete redis failure", delete: true, redisFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newOnboardingEnv(t)
			mr := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
			t.Cleanup(func() { _ = rdb.Close() })
			cache := data.NewUserTokenCache(newRepoContext(), rdb)
			a := newAuthenticator(t, &conf.Authentication_Jwt{Method: "HS256", Key: authSvcTestJWTKey}, cache)
			e.users.authenticator = a
			var err error

			tenantID, roleID := uint32(0), e.roleID
			if tc.tenant {
				tenantID = e.client.Tenant.Create().SetName("session tenant").SetCode("session-tenant").SaveX(e.ctx).ID
				roleID = e.client.Role.Create().SetTenantID(tenantID).SetCode("tenant:manager").SetName("tenant manager").SetType(role.TypeTenant).SaveX(e.ctx).ID
			}
			target := e.client.User.Create().SetTenantID(tenantID).SetUsername("session-target").SetStatus(user.StatusNormal).SaveX(e.ctx)
			other := e.client.User.GetX(e.ctx, 1) // protected operator; its sessions must survive
			type session struct {
				uid, ct                   uint32
				jti, sid, access, refresh string
			}
			var sessions []session
			for _, uid := range []uint32{target.ID, other.ID} {
				for i := 0; i < 2; i++ {
					tid := uint32(0)
					if uid == target.ID {
						tid = tenantID
					}
					// 会话令牌对必须携带 application 与 sid（服务端会话锚点）：
					// sid 唯一，供按逻辑会话吊销与轮换探测使用。
					sid := fmt.Sprintf("test-sid-%d-%d", uid, i)
					payload := &authV1.UserTokenPayload{UserId: uid, TenantId: trans.Ptr(tid), Application: authV1.ApplicationType_console.Enum(), Sid: trans.Ptr(sid)}
					access, refresh, issueErr := a.CreateUserToken(e.ctx, authV1.ClientType_admin, payload)
					require.NoError(t, issueErr)
					require.NoError(t, a.SaveSessionMeta(e.ctx, authV1.ClientType_admin, uid, payload.GetJti(), &data.SessionMeta{Username: "test"}, time.Hour))
					sessions = append(sessions, session{uid: uid, jti: payload.GetJti(), sid: sid, access: access, refresh: refresh})
				}
				// The app client has no signing implementation; verify its cache
				// entries are still covered by the all-client revocation helper.
				require.NoError(t, cache.AddTokenPair(e.ctx, authV1.ClientType_app, uid, "app-session", "app-access", "app-refresh", time.Hour, time.Hour))
				require.NoError(t, cache.SaveSessionMeta(e.ctx, authV1.ClientType_app, uid, "app-session", &data.SessionMeta{Username: "test"}, time.Hour))
				sessions = append(sessions, session{uid: uid, ct: uint32(authV1.ClientType_app), jti: "app-session", access: "app-access", refresh: "app-refresh"})
			}

			if tc.redisFail {
				mr.SetError("ERR injected Redis failure")
			}
			if tc.delete {
				id := target.ID
				if tc.self {
					id = 1 // newOnboardingEnv's protected platform operator
				}
				_, err = e.users.Delete(e.ctx, &identityV1.DeleteUserRequest{QueryBy: &identityV1.DeleteUserRequest_Id{Id: id}})
			} else {
				_, err = e.users.Update(e.ctx, &identityV1.UpdateUserRequest{
					Id:         target.ID,
					Data:       &identityV1.User{TenantId: trans.Ptr(tenantID), Status: tc.status, Nickname: trans.Ptr("edited"), RoleIds: []uint32{roleID}},
					UpdateMask: &fieldmaskpb.FieldMask{Paths: tc.mask},
				})
			}
			mr.SetError("")
			if tc.redisFail {
				require.True(t, adminV1.IsInternalServerError(err), "partial failure must not report success: %v", err)
				require.Contains(t, err.Error(), "session revocation failed")
			} else if tc.self {
				require.True(t, adminV1.IsBadRequest(err))
			} else {
				require.NoError(t, err)
			}

			exists, err := e.client.User.Query().Where(user.IDEQ(target.ID)).Exist(e.ctx)
			require.NoError(t, err)
			require.Equal(t, !tc.delete || tc.self, exists)
			if exists && !tc.delete {
				persisted, getErr := e.client.User.Get(e.ctx, target.ID)
				require.NoError(t, getErr)
				want := user.StatusNormal
				if tc.status != nil && tc.mask[0] == "status" {
					want = user.Status(tc.status.String())
				}
				require.Equal(t, want, *persisted.Status)
			}
			for _, s := range sessions {
				clientType := authV1.ClientType(s.ct)
				wantValid := !tc.revoked || s.uid != target.ID
				valid, checkErr := cache.IsValidAccessToken(e.ctx, clientType, s.uid, s.jti, s.access)
				require.NoError(t, checkErr)
				require.Equal(t, wantValid, valid)
				valid, checkErr = cache.IsValidRefreshToken(e.ctx, clientType, s.uid, s.jti, s.refresh)
				require.NoError(t, checkErr)
				require.Equal(t, wantValid, valid)
				meta, checkErr := a.GetSessionMeta(e.ctx, clientType, s.uid, s.jti)
				require.NoError(t, checkErr)
				require.Equal(t, wantValid, meta != nil)
				if clientType == authV1.ClientType_admin {
					resp, authErr := a.Authenticate(e.ctx, &authV1.ValidateTokenRequest{Token: s.access, ClientType: clientType, TokenCategory: authV1.TokenCategory_ACCESS})
					if wantValid {
						require.NoError(t, authErr)
						require.True(t, resp.GetIsValid())
					} else {
						require.Error(t, authErr)
						// 吊销检测已移至提交阶段：VerifyRefreshToken 只验签
						// （JWT 仍自洽），会话是否被吊销由 sid 指针是否仍指向
						// 该 jti 决定——用 RotateSession 探测：被吊销会话的
						// sid 指针已删除，提交必须被拒绝。
						committed, rotateErr := cache.RotateSession(e.ctx, clientType, s.uid, s.sid, s.jti, "probe-jti", "probe-at", "probe-rt", nil, time.Hour, time.Hour)
						require.NoError(t, rotateErr)
						require.False(t, committed, "被吊销会话的轮换提交必须被拒绝")
					}
				}
			}
		})
	}
}
