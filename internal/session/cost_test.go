package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"

	sqlite3 "github.com/ncruces/go-sqlite3"

	"github.com/opencode-ai/opencode/internal/db"
)

const costTolerance = 1e-9

func assertCost(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > costTolerance {
		t.Errorf("%s cost = %.12f, want %.12f", what, got, want)
	}
}

func TestAddCost_StaleSaveKeepsIncrements(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, _ := svc.Create(ctx, "s")

	stale, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := svc.AddCost(ctx, created.ID, 1.0); err != nil {
		t.Fatalf("add cost: %v", err)
	}
	if _, err := svc.RecordUsage(ctx, created.ID, UsageUpdate{PromptTokens: 10, CompletionTokens: 5, AddPromptTokens: 10, AddCompletionTokens: 5, AddCost: 0.25}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	stale.Title = "renamed by a stale copy"
	stale.PromptTokens = 777
	saved, err := svc.Save(ctx, stale)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	assertCost(t, "Save result", saved.Cost, 1.25)

	got, _ := svc.Get(ctx, created.ID)
	assertCost(t, "stored", got.Cost, 1.25)
	if got.Title != "renamed by a stale copy" || got.PromptTokens != 777 {
		t.Errorf("saved fields = {%q, %d}, want the stale copy's title and tokens", got.Title, got.PromptTokens)
	}
}

func TestAddCost_ConcurrentIncrementsSumExactly(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, _ := svc.Create(ctx, "s")

	const n = 50
	var want float64
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		delta := 0.001 * float64(i+1)
		want += delta
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.AddCost(ctx, created.ID, delta); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("AddCost: %v", err)
	}
	got, _ := svc.Get(ctx, created.ID)
	assertCost(t, "stored", got.Cost, want)
}

func TestRecordUsage_ConcurrentWithAddCostSumsExactly(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, _ := svc.Create(ctx, "s")

	const n = 25
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := svc.RecordUsage(ctx, created.ID, UsageUpdate{PromptTokens: 100, CompletionTokens: 10, AddPromptTokens: 100, AddCompletionTokens: 10, AddCost: 0.01}); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := svc.AddCost(ctx, created.ID, 0.02); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("write: %v", err)
	}
	got, _ := svc.Get(ctx, created.ID)
	assertCost(t, "stored", got.Cost, n*0.03)
	if got.TotalPromptTokens != n*100 || got.TotalCompletionTokens != n*10 {
		t.Errorf("totals = {%d, %d}, want {%d, %d}", got.TotalPromptTokens, got.TotalCompletionTokens, n*100, n*10)
	}
	if got.PromptTokens != 100 || got.CompletionTokens != 10 {
		t.Errorf("last-call tokens = {%d, %d}, want {100, 10}", got.PromptTokens, got.CompletionTokens)
	}
}

