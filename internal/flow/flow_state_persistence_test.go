package flow

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/pressly/goose/v3"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"github.com/opencode-ai/opencode/internal/db"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/session"
)

// The tests in this file run the flow service against a real database schema
// instead of stubQuerier, because the bug they guard (CD-5457) lives in the
// schema: flow_states.session_id is a foreign key to sessions(id)
// (fk_flow_states_session), and a flow_states row written before its step
// session exists is refused. MySQL always enforces it; production SQLite sets
// `PRAGMA foreign_keys = ON` on one pooled connection only, which is how the
// bug went unnoticed there.
//
// runFlowStatePersistenceSuite is backend-neutral. TestFlowStatePersistenceSQLite
// runs it in the default `go test` with foreign keys enforced on every
// connection; TestFlowStatePersistenceMySQL (mysql_integration build tag, see
// flow_state_persistence_mysql_integration_test.go) runs it against the real
// MySQL migrations.

// gooseMu serialises goose's package-global SetBaseFS / SetDialect.
var gooseMu sync.Mutex

// txlessQuerier adapts a plain db.Querier to db.QuerierWithTx. The flow
// service never opens a transaction, so WithTx is never called.
type txlessQuerier struct {
	db.Querier
}

func (q txlessQuerier) WithTx(_ *sql.Tx) db.QuerierWithTx { return q }

func migrate(t *testing.T, conn *sql.DB, dialect, dir string) {
	t.Helper()
	gooseMu.Lock()
	defer gooseMu.Unlock()
	goose.SetBaseFS(db.FS)
	if err := goose.SetDialect(dialect); err != nil {
		t.Fatalf("goose dialect %s: %v", dialect, err)
	}
	if err := goose.Up(conn, dir); err != nil {
		t.Fatalf("goose up %s: %v", dir, err)
	}
}

func TestFlowStatePersistenceSQLite(t *testing.T) {
	runFlowStatePersistenceSuite(t, func(t *testing.T) db.Querier {
		t.Helper()
		// _pragma in the DSN applies to every connection the pool opens, so
		// the foreign key is enforced deterministically, like on MySQL.
		dsn := "file:" + filepath.Join(t.TempDir(), "flow.db") + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
		conn, err := sql.Open("sqlite3", dsn)
		if err != nil {
			t.Fatalf("sql.Open: %v", err)
		}
		conn.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = conn.Close() })
		migrate(t, conn, "sqlite3", "migrations/sqlite")
		return db.New(conn)
	})
}

// persistenceAgentFactory hands out stub agents and lets a test fail NewAgent
// per step, or inspect the database at the moment NewAgent is called.
type persistenceAgentFactory struct {
	*stubAgentFactory
	beforeNewAgent func(stepID string) error
}

func (f *persistenceAgentFactory) NewAgent(ctx context.Context, agentID string, schema map[string]any, stepID string, interactive bool, peers []bridge.PeerRef, stepCtx *contextfile.StepContext, flowVars contextfile.TemplateVars, override agentpkg.ModelOverride) (agentpkg.Service, error) {
	if f.beforeNewAgent != nil {
		if err := f.beforeNewAgent(stepID); err != nil {
			return nil, err
		}
	}
	return f.stubAgentFactory.NewAgent(ctx, agentID, schema, stepID, interactive, peers, stepCtx, flowVars, override)
}

// unavailableWrites fails every write the flow service and the session
// service make, as a database that is down (or refusing writes) would.
type unavailableWrites struct {
	db.Querier
}

var errDatabaseUnavailable = errors.New("database unavailable")

func (unavailableWrites) CreateSession(context.Context, db.CreateSessionParams) (db.Session, error) {
	return db.Session{}, errDatabaseUnavailable
}

func (unavailableWrites) CreateFlowState(context.Context, db.CreateFlowStateParams) (db.FlowState, error) {
	return db.FlowState{}, errDatabaseUnavailable
}

