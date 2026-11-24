package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
)

const rollupTolerance = 1e-9

// newCostSessions is a session.Service on a real, migrated SQLite database,
// so the roll-up is checked against the stored rows and the real increments.
func newCostSessions(t *testing.T) session.Service {
	t.Helper()
	sqlDB, err := db.NewSQLiteProvider(t.TempDir()).Connect()
	if err != nil {
		t.Fatalf("connect sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	goose.SetBaseFS(db.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("goose dialect: %v", err)
	}
	if err := goose.Up(sqlDB, "migrations/sqlite"); err != nil {
		t.Fatalf("goose up: %v", err)
	}
	return session.NewService(db.New(sqlDB), "cost-test")
}

func storedCost(t *testing.T, svc session.Service, id string) float64 {
	t.Helper()
	s, err := svc.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return s.Cost
}

func assertNear(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > rollupTolerance {
		t.Errorf("%s = %.12f, want %.12f", what, got, want)
	}
}

// spendingSubagent is a task subagent whose run adds spend[i] (the i-th run)
// to its own session through AddCost, as TrackUsage does for real model calls.
// hold, when set, keeps the run open until it is closed; inRun runs inside the
// run after the spend (nested tasks, cancelling the caller).
type spendingSubagent struct {
	Service
	sessions    session.Service
	mu          sync.Mutex
	spend       []float64
	runs        int
	runErr      error
	hold        chan struct{}
	started     chan struct{}
	inRun       func(ctx context.Context, sessionID string)
	afterResult func(sessionID string)
}

func (s *spendingSubagent) RunWith(ctx context.Context, sessionID, _ string, _ int, _ RunOptions, _ ...message.Attachment) (<-chan AgentEvent, error) {
	s.mu.Lock()
	cost := s.spend[s.runs%len(s.spend)]
	s.runs++
	s.mu.Unlock()
	done := make(chan AgentEvent, 1)
	go func() {
		defer close(done)
		if _, err := s.sessions.AddCost(context.Background(), sessionID, cost); err != nil {
			done <- AgentEvent{Type: AgentEventTypeError, Error: err}
			return
		}
		if s.inRun != nil {
			s.inRun(ctx, sessionID)
		}
		if s.started != nil {
			s.started <- struct{}{}
		}
		if s.hold != nil {
			<-s.hold
		}
		done <- AgentEvent{
			Type:    AgentEventTypeResponse,
			Message: message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "done"}}},
			Error:   s.runErr,
		}
		if s.afterResult != nil {
			s.afterResult(sessionID)
		}
	}()
	return done, nil
}

type fixedFactory struct {
	AgentFactory
	sub Service
}

func (f *fixedFactory) NewAgent(context.Context, string, map[string]any, string, bool, []bridge.PeerRef, *contextfile.StepContext, contextfile.TemplateVars, ModelOverride) (Service, error) {
	return f.sub, nil
}

type workerRegistry struct{ agentregistry.Registry }

func (workerRegistry) Get(id string) (agentregistry.AgentInfo, bool) {
	return agentregistry.AgentInfo{ID: id, Mode: config.AgentModeSubagent}, true
}

type linkOnlyPermissions struct{ permission.Service }

func (linkOnlyPermissions) LinkSession(string, string) {}

func newCostTaskTool(svc session.Service, sub Service) *agentTool {
	return &agentTool{sessions: svc, permissions: linkOnlyPermissions{}, registry: workerRegistry{}, factory: &fixedFactory{sub: sub}}
}

func taskCall(ctx context.Context, parentID string) context.Context {
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, parentID)
	return context.WithValue(ctx, tools.MessageIDContextKey, "msg-"+parentID)
}

func runTask(ctx context.Context, b *agentTool, parentID, callID, taskID string, async bool) (tools.ToolResponse, error) {
	input := fmt.Sprintf(`{"prompt":"go","subagent_type":"worker","task_title":"t","task_id":%q,"async":%t}`, taskID, async)
	return b.Run(taskCall(ctx, parentID), tools.ToolCall{ID: callID, Name: TaskToolName, Input: input})
}

func newParent(t *testing.T, svc session.Service, own float64) session.Session {
	t.Helper()
	p, err := svc.Create(context.Background(), "parent")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if own > 0 {
		if _, err := svc.AddCost(context.Background(), p.ID, own); err != nil {
			t.Fatalf("seed parent: %v", err)
		}
	}
	return p
}

