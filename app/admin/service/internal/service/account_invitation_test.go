package service

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	"go-wind-admin/pkg/localdeps/go-utils/password"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/notificationchannel"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/usercredential"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/tests/testutil"
	"go-wind-admin/pkg/constants"
	"go-wind-admin/pkg/middleware/auth"
)

// With ANI_ONBOARDING_TEST_DSN these same tests use a fresh PostgreSQL schema
// per case, dropped on cleanup. The DSN must point to a disposable test database.
func onboardingClient(t *testing.T) *entCrud.EntClient[*ent.Client] {
	t.Helper()
	dsn := os.Getenv("ANI_ONBOARDING_TEST_DSN")
	if dsn == "" {
		return testutil.NewEntClientForTest(t)
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("onboarding_%d", time.Now().UnixNano())
	_, err = db.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE"); _ = db.Close() })
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	driver, err := entsql.Open(dialect.Postgres, u.String())
	require.NoError(t, err)
	client := ent.NewClient(ent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Schema.Create(context.Background()))
	return entCrud.NewEntClient[*ent.Client](client, driver)
}

type onboardingEnv struct {
	ctx         context.Context
	client      *ent.Client
	users       *UserService
	tenants     *TenantService
	credentials *data.UserCredentialRepo
	roleID      uint32
}

func newOnboardingEnv(t *testing.T) *onboardingEnv {
	t.Helper()
	ec := onboardingClient(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	bctx := bootstrap.NewContextWithParam(ctx, nil, nil, bLogger.NopLogger())
	u := data.NewUserRepo(bctx, ec, data.NewUserRoleRepo(bctx, ec), data.NewUserOrgUnitRepo(bctx, ec), data.NewUserPositionRepo(bctx, ec), nil, newRoleRepo(ec))
	c := data.NewUserCredentialRepo(newRepoContext(), ec, password.NewBCryptCrypto(), newConfigRepo(t, ec, nil))
	r := newRoleRepo(ec)
	channels := data.NewNotificationChannelRepo(newRepoContext(), ec)
	operator := ec.Client().User.Create().SetTenantID(0).SetUsername("operator").SaveX(ctx)
	platformRole := ec.Client().Role.Create().SetTenantID(0).SetCode(constants.PlatformAdminRoleCode).SetName("platform admin").SetType(role.TypeSystem).SaveX(ctx)
	ec.Client().Role.Create().SetTenantID(0).SetCode(constants.TenantAdminTemplateRoleCode).SetName("template").SetType(role.TypeTemplate).SetStatus(role.StatusOn).SetIsProtected(true).SaveX(ctx)
	return &onboardingEnv{
		ctx:    auth.NewContext(ctx, &authV1.UserTokenPayload{UserId: operator.ID, TenantId: trans.Ptr(uint32(0))}),
		client: ec.Client(), credentials: c, roleID: platformRole.ID,
		users:   &UserService{log: bLogger.NewHelper(bLogger.NopLogger()), userRepo: u, userCredentialRepo: c, roleRepo: r, notificationChannels: channels},
		tenants: &TenantService{log: bLogger.NewHelper(bLogger.NopLogger()), tenantRepo: data.NewTenantRepo(newRepoContext(), ec), userRepo: u, userCredentialsRepo: c, roleRepo: r, notificationChannels: channels},
	}
}

// An isolated SMTP receiver validates the real net/smtp exchange; no real mail
// is sent. reject simulates an SMTP delivery refusal before DATA acceptance.
func (e *onboardingEnv) smtp(t *testing.T, reject bool) <-chan string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	messages := make(chan string, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				fmt.Fprint(conn, "220 localhost ESMTP\r\n")
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					line := scanner.Text()
					switch {
					case strings.HasPrefix(line, "RCPT") && reject:
						fmt.Fprint(conn, "550 rejected\r\n")
					case line == "DATA":
						fmt.Fprint(conn, "354 send mail\r\n")
						var body strings.Builder
						for scanner.Scan() {
							if scanner.Text() == "." {
								break
							}
							body.WriteString(scanner.Text())
							body.WriteByte('\n')
						}
						messages <- body.String()
						fmt.Fprint(conn, "250 accepted\r\n")
					case line == "QUIT":
						fmt.Fprint(conn, "221 bye\r\n")
						return
					default:
						fmt.Fprint(conn, "250 localhost\r\n")
					}
				}
			}()
		}
	}()
	e.client.NotificationChannel.Create().SetName("onboarding SMTP").SetStatus(notificationchannel.StatusOn).
		SetSMTPHost("127.0.0.1").SetSMTPPort(uint32(ln.Addr().(*net.TCPAddr).Port)).SetSMTPFrom("ani@example.com").SetSMTPTLS(notificationchannel.SMTPTLSNone).SaveX(e.ctx)
	t.Setenv("ANI_INVITATION_BASE_URL", "http://127.0.0.1:7788")
	return messages
}

