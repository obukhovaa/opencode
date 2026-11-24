//go:build mysql_integration

package db_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/session"
)

func createCostSession(t *testing.T, q *db.MySQLQuerier, id string) {
	t.Helper()
	if _, err := q.CreateSession(context.Background(), db.CreateSessionParams{
		ID:        id,
		ProjectID: sql.NullString{String: "proj", Valid: true},
		Title:     "cost",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
}

func mysqlCost(t *testing.T, q *db.MySQLQuerier, id string) db.Session {
	t.Helper()
	s, err := q.GetSessionByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSessionByID: %v", err)
	}
	return s
}

func TestMySQLAddSessionCostConcurrentSumsExactly(t *testing.T) {
	conn := openTestMySQL(t)
	q := db.NewMySQLQuerier(conn)
	svc := session.NewService(q, "proj")
	createCostSession(t, q, "S-cost-concurrent")

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
			if _, err := svc.AddCost(context.Background(), "S-cost-concurrent", delta); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("AddCost: %v", err)
	}
	if got := mysqlCost(t, q, "S-cost-concurrent").Cost; math.Abs(got-want) > 1e-9 {
		t.Errorf("cost = %.12f, want %.12f", got, want)
	}
}

func TestMySQLRecordSessionUsageConcurrentWithAddSessionCost(t *testing.T) {
	conn := openTestMySQL(t)
	q := db.NewMySQLQuerier(conn)
	svc := session.NewService(q, "proj")
	createCostSession(t, q, "S-cost-usage")

	const n = 25
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := svc.RecordUsage(context.Background(), "S-cost-usage", session.UsageUpdate{
				PromptTokens: 100, CompletionTokens: 10, AddPromptTokens: 100, AddCompletionTokens: 10, AddCost: 0.01,
			}); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := svc.AddCost(context.Background(), "S-cost-usage", 0.02); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("write: %v", err)
	}
	got := mysqlCost(t, q, "S-cost-usage")
	if math.Abs(got.Cost-n*0.03) > 1e-9 {
		t.Errorf("cost = %.12f, want %.12f", got.Cost, n*0.03)
	}
	if got.TotalPromptTokens != n*100 || got.TotalCompletionTokens != n*10 {
		t.Errorf("totals = {%d, %d}, want {%d, %d}", got.TotalPromptTokens, got.TotalCompletionTokens, n*100, n*10)
	}
}

func TestMySQLRecordSessionCompactionWritesSummaryAndCost(t *testing.T) {
	conn := openTestMySQL(t)
	q := db.NewMySQLQuerier(conn)
	createCostSession(t, q, "S-cost-compact")

	got, err := q.RecordSessionCompaction(context.Background(), db.RecordSessionCompactionParams{
		ID:                  "S-cost-compact",
		SummaryMessageID:    sql.NullString{String: "summary-1", Valid: true},
		CompletionTokens:    40,
		AddPromptTokens:     900,
		AddCompletionTokens: 40,
		AddCost:             0.125,
	})
	if err != nil {
		t.Fatalf("RecordSessionCompaction: %v", err)
	}
	if got.SummaryMessageID.String != "summary-1" || math.Abs(got.Cost-0.125) > 1e-9 || got.TotalPromptTokens != 900 {
		t.Errorf("row = {%q, %v, %d}, want {summary-1, 0.125, 900}", got.SummaryMessageID.String, got.Cost, got.TotalPromptTokens)
	}
}

func TestMySQLUpdateSessionLeavesCost(t *testing.T) {
	conn := openTestMySQL(t)
	q := db.NewMySQLQuerier(conn)
	createCostSession(t, q, "S-cost-update")
	if _, err := q.AddSessionCost(context.Background(), db.AddSessionCostParams{ID: "S-cost-update", Cost: 1.5}); err != nil {
		t.Fatalf("AddSessionCost: %v", err)
	}

	got, err := q.UpdateSession(context.Background(), db.UpdateSessionParams{ID: "S-cost-update", Title: "renamed", PromptTokens: 7})
	if err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	if got.Title != "renamed" || got.PromptTokens != 7 || math.Abs(got.Cost-1.5) > 1e-9 {
		t.Errorf("after UpdateSession = {%q, %d, %v}, want {renamed, 7, 1.5}", got.Title, got.PromptTokens, got.Cost)
	}
}

func TestMySQLAddSessionCostMissingID(t *testing.T) {
	conn := openTestMySQL(t)
	q := db.NewMySQLQuerier(conn)
	_, err := q.AddSessionCost(context.Background(), db.AddSessionCostParams{ID: "S-missing", Cost: 0.1})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err = %v, want sql.ErrNoRows", err)
	}
	if errors.Is(err, db.ErrWriteCommitted) {
		t.Errorf("err = %v must not claim a committed write", err)
	}
}
