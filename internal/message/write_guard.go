package message

import (
	"context"
	"database/sql"

	"github.com/opencode-ai/opencode/internal/db"
)

func WithWriteGuard(ctx context.Context, guard func(context.Context, *sql.Tx) error) context.Context {
	return db.WithWriteGuard(ctx, db.WriteGuard(guard))
}
func InheritWriteGuard(dst, src context.Context) context.Context {
	return db.InheritWriteGuard(dst, src)
}
func (s *service) guardedWrite(ctx context.Context, mutate func(db.QuerierWithTx) error) error {
	if db.WriteGuardFromContext(ctx) == nil {
		return mutate(s.q)
	}
	return db.WithTxRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = db.CheckWriteGuard(ctx, tx); err != nil {
			return err
		}
		if err = mutate(s.q.WithTx(tx)); err != nil {
			return err
		}
		return tx.Commit()
	})
}
