package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go-wind-admin/app/admin/service/internal/data/ent"
)

// quotaReadSnapshot keeps count, page, policies and accounts on one immutable
// PostgreSQL snapshot. Admission/refund transactions retain their write behavior.
func quotaReadSnapshot(ctx context.Context, client *ent.Client, read func(*ent.Tx) error) (err error) {
	tx, err := client.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback quota read snapshot: %w", rollbackErr))
		}
	}()
	if err = read(tx); err != nil {
		return err
	}
	err = tx.Commit()
	committed = err == nil
	return err
}
