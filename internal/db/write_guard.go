package db

import (
	"context"
	"database/sql"
)

// WriteGuard runs inside the same transaction as a guarded service mutation.
// The implementation can lock/check run ownership without a check-then-write
// window. It is optional; ordinary sessions retain their existing write path.
type WriteGuard func(context.Context, *sql.Tx) error
type writeGuardKey struct{}

func WithWriteGuard(ctx context.Context, guard WriteGuard) context.Context {
	return context.WithValue(ctx, writeGuardKey{}, guard)
}
func WriteGuardFromContext(ctx context.Context) WriteGuard {
	guard, _ := ctx.Value(writeGuardKey{}).(WriteGuard)
	return guard
}
func InheritWriteGuard(dst, src context.Context) context.Context {
	if guard := WriteGuardFromContext(src); guard != nil {
		return WithWriteGuard(dst, guard)
	}
	return dst
}
func CheckWriteGuard(ctx context.Context, tx *sql.Tx) error {
	if guard := WriteGuardFromContext(ctx); guard != nil {
		return guard(ctx, tx)
	}
	return nil
}
