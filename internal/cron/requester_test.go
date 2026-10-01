package cron

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/pressly/goose/v3"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// newRequesterTestService builds a cron service over a migrated in-memory
// SQLite with one session row ("S1") for the jobs' FK.
func newRequesterTestService(t *testing.T) *service {
	t.Helper()
	conn, err := sql.Open("sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	goose.SetBaseFS(db.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}
	if err := goose.Up(conn, "migrations/sqlite"); err != nil {
		t.Fatalf("goose up: %v", err)
	}
	if _, err := conn.Exec(`
		INSERT INTO sessions (id, project_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at)
		VALUES ('S1', 'proj', 't', 0, 0, 0, 0, strftime('%s','now'), strftime('%s','now'))
	`); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return NewService(db.New(conn)).(*service)
}

func createJob(t *testing.T, svc *service, ctx context.Context, explicit string) CronJob {
	t.Helper()
	job, err := svc.Create(ctx, CreateParams{
		SessionID:    "S1",
		Schedule:     "*/5 * * * *",
		Prompt:       "check the deploy",
		SubagentType: "coder",
		TaskTitle:    "deploy check",
		IsRecurring:  true,
		Source:       "agent",
		Requester:    explicit,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return job
}

func TestCreateStoresRequester(t *testing.T) {
	svc := newRequesterTestService(t)
	turnCtx := tools.WithRequester(context.Background(), "author@example.com")

	tests := []struct {
		name     string
		ctx      context.Context
		explicit string
		want     string
	}{
		{name: "taken from the creating turn's ctx", ctx: turnCtx, want: "author@example.com"},
		{name: "explicit param wins over ctx", ctx: turnCtx, explicit: "other@example.com", want: "other@example.com"},
		{name: "none known stores empty", ctx: context.Background(), want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := createJob(t, svc, tt.ctx, tt.explicit)
			if job.Requester != tt.want {
				t.Fatalf("created job requester = %q, want %q", job.Requester, tt.want)
			}
			got, err := svc.Get(context.Background(), job.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Requester != tt.want {
				t.Fatalf("stored requester = %q, want %q", got.Requester, tt.want)
			}
		})
	}
}

// captureRunner records the requester on the task ctx and fails the run so
// fireJob takes the error path (no synthetic-message write needed).
type captureRunner struct{ got []string }

func (r *captureRunner) RunTask(ctx context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
	r.got = append(r.got, tools.RequesterFromContext(ctx))
	return tools.ToolResponse{}, errors.New("stop here")
}

func TestFireJobReplaysRequester(t *testing.T) {
	svc := newRequesterTestService(t)
	runner := &captureRunner{}
	sched := NewScheduler(svc, nil, nil, nil, nil, nil, runner)

	withRequester := createJob(t, svc, tools.WithRequester(context.Background(), "author@example.com"), "")
	without := createJob(t, svc, context.Background(), "")

	// The scheduler's own ctx carries no requester; only the job's does.
	for _, job := range []CronJob{withRequester, without} {
		if err := svc.RescheduleAndClear(context.Background(), job.ID, time.Now().Add(-time.Minute)); err != nil {
			t.Fatalf("make due: %v", err)
		}
		sched.fireJob(context.Background(), job)
	}

	want := []string{"author@example.com", ""}
	if len(runner.got) != len(want) {
		t.Fatalf("runs = %d, want %d", len(runner.got), len(want))
	}
	for i := range want {
		if runner.got[i] != want[i] {
			t.Errorf("run %d requester = %q, want %q", i, runner.got[i], want[i])
		}
	}
}
