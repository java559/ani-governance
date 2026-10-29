//go:build modeldev_management_pg

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/pem"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type modelDevManagementBudgetPeer struct {
	modeldevv1.UnimplementedModelDevOperationsServiceServer
	remaining     chan time.Duration
	tenant, actor string
	t             *testing.T
}

func (p *modelDevManagementBudgetPeer) observe(ctx context.Context, method string) {
	p.t.Helper()
	md, _ := metadata.FromIncomingContext(ctx)
	require.Equal(p.t, []string{p.tenant}, md.Get("x-ani-tenant-id"))
	require.Equal(p.t, []string{p.actor}, md.Get("x-ani-actor"))
	require.Equal(p.t, []string{method}, md.Get("x-ani-authorized-method"))
	deadline, ok := ctx.Deadline()
	require.True(p.t, ok)
	p.remaining <- time.Until(deadline)
}
func (p *modelDevManagementBudgetPeer) ReconcileExecution(ctx context.Context, _ *modeldevv1.ReconcileExecutionRequest) (*modeldevv1.ReconcileExecutionResponse, error) {
	p.observe(ctx, modeldevv1.ModelDevOperationsService_ReconcileExecution_FullMethodName)
	return nil, status.Error(codes.FailedPrecondition, "budget observed")
}
func (p *modelDevManagementBudgetPeer) PlanExecutionCleanup(ctx context.Context, _ *modeldevv1.PlanExecutionCleanupRequest) (*modeldevv1.PlanExecutionCleanupResponse, error) {
	p.observe(ctx, modeldevv1.ModelDevOperationsService_PlanExecutionCleanup_FullMethodName)
	return nil, status.Error(codes.FailedPrecondition, "budget observed")
}
func runModelDevManagementBudgetCase(t *testing.T, directory, tokenFile, tenant, actor string) {
	t.Helper()
	config, security := modelDevBudgetTLS(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	peer := &modelDevManagementBudgetPeer{remaining: make(chan time.Duration, 1), tenant: tenant, actor: actor, t: t}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
	modeldevv1.RegisterModelDevOperationsServiceServer(server, peer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	for key, value := range map[string]string{"ANI_MODELDEV_ADDR": listener.Addr().String(), "ANI_MODELDEV_CA": config.CAFile, "ANI_MODELDEV_CERT": config.CertFile, "ANI_MODELDEV_KEY": config.KeyFile, "ANI_MODELDEV_TIMEOUT": "2m"} {
		t.Setenv(key, value)
	}
	request := filepath.Join(t.TempDir(), "request.json")
	require.NoError(t, os.WriteFile(request, []byte(`{"execution_id":"`+uuid.NewString()+`"}`), 0600))
	for _, tc := range []struct {
		name, command        string
		caller, lower, upper time.Duration
	}{
		{"reconcile outer and client five minutes", "modeldev-reconcile", 0, 4 * time.Minute, 5 * time.Minute},
		{"other command remains two minutes", "modeldev-cleanup-plan", 0, time.Minute, 2 * time.Minute},
		{"shorter caller retained", "modeldev-reconcile", time.Second, 500 * time.Millisecond, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(request, []byte(`{"execution_id":"`+uuid.NewString()+`"}`), 0600))
			ctx := context.Background()
			if tc.caller > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.caller)
				defer cancel()
			}
			var output bytes.Buffer
			require.EqualError(t, runAdmin(ctx, []string{tc.command, "--conf", directory, "--token-file", tokenFile, "--request-file", request}, &output), "modeldev management remote operation failed (FailedPrecondition)")
			remaining := <-peer.remaining
			require.GreaterOrEqual(t, remaining, tc.lower, "COMMAND_TIME_BUDGET")
			require.LessOrEqual(t, remaining, tc.upper)
		})
	}
}