// completionCounter is the task.Deps of async completions; a pair is written
// only after the roll-up, so its arrival means the parent was charged.
type completionCounter struct{ pairs chan string }

func (c *completionCounter) WritePair(_ context.Context, sessionID string, _ task.SyntheticPair) error {
	c.pairs <- sessionID
	return nil
}
func (c *completionCounter) IsSessionBusy(string) bool                    { return true }
func (c *completionCounter) ResumeSession(string, string, map[string]any) {}

func withCompletionCounter(t *testing.T, n int) *completionCounter {
	t.Helper()
	withFreshTaskRegistry(t)
	c := &completionCounter{pairs: make(chan string, n)}
	t.Cleanup(task.SetDeps(c))
	return c
}

func (c *completionCounter) wait(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-c.pairs:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d async completions arrived", i, n)
		}
	}
}

func TestTaskRollUp_SyncResumeChargesOnlyEachRun(t *testing.T) {
	svc := newCostSessions(t)
	parent := newParent(t, svc, 1.0)
	sub := &spendingSubagent{sessions: svc, spend: []float64{0.32487257, 0.09976659}}
	b := newCostTaskTool(svc, sub)
	ctx := context.Background()

	if resp, err := runTask(ctx, b, parent.ID, "sync-task-1", "", false); err != nil || resp.IsError {
		t.Fatalf("first run: err=%v resp=%q", err, resp.Content)
	}
	if resp, err := runTask(ctx, b, parent.ID, "sync-call-2", "sync-task-1", false); err != nil || resp.IsError {
		t.Fatalf("resume: err=%v resp=%q", err, resp.Content)
	}

	assertNear(t, "child cost", storedCost(t, svc, "sync-task-1"), 0.42463916)
	assertNear(t, "parent cost (1.0 own + 0.42463916, not + 0.74951173)", storedCost(t, svc, parent.ID), 1.0+0.42463916)
}

func TestTaskRollUp_IncludesPostTurnCompaction(t *testing.T) {
	ctx := context.Background()
	svc := newCostSessions(t)
	parent := newParent(t, svc, 0)
	sub := &spendingSubagent{sessions: svc, spend: []float64{0.2}, afterResult: func(id string) {
		if _, err := svc.AddCost(ctx, id, 0.1); err != nil {
			t.Errorf("post-turn cost: %v", err)
		}
	}}
	if _, err := runTask(ctx, newCostTaskTool(svc, sub), parent.ID, "post-turn", "", false); err != nil {
		t.Fatal(err)
	}
	assertNear(t, "cost after post-turn work", storedCost(t, svc, parent.ID), 0.3)
}

func TestTaskRollUp_AsyncResumeChargesOnlyEachRun(t *testing.T) {
	svc := newCostSessions(t)
	parent := newParent(t, svc, 0)
	done := withCompletionCounter(t, 2)
	sub := &spendingSubagent{sessions: svc, spend: []float64{0.32487257, 0.09976659}}
	b := newCostTaskTool(svc, sub)
	ctx := context.Background()

	if resp, err := runTask(ctx, b, parent.ID, "async-task-1", "", true); err != nil || resp.IsError {
		t.Fatalf("first run: err=%v resp=%q", err, resp.Content)
	}
	done.wait(t, 1)
	if resp, err := runTask(ctx, b, parent.ID, "async-call-2", "async-task-1", true); err != nil || resp.IsError {
		t.Fatalf("resume: err=%v resp=%q", err, resp.Content)
	}
	done.wait(t, 1)

	assertNear(t, "parent cost", storedCost(t, svc, parent.ID), 0.42463916)
}

