//go:build quota_pg

package data

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

func TestTenantDeleteKeepsTenantLockUntilDeletePostgres(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	tenantID, _ := seedTenantPlan(t, c, "tenant-delete-lock", 8)
	_, err := c.DB().ExecContext(context.Background(), "UPDATE sys_tenants SET tenant_space_state='CLOSED',tenant_space_operation_id=$2 WHERE id=$1", tenantID, uuid.NewString())
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	c.Client().Tenant.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if m.Op().Is(ent.OpDeleteOne) {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return next.Mutate(ctx, m)
		})
	})
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- newTenantRepoForLedgerTest(c).Delete(ctx, deleteTenantReq(tenantID)) }()
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("delete returned before reaching the delete statement: %v", err)
	case <-ctx.Done():
		t.Fatal("delete did not reach the delete statement")
	}

	tx, err := c.DB().BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	var id uint32
	err = tx.QueryRowContext(ctx, "SELECT id FROM sys_tenants WHERE id=$1 FOR UPDATE NOWAIT", tenantID).Scan(&id)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("tenant row was not locked through deletion: id=%d err=%v", id, err)
	}
	unblock()
	require.NoError(t, <-result)
}