// The budget is observed only after normal current PG grants, JWT/Redis session
// validation, tenant mapping and a fresh production mTLS client have succeeded.
func TestModelDevReconcileCommandFiniteTimeBudget(t *testing.T) {
	ctx := context.Background()
	runtimeDSN := os.Getenv("ANI_TEST_DATABASE_DSN")
	fixtureFile := os.Getenv("ANI_MODELDEV_FIXTURE_DSN_FILE")
	if os.Getenv("ANI_TEST_DATABASE_EXCLUSIVE") != "1" || runtimeDSN == "" || !filepath.IsAbs(fixtureFile) {
		t.Fatal("exclusive task PG required")
	}
	info, err := os.Lstat(fixtureFile)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	raw, err := os.ReadFile(fixtureFile)
	require.NoError(t, err)
	fixtureDSN := strings.TrimSpace(string(raw))
	a, err := pgx.ParseConfig(runtimeDSN)
	require.NoError(t, err)
	b, err := pgx.ParseConfig(fixtureDSN)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", a.Host)
	require.Equal(t, a.Host, b.Host)
	require.Equal(t, a.Port, b.Port)
	require.Equal(t, a.Database, b.Database)
	require.NotEqual(t, a.User, b.User)
	open := func(dsn string) *entCrud.EntClient[*ent.Client] {
		db, err := sql.Open("pgx", dsn)
		require.NoError(t, err)
		driver := entsql.OpenDB("postgres", db)
		client := ent.NewClient(ent.Driver(driver))
		t.Cleanup(func() { require.NoError(t, client.Close()) })
		return entCrud.NewEntClient(client, driver)
	}
	writer, runtime := open(fixtureDSN), open(runtimeDSN)
	var runtimeSuper, runtimeBypass bool
	require.NoError(t, runtime.DB().QueryRowContext(ctx, "SELECT rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user").Scan(&runtimeSuper, &runtimeBypass))
	require.False(t, runtimeSuper)
	require.False(t, runtimeBypass)
	sys := appViewer.NewSystemViewerContext(ctx)
	cleanup := func(fn func(context.Context) error) {
		t.Cleanup(func() {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, fn(appViewer.NewSystemViewerContext(c)))
		})
	}
	suffix := uuid.NewString()
	resourceTenant := uuid.NewString()
	owner, err := writer.Client().Tenant.Create().SetCode("budget-" + suffix).SetName("budget fixture").SetResourceTenantID(resourceTenant).SetStatus(tenant.StatusOn).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Tenant.DeleteOneID(owner.ID).Exec(c) })
	operator, err := writer.Client().User.Create().SetTenantID(owner.ID).SetUsername("budget-" + suffix).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().User.DeleteOneID(operator.ID).Exec(c) })
	currentRole, err := writer.Client().Role.Create().SetTenantID(owner.ID).SetName("budget maintainer").SetCode("tenant:budget:" + suffix).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Role.DeleteOneID(currentRole.ID).Exec(c) })
	now := time.Now().UTC().Truncate(time.Microsecond)
	membership, err := writer.Client().UserRole.Create().SetTenantID(owner.ID).SetUserID(operator.ID).SetRoleID(currentRole.ID).SetStatus(userrole.StatusActive).SetStartAt(now.Add(-time.Hour)).SetEndAt(now.Add(time.Hour)).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().UserRole.DeleteOneID(membership.ID).Exec(c) })
	point, err := writer.Client().Permission.Create().SetName("budget management").SetCode(data.ModelDevManageExecutionPermissionCode).SetStatus(permission.StatusOn).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Permission.DeleteOneID(point.ID).Exec(c) })
	grant, err := writer.Client().RolePermission.Create().SetTenantID(owner.ID).SetRoleID(currentRole.ID).SetPermissionID(point.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().RolePermission.DeleteOneID(grant.ID).Exec(c) })
	redisAddress := os.Getenv("ANI_MODELDEV_REDIS_ADDR")
	host, _, err := net.SplitHostPort(redisAddress)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", host)
	for _, name := range []string{"GWA_AUTH_JWT_PRIVATE_KEY", "GWA_AUTH_JWT_PUBLIC_KEY", "GWA_AUTH_JWT_KEY"} {
		t.Setenv(name, "")
	}
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	cfg := &conf.Bootstrap{Data: &conf.Data{Database: &conf.Data_Database{Driver: "postgres", Source: runtimeDSN, Migrate: false, MaxOpenConnections: trans.Ptr(int32(2)), MaxIdleConnections: trans.Ptr(int32(1))}, Redis: &conf.Data_Redis{Addr: redisAddress}}, Authn: &conf.Authentication{Type: "jwt", Jwt: &conf.Authentication_Jwt{Method: "HS256", Key: hex.EncodeToString(key)}}, Authz: &conf.Authorization{Type: "casbin"}}
	bctx := testutil.NewBootstrapContext(cfg)
	rdb := redis.NewClient(&redis.Options{Addr: redisAddress})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	cache := data.NewUserTokenCache(bctx, rdb)
	authenticator := data.NewAuthenticator(bctx, cache)
	payload := &authv1.UserTokenPayload{UserId: operator.ID, TenantId: trans.Ptr(owner.ID), Roles: []string{*currentRole.Code}, Application: authv1.ApplicationType_console.Enum(), Sid: trans.Ptr(uuid.NewString())}
	token, _, err := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, payload)
	require.NoError(t, err)
	cleanup(func(c context.Context) error {
		return cache.RevokeTokenByJti(c, authv1.ClientType_admin, operator.ID, payload.GetJti())
	})
	private := t.TempDir()
	require.NoError(t, os.Chmod(private, 0700))
	configDirectory := filepath.Join(private, "config")
	require.NoError(t, os.Mkdir(configDirectory, 0700))
	configuration, err := protojson.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(configDirectory, "bootstrap.json"), configuration, 0600))
	tokenFile := filepath.Join(private, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(token), 0600))
	actor := "governance:user:" + strconv.FormatUint(uint64(operator.ID), 10)
	runModelDevManagementBudgetCase(t, configDirectory, tokenFile, resourceTenant, actor)
	t.Run("CSV import retains current authentication and durable replay identity", func(t *testing.T) {
		point, err := writer.Client().Permission.Create().SetName("current import management").SetCode(data.ModelDevManageInputPermissionCode).SetStatus(permission.StatusOn).Save(sys)
		require.NoError(t, err)
		cleanup(func(c context.Context) error { return writer.Client().Permission.DeleteOneID(point.ID).Exec(c) })
		grant, err := writer.Client().RolePermission.Create().SetTenantID(owner.ID).SetRoleID(currentRole.ID).SetPermissionID(point.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
		require.NoError(t, err)
		cleanup(func(c context.Context) error { return writer.Client().RolePermission.DeleteOneID(grant.ID).Exec(c) })
		t.Setenv("ANI_MODELDEV_IMPORT_TIMEOUT_SECONDS", "1800")
		runModelDevCSVReplayVerticalCase(t, ctx, configDirectory, tokenFile, resourceTenant, actor)
	})
}

func modelDevBudgetTLS(t *testing.T) (data.ModelDevClientConfig, *tls.Config) {
	t.Helper()
	directory := t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, public, private)
	require.NoError(t, err)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caFile := filepath.Join(directory, "ca.pem")
	require.NoError(t, os.WriteFile(caFile, caPEM, 0600))
	issue := func(label string, serial int64, usage x509.ExtKeyUsage) (string, string, tls.Certificate) {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{label}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		encoded, err := x509.CreateCertificate(rand.Reader, leaf, ca, pub, private)
		require.NoError(t, err)
		keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
		require.NoError(t, err)
		certFile, keyFile := filepath.Join(directory, label+".pem"), filepath.Join(directory, label+".key")
		require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: encoded}), 0600))
		require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), 0600))
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		require.NoError(t, err)
		return certFile, keyFile, pair
	}
	_, _, server := issue("ani-modeldev-service", 2, x509.ExtKeyUsageServerAuth)
	cert, key, _ := issue("ani-governance", 3, x509.ExtKeyUsageClientAuth)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(caPEM))
	return data.ModelDevClientConfig{CAFile: caFile, CertFile: cert, KeyFile: key, Timeout: 2 * time.Second}, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{server}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}
}
