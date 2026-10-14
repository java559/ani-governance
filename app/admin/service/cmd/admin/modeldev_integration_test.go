//go:build modeldev_pg

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevreleasebinding"
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
	bConfig "go-wind-admin/pkg/localdeps/kratos-bootstrap/config"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
)

// This selected test uses only the runner's exclusive PG and Redis. It never
// seeds deployment permissions or starts ModelDev, HTTP, or a delivery worker.
func TestModelDevPauseCommandPersistsGateAndReplays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sys := appViewer.NewSystemViewerContext(ctx)
	runtimeDSN, fixtureDSN := modelDevPauseDatabaseSettings(t)
	writer := openModelDevPausePG(t, fixtureDSN)
	runtime := openModelDevPausePG(t, runtimeDSN)
	assertModelDevPauseDatabaseRoles(t, ctx, runtime, writer)
	removeAfterTest := func(remove func(context.Context) error) {
		cleanupModelDevPauseFixture(t, remove)
	}
	suffix := uuid.NewString()
	resourceTenantID := uuid.NewString()
	owner, err := writer.Client().Tenant.Create().SetName("modeldev pause fixture").SetCode("cpu-pause-" + suffix).
		SetResourceTenantID(resourceTenantID).SetStatus(tenant.StatusOn).Save(sys)
	require.NoError(t, err, "MODELDEV_PAUSE_PREFLIGHT: fresh tenant fixture")
	removeAfterTest(func(c context.Context) error { return writer.Client().Tenant.DeleteOneID(owner.ID).Exec(c) })
	operator, err := writer.Client().User.Create().SetTenantID(owner.ID).SetUsername("cpu-pause-" + suffix).
		SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	removeAfterTest(func(c context.Context) error { return writer.Client().User.DeleteOneID(operator.ID).Exec(c) })
	currentRole, err := writer.Client().Role.Create().SetTenantID(owner.ID).SetName("modeldev binding maintainer fixture").
		SetCode("tenant:modeldev-pause:" + suffix).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	removeAfterTest(func(c context.Context) error { return writer.Client().Role.DeleteOneID(currentRole.ID).Exec(c) })
	now := time.Now().UTC().Truncate(time.Microsecond)
	membership, err := writer.Client().UserRole.Create().SetTenantID(owner.ID).SetUserID(operator.ID).SetRoleID(currentRole.ID).
		SetStatus(userrole.StatusActive).SetStartAt(now.Add(-time.Hour)).SetEndAt(now.Add(time.Hour)).Save(sys)
	require.NoError(t, err)
	removeAfterTest(func(c context.Context) error { return writer.Client().UserRole.DeleteOneID(membership.ID).Exec(c) })
	// The exact non-sys capability is the only action grant. No Create API or
	// PermissionApi is added, and an existing permission is never taken over.
	managePermission, err := writer.Client().Permission.Create().SetName("modeldev binding maintenance fixture").
		SetCode("modeldev:manage_release_binding").SetStatus(permission.StatusOn).Save(sys)
	require.NoError(t, err, "MODELDEV_PAUSE_PREFLIGHT: exclusive database must not contain a conflicting managed permission")
	removeAfterTest(func(c context.Context) error {
		return writer.Client().Permission.DeleteOneID(managePermission.ID).Exec(c)
	})
	grant, err := writer.Client().RolePermission.Create().SetTenantID(owner.ID).SetRoleID(currentRole.ID).SetPermissionID(managePermission.ID).
		SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
	require.NoError(t, err)
	removeAfterTest(func(c context.Context) error { return writer.Client().RolePermission.DeleteOneID(grant.ID).Exec(c) })

	scope := data.ModelDevReleaseBindingScope{TenantID: owner.ID, ResourceTenantID: resourceTenantID, PresetID: uuid.NewString()}
	target := data.ModelDevReleaseBindingTarget{ReleaseID: uuid.NewString(), ReleaseDigest: strings.Repeat("a", 64), NewSubmissionsEnabled: true}
	const previousActor = "governance:access-key:42"
	before, err := data.NewModelDevReleaseBindingRepo(runtime).CompareAndSwap(sys, scope, data.ModelDevReleaseBindingUpdate{
		Target: target, Actor: previousActor, RequestedAt: now, Reason: "synthetic enabled binding", EvidenceReference: "contract:cpu-p01:pause-preparation",
	})
	require.NoError(t, err, "MODELDEV_PAUSE_PREFLIGHT: real binding CAS must commit")
	removeAfterTest(func(c context.Context) error {
		_, e := writer.Client().ModelDevReleaseBinding.Delete().Where(modeldevreleasebinding.TenantIDEQ(owner.ID)).Exec(c)
		return e
	})
	require.False(t, before.Replayed)
	require.Equal(t, uint64(1), before.After.Generation)

	redisAddress := os.Getenv("ANI_MODELDEV_REDIS_ADDR")
	host, port, addressErr := net.SplitHostPort(redisAddress)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if addressErr != nil || host != "127.0.0.1" || portErr != nil || portNumber == 0 {
		t.Fatal("MODELDEV_PAUSE_PREFLIGHT: explicit task loopback Redis required")
	}
	for _, name := range []string{
		"GWA_AUTH_JWT_PRIVATE_KEY", "GWA_AUTH_JWT_PUBLIC_KEY", "GWA_AUTH_JWT_KEY",
		"ANI_MODELDEV_ADDR", "ANI_MODELDEV_CA", "ANI_MODELDEV_CERT", "ANI_MODELDEV_KEY", "ANI_MODELDEV_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
	keyBytes := make([]byte, 32)
	_, err = rand.Read(keyBytes)
	require.NoError(t, err)
	cfg := &conf.Bootstrap{
		Data: &conf.Data{
			Database: &conf.Data_Database{Driver: "postgres", Source: runtimeDSN, Migrate: false, MaxOpenConnections: trans.Ptr(int32(2)), MaxIdleConnections: trans.Ptr(int32(1)), ConnectionMaxLifetime: durationpb.New(time.Minute)},
			Redis:    &conf.Data_Redis{Addr: redisAddress, DialTimeout: durationpb.New(2 * time.Second), ReadTimeout: durationpb.New(2 * time.Second), WriteTimeout: durationpb.New(2 * time.Second)},
		},
		Authn: &conf.Authentication{Type: "jwt", Jwt: &conf.Authentication_Jwt{Method: "HS256", Key: hex.EncodeToString(keyBytes)}},
		Authz: &conf.Authorization{Type: "casbin"},
	}
	bctx := testutil.NewBootstrapContext(cfg)
	rdb := redis.NewClient(&redis.Options{Addr: redisAddress})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	require.NoError(t, rdb.Ping(ctx).Err(), "MODELDEV_PAUSE_PREFLIGHT: real Redis")
	cache := data.NewUserTokenCache(bctx, rdb)
	authenticator := data.NewAuthenticator(bctx, cache)
	payload := &authv1.UserTokenPayload{UserId: operator.ID, TenantId: trans.Ptr(owner.ID), Roles: []string{*currentRole.Code}}
	token, _, err := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, payload)
	require.NoError(t, err, "MODELDEV_PAUSE_PREFLIGHT: actual signed token and Redis session")
	removeAfterTest(func(c context.Context) error {
		return cache.RevokeTokenByJti(c, authv1.ClientType_admin, operator.ID, payload.GetJti())
	})
	verified, err := authenticator.Authenticate(ctx, &authv1.ValidateTokenRequest{
		ClientType: authv1.ClientType_admin, TokenCategory: authv1.TokenCategory_ACCESS, Token: token,
	})
	if err != nil || verified == nil || !verified.IsValid || verified.Payload.GetUserId() != operator.ID || verified.Payload.GetTenantId() != owner.ID {
		t.Fatal("MODELDEV_PAUSE_PREFLIGHT: real token verification failed")
	}
	// The token deliberately carries no permissions. The command must consult
	// the current database grant, not infer authority from token claims.
	require.Empty(t, verified.Payload.GetPermissions())
	mapped, err := data.NewTenantRepo(bctx, runtime).ResourceTenantID(sys, owner.ID)
	require.NoError(t, err)
	require.Equal(t, resourceTenantID, mapped)
	var grantCount int
	require.NoError(t, runtime.DB().QueryRowContext(ctx, `SELECT count(*)
		FROM sys_tenants t JOIN sys_users u ON u.tenant_id=t.id
		JOIN sys_user_roles ur ON ur.tenant_id=t.id AND ur.user_id=u.id
		JOIN sys_roles r ON r.tenant_id=t.id AND r.id=ur.role_id
		JOIN sys_role_permissions rp ON rp.tenant_id=t.id AND rp.role_id=r.id
		JOIN sys_permissions p ON p.id=rp.permission_id
		WHERE t.id=$1 AND t.resource_tenant_id=$2 AND t.status='ON' AND t.deleted_at IS NULL
		AND u.id=$3 AND u.status='NORMAL' AND u.deleted_at IS NULL
		AND ur.status='ACTIVE' AND ur.start_at<=statement_timestamp() AND ur.end_at>statement_timestamp() AND ur.deleted_at IS NULL
		AND r.id=$4 AND r.status='ON' AND r.type='TENANT' AND r.data_scope='ALL' AND r.deleted_at IS NULL
		AND rp.status='ON' AND rp.effect='ALLOW' AND rp.deleted_at IS NULL
		AND p.id=$5 AND p.code='modeldev:manage_release_binding' AND p.status='ON' AND p.deleted_at IS NULL`,
		owner.ID, resourceTenantID, operator.ID, currentRole.ID, managePermission.ID).Scan(&grantCount))
	require.Equal(t, 1, grantCount, "current authorization fixtures must already be committed on an independent connection")
	storedBefore, err := data.NewModelDevReleaseBindingRepo(runtime).Get(sys, scope)
	require.NoError(t, err)
	require.Equal(t, target, storedBefore.Target)
	require.Equal(t, uint64(1), storedBefore.Generation)

	private := t.TempDir()
	require.NoError(t, os.Chmod(private, 0700))
	configDirectory := filepath.Join(private, "config")
	require.NoError(t, os.Mkdir(configDirectory, 0700))
	configuration, err := protojson.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(configDirectory, "bootstrap.json"), configuration, 0600))
	configProvider, err := bConfig.NewConfigProvider(configDirectory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, configProvider.Close()) })
	require.NoError(t, configProvider.Load())
	var loaded conf.Bootstrap
	require.NoError(t, configProvider.Scan(&loaded), "MODELDEV_PAUSE_PREFLIGHT: actual config loader must accept the private fixture")
	require.Equal(t, "postgres", loaded.GetData().GetDatabase().GetDriver())
	require.False(t, loaded.GetData().GetDatabase().GetMigrate())
	tokenFile := filepath.Join(private, "access-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(token), 0600))
	request := struct {
		PresetID           string `json:"preset_id"`
		ReleaseID          string `json:"release_id"`
		ReleaseDigest      string `json:"release_digest"`
		ExpectedGeneration uint64 `json:"expected_generation"`
		Reason             string `json:"reason"`
		EvidenceReference  string `json:"evidence_reference"`
	}{scope.PresetID, target.ReleaseID, target.ReleaseDigest, 1, "pause new CPU admissions", "contract:cpu-p01:pause-" + suffix}
	requestBytes, err := json.Marshal(request)
	require.NoError(t, err)
	requestFile := filepath.Join(private, "pause.json")
	require.NoError(t, os.WriteFile(requestFile, requestBytes, 0600))
	args := []string{"modeldev-pause", "--conf", configDirectory, "--token-file", tokenFile, "--request-file", requestFile}
	t.Log("MODELDEV_PAUSE_PREFLIGHT PASS: actual JWT/Redis, current PostgreSQL management grant, trusted tenant mapping and enabled binding; owner unconfigured")

	var output bytes.Buffer
	started := time.Now().UTC().Truncate(time.Microsecond)
	if err := runAdmin(ctx, args, &output); err != nil {
		t.Fatalf("MODELDEV_PAUSE_BEHAVIOR: %v", err)
	}
	actor := "governance:user:" + strconv.FormatUint(uint64(operator.ID), 10)
	first := decodeModelDevPauseOutput(t, output.Bytes())
	wantBefore := modelDevPauseBindingOutput{target.ReleaseID, target.ReleaseDigest, 1, true, previousActor}
	wantAfter := modelDevPauseBindingOutput{target.ReleaseID, target.ReleaseDigest, 2, false, actor}
	require.Equal(t, modelDevPauseOutput{actor, wantBefore, wantAfter, false}, first)
	observer := openModelDevPausePG(t, runtimeDSN)
	storedAfter, err := data.NewModelDevReleaseBindingRepo(observer).Get(sys, scope)
	require.NoError(t, err, "independent connection must recover the command's committed pause")
	require.Equal(t, scope, storedAfter.Scope)
	require.Equal(t, data.ModelDevReleaseBindingTarget{ReleaseID: target.ReleaseID, ReleaseDigest: target.ReleaseDigest, NewSubmissionsEnabled: false}, storedAfter.Target)
	require.Equal(t, uint64(2), storedAfter.Generation)
	require.Equal(t, actor, storedAfter.UpdatedBy)
	require.Equal(t, request.Reason, storedAfter.Reason)
	require.Equal(t, request.EvidenceReference, storedAfter.EvidenceReference)
	require.False(t, storedAfter.UpdatedAt.Before(started))
	require.False(t, storedAfter.UpdatedAt.After(time.Now().UTC()))
	require.Zero(t, storedAfter.UpdatedAt.Nanosecond()%1000)
	output.Reset()
	require.NoError(t, runAdmin(ctx, args, &output))
	require.Equal(t, modelDevPauseOutput{actor, wantAfter, wantAfter, true}, decodeModelDevPauseOutput(t, output.Bytes()))
	replayed, err := data.NewModelDevReleaseBindingRepo(observer).Get(sys, scope)
	require.NoError(t, err)
	require.Equal(t, storedAfter, replayed, "same-target replay retains generation and original audit fields")
	t.Log("MODELDEV_PAUSE_BEHAVIOR PASS: trusted operator paused the unchanged target at generation 2; same-target replay preserved audit without owner access")

	// Keep the original expected-generation=1 request for every denial. A
	// paused target must not turn replay into an authorization bypass.
	assertValidToken := func(t *testing.T, accessToken string, userID, tenantID uint32) {
		t.Helper()
		verified, err := authenticator.Authenticate(ctx, &authv1.ValidateTokenRequest{
			ClientType: authv1.ClientType_admin, TokenCategory: authv1.TokenCategory_ACCESS, Token: accessToken,
		})
		require.NoError(t, err, "negative preflight requires a live JWT and Redis session")
		require.NotNil(t, verified)
		require.True(t, verified.IsValid)
		require.Equal(t, userID, verified.Payload.GetUserId())
		require.Equal(t, tenantID, verified.Payload.GetTenantId())
	}
	bindingSnapshot := func(t *testing.T) string {
		t.Helper()
		row, err := observer.Client().ModelDevReleaseBinding.Query().Where(
			modeldevreleasebinding.TenantIDEQ(scope.TenantID),
			modeldevreleasebinding.ResourceTenantIDEQ(scope.ResourceTenantID),
			modeldevreleasebinding.PresetIDEQ(scope.PresetID),
		).Only(sys)
		require.NoError(t, err)
		raw, err := json.Marshal(row)
		require.NoError(t, err)
		return string(raw) // Every persisted column, including the row ID.
	}
	unchangedBinding := bindingSnapshot(t)
	assertRejected := func(t *testing.T, commandArgs []string, code int32, reason string) {
		t.Helper()
		var rejectedOutput bytes.Buffer
		err := runAdmin(ctx, commandArgs, &rejectedOutput)
		require.Error(t, err, "a rejected pause must not return a successful replay")
		failure := kratoserrors.FromError(err)
		require.Equal(t, code, failure.Code)
		require.Equal(t, reason, failure.Reason)
		require.Empty(t, rejectedOutput.String(), "denied commands emit no successful result")
		require.JSONEq(t, unchangedBinding, bindingSnapshot(t), "denial must preserve every target-tenant binding column")
	}
	disableManagementGrant := func(t *testing.T) {
		t.Helper()
		// Subtest cleanup restores this existing grant before the next scenario.
		cleanupModelDevPauseFixture(t, func(c context.Context) error {
			return writer.Client().RolePermission.UpdateOneID(grant.ID).SetStatus(rolepermission.StatusOn).Exec(c)
		})
		require.NoError(t, writer.Client().RolePermission.UpdateOneID(grant.ID).SetStatus(rolepermission.StatusOff).Exec(sys))
		stored, err := observer.Client().RolePermission.Get(sys, grant.ID)
		require.NoError(t, err)
		require.Equal(t, trans.Ptr(rolepermission.StatusOff), stored.Status, "grant revocation must already be committed")
	}

	t.Run("revoked management rejects original replay", func(t *testing.T) {
		disableManagementGrant(t)
		assertValidToken(t, token, operator.ID, owner.ID)
		assertRejected(t, args, 403, "FORBIDDEN")
	})

	t.Run("current create grant cannot replace management", func(t *testing.T) {
		disableManagementGrant(t)
		createPermission, err := writer.Client().Permission.Create().
			SetName("modeldev pause create-only fixture").SetCode("modeldev.execution.create.pause." + suffix).
			SetStatus(permission.StatusOn).Save(sys)
		require.NoError(t, err)
		cleanupModelDevPauseFixture(t, func(c context.Context) error {
			return writer.Client().Permission.DeleteOneID(createPermission.ID).Exec(c)
		})
		createAPI, err := writer.Client().Api.Create().
			SetModule("modeldev-pause-create-" + suffix).SetPath("/api/v1/modeldev/executions").SetMethod("POST").
			SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleModel).SetStatus(api.StatusOn).Save(sys)
		require.NoError(t, err)
		cleanupModelDevPauseFixture(t, func(c context.Context) error {
			return writer.Client().Api.DeleteOneID(createAPI.ID).Exec(c)
		})
		permissionAPI, err := writer.Client().PermissionApi.Create().SetPermissionID(createPermission.ID).SetAPIID(createAPI.ID).Save(sys)
		require.NoError(t, err)
		cleanupModelDevPauseFixture(t, func(c context.Context) error {
			return writer.Client().PermissionApi.DeleteOneID(permissionAPI.ID).Exec(c)
		})
		createGrant, err := writer.Client().RolePermission.Create().SetTenantID(owner.ID).SetRoleID(currentRole.ID).
			SetPermissionID(createPermission.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
		require.NoError(t, err)
		cleanupModelDevPauseFixture(t, func(c context.Context) error {
			return writer.Client().RolePermission.DeleteOneID(createGrant.ID).Exec(c)
		})
		require.NoError(t, data.NewModelDevAuthorizationRepo(observer).AuthorizeCreate(sys, owner.ID, operator.ID),
			"negative preflight: the independent reader must authorize the current real PermissionApi create grant")
		assertValidToken(t, token, operator.ID, owner.ID)
		assertRejected(t, args, 403, "FORBIDDEN")
	})

	t.Run("another authorized tenant cannot pause this target", func(t *testing.T) {
		otherResourceTenantID := uuid.NewString()
		otherTenant, err := writer.Client().Tenant.Create().SetName("modeldev pause other tenant fixture").
			SetCode("cpu-pause-other-" + suffix).SetResourceTenantID(otherResourceTenantID).SetStatus(tenant.StatusOn).Save(sys)
		require.NoError(t, err)
		cleanupModelDevPauseFixture(t, func(c context.Context) error {
			return writer.Client().Tenant.DeleteOneID(otherTenant.ID).Exec(c)
		})
		otherUser, err := writer.Client().User.Create().SetTenantID(otherTenant.ID).
			SetUsername("cpu-pause-other-" + suffix).SetStatus(user.StatusNormal).Save(sys)
		require.NoError(t, err)
		cleanupModelDevPauseFixture(t, func(c context.Context) error {
			return writer.Client().User.DeleteOneID(otherUser.ID).Exec(c)
		})
		otherRole, err := writer.Client().Role.Create().SetTenantID(otherTenant.ID).SetName("modeldev other binding maintainer fixture").
			SetCode("tenant:modeldev-pause-other:" + suffix).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
		require.NoError(t, err)
		cleanupModelDevPauseFixture(t, func(c context.Context) error {
			return writer.Client().Role.DeleteOneID(otherRole.ID).Exec(c)
		})
		otherMembership, err := writer.Client().UserRole.Create().SetTenantID(otherTenant.ID).SetUserID(otherUser.ID).
			SetRoleID(otherRole.ID).SetStatus(userrole.StatusActive).SetStartAt(now.Add(-time.Hour)).SetEndAt(now.Add(time.Hour)).Save(sys)
		require.NoError(t, err)
		cleanupModelDevPauseFixture(t, func(c context.Context) error {
			return writer.Client().UserRole.DeleteOneID(otherMembership.ID).Exec(c)
		})
		otherGrant, err := writer.Client().RolePermission.Create().SetTenantID(otherTenant.ID).SetRoleID(otherRole.ID).
			SetPermissionID(managePermission.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
		require.NoError(t, err)
		cleanupModelDevPauseFixture(t, func(c context.Context) error {
			return writer.Client().RolePermission.DeleteOneID(otherGrant.ID).Exec(c)
		})
		require.NoError(t, data.NewModelDevAuthorizationRepo(observer).AuthorizeManageReleaseBinding(sys, otherTenant.ID, otherUser.ID),
			"negative preflight: tenant B has its own current management grant and ALL data scope")
		otherMapping, err := data.NewTenantRepo(bctx, observer).ResourceTenantID(sys, otherTenant.ID)
		require.NoError(t, err)
		require.Equal(t, otherResourceTenantID, otherMapping)
		otherPayload := &authv1.UserTokenPayload{UserId: otherUser.ID, TenantId: trans.Ptr(otherTenant.ID), Roles: []string{*otherRole.Code}}
		otherToken, _, err := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, otherPayload)
		require.NoError(t, err)
		cleanupModelDevPauseFixture(t, func(c context.Context) error {
			return cache.RevokeTokenByJti(c, authv1.ClientType_admin, otherUser.ID, otherPayload.GetJti())
		})
		assertValidToken(t, otherToken, otherUser.ID, otherTenant.ID)
		otherPrivate := t.TempDir()
		require.NoError(t, os.Chmod(otherPrivate, 0700))
		otherTokenFile := filepath.Join(otherPrivate, "access-token")
		require.NoError(t, os.WriteFile(otherTokenFile, []byte(otherToken), 0600))
		otherArgs := []string{"modeldev-pause", "--conf", configDirectory, "--token-file", otherTokenFile, "--request-file", requestFile}
		otherScope := data.ModelDevReleaseBindingScope{
			TenantID: otherTenant.ID, ResourceTenantID: otherResourceTenantID, PresetID: scope.PresetID,
		}
		_, err = data.NewModelDevReleaseBindingRepo(observer).Get(sys, otherScope)
		require.ErrorIs(t, err, data.ErrModelDevBindingNotFound, "tenant B must begin without this preset binding")
		cleanupModelDevPauseFixture(t, func(c context.Context) error {
			// Also remove an unexpected row if the negative assertion detects a write.
			_, err := writer.Client().ModelDevReleaseBinding.Delete().Where(
				modeldevreleasebinding.TenantIDEQ(otherTenant.ID),
				modeldevreleasebinding.ResourceTenantIDEQ(otherResourceTenantID),
				modeldevreleasebinding.PresetIDEQ(scope.PresetID),
			).Exec(c)
			return err
		})
		assertRejected(t, otherArgs, 404, "MODELDEV_BINDING_NOT_FOUND")
		_, err = data.NewModelDevReleaseBindingRepo(observer).Get(sys, otherScope)
		require.ErrorIs(t, err, data.ErrModelDevBindingNotFound, "a foreign target must not create a tenant B binding")
	})

	for _, rejection := range []struct {
		name       string
		releaseID  string
		generation uint64
	}{
		{"different fixed release cannot be reinterpreted", uuid.NewString(), 1},
		{"future generation cannot replay paused target", target.ReleaseID, 3},
	} {
		t.Run(rejection.name, func(t *testing.T) {
			invalidTarget := request
			invalidTarget.ReleaseID = rejection.releaseID
			invalidTarget.ExpectedGeneration = rejection.generation
			raw, err := json.Marshal(invalidTarget)
			require.NoError(t, err)
			inputFile := filepath.Join(t.TempDir(), "request.json")
			require.NoError(t, os.WriteFile(inputFile, raw, 0600))
			assertValidToken(t, token, operator.ID, owner.ID)
			assertRejected(t, []string{"modeldev-pause", "--conf", configDirectory, "--token-file", tokenFile, "--request-file", inputFile}, 409, "MODELDEV_BINDING_CHANGED")
		})
	}

	t.Run("blacklist lookup unavailable denies without writing", func(t *testing.T) {
		// Restore this owned synthetic binding to enabled so an authentication
		// bypass would cause an observable write rather than an unchanged replay.
		enabled, err := data.NewModelDevReleaseBindingRepo(runtime).CompareAndSwap(sys, scope, data.ModelDevReleaseBindingUpdate{
			ExpectedGeneration: 2, Target: target, Actor: previousActor, RequestedAt: time.Now().UTC().Truncate(time.Microsecond),
			Reason: "synthetic blacklist-failure preparation", EvidenceReference: "contract:cpu-p01:pause-blacklist",
		})
		require.NoError(t, err)
		require.Equal(t, uint64(3), enabled.After.Generation)
		request.ExpectedGeneration = 3
		raw, err := json.Marshal(request)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(requestFile, raw, 0600))
		stored, err := data.NewModelDevReleaseBindingRepo(observer).Get(sys, scope)
		require.NoError(t, err)
		require.Equal(t, enabled.After, stored)
		// Only the runner-owned Redis is affected. Restoring the ACL uses an
		// independent cleanup context before the owned token/rows are removed.
		require.NoError(t, rdb.Do(ctx, "ACL", "SETUSER", "default", "-exists").Err())
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			require.NoError(t, rdb.Do(cleanup, "ACL", "SETUSER", "default", "+exists").Err())
		})
		require.NoError(t, rdb.Ping(ctx).Err())
		valid, err := cache.IsValidAccessToken(ctx, authv1.ClientType_admin, operator.ID, payload.GetJti(), token)
		require.NoError(t, err)
		require.True(t, valid, "real signed token still has its Redis session")
		err = rdb.Exists(ctx, "cpu-p01-synthetic-blacklist-probe").Err()
		require.ErrorContains(t, err, "NOPERM", "only the real blacklist command is unavailable")
		t.Log("MODELDEV_PAUSE_BLACKLIST_PREFLIGHT PASS: real session GET/PING allowed and EXISTS denied on owned Redis; enabled binding generation 3 persisted")
		output.Reset()
		err = runAdmin(ctx, args, &output)
		require.Error(t, err, "MODELDEV_PAUSE_BLACKLIST_BEHAVIOR: unavailable blacklist must reject the command")
		require.Empty(t, output.String())
		afterDenied, err := data.NewModelDevReleaseBindingRepo(observer).Get(sys, scope)
		require.NoError(t, err)
		require.Equal(t, stored, afterDenied, "denial preserves gate, generation and all audit fields")
		t.Log("MODELDEV_PAUSE_BLACKLIST_BEHAVIOR PASS: unavailable blacklist rejected with zero binding mutation")
	})
	t.Run("enable and rollback share the authenticated current binding", func(t *testing.T) {
		runModelDevEnableVerticalCase(t, ctx, configDirectory, tokenFile, resourceTenantID, actor, scope, target)
	})
	t.Run("CSV import replays through fresh authenticated clients", func(t *testing.T) {
		runModelDevCSVReplayVerticalCase(t, ctx, configDirectory, tokenFile, resourceTenantID, actor)
	})
}