func (unavailableWrites) UpdateFlowState(context.Context, db.UpdateFlowStateParams) (db.FlowState, error) {
	return db.FlowState{}, errDatabaseUnavailable
}

type flowRunResult struct {
	states []*FlowState
	errors []error
}

func (r flowRunResult) failedSteps() map[string]string {
	out := map[string]string{}
	for _, s := range r.states {
		if s.Status == FlowStatusFailed {
			out[s.StepID] = s.Output
		}
	}
	return out
}

func drainFlowRun(t *testing.T, agentEvents <-chan agentpkg.AgentEvent, flowStates <-chan *FlowState) flowRunResult {
	t.Helper()
	var res flowRunResult
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range agentEvents {
			if ev.Type == agentpkg.AgentEventTypeError {
				mu.Lock()
				res.errors = append(res.errors, ev.Error)
				mu.Unlock()
			}
		}
	}()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case s, ok := <-flowStates:
			if !ok {
				<-done
				return res
			}
			res.states = append(res.states, s)
		case <-timeout:
			t.Fatal("flow run did not finish within 30s")
		}
	}
}

func runFlowStatePersistenceSuite(t *testing.T, open func(t *testing.T) db.Querier) {
	const prefix = "cd5457"
	ctx := context.Background()

	newService := func(q db.Querier, factory *persistenceAgentFactory) *service {
		return NewService(session.NewService(q, "proj"), nil, txlessQuerier{q}, &stubPermissions{}, factory).(*service)
	}
	stepSession := func(flowID, stepID string) string {
		return prefix + "-" + flowID + "-" + stepID
	}

	t.Run("a fresh run writes the first step's running row before the agent is built", func(t *testing.T) {
		q := open(t)
		registerTestFlow(t, Flow{
			ID: "cd5457-fresh",
			Spec: FlowSpec{Steps: []Step{
				{ID: "start", Prompt: "go", Rules: []Rule{{Then: "finish"}}},
				{ID: "finish", Prompt: "wrap up"},
			}},
		})

		var mu sync.Mutex
		observed := map[string]string{}
		factory := &persistenceAgentFactory{stubAgentFactory: &stubAgentFactory{agent: newStubAgent()}}
		factory.beforeNewAgent = func(stepID string) error {
			status := "missing"
			if row, err := q.GetFlowState(ctx, stepSession("cd5457-fresh", stepID)); err == nil {
				status = row.Status
			}
			mu.Lock()
			observed[stepID] = status
			mu.Unlock()
			return nil
		}
		svc := newService(q, factory)

		agentEvents, flowStates, err := svc.Run(ctx, prefix, "cd5457-fresh", map[string]any{}, true)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		res := drainFlowRun(t, agentEvents, flowStates)
		if failed := res.failedSteps(); len(failed) > 0 {
			t.Fatalf("unexpected failed steps: %v", failed)
		}

		for _, stepID := range []string{"start", "finish"} {
			if observed[stepID] != string(FlowStatusRunning) {
				t.Errorf("step %s flow state when its agent was built = %q, want %q", stepID, observed[stepID], FlowStatusRunning)
			}
			row, err := q.GetFlowState(ctx, stepSession("cd5457-fresh", stepID))
			if err != nil {
				t.Fatalf("step %s: no flow state after the run: %v", stepID, err)
			}
			if row.Status != string(FlowStatusCompleted) {
				t.Errorf("step %s final status = %q, want %q", stepID, row.Status, FlowStatusCompleted)
			}
		}
	})

	// The startup failure from the CD-5535 rehearsal: the step fails before
	// any model call, and so does its fallback. Both failures must survive as
	// rows, whichever setup stage fails.
	startupFailures := []struct {
		name   string
		flowID string
		setup  func(svc *service, factory *persistenceAgentFactory)
		steps  []Step
	}{
		{
			name:   "agent construction fails",
			flowID: "cd5457-agent-fails",
			setup: func(_ *service, factory *persistenceAgentFactory) {
				factory.beforeNewAgent = func(stepID string) error {
					return errors.New("resolving langfusePromptPath for " + stepID + ": context deadline exceeded")
				}
			},
			steps: []Step{
				{ID: "start", Prompt: "go", Fallback: &Fallback{To: "run-failed"}},
				{ID: "run-failed", Prompt: "report"},
			},
		},
		{
			name:   "step prompt resolution fails",
			flowID: "cd5457-prompt-fails",
			setup: func(svc *service, _ *persistenceAgentFactory) {
				svc.SetPromptResolver(&stubPromptResolver{err: errors.New("context deadline exceeded")})
			},
			steps: []Step{
				{ID: "start", LangfusePromptPath: "flows/x/start", Fallback: &Fallback{To: "run-failed"}},
				{ID: "run-failed", LangfusePromptPath: "flows/x/run-failed"},
			},
		},
	}
	for _, tc := range startupFailures {
		t.Run("initial and fallback steps persist as failed when "+tc.name, func(t *testing.T) {
			q := open(t)
			flowID := tc.flowID
			registerTestFlow(t, Flow{ID: flowID, Spec: FlowSpec{Steps: tc.steps}})

			agent := newStubAgent()
			factory := &persistenceAgentFactory{stubAgentFactory: &stubAgentFactory{agent: agent}}
			svc := newService(q, factory)
			tc.setup(svc, factory)

			agentEvents, flowStates, err := svc.Run(ctx, prefix, flowID, map[string]any{}, true)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			res := drainFlowRun(t, agentEvents, flowStates)

			if n := agent.callCount(); n != 0 {
				t.Errorf("agent ran %d times, want 0 (both steps fail before any model call)", n)
			}
			failed := res.failedSteps()
			for _, stepID := range []string{"start", "run-failed"} {
				if _, ok := failed[stepID]; !ok {
					t.Errorf("no failed flow state published for step %s (got %v)", stepID, failed)
				}
				row, err := q.GetFlowState(ctx, stepSession(flowID, stepID))
				if err != nil {
					t.Errorf("step %s: failed state was not persisted: %v", stepID, err)
					continue
				}
				if row.Status != string(FlowStatusFailed) {
					t.Errorf("step %s persisted status = %q, want %q", stepID, row.Status, FlowStatusFailed)
				}
				if !strings.Contains(row.Output.String, "context deadline exceeded") {
					t.Errorf("step %s persisted output = %q, want the setup error", stepID, row.Output.String)
				}
			}
		})
	}

	// Persistence is best effort; reporting the failure is not. The API's
	// flow.failed is derived from the failed FlowState on the flowStates
	// channel (internal/api flowRunner.observeStep), so it must arrive even
	// when every write is refused.
	t.Run("failures still publish when persistence is unavailable", func(t *testing.T) {
		q := unavailableWrites{Querier: open(t)}
		registerTestFlow(t, Flow{
			ID: "cd5457-db-down",
			Spec: FlowSpec{Steps: []Step{
				{ID: "start", Prompt: "go", Fallback: &Fallback{To: "run-failed"}},
				{ID: "run-failed", Prompt: "report"},
			}},
		})
		factory := &persistenceAgentFactory{stubAgentFactory: &stubAgentFactory{agent: newStubAgent()}}
		factory.beforeNewAgent = func(string) error { return errors.New("context deadline exceeded") }
		svc := newService(q, factory)

		agentEvents, flowStates, err := svc.Run(ctx, prefix, "cd5457-db-down", map[string]any{}, true)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		res := drainFlowRun(t, agentEvents, flowStates)

		failed := res.failedSteps()
		for _, stepID := range []string{"start", "run-failed"} {
			if _, ok := failed[stepID]; !ok {
				t.Errorf("no failed flow state published for step %s (got %v)", stepID, failed)
			}
		}
		if len(res.errors) != 2 {
			t.Errorf("error events = %d, want 2 (one per failed step)", len(res.errors))
		}
	})
}
