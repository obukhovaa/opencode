package db

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestIsRetryableTxError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"deadlock 1213", &mysql.MySQLError{Number: mysqlErrLockDeadlock, Message: "Deadlock found"}, true},
		{"lock wait timeout 1205", &mysql.MySQLError{Number: mysqlErrLockWaitTimeout, Message: "Lock wait timeout"}, true},
		{"wrapped deadlock", fmt.Errorf("create message: %w", &mysql.MySQLError{Number: mysqlErrLockDeadlock}), true},
		{"other mysql error (dup key 1062)", &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}, false},
		{"non-mysql error", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryableTxError(tt.err); got != tt.want {
				t.Fatalf("IsRetryableTxError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestWithTxRetry(t *testing.T) {
	deadlock := &mysql.MySQLError{Number: mysqlErrLockDeadlock, Message: "Deadlock found"}

	t.Run("succeeds on first attempt", func(t *testing.T) {
		calls := 0
		err := WithTxRetry(context.Background(), func() error {
			calls++
			return nil
		})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1", calls)
		}
	})

	t.Run("retries retryable error then succeeds", func(t *testing.T) {
		calls := 0
		err := WithTxRetry(context.Background(), func() error {
			calls++
			if calls < 3 {
				return deadlock
			}
			return nil
		})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if calls != 3 {
			t.Fatalf("calls = %d, want 3", calls)
		}
	})

	t.Run("returns non-retryable error immediately", func(t *testing.T) {
		calls := 0
		sentinel := errors.New("not retryable")
		err := WithTxRetry(context.Background(), func() error {
			calls++
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1", calls)
		}
	})

	t.Run("gives up after max attempts and returns last error", func(t *testing.T) {
		calls := 0
		err := WithTxRetry(context.Background(), func() error {
			calls++
			return deadlock
		})
		if !errors.Is(err, deadlock) {
			t.Fatalf("err = %v, want deadlock", err)
		}
		if calls != txRetryMaxAttempts {
			t.Fatalf("calls = %d, want %d", calls, txRetryMaxAttempts)
		}
	})

	t.Run("returns context error without calling fn when already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		err := WithTxRetry(ctx, func() error {
			calls++
			return deadlock
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if calls != 0 {
			t.Fatalf("calls = %d, want 0", calls)
		}
	})
}
