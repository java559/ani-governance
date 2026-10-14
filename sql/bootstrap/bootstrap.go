package bootstrap

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"

	passwordPolicy "go-wind-admin/pkg/password"
	"golang.org/x/crypto/bcrypt"
)

// InitialSQL is a frozen, reviewable data seed, not a schema migration.
// Changes for an existing installation must use a separate reviewed data script.
//
//go:embed 001_initial.sql
var InitialSQL string

const MarkerKey = "deployment.bootstrap.v1"

// Initialize commits API registration, data and the administrator together.
// Repeating a completed initialization performs no data updates.
func Initialize(ctx context.Context, db *sql.DB, catalog []API, username, password string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(718294632)`); err != nil {
		return false, err
	}
	var done bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sys_configs WHERE key=$1)`, MarkerKey).Scan(&done); err != nil {
		return false, fmt.Errorf("schema unavailable; apply Atlas migrations first: %w", err)
	}
	if done {
		return false, nil
	}
	if strings.TrimSpace(username) != username || username == "" || len(username) > 64 {
		return false, fmt.Errorf("administrator username must contain 1-64 characters without surrounding whitespace")
	}
	if err = passwordPolicy.ValidateComplexity(password, passwordPolicy.DefaultMinLen); err != nil {
		return false, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, fmt.Errorf("hash administrator password: %w", err)
	}
	if _, err = SyncAPIs(ctx, tx, catalog, false); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT set_config('ani.bootstrap_username',$1,true),set_config('ani.bootstrap_password_hash',$2,true)`, username, string(hash)); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, InitialSQL); err != nil {
		return false, fmt.Errorf("seed data rolled back: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// Check is read-only, and also works on pre-existing installations without a
// bootstrap marker. It checks the platform login/management chain, not passwords
// or downstream reachability. Tenant plan checks are a separate acceptance step.
func Check(ctx context.Context, db *sql.DB) error {
	var healthy bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (
 SELECT 1 FROM sys_users u
 JOIN sys_user_credentials c ON c.user_id=u.id AND c.tenant_id=u.tenant_id
 JOIN sys_user_roles ur ON ur.user_id=u.id AND ur.tenant_id=u.tenant_id
 JOIN sys_roles r ON r.id=ur.role_id AND r.tenant_id=ur.tenant_id
 WHERE u.tenant_id=0 AND u.status='NORMAL' AND c.status='ENABLED'
 AND c.identity_type='USERNAME' AND c.credential_type='PASSWORD_HASH' AND c.identifier=u.username AND c.credential LIKE '$2%'
 AND ur.status='ACTIVE' AND (ur.start_at IS NULL OR ur.start_at<=NOW()) AND (ur.end_at IS NULL OR ur.end_at>NOW())
 AND r.status='ON' AND r.code LIKE 'platform:%'
 AND EXISTS(SELECT 1 FROM sys_role_permissions rp JOIN sys_permissions p ON p.id=rp.permission_id
 WHERE rp.role_id=r.id AND rp.tenant_id=0 AND rp.status='ON' AND rp.effect='ALLOW' AND p.status='ON' AND p.code='sys:access_backend')
 AND NOT EXISTS (
 SELECT 1 FROM (VALUES ('GET','/api/v1/admin/me'),('GET','/api/v1/admin/initial-context'),('GET','/api/v1/admin/plans'),('GET','/api/v1/admin/tenants')) required(method,path)
 WHERE NOT EXISTS(SELECT 1 FROM sys_apis a JOIN sys_permission_apis pa ON pa.api_id=a.id
 JOIN sys_permissions p ON p.id=pa.permission_id JOIN sys_role_permissions rp ON rp.permission_id=p.id
 WHERE a.method=required.method AND a.path=required.path AND a.status='ON' AND p.status='ON'
 AND rp.role_id=r.id AND rp.tenant_id=0 AND rp.status='ON' AND rp.effect='ALLOW'))
 )`).Scan(&healthy)
	if err != nil {
		return fmt.Errorf("database preflight failed; apply Atlas migrations and explicit initialization before starting: %w", err)
	}
	if !healthy {
		return fmt.Errorf("database preflight failed: no enabled platform administrator with backend access and required API grants; run admin init for a fresh database, or repair existing grants explicitly (see docs/deployment.md)")
	}
	return nil
}