type modelDevPauseBindingOutput struct {
	ReleaseID             string `json:"release_id"`
	ReleaseDigest         string `json:"release_digest"`
	Generation            uint64 `json:"generation"`
	NewSubmissionsEnabled bool   `json:"new_submissions_enabled"`
	UpdatedBy             string `json:"updated_by"`
}

type modelDevPauseOutput struct {
	Operator string                     `json:"operator"`
	Before   modelDevPauseBindingOutput `json:"before"`
	After    modelDevPauseBindingOutput `json:"after"`
	Replayed bool                       `json:"replayed"`
}

func decodeModelDevPauseOutput(t *testing.T, raw []byte) modelDevPauseOutput {
	t.Helper()
	require.LessOrEqual(t, len(raw), 4096)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result modelDevPauseOutput
	require.NoError(t, decoder.Decode(&result))
	require.Equal(t, io.EOF, decoder.Decode(new(any)))
	return result
}

func modelDevPauseDatabaseSettings(t *testing.T) (string, string) {
	t.Helper()
	dsn, path := os.Getenv("ANI_TEST_DATABASE_DSN"), os.Getenv("ANI_MODELDEV_FIXTURE_DSN_FILE")
	if dsn == "" || os.Getenv("ANI_TEST_DATABASE_EXCLUSIVE") != "1" || !filepath.IsAbs(path) {
		t.Fatal("MODELDEV_PAUSE_PREFLIGHT: explicit exclusive PG and private fixture identity required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		t.Fatal("MODELDEV_PAUSE_PREFLIGHT: bounded private fixture identity required")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 16384 {
		t.Fatal("MODELDEV_PAUSE_PREFLIGHT: fixture identity read failed")
	}
	fixtureDSN := strings.TrimSpace(string(raw))
	runtimeConfig, runtimeErr := pgx.ParseConfig(dsn)
	fixtureConfig, fixtureErr := pgx.ParseConfig(fixtureDSN)
	if runtimeErr != nil || fixtureErr != nil || runtimeConfig.Host != fixtureConfig.Host || runtimeConfig.Port != fixtureConfig.Port || runtimeConfig.Database != fixtureConfig.Database || runtimeConfig.User == fixtureConfig.User {
		t.Fatal("MODELDEV_PAUSE_PREFLIGHT: distinct fixture/runtime roles must use the same task database")
	}
	return dsn, fixtureDSN
}

func openModelDevPausePG(t *testing.T, dsn string) *entCrud.EntClient[*ent.Client] {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("MODELDEV_PAUSE_PREFLIGHT: PG client configuration failed")
	}
	db.SetMaxOpenConns(2)
	driver := entsql.OpenDB("postgres", db)
	client := ent.NewClient(ent.Driver(driver))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("MODELDEV_PAUSE_PREFLIGHT: PG unavailable")
	}
	return entCrud.NewEntClient(client, driver)
}