func TestOnboardingModes(t *testing.T) {
	for _, tenant := range []bool{false, true} {
		for _, invite := range []bool{false, true} {
			t.Run(fmt.Sprintf("tenant=%v/invite=%v", tenant, invite), func(t *testing.T) {
				e := newOnboardingEnv(t)
				t.Setenv("ANI_INVITATION_BASE_URL", "invalid-unused-in-immediate-mode")
				var messages <-chan string
				mode, preset := identityV1.ActivationMode_IMMEDIATE, "Password123!"
				if invite {
					mode, preset = identityV1.ActivationMode_EMAIL_INVITATION, ""
					messages = e.smtp(t, false)
				}
				u := &identityV1.User{Username: trans.Ptr("invited"), Email: trans.Ptr("invite@example.com"), RoleIds: []uint32{e.roleID}}
				var err error
				if tenant {
					_, err = e.tenants.CreateTenantWithAdminUser(e.ctx, &identityV1.CreateTenantWithAdminUserRequest{
						Tenant: &identityV1.Tenant{Name: trans.Ptr("Tenant"), Code: trans.Ptr("tenant")}, User: u, Password: preset, ActivationMode: mode,
					})
				} else {
					_, err = e.users.Create(e.ctx, &identityV1.CreateUserRequest{Data: u, Password: trans.Ptr(preset), ActivationMode: mode})
				}
				require.NoError(t, err)
				created := e.client.User.Query().Where(user.UsernameEQ("invited")).OnlyX(e.ctx)
				tid := *created.TenantID
				relations := e.client.UserRole.Query().Where(userrole.UserIDEQ(created.ID)).AllX(e.ctx)
				require.Len(t, relations, 1)
				require.Equal(t, tid, *relations[0].TenantID)
				if tenant {
					tr := e.client.Tenant.GetX(e.ctx, tid)
					require.Equal(t, created.ID, *tr.AdminUserID)
					require.Equal(t, tid, *e.client.Role.GetX(e.ctx, *relations[0].RoleID).TenantID)
				}
				if invite {
					require.Equal(t, user.StatusPending, *created.Status)
					var body string
					select {
					case body = <-messages:
					case <-time.After(time.Second):
						t.Fatal("no SMTP message")
					}
					match := regexp.MustCompile(`/api/v1/auth/invitations/accept#([A-Za-z0-9_-]{43})`).FindStringSubmatch(body)
					require.Len(t, match, 2)
					// Real generated public HTTP handler plus repository transaction.
					srv := khttp.NewServer()
					adminV1.RegisterAuthenticationServiceHTTPServer(srv, &AuthenticationService{userCredentialRepo: e.credentials})
					rr := httptest.NewRecorder()
					req := httptest.NewRequest("POST", InvitationPath, strings.NewReader(fmt.Sprintf(`{"token":%q,"password":"Password123!"}`, match[1])))
					req.Header.Set("Content-Type", "application/json")
					srv.ServeHTTP(rr, req)
					require.Equal(t, 200, rr.Code, rr.Body.String())
					require.Error(t, e.credentials.AcceptInvitation(context.Background(), match[1], "OtherPassword123!"))
					confirmed := e.client.UserCredential.Query().Where(usercredential.UserIDEQ(created.ID), usercredential.IdentityTypeEQ(usercredential.IdentityTypeEmail)).OnlyX(e.ctx)
					require.Equal(t, tid, *confirmed.TenantID)
				}
				require.Equal(t, user.StatusNormal, *e.client.User.GetX(e.ctx, created.ID).Status)
				id, err := e.credentials.FindUserCredential(e.ctx, tid, authV1.UserCredential_USERNAME, "invited", "Password123!", false)
				require.NoError(t, err)
				require.Equal(t, created.ID, id)
			})
		}
	}
}

func TestOnboardingSendFailureRollsBack(t *testing.T) {
	for _, tenant := range []bool{false, true} {
		t.Run(fmt.Sprintf("tenant=%v", tenant), func(t *testing.T) {
			e := newOnboardingEnv(t)
			e.smtp(t, true)
			roles := e.client.Role.Query().CountX(e.ctx)
			u := &identityV1.User{Username: trans.Ptr("rollback"), Email: trans.Ptr("invite@example.com"), RoleIds: []uint32{e.roleID}}
			var err error
			if tenant {
				_, err = e.tenants.CreateTenantWithAdminUser(e.ctx, &identityV1.CreateTenantWithAdminUserRequest{Tenant: &identityV1.Tenant{Name: trans.Ptr("rollback"), Code: trans.Ptr("rollback")}, User: u, ActivationMode: identityV1.ActivationMode_EMAIL_INVITATION})
			} else {
				_, err = e.users.Create(e.ctx, &identityV1.CreateUserRequest{Data: u, ActivationMode: identityV1.ActivationMode_EMAIL_INVITATION})
			}
			require.ErrorContains(t, err, "rolled back")
			require.Equal(t, 1, e.client.User.Query().CountX(e.ctx))
			require.Zero(t, e.client.UserCredential.Query().CountX(e.ctx))
			require.Zero(t, e.client.UserRole.Query().CountX(e.ctx))
			require.Zero(t, e.client.Tenant.Query().CountX(e.ctx))
			require.Equal(t, roles, e.client.Role.Query().CountX(e.ctx))
		})
	}
}

