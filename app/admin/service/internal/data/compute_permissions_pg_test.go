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
)

// This explicitly selected suite requires a task-owned disposable PostgreSQL
// database. It exercises the real Ent schema and the shipped operations SQL.
func TestComputePresetPermissionMigrationPostgres(t *testing.T) {
	dsn := os.Getenv("GOV_COMPUTE_PERMISSION_TEST_DSN")
	require.NotEmpty(t, dsn, "compute_pg requires an explicitly assigned disposable database")
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	var database string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT current_database()").Scan(&database))
	require.True(t, strings.HasSuffix(database, "_compute_permissions_test"), "refuse a database outside this migration suite")
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	require.NoError(t, client.Schema.Create(ctx))
	read := func(name string) string {
		raw, err := os.ReadFile("../../../../../scripts/ops/sql/" + name)
		require.NoError(t, err)
		return string(raw)
	}
	up := read("migrate-compute-preset-permissions.sql")
	down := read("restore-instance-preset-permissions.sql")
	fresh := read("register-compute-permissions.sql")
	exec := func(statement string) error {
		_, err := db.ExecContext(ctx, statement)
		if err != nil {
			// A failed explicit script must release its aborted transaction before
			// observing rollback through the same sole database connection.
			_, rollbackErr := db.ExecContext(ctx, "ROLLBACK")
			require.NoError(t, rollbackErr)
		}
		return err
	}
	snapshot := func(normalize bool) string {
		var out string
		for _, table := range []string{"sys_permissions", "sys_apis", "sys_permission_apis", "sys_role_permissions", "sys_role_metadata", "sys_plans", "sys_plan_modules"} {
			row := "to_jsonb(t)"
			if normalize && table == "sys_permissions" {
				row += " - 'code'"
			}
			if normalize && table == "sys_apis" {
				row += " - 'module' - 'business_module' - 'operation'"
			}
			var value string
			require.NoError(t, db.QueryRowContext(ctx, "SELECT coalesce(jsonb_agg("+row+" ORDER BY id),'[]'::jsonb)::text FROM "+table+" t").Scan(&value))
			out += value
		}
		return out
	}
	seed := func() {
		require.NoError(t, exec(`TRUNCATE sys_permissions,sys_apis,sys_permission_apis,sys_role_permissions,sys_role_metadata,sys_plans,sys_plan_modules RESTART IDENTITY CASCADE;
INSERT INTO sys_permissions(id,name,code,status,created_at,updated_at,created_by,description)
SELECT n,'existing permission '||action,'instance:preset:admin:'||action,
CASE WHEN n%2=0 THEN 'OFF' ELSE 'ON' END,'2026-10-01','2026-10-02',77,'keep restriction'
FROM (VALUES(1,'list'),(2,'read'),(3,'create'),(4,'update'),(5,'delete')) p(n,action);
INSERT INTO sys_permissions(id,name,code,status) VALUES(91,'tenant catalog','instance:preset:read','ON');
INSERT INTO sys_apis(id,path,method,module,business_module,operation,status,created_at,updated_at,created_by)
SELECT n,path,method,'InstanceService','INSTANCE','InstanceService_'||rpc,
CASE WHEN n%2=0 THEN 'OFF' ELSE 'ON' END,'2026-10-01','2026-10-02',77
FROM (VALUES
(1,'GET','/api/v1/admin/compute-presets','AdminListPresets'),
(2,'GET','/api/v1/admin/compute-presets/{id}','AdminGetPreset'),
(3,'POST','/api/v1/admin/compute-presets','AdminCreatePreset'),
(4,'PATCH','/api/v1/admin/compute-presets/{id}','AdminUpdatePreset'),
(5,'POST','/api/v1/admin/compute-presets/{id}:set-enabled','AdminSetPresetEnabled'),
(6,'DELETE','/api/v1/admin/compute-presets/{id}','AdminDeletePreset')) a(n,method,path,rpc);
INSERT INTO sys_apis(id,path,method,module,business_module,operation)
VALUES(91,'/api/v1/instance-presets','GET','InstanceService','INSTANCE','InstanceService_ListInstancePresets');
INSERT INTO sys_permission_apis(id,permission_id,api_id,created_at)
SELECT n,p,n,'2026-10-01' FROM (VALUES(1,1),(2,2),(3,3),(4,4),(5,4),(6,5),(91,91)) a(n,p);
INSERT INTO sys_role_permissions(id,role_id,permission_id,tenant_id,status,effect,priority,created_at)
SELECT id,20,id,0,status,CASE WHEN id=2 THEN 'DENY' ELSE 'ALLOW' END,13,'2026-10-01' FROM sys_permissions;
INSERT INTO sys_role_metadata(id,tenant_id,role_id,scope,sync_policy,custom_overrides)
VALUES(1,0,20,'PLATFORM','MANUAL','{"preserve":"restriction"}');
INSERT INTO sys_plans(id,name) VALUES(8,'unchanged instance plan');
INSERT INTO sys_plan_modules(id,plan_id,module) VALUES(1,8,'INSTANCE');
SELECT setval(pg_get_serial_sequence('sys_permissions','id'),200,true),
 setval(pg_get_serial_sequence('sys_apis','id'),200,true),
 setval(pg_get_serial_sequence('sys_permission_apis','id'),200,true);`))
	}
	count := func(query string, want int) {
		var got int
		require.NoError(t, db.QueryRowContext(ctx, query).Scan(&got))
		require.Equal(t, want, got)
	}
	t.Run("preserve IDs grants disabled states restrictions and audit through upgrade recovery and rerun", func(t *testing.T) {
		seed()
		before, stable := snapshot(false), snapshot(true)
		require.NoError(t, exec(up))
		count("SELECT count(*) FROM sys_permissions WHERE code LIKE 'compute:preset:admin:%'", 5)
		count("SELECT count(*) FROM sys_permissions WHERE code LIKE 'instance:preset:admin:%'", 0)
		count("SELECT count(*) FROM sys_apis WHERE module='ComputePresetService' AND business_module='SYSTEM'", 6)
		count("SELECT count(*) FROM sys_apis WHERE id=91 AND module='InstanceService' AND business_module='INSTANCE'", 1)
		require.Equal(t, stable, snapshot(true))
		after := snapshot(false)
		require.NoError(t, exec(up))
		require.Equal(t, after, snapshot(false))
		require.NoError(t, exec(down))
		require.Equal(t, before, snapshot(false))
		require.NoError(t, exec(down))
		require.Equal(t, before, snapshot(false))
	})
	for _, tc := range []struct{ name, fault string }{
		{"target conflict", "INSERT INTO sys_permissions(name,code) VALUES('collision','compute:preset:admin:delete')"},
		{"soft deleted target conflict", "INSERT INTO sys_permissions(name,code,deleted_at) VALUES('collision','compute:preset:admin:delete',now())"},
		{"missing permission", "DELETE FROM sys_permissions WHERE id=5"},
		{"deleted source", "UPDATE sys_permissions SET deleted_at=now() WHERE id=5"},
		{"missing route", "DELETE FROM sys_apis WHERE id=6"},
		{"deleted route", "UPDATE sys_apis SET deleted_at=now() WHERE id=6"},
		{"foreign owner", "UPDATE sys_apis SET module='OtherService' WHERE id=6"},
		{"foreign scope", "UPDATE sys_apis SET scope='APP' WHERE id=6"},
		{"foreign operation", "UPDATE sys_apis SET operation='InstanceService_Other' WHERE id=6"},
		{"duplicate route", "INSERT INTO sys_apis(path,method,module) VALUES('/api/v1/admin/compute-presets/{id}','DELETE','OtherService')"},
		{"foreign permission", "INSERT INTO sys_permission_apis(permission_id,api_id) VALUES(91,6)"},
	} {
		t.Run(tc.name+" rolls back every changed row", func(t *testing.T) {
			seed()
			require.NoError(t, exec(tc.fault))
			before := snapshot(false)
			require.Error(t, exec(up))
			require.Equal(t, before, snapshot(false))
		})
	}
	t.Run("unbound route remains unbound and recovery target conflict is atomic", func(t *testing.T) {
		seed()
		require.NoError(t, exec("DELETE FROM sys_permission_apis WHERE api_id=6"))
		require.NoError(t, exec(up))
		count("SELECT count(*) FROM sys_permission_apis WHERE api_id=6", 0)
		require.NoError(t, exec("INSERT INTO sys_permissions(name,code) VALUES('collision','instance:preset:admin:delete')"))
		before := snapshot(false)
		require.Error(t, exec(down))
		require.Equal(t, before, snapshot(false))
	})
	t.Run("contended maintenance lock fails within its bound without changes", func(t *testing.T) {
		seed()
		before := snapshot(false)
		blocker, err := sql.Open("pgx", dsn)
		require.NoError(t, err)
		defer blocker.Close()
		tx, err := blocker.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer tx.Rollback()
		_, err = tx.ExecContext(ctx, "LOCK TABLE sys_apis IN ACCESS EXCLUSIVE MODE")
		require.NoError(t, err)
		started := time.Now()
		err = exec(up)
		require.ErrorContains(t, err, "lock timeout")
		require.Less(t, time.Since(started), 15*time.Second)
		require.NoError(t, tx.Rollback())
		require.Equal(t, before, snapshot(false))
	})
	t.Run("fresh registration separate from upgrade never grants roles or enables existing permissions", func(t *testing.T) {
		seed()
		before := snapshot(false)
		require.Error(t, exec(fresh))
		require.Equal(t, before, snapshot(false))
		require.NoError(t, exec(up))
		require.NoError(t, exec("DELETE FROM sys_role_permissions WHERE permission_id<91; DELETE FROM sys_permission_apis WHERE api_id<91; DELETE FROM sys_permissions WHERE id<91"))
		require.NoError(t, exec("INSERT INTO sys_permissions(name,code,status) VALUES('existing disabled','compute:preset:admin:read','OFF')"))
		require.NoError(t, exec(fresh))
		count("SELECT count(*) FROM sys_permissions WHERE code='compute:preset:admin:read' AND status='OFF'", 1)
		count("SELECT count(*) FROM sys_permissions WHERE code LIKE 'compute:preset:admin:%' AND status='OFF'", 5)
		count("SELECT count(*) FROM sys_role_permissions", 1)
		count("SELECT count(*) FROM sys_permission_apis WHERE api_id<91", 6)
		after := snapshot(false)
		require.NoError(t, exec(fresh))
		require.Equal(t, after, snapshot(false))
	})
}