func assertModelDevPauseDatabaseRoles(t *testing.T, ctx context.Context, runtime, writer *entCrud.EntClient[*ent.Client]) {
	t.Helper()
	var runtimeUser, fixtureUser, runtimeDatabase, fixtureDatabase string
	require.NoError(t, runtime.DB().QueryRowContext(ctx, `SELECT current_user,current_database()`).Scan(&runtimeUser, &runtimeDatabase))
	require.NoError(t, writer.DB().QueryRowContext(ctx, `SELECT current_user,current_database()`).Scan(&fixtureUser, &fixtureDatabase))
	require.NotEqual(t, runtimeUser, fixtureUser)
	require.Equal(t, runtimeDatabase, fixtureDatabase)
	var superuser, bypassRLS, canCreate, canTemp bool
	require.NoError(t, runtime.DB().QueryRowContext(ctx, `SELECT rolsuper,rolbypassrls,has_database_privilege(current_database(),'CREATE'),has_database_privilege(current_database(),'TEMP') FROM pg_roles WHERE rolname=current_user`).Scan(&superuser, &bypassRLS, &canCreate, &canTemp))
	require.False(t, superuser)
	require.False(t, bypassRLS)
	require.False(t, canCreate)
	require.False(t, canTemp)
}

// Cleanup is scoped to the calling test: a negative subtest restores its grants
// before the parent resumes, while the original fixture retains LIFO cleanup.
func cleanupModelDevPauseFixture(t *testing.T, cleanup func(context.Context) error) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
		defer cancel()
		require.NoError(t, cleanup(ctx), "restore or remove only the owned pause fixture")
	})
}