// A resume arriving while the previous run (or the roll-up after it) still
// holds the task is refused as busy and charges nothing.
func TestTaskRollUp_ResumeWhileInFlightIsBusy(t *testing.T) {
	svc := newCostSessions(t)
	parent := newParent(t, svc, 0)
	done := withCompletionCounter(t, 1)
	sub := &spendingSubagent{sessions: svc, spend: []float64{0.25, 0.5}, hold: make(chan struct{}), started: make(chan struct{}, 1)}
	b := newCostTaskTool(svc, sub)
	ctx := context.Background()

	if _, err := runTask(ctx, b, parent.ID, "busy-task", "", true); err != nil {
		t.Fatalf("first run: %v", err)
	}
	<-sub.started

	_, err := runTask(ctx, b, parent.ID, "busy-call-2", "busy-task", false)
	if !errors.Is(err, ErrSessionBusy) || err.Error() != "error while running task agent: "+ErrSessionBusy.Error() {
		t.Errorf("sync resume while in flight: err = %v, want busy", err)
	}
	_, err = runTask(ctx, b, parent.ID, "busy-call-3", "busy-task", true)
	if !errors.Is(err, ErrSessionBusy) || err.Error() != "async task: start subagent: "+ErrSessionBusy.Error() {
		t.Errorf("async resume while in flight: err = %v, want busy", err)
	}

	// The claim also covers the roll-up after the run: held, it still refuses.
	b2 := newCostTaskTool(svc, &spendingSubagent{sessions: svc, spend: []float64{0}})
	if _, err := svc.CreateTaskSession(ctx, "rollup-held", parent.ID, "t"); err != nil {
		t.Fatalf("create task session: %v", err)
	}
	if _, err := b.beginTaskRun(ctx, session.Session{ID: "rollup-held"}, true); err != nil {
		t.Fatal("fresh claim refused")
	}
	if _, err := runTask(ctx, b2, parent.ID, "rollup-held-call", "rollup-held", false); !errors.Is(err, ErrSessionBusy) {
		t.Errorf("resume during a held roll-up: err = %v, want busy", err)
	}
	activeTaskRuns.Delete("rollup-held")

	close(sub.hold)
	done.wait(t, 1)
	sub.mu.Lock()
	runs := sub.runs
	sub.mu.Unlock()
	if runs != 1 {
		t.Errorf("subagent ran %d times, want 1 (busy resumes must not run)", runs)
	}
	assertNear(t, "parent cost", storedCost(t, svc, parent.ID), 0.25)
}

// Another worker completed and accounted for a run against the same database.
func TestTaskRollUp_ResumeAfterAnotherWorker(t *testing.T) {
	svc := newCostSessions(t)
	parent := newParent(t, svc, 0)
	sub := &spendingSubagent{sessions: svc, spend: []float64{0.1, 0.2, 0.4}}
	b := newCostTaskTool(svc, sub)
	ctx := context.Background()

	if _, err := runTask(ctx, b, parent.ID, "late-task", "", false); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := svc.AddCost(ctx, "late-task", 0.05); err != nil {
		t.Fatalf("late child cost: %v", err)
	}
	if _, err := svc.AddCost(ctx, parent.ID, 0.05); err != nil {
		t.Fatal(err)
	}
	assertNear(t, "parent before resume", storedCost(t, svc, parent.ID), 0.15)

	if _, err := runTask(ctx, b, parent.ID, "late-call-2", "late-task", false); err != nil {
		t.Fatalf("resume: %v", err)
	}
	assertNear(t, "parent after resume", storedCost(t, svc, parent.ID), 0.1+0.05+0.2)
	if _, err := runTask(ctx, b, parent.ID, "late-call-3", "late-task", false); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	assertNear(t, "parent after second resume", storedCost(t, svc, parent.ID), 0.1+0.05+0.2+0.4)
}

// A synchronous task whose caller ctx is cancelled mid-run still hands its
// spend to the parent: the roll-up ctx is detached from cancellation.
func TestTaskRollUp_CancelledCallerStillRollsUp(t *testing.T) {
	svc := newCostSessions(t)
	parent := newParent(t, svc, 0)
	ctx, cancel := context.WithCancel(context.Background())
	sub := &spendingSubagent{sessions: svc, spend: []float64{0.05}, runErr: context.Canceled,
		inRun: func(context.Context, string) { cancel() }}
	b := newCostTaskTool(svc, sub)

	if _, err := runTask(ctx, b, parent.ID, "cancel-task", "", false); err == nil {
		t.Fatal("cancelled run returned no error")
	}
	if ctx.Err() == nil {
		t.Fatal("caller ctx was not cancelled")
	}
	assertNear(t, "parent cost", storedCost(t, svc, parent.ID), 0.05)
}

