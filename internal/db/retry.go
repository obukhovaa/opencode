package db

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/go-sql-driver/mysql"
)

// MySQL server error numbers that InnoDB expects the caller to recover from by
// restarting the transaction (the 1213 message literally says "try restarting
// transaction").
const (
	mysqlErrLockDeadlock    = 1213 // ER_LOCK_DEADLOCK
	mysqlErrLockWaitTimeout = 1205 // ER_LOCK_WAIT_TIMEOUT
)

// Transaction-retry policy. A deadlock clears the instant InnoDB rolls the
// victim back, so a few quick jittered attempts absorb the contention without a
// human-perceptible stall.
const (
	txRetryMaxAttempts = 5
	txRetryBaseDelay   = 2 * time.Millisecond
	txRetryMaxDelay    = 100 * time.Millisecond
)

// IsRetryableTxError reports whether err is a transient transaction conflict
// worth retrying: a MySQL deadlock (1213) or lock-wait timeout (1205). It
// unwraps err, so wrapped errors are detected. Any non-MySQL error — including
// every SQLite error — returns false.
func IsRetryableTxError(err error) bool {
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) {
		return myErr.Number == mysqlErrLockDeadlock || myErr.Number == mysqlErrLockWaitTimeout
	}
	return false
}

// WithTxRetry runs fn, retrying while it returns a retryable transaction
// conflict (see IsRetryableTxError), up to txRetryMaxAttempts times with
// full-jitter exponential backoff between attempts.
//
// fn MUST be self-contained and safe to run more than once: it opens its own
// transaction and rolls back on any error, because a retry re-runs it from
// scratch. On the message store's concurrent SELECT MAX(seq) / INSERT path this
// turns an otherwise fatal InnoDB deadlock — two writes to the same session
// racing for the session_id index range — into a transparent retry, matching
// MySQL's own "try restarting transaction" guidance.
//
// A non-retryable error is returned immediately. If ctx is cancelled, the
// context error is returned without another attempt.
func WithTxRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < txRetryMaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = fn()
		if err == nil || !IsRetryableTxError(err) {
			return err
		}
		if attempt == txRetryMaxAttempts-1 {
			break // exhausted: surface the last error below rather than sleeping
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(txRetryBackoff(attempt)):
		}
	}
	return err
}

// txRetryBackoff returns a full-jitter exponential delay for the given
// zero-based attempt: a random duration in [0, min(base<<attempt, max)]. Full
// jitter keeps two transactions that just deadlocked from retrying in lockstep
// and re-colliding.
func txRetryBackoff(attempt int) time.Duration {
	d := txRetryBaseDelay << attempt
	if d <= 0 || d > txRetryMaxDelay {
		d = txRetryMaxDelay
	}
	return time.Duration(rand.Int64N(int64(d) + 1))
}
