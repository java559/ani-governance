package ent

import (
	"context"
	"database/sql"

	entsql "entgo.io/ent/dialect/sql"
)

// SandboxExec and SandboxScan keep the narrowly scoped SQL adapter on the
// same wrapped Ent transaction, including the data access audit driver.
func (tx *Tx) SandboxExec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := tx.config.driver.Exec(ctx, query, args, &result)
	return result, err
}
func (tx *Tx) SandboxScan(ctx context.Context, query string, args []any, values ...any) error {
	var rows entsql.Rows
	if err := tx.config.driver.Query(ctx, query, args, &rows); err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := rows.Scan(values...); err != nil {
		return err
	}
	return rows.Err()
}