func TestRecordCompaction_WritesSummaryAndCostTogether(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, _ := svc.Create(ctx, "s")
	if _, err := svc.RecordUsage(ctx, created.ID, UsageUpdate{PromptTokens: 900, CompletionTokens: 50, AddPromptTokens: 900, AddCompletionTokens: 50, AddCost: 0.5}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	got, err := svc.RecordCompaction(ctx, created.ID, CompactionUpdate{
		SummaryMessageID: "summary-1",
		UsageUpdate:      UsageUpdate{PromptTokens: 0, CompletionTokens: 40, AddPromptTokens: 950, AddCompletionTokens: 40, AddCost: 0.125},
	})
	if err != nil {
		t.Fatalf("record compaction: %v", err)
	}
	stored, _ := svc.Get(ctx, created.ID)
	for _, s := range []Session{got, stored} {
		if s.SummaryMessageID != "summary-1" {
			t.Errorf("summary id = %q, want summary-1", s.SummaryMessageID)
		}
		assertCost(t, "compacted", s.Cost, 0.625)
		if s.PromptTokens != 0 || s.CompletionTokens != 40 || s.TotalPromptTokens != 1850 || s.TotalCompletionTokens != 90 {
			t.Errorf("tokens = {%d, %d, %d, %d}, want {0, 40, 1850, 90}", s.PromptTokens, s.CompletionTokens, s.TotalPromptTokens, s.TotalCompletionTokens)
		}
	}
}

func TestCostWrites_RejectNegativeDelta(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, _ := svc.Create(ctx, "s")
	if _, err := svc.AddCost(ctx, created.ID, 0.5); err != nil {
		t.Fatalf("add cost: %v", err)
	}

	if _, err := svc.AddCost(ctx, created.ID, -0.1); !errors.Is(err, ErrNegativeCost) {
		t.Errorf("AddCost(-0.1) err = %v, want ErrNegativeCost", err)
	}
	if _, err := svc.AddCost(ctx, created.ID, math.NaN()); !errors.Is(err, ErrNegativeCost) {
		t.Errorf("AddCost(NaN) err = %v, want ErrNegativeCost", err)
	}
	if _, err := svc.RecordUsage(ctx, created.ID, UsageUpdate{AddCost: -1}); !errors.Is(err, ErrNegativeCost) {
		t.Errorf("RecordUsage(-1) err = %v, want ErrNegativeCost", err)
	}
	if _, err := svc.RecordCompaction(ctx, created.ID, CompactionUpdate{UsageUpdate: UsageUpdate{AddCost: -1}}); !errors.Is(err, ErrNegativeCost) {
		t.Errorf("RecordCompaction(-1) err = %v, want ErrNegativeCost", err)
	}
	got, _ := svc.Get(ctx, created.ID)
	assertCost(t, "stored", got.Cost, 0.5)
}

func TestAddCost_ZeroDeltaAndMissingSession(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, _ := svc.Create(ctx, "s")
	got, err := svc.AddCost(ctx, created.ID, 0)
	if err != nil || got.ID != created.ID {
		t.Fatalf("AddCost(0) = {%q, %v}, want the stored row", got.ID, err)
	}
	if _, err := svc.AddCost(ctx, "missing", 0.1); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("AddCost(missing) err = %v, want sql.ErrNoRows", err)
	}
}

// scriptedCostQuerier stands in for a dialect querier so the service's retry
// discipline can be observed: calls counts every write statement issued,
// applied counts the ones that took effect.
type scriptedCostQuerier struct {
	db.Querier
	calls   atomic.Int32
	applied atomic.Int32
	// respond decides the outcome of the n-th call (1-based).
	respond func(n int32) (apply bool, err error)
}

func (q *scriptedCostQuerier) AddSessionCost(_ context.Context, arg db.AddSessionCostParams) (db.Session, error) {
	n := q.calls.Add(1)
	apply, err := q.respond(n)
	if apply {
		q.applied.Add(1)
	}
	if err != nil {
		return db.Session{}, err
	}
	return db.Session{ID: arg.ID, Cost: arg.Cost}, nil
}

func TestAddCost_RetriesOnlyTheWrite(t *testing.T) {
	q := &scriptedCostQuerier{respond: func(n int32) (bool, error) {
		if n == 1 {
			return false, sqlite3.BUSY // did not apply: retryable
		}
		return true, nil
	}}
	svc := NewService(q, "p")
	got, err := svc.AddCost(context.Background(), "s", 0.3)
	if err != nil {
		t.Fatalf("AddCost: %v", err)
	}
	if q.calls.Load() != 2 || q.applied.Load() != 1 {
		t.Errorf("calls = %d, applied = %d; want 2 calls, 1 applied", q.calls.Load(), q.applied.Load())
	}
	assertCost(t, "returned", got.Cost, 0.3)
}

func TestAddCost_ReadBackFailureIsNotRetried(t *testing.T) {
	// What MySQLQuerier returns when the UPDATE committed and GetSessionByID
	// failed. The inner error is itself retryable; it must not reach the
	// retry loop through the wrapper.
	q := &scriptedCostQuerier{respond: func(int32) (bool, error) {
		return true, fmt.Errorf("%w: %v", db.ErrWriteCommitted, sqlite3.BUSY)
	}}
	svc := NewService(q, "p")
	_, err := svc.AddCost(context.Background(), "s", 0.3)
	if !errors.Is(err, ErrCostCommitted) {
		t.Fatalf("AddCost err = %v, want ErrCostCommitted", err)
	}
	if q.calls.Load() != 1 || q.applied.Load() != 1 {
		t.Errorf("calls = %d, applied = %d; want the write issued once", q.calls.Load(), q.applied.Load())
	}
}