func TestOnboardingConcurrentAcceptance(t *testing.T) {
	if os.Getenv("ANI_ONBOARDING_TEST_DSN") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	e := newOnboardingEnv(t)
	messages := e.smtp(t, false)
	_, err := e.users.Create(e.ctx, &identityV1.CreateUserRequest{
		Data:           &identityV1.User{Username: trans.Ptr("concurrent"), Email: trans.Ptr("invite@example.com"), RoleIds: []uint32{e.roleID}},
		ActivationMode: identityV1.ActivationMode_EMAIL_INVITATION,
	})
	require.NoError(t, err)
	var body string
	select {
	case body = <-messages:
	case <-time.After(time.Second):
		t.Fatal("no SMTP message")
	}
	match := regexp.MustCompile(`/api/v1/auth/invitations/accept#([A-Za-z0-9_-]{43})`).FindStringSubmatch(body)
	require.Len(t, match, 2)
	start := make(chan struct{})
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- e.credentials.AcceptInvitation(context.Background(), match[1], "Password123!")
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes, "exactly one transaction may consume the token")
	require.Equal(t, 1, e.client.UserCredential.Query().Where(usercredential.IdentityTypeEQ(usercredential.IdentityTypeEmail)).CountX(e.ctx))
}

func TestOnboardingInvitationGuards(t *testing.T) {
	e := newOnboardingEnv(t)
	for _, test := range []struct {
		mode                    identityV1.ActivationMode
		email, password, origin string
	}{
		{mode: 99},
		{mode: identityV1.ActivationMode_EMAIL_INVITATION, email: "invalid"},
		{mode: identityV1.ActivationMode_EMAIL_INVITATION, email: "invite@example.com", password: "Password123!"},
		{mode: identityV1.ActivationMode_EMAIL_INVITATION, email: "invite@example.com", origin: "http://public.example.com"},
		{mode: identityV1.ActivationMode_EMAIL_INVITATION, email: "invite@example.com", origin: "https://public.example.com"}, // no SMTP
	} {
		t.Setenv("ANI_INVITATION_BASE_URL", test.origin)
		_, err := e.users.Create(e.ctx, &identityV1.CreateUserRequest{Data: &identityV1.User{Username: trans.Ptr("rejected"), Email: trans.Ptr(test.email), RoleIds: []uint32{e.roleID}}, Password: trans.Ptr(test.password), ActivationMode: test.mode})
		require.Error(t, err)
		require.Equal(t, 1, e.client.User.Query().CountX(e.ctx))
	}
	// A tenant operator cannot create a new tenant or assign a platform role.
	ctx := auth.NewContext(e.ctx, &authV1.UserTokenPayload{UserId: 1, TenantId: trans.Ptr(uint32(7))})
	_, err := e.tenants.CreateTenantWithAdminUser(ctx, &identityV1.CreateTenantWithAdminUserRequest{Tenant: &identityV1.Tenant{}, User: &identityV1.User{}})
	require.Error(t, err)
	_, err = e.users.Create(ctx, &identityV1.CreateUserRequest{Data: &identityV1.User{Username: trans.Ptr("escalate"), TenantId: trans.Ptr(uint32(0)), RoleIds: []uint32{e.roleID}}})
	require.Error(t, err)
}

func TestOnboardingAPIRegistration(t *testing.T) {
	if os.Getenv("ANI_ONBOARDING_TEST_DSN") == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ec := onboardingClient(t)
	ctx := testutil.NewSystemViewerCtx(context.Background())
	ec.Client().Api.Create().SetID(900).SetModule("UserService").SetPath("/api/v1/admin/users").SetMethod("POST").SaveX(ctx)
	script, err := os.ReadFile("../../../../../scripts/bootstrap-invitation-api.sql")
	require.NoError(t, err)
	_, err = ec.DB().ExecContext(ctx, string(script))
	require.NoError(t, err)
	invitation := ec.Client().Api.Query().Where(api.PathEQ(InvitationPath), api.MethodEQ("POST")).OnlyX(ctx)
	require.Greater(t, invitation.ID, uint32(900))
	_, err = ec.DB().ExecContext(ctx, string(script))
	require.NoError(t, err)
	require.Equal(t, 2, ec.Client().Api.Query().CountX(ctx))
	require.Equal(t, "/api/v1/admin/users", *ec.Client().Api.GetX(ctx, 900).Path)
	require.Equal(t, invitation.ID, ec.Client().Api.Query().Where(api.PathEQ(InvitationPath)).OnlyX(ctx).ID)
}
