//go:build compute_pg

package data

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/pkg/authorizer"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	authzEngine "go-wind-admin/pkg/localdeps/kratos-authz/engine"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
)

// SQL maintenance is followed by ResetPolicies on every live process, or by
// constructing a matching process and loading current policies before serving.
func TestComputePermissionPolicyRefreshPostgres(t *testing.T) {
	dsn := os.Getenv("GOV_COMPUTE_PERMISSION_TEST_DSN")
	require.NotEmpty(t, dsn, "compute_pg requires an explicitly assigned disposable database")
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	var database string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT current_database()").Scan(&database))
	require.True(t, strings.HasSuffix(database, "_compute_permissions_test"), "refuse a database outside this migration suite")
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := ent.NewClient(ent.Driver(driver))
	require.NoError(t, client.Schema.Create(ctx))
	ec := entCrud.NewEntClient(client, driver)
	bctx := bootstrap.NewContextWithParam(context.Background(), &conf.AppInfo{},
		&conf.Bootstrap{Authz: &conf.Authorization{Type: "casbin"}}, bLogger.NopLogger())
	roles := NewRoleRepo(bctx, ec, NewRolePermissionRepo(bctx, ec), NewRoleOrgUnitRepo(bctx, ec),
		NewPermissionRepo(bctx, ec, NewPermissionApiRepo(bctx, ec), NewPermissionMenuRepo(bctx, ec)),
		NewRoleMetadataRepo(bctx, ec), NewRoleFieldPermissionRepo(bctx, ec))
	provider := NewAuthorizerProvider(bctx, roles, NewApiRepo(bctx, ec))
	exec := func(statement string) { _, err := db.ExecContext(ctx, statement); require.NoError(t, err) }
	exec(`TRUNCATE sys_permissions,sys_apis,sys_permission_apis,sys_role_permissions,sys_role_metadata,sys_roles RESTART IDENTITY CASCADE;
INSERT INTO sys_roles(id,tenant_id,name,code,type,status,data_scope) VALUES
 (20,0,'Goal platform','platform:compute-rehearsal','SYSTEM','ON','ALL'),
 (21,0,'Goal disabled','platform:compute-disabled','SYSTEM','OFF','ALL');
INSERT INTO sys_permissions(id,name,code,status)
 SELECT n,'Goal permission '||action,'instance:preset:admin:'||action,CASE WHEN n=2 THEN 'OFF' ELSE 'ON' END
 FROM (VALUES(1,'list'),(2,'read'),(3,'create'),(4,'update'),(5,'delete')) p(n,action);
INSERT INTO sys_apis(id,path,method,module,business_module,operation,status)
 SELECT n,path,method,'InstanceService','INSTANCE','InstanceService_'||rpc,CASE WHEN n=6 THEN 'OFF' ELSE 'ON' END
 FROM (VALUES
 (1,'GET','/api/v1/admin/compute-presets','AdminListPresets'),
 (2,'GET','/api/v1/admin/compute-presets/{id}','AdminGetPreset'),
 (3,'POST','/api/v1/admin/compute-presets','AdminCreatePreset'),
 (4,'PATCH','/api/v1/admin/compute-presets/{id}','AdminUpdatePreset'),
 (5,'POST','/api/v1/admin/compute-presets/{id}:set-enabled','AdminSetPresetEnabled'),
 (6,'DELETE','/api/v1/admin/compute-presets/{id}','AdminDeletePreset')) a(n,method,path,rpc);
INSERT INTO sys_permission_apis(id,permission_id,api_id)
 SELECT n,p,n FROM (VALUES(1,1),(2,2),(3,3),(4,4),(5,4),(6,5)) a(n,p);
INSERT INTO sys_role_permissions(id,role_id,permission_id,tenant_id,status,effect,priority)
 SELECT id,20,id,0,'ON',CASE WHEN id=2 THEN 'DENY' ELSE 'ALLOW' END,13 FROM sys_permissions;
INSERT INTO sys_role_permissions(id,role_id,permission_id,tenant_id,status,effect,priority)
 SELECT id+20,21,id,0,'ON','ALLOW',13 FROM sys_permissions;
SELECT setval(pg_get_serial_sequence('sys_permissions','id'),200,true);`)
	newPolicy := func() *authorizer.Authorizer {
		policy := authorizer.NewAuthorizer(bctx, provider)
		require.NotNil(t, policy.Engine())
		require.Equal(t, "casbin", policy.Engine().Name())
		require.NoError(t, policy.ResetPolicies(ctx))
		return policy
	}
	check := func(policy *authorizer.Authorizer, listAllowed bool) {
		for _, route := range []struct {
			path, method string
			allowed      bool
		}{
			{"/api/v1/admin/compute-presets", "GET", listAllowed},
			{"/api/v1/admin/compute-presets/{id}", "GET", false},
			{"/api/v1/admin/compute-presets", "POST", true},
			{"/api/v1/admin/compute-presets/{id}", "PATCH", true},
			{"/api/v1/admin/compute-presets/{id}:set-enabled", "POST", true},
			{"/api/v1/admin/compute-presets/{id}", "DELETE", false},
		} {
			allowed, err := policy.Engine().IsAuthorized(ctx, authzEngine.Subject("platform:compute-rehearsal"),
				authzEngine.Action(route.method), authzEngine.Resource(route.path), authzEngine.Project("0"))
			require.NoError(t, err)
			require.Equal(t, route.allowed, allowed, "%s %s", route.method, route.path)
			allowed, err = policy.Engine().IsAuthorized(ctx, authzEngine.Subject("platform:compute-disabled"),
				authzEngine.Action(route.method), authzEngine.Resource(route.path), authzEngine.Project("0"))
			require.NoError(t, err)
			require.False(t, allowed, "disabled role must stay denied")
		}
	}
	before, err := provider.ProvidePolicies(ctx)
	require.NoError(t, err)
	policy := newPolicy()
	check(policy, true)
	read := func(name string) string {
		raw, err := os.ReadFile("../../../../../scripts/ops/sql/" + name)
		require.NoError(t, err)
		return string(raw)
	}
	exec(read("migrate-compute-preset-permissions.sql"))
	after, err := provider.ProvidePolicies(ctx)
	require.NoError(t, err)
	require.Len(t, after, len(before))
	for code, rules := range before {
		require.ElementsMatch(t, rules, after[code], "owner/code rename preserves the effective route policy")
	}
	require.NoError(t, policy.ResetPolicies(ctx))
	check(policy, true)
	check(newPolicy(), true)

	// A real SQL maintenance change stays cached until this process refreshes.
	exec("UPDATE sys_apis SET status='OFF' WHERE id=1")
	check(policy, true)
	require.NoError(t, policy.ResetPolicies(ctx))
	check(policy, false)
	check(newPolicy(), false)
	exec("UPDATE sys_apis SET status='ON' WHERE id=1")
	exec(read("restore-instance-preset-permissions.sql"))
	require.NoError(t, policy.ResetPolicies(ctx))
	check(policy, true)
	check(newPolicy(), true)
}
