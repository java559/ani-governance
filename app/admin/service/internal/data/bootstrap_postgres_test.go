package data

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"go-wind-admin/app/admin/service/cmd/server/assets"
	"go-wind-admin/app/admin/service/internal/data/ent"
	dbbootstrap "go-wind-admin/sql/bootstrap"
)

// The DSN must point to a disposable database ending in _bootstrap_test.
func TestBootstrapPostgres(t *testing.T) {
	dsn := os.Getenv("GOV_BOOTSTRAP_TEST_DSN")
	if dsn == "" {
		t.Skip("GOV_BOOTSTRAP_TEST_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	var name string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&name))
	require.True(t, strings.HasSuffix(name, "_bootstrap_test"), "refuse to initialize a non-test database")
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	// Test setup only. The application has no schema migration call.
	require.NoError(t, client.Schema.Create(ctx))
	catalog, err := dbbootstrap.Catalog(assets.OpenApiData)
	require.NoError(t, err)
	require.Error(t, dbbootstrap.Check(ctx, db))
	count := func(table string) int {
		var n int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n))
		return n
	}
	snapshot := func() string {
		var result string
		for _, table := range []string{"sys_users", "sys_user_credentials", "sys_user_roles", "sys_roles", "sys_role_metadata", "sys_permissions", "sys_permission_groups", "sys_role_permissions", "sys_permission_apis", "sys_permission_menus", "sys_plans", "sys_plan_modules", "sys_configs", "sys_menus", "sys_languages", "sys_apis"} {
			var s string
			require.NoError(t, db.QueryRowContext(ctx, "SELECT COALESCE(json_agg(t ORDER BY id)::text,'[]') FROM "+table+" t").Scan(&s))
			result += s
		}
		return result
	}
	_, err = dbbootstrap.Initialize(ctx, db, catalog, "admin", "weak")
	require.Error(t, err)
	require.Zero(t, count("sys_users"))
	// Omit one API required by 001_initial.sql without changing the original catalog.
	const requiredMethod, requiredPath = "GET", "/api/v1/admin/me"
	originalCatalog := append([]dbbootstrap.API(nil), catalog...)
	incompleteCatalog := make([]dbbootstrap.API, 0, len(catalog))
	missingCount := 0
	for _, api := range catalog {
		if api.Method == requiredMethod && api.Path == requiredPath {
			missingCount++
			continue
		}
		incompleteCatalog = append(incompleteCatalog, api)
	}
	require.Equal(t, 1, missingCount)
	require.Len(t, incompleteCatalog, len(catalog)-1)
	for _, api := range incompleteCatalog {
		require.False(t, api.Method == requiredMethod && api.Path == requiredPath)
	}
	require.Equal(t, originalCatalog, catalog)
	_, err = dbbootstrap.Initialize(ctx, db, incompleteCatalog, "admin", "Valid-bootstrap@2026")
	require.Error(t, err)
	require.ErrorContains(t, err, "API catalog is incomplete")
	require.Zero(t, count("sys_users"))
	require.Zero(t, count("sys_apis"))
	require.Zero(t, count("sys_roles"))
	// Prove that role/user/API bindings do not depend on a sequence beginning at 1.
	_, err = db.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('sys_roles','id'),100,false),setval(pg_get_serial_sequence('sys_users','id'),200,false),setval(pg_get_serial_sequence('sys_permissions','id'),300,false)`)
	require.NoError(t, err)
	created, err := dbbootstrap.Initialize(ctx, db, catalog, "owner", "Valid-bootstrap@2026")
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, dbbootstrap.Check(ctx, db))
	// Fresh seed must grant logout to the existing tenant-manager permission.
	const logoutGrant = `SELECT COUNT(*) FROM sys_permission_apis pa
 JOIN sys_permissions p ON p.id=pa.permission_id JOIN sys_apis a ON a.id=pa.api_id
 WHERE p.code='sys:tenant_manager' AND a.method='POST' AND a.path='/api/v1/auth/logout'`
	var logoutGrants int
	require.NoError(t, db.QueryRowContext(ctx, logoutGrant).Scan(&logoutGrants))
	require.Equal(t, 1, logoutGrants)
	patch, err := os.ReadFile("../../../../../scripts/ops/sql/repair-tenant-logout.sql")
	require.NoError(t, err)
	// Reproduce the old seed omission; the patch may only add this one binding.
	_, err = db.ExecContext(ctx, `DELETE FROM sys_permission_apis WHERE permission_id IN
 (SELECT id FROM sys_permissions WHERE code='sys:tenant_manager') AND api_id IN
 (SELECT id FROM sys_apis WHERE method='POST' AND path='/api/v1/auth/logout')`)
	require.NoError(t, err)
	grantsWithoutLogout := count("sys_permission_apis")
	_, err = db.ExecContext(ctx, string(patch))
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, logoutGrant).Scan(&logoutGrants))
	require.Equal(t, 1, logoutGrants)
	require.Equal(t, grantsWithoutLogout+1, count("sys_permission_apis"))
	afterPatch := snapshot()
	_, err = db.ExecContext(ctx, string(patch))
	require.NoError(t, err)
	require.Equal(t, afterPatch, snapshot(), "repeat patch must preserve all existing rows and IDs")
	// A missing API must abort, without creating any grants or replacement APIs.
	_, err = db.ExecContext(ctx, `UPDATE sys_apis SET path='/test-missing-logout' WHERE path='/api/v1/auth/logout' AND method='POST'`)
	require.NoError(t, err)
	withoutAPI := snapshot()
	_, err = db.ExecContext(ctx, string(patch))
	require.Error(t, err)
	require.Equal(t, withoutAPI, snapshot())
	_, err = db.ExecContext(ctx, `UPDATE sys_apis SET path='/api/v1/auth/logout' WHERE path='/test-missing-logout' AND method='POST'`)
	require.NoError(t, err)
	var hash string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT credential FROM sys_user_credentials WHERE identifier='owner'`).Scan(&hash))
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("Valid-bootstrap@2026")))
	before := snapshot()
	created, err = dbbootstrap.Initialize(ctx, db, catalog, "another-owner", "")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, before, snapshot(), "repeat initialization must not refresh any row")
	// Read-only preflight must work under PostgreSQL's actual read-only enforcement.
	_, err = db.ExecContext(ctx, `SET default_transaction_read_only=on`)
	require.NoError(t, err)
	require.NoError(t, dbbootstrap.Check(ctx, db))
	_, err = db.ExecContext(ctx, `SET default_transaction_read_only=off`)
	require.NoError(t, err)
	require.Equal(t, before, snapshot())
	var apiID int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM sys_apis WHERE path='/api/v1/admin/users' AND method='GET'`).Scan(&apiID))
	grantsBefore := count("sys_permission_apis")
	_, err = db.ExecContext(ctx, `UPDATE sys_apis SET status='OFF',description='old' WHERE id=$1`, apiID)
	require.NoError(t, err)
	newCatalog := append(append([]dbbootstrap.API{}, catalog...), dbbootstrap.API{Path: "/api/v1/new-test-api", Method: "GET", Operation: "Test_New", Module: "UserService", BusinessModule: "OPM"})
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = dbbootstrap.SyncAPIs(ctx, tx, newCatalog, false)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	var actualID int
	var status string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id,status FROM sys_apis WHERE path='/api/v1/admin/users' AND method='GET'`).Scan(&actualID, &status))
	require.Equal(t, apiID, actualID)
	require.Equal(t, "OFF", status)
	require.Equal(t, grantsBefore, count("sys_permission_apis"))
	var newGrants int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sys_permission_apis pa JOIN sys_apis a ON a.id=pa.api_id WHERE a.path='/api/v1/new-test-api'`).Scan(&newGrants))
	require.Zero(t, newGrants)
	// Existing installations without the new marker must never be reset or claimed.
	_, err = db.ExecContext(ctx, `DELETE FROM sys_configs WHERE key=$1`, dbbootstrap.MarkerKey)
	require.NoError(t, err)
	before = snapshot()
	_, err = dbbootstrap.Initialize(ctx, db, catalog, "owner", "Different-password@2026")
	require.ErrorContains(t, err, "existing installation")
	require.Equal(t, before, snapshot())
}
