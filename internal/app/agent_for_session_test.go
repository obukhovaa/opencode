package app

import (
	"context"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/session"
)

type namedAgent struct {
	agent.Service
	name string
}

func newAgentsApp(sessions session.Service) (*App, *namedAgent, *namedAgent) {
	coder := &namedAgent{name: "coder"}
	hive := &namedAgent{name: "hivemind"}
	return &App{
		Sessions:         sessions,
		PrimaryAgents:    map[config.AgentName]agent.Service{config.AgentCoder: coder, config.AgentHivemind: hive},
		PrimaryAgentKeys: []config.AgentName{config.AgentCoder, config.AgentHivemind},
		ActiveAgentIdx:   0,
	}, coder, hive
}

// The session's agent lives in the database, so a fresh App and session
// service over the same file see the choice made before the "restart".
func TestAgentForSessionSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	open := func() session.Service {
		sqlDB, err := db.NewSQLiteProvider(dir).Connect()
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
		return session.NewService(db.New(sqlDB), "proj")
	}

	first := open()
	sess, err := first.Create(ctx, "chat")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	other, err := first.Create(ctx, "other chat")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	before, _, _ := newAgentsApp(first)
	if err := first.SetAgentName(ctx, sess.ID, string(config.AgentHivemind)); err != nil {
		t.Fatalf("SetAgentName: %v", err)
	}
	if got := before.AgentForSession(ctx, sess.ID); got.(*namedAgent).name != "hivemind" {
		t.Fatalf("before restart: %v, want hivemind", got)
	}

	second := open()
	after, coder, hive := newAgentsApp(second)
	if got := after.AgentForSession(ctx, sess.ID); got != agent.Service(hive) {
		t.Errorf("after restart the session runs on %v, want hivemind", got)
	}
	if got := after.AgentForSession(ctx, other.ID); got != agent.Service(coder) {
		t.Errorf("a session with no stored agent runs on %v, want the active agent (coder)", got)
	}
	if err := after.SetActiveAgent(config.AgentHivemind); err != nil {
		t.Fatal(err)
	}
	if got := after.AgentForSession(ctx, other.ID); got != agent.Service(hive) {
		t.Errorf("an unstamped session must follow the active agent, got %v", got)
	}
}

func TestAgentForSessionUnknownNameFallsBackToActive(t *testing.T) {
	ctx := context.Background()
	sessions := &fakeSessions{agentName: "retired-agent"}
	app, coder, _ := newAgentsApp(sessions)

	name, got := app.ResolveSessionAgent(ctx, "S1")
	if got != agent.Service(coder) || name != config.AgentCoder {
		t.Errorf("unknown persisted agent resolved to %q / %v, want the active coder", name, got)
	}
}

func TestAgentForSessionLookupFailureFallsBackToActive(t *testing.T) {
	ctx := context.Background()
	app, coder, _ := newAgentsApp(&fakeSessions{err: context.DeadlineExceeded})
	if got := app.AgentForSession(ctx, "S1"); got != agent.Service(coder) {
		t.Errorf("failed session read resolved to %v, want the active agent", got)
	}
	if got := app.AgentForSession(ctx, ""); got != agent.Service(coder) {
		t.Errorf("empty session id resolved to %v, want the active agent", got)
	}
}

type fakeSessions struct {
	session.Service
	agentName string
	err       error
}

func (f *fakeSessions) Get(_ context.Context, id string) (session.Session, error) {
	if f.err != nil {
		return session.Session{}, f.err
	}
	return session.Session{ID: id, AgentName: f.agentName}, nil
}
