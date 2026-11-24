package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// The MySQL read-back after an increment must never look retryable once the
// write has applied, or db.WithTxRetry would re-issue the increment. A closed
// handle makes GetSessionByID fail without a MySQL server.
func TestMySQLQuerierReadBackAfterWrite(t *testing.T) {
	sqlDB, err := NewSQLiteProvider(filepath.Join(t.TempDir(), "x")).Connect()
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	q := NewMySQLQuerier(sqlDB)
	_ = sqlDB.Close()
	ctx := context.Background()

	_, err = q.readBackAfterWrite(ctx, "s", 1)
	if !errors.Is(err, ErrWriteCommitted) {
		t.Errorf("rows=1: err = %v, want ErrWriteCommitted", err)
	}
	if IsRetryableTxError(err) {
		t.Errorf("rows=1: err %v must not be retryable", err)
	}

	_, err = q.readBackAfterWrite(ctx, "s", 0)
	if err == nil || errors.Is(err, ErrWriteCommitted) {
		t.Errorf("rows=0: err = %v, want the plain read error (nothing was written)", err)
	}
}
