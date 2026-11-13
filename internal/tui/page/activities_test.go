package page

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/pressly/goose/v3"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/slashcmd"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
)

func TestActivityTailBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.out")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 100000)+"end"), 0600); err != nil {
		t.Fatal(err)
	}
	got := readActivityTail(path)
	if len(got) > 32768 || !strings.HasSuffix(got, "end") {
		t.Fatalf("bad tail length=%d", len(got))
	}
}
func TestFlowGoalPreservesSlashText(t *testing.T) {
	p := &chatPage{commands: []dialog.Command{{CommandInfo: slashcmd.CommandInfo{ID: "flow"}}}}
	goal := "fix feature\n/review\n/path/to/file"
	got, err := p.expandSubmission("/flow " + goal)
	if err != nil || got.Action == nil || got.Action.ID != "flow" || got.ActionArgs != goal {
		t.Fatalf("expansion %+v %v", got, err)
	}
}

func TestActivityLifecycleIntegration(t *testing.T) {
	if config.Get() == nil {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", home)
		if _, err := config.Load(home, false); err != nil {
			t.Fatal(err)
		}
	}
	database, err := db.NewSQLiteProvider(t.TempDir()).Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	goose.SetBaseFS(db.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(database, "migrations/sqlite"); err != nil {
		t.Fatal(err)
	}
	q := db.NewQuerier(database)
	sessions := session.NewService(q, "activities")
	messages := message.NewService(q, database)
	ctx := context.Background()
	root, err := sessions.Create(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	previous := task.GlobalRegistry()
	if previous == nil {
		previous = task.NewRegistry(func() string { return "" })
	}
	registry := task.NewRegistry(func() string { return t.TempDir() })
	task.ResetGlobalRegistry()
	task.SetGlobalRegistry(registry)
	defer func() { task.ResetGlobalRegistry(); task.SetGlobalRegistry(previous) }()
	cancelled := false
	if err := registry.Register(&task.Task{ID: "mine", SessionID: root.ID, Kind: task.KindBash, Description: "build", Cancel: func() { cancelled = true }}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&task.Task{ID: "other", SessionID: "unrelated", Kind: task.KindBash, Description: "unrelated"}); err != nil {
		t.Fatal(err)
	}
	p := NewActivitiesPage(&app.App{Sessions: sessions, Messages: messages}, false).(*activitiesPage)
	_, cmd := p.Update(chat.SessionSelectedMsg(root))
	loaded := cmd()
	p.Update(loaded)
	if len(p.items) != 1 || p.items[0].id != "mine" {
		t.Fatalf("session isolation failed: %+v", p.items)
	}
	_, cmd = p.Update(tea.KeyPressMsg{Code: 'x'})
	if cmd == nil {
		t.Fatal("missing cancellation command")
	}
	cmd()
	if !cancelled {
		t.Fatal("cancellation did not reach registry hook")
	}
	if got, ok := registry.Get("mine"); !ok || got.State() != task.StateKilled {
		t.Fatal("task did not become killed")
	}
}

func TestActivityResultsIgnoreOldGeneration(t *testing.T) {
	p := &activitiesPage{sessionID: "current", generation: 3, items: []activity{{id: "visible"}}}
	_, cmd := p.Update(activitiesLoaded{sessionID: "current", generation: 2, items: []activity{{id: "stale"}}})
	if cmd != nil || p.items[0].id != "visible" {
		t.Fatal("stale load changed page or started polling")
	}
	_, cmd = p.Update(activityTick{generation: 2})
	if cmd != nil {
		t.Fatal("stale timer started polling")
	}
}
func TestActivityStripIgnoresOldGeneration(t *testing.T) {
	p := &chatPage{stripGeneration: 3, activityTasks: 2}
	cmd, handled := p.activityUpdate(stripCounts{generation: 2, tasks: 99})
	if !handled || cmd != nil || p.activityTasks != 2 {
		t.Fatal("stale strip response changed counts or restarted polling")
	}
}

func TestActivityStripFlowQueueTracksDeliveryAndIgnoresOtherSessions(t *testing.T) {
	p := &chatPage{app: &app.App{}, session: session.Session{ID: "root"}, stripGeneration: 3}
	p.activityUpdate(stripCounts{sessionID: "root", generation: 3, flowQueued: 2})
	if p.activityFlowQueued != 2 {
		t.Fatal("pending flow messages not counted")
	}
	p.activityUpdate(stripCounts{sessionID: "other", generation: 3, flowQueued: 99})
	if p.activityFlowQueued != 2 {
		t.Fatal("other session changed the count")
	}
	p.activityUpdate(stripCounts{sessionID: "root", generation: 3})
	if p.activityFlowQueued != 0 {
		t.Fatal("delivered messages still counted")
	}
}

func TestActivityAnimationDoesNotPollStorageAndStopsWhenIdle(t *testing.T) {
	p := &chatPage{app: &app.App{}, activityTasks: 1, stripGeneration: 7, activityAnimationGeneration: 2, activityAnimating: true}
	cmd, handled := p.activityUpdate(activityPulse{generation: 2})
	if !handled || cmd == nil || p.activityFrame != 1 || p.stripGeneration != 7 {
		t.Fatal("animation failed or triggered a storage refresh")
	}
	cmd, _ = p.activityUpdate(activityPulse{generation: 1})
	if cmd != nil || p.activityFrame != 1 {
		t.Fatal("stale pulse advanced animation")
	}
	p.activityTasks = 0
	cmd, _ = p.activityUpdate(activityPulse{generation: 2})
	if cmd != nil || p.activityAnimating || p.activityFrame != 0 {
		t.Fatal("idle animation kept running")
	}
	p.activityAgents = 1
	p.activityUpdate(stripCounts{generation: 7, agents: 1})
	if !p.activityAnimating || p.activityAnimationGeneration != 3 {
		t.Fatal("subagent activity did not restart animation")
	}
}