func TestTaskRollUp_ErroredRunStillRollsUp(t *testing.T) {
	svc := newCostSessions(t)
	parent := newParent(t, svc, 0)
	sub := &spendingSubagent{sessions: svc, spend: []float64{0.07}, runErr: errors.New("model failed")}
	b := newCostTaskTool(svc, sub)

	if _, err := runTask(context.Background(), b, parent.ID, "err-task", "", false); err == nil {
		t.Fatal("errored run returned no error")
	}
	assertNear(t, "parent cost", storedCost(t, svc, parent.ID), 0.07)

	done := withCompletionCounter(t, 1)
	if _, err := runTask(context.Background(), b, parent.ID, "err-call-2", "err-task", true); err != nil {
		t.Fatalf("async resume: %v", err)
	}
	done.wait(t, 1)
	assertNear(t, "parent cost after errored async resume", storedCost(t, svc, parent.ID), 0.14)
}

// A grandchild rolls into the child during the child's run, so it is part of
// the child's delta and reaches the parent once.
func TestTaskRollUp_NestedSubagentCountedOnce(t *testing.T) {
	svc := newCostSessions(t)
	parent := newParent(t, svc, 0)
	grandchild := &spendingSubagent{sessions: svc, spend: []float64{0.3}}
	gb := newCostTaskTool(svc, grandchild)
	child := &spendingSubagent{sessions: svc, spend: []float64{0.2}, inRun: func(_ context.Context, childID string) {
		if _, err := runTask(context.Background(), gb, childID, "nested-grandchild", "", false); err != nil {
			t.Errorf("grandchild: %v", err)
		}
	}}
	b := newCostTaskTool(svc, child)

	if _, err := runTask(context.Background(), b, parent.ID, "nested-child", "", false); err != nil {
		t.Fatalf("child run: %v", err)
	}
	assertNear(t, "child cost", storedCost(t, svc, "nested-child"), 0.5)
	assertNear(t, "parent cost", storedCost(t, svc, parent.ID), 0.5)
}

// N async tasks complete together while the parent keeps recording its own
// model calls: every increment must survive.
func TestTaskRollUp_ParallelAsyncCompletionsWithParentUsage(t *testing.T) {
	const n = 8
	svc := newCostSessions(t)
	parent := newParent(t, svc, 0)
	done := withCompletionCounter(t, n)
	hold := make(chan struct{})
	ctx := context.Background()

	var want float64
	for i := 0; i < n; i++ {
		cost := 0.01 * float64(i+1)
		want += cost
		sub := &spendingSubagent{sessions: svc, spend: []float64{cost}, hold: hold}
		if _, err := runTask(ctx, newCostTaskTool(svc, sub), parent.ID, fmt.Sprintf("par-task-%d", i), "", true); err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
	}

	model := models.Model{CostPer1MIn: 1, CostPer1MOut: 2}
	usage := provider.TokenUsage{InputTokens: 1000, OutputTokens: 500}
	in, out := provider.CalculateCost(model, usage)
	const ownCalls = 40
	parentAgent := &agent{sessions: svc}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < ownCalls; i++ {
			if err := parentAgent.TrackUsage(ctx, parent.ID, model, usage); err != nil {
				t.Errorf("TrackUsage: %v", err)
			}
		}
	}()
	close(hold)
	wg.Wait()
	done.wait(t, n)

	assertNear(t, "parent cost (own + sum of children)", storedCost(t, svc, parent.ID), ownCalls*(in+out)+want)
}

// waitAsyncAndNotify releases the claim even when it panics after the run.
func TestTaskRollUp_AsyncPanicReleasesLedger(t *testing.T) {
	t.Chdir(t.TempDir()) // RecoverPanic writes its panic log to the cwd
	withCompletionCounter(t, 1)
	b := &agentTool{sessions: nil} // Get on a nil service panics inside the roll-up
	id := "panic-task"
	if _, err := b.beginTaskRun(context.Background(), session.Session{ID: id}, false); err != nil {
		t.Fatal("claim refused")
	}
	done := make(chan AgentEvent, 1)
	done <- AgentEvent{Type: AgentEventTypeResponse}
	close(done)
	b.waitAsyncAndNotify(done, nil, "", "parent", "call", "tk", id, "{}", 0)
	if _, err := b.beginTaskRun(context.Background(), session.Session{ID: id}, false); err != nil {
		t.Fatal("ledger still busy after a recovered panic")
	}
	activeTaskRuns.Delete(id)
}
