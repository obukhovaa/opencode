package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/pubsub"

	"github.com/pressly/goose/v3"
)

// newTestService builds a session Service backed by a real, migrated SQLite
// database in a temp dir. This exercises the actual SQL — importantly the
// guarded UPDATE in SetGeneratedTitle — rather than a stubbed Querier.
func newTestService(t *testing.T) Service {
	t.Helper()
	provider := db.NewSQLiteProvider(t.TempDir())
	sqlDB, err := provider.Connect()
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
	// db.New yields the SQLite-backed Queries directly; db.NewQuerier would
	// need a global config to pick a provider (nil in tests).
	return NewService(db.New(sqlDB), "test-project")
}

func TestRename(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	created, err := svc.Create(ctx, "New Session")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.UserSetTitle {
		t.Fatal("new session should not be user-titled")
	}

	renamed, err := svc.Rename(ctx, created.ID, "  My Title  ")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.Title != "My Title" {
		t.Errorf("title = %q, want %q (trimmed)", renamed.Title, "My Title")
	}
	if !renamed.UserSetTitle {
		t.Error("rename should mark the session user-titled")
	}

	// Persisted.
	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "My Title" || !got.UserSetTitle {
		t.Errorf("persisted = {%q, userSet=%v}, want {My Title, true}", got.Title, got.UserSetTitle)
	}
}

func TestRenameRejectsEmpty(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, _ := svc.Create(ctx, "Original")

	for _, title := range []string{"", "   ", "\t\n"} {
		if _, err := svc.Rename(ctx, created.ID, title); !errors.Is(err, ErrEmptyTitle) {
			t.Errorf("Rename(%q) error = %v, want ErrEmptyTitle", title, err)
		}
	}

	got, _ := svc.Get(ctx, created.ID)
	if got.Title != "Original" || got.UserSetTitle {
		t.Errorf("after rejected renames = {%q, userSet=%v}, want {Original, false}", got.Title, got.UserSetTitle)
	}
}

func TestSetGeneratedTitleWritesWhenNotUserTitled(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, _ := svc.Create(ctx, "New Session")

	got, err := svc.SetGeneratedTitle(ctx, created.ID, "Auto Generated")
	if err != nil {
		t.Fatalf("SetGeneratedTitle: %v", err)
	}
	if got.Title != "Auto Generated" {
		t.Errorf("title = %q, want %q", got.Title, "Auto Generated")
	}
	if got.UserSetTitle {
		t.Error("generated title must not mark the session user-titled")
	}
}

// TestSetGeneratedTitleNoOpWhenUserTitled is the core anti-clobber guarantee:
// once a user renames, an automatic title write must not overwrite it. This is
// enforced by the SQL predicate (WHERE user_set_title = FALSE), so it holds
// even if the generator read the session before the rename committed.
func TestSetGeneratedTitleNoOpWhenUserTitled(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, _ := svc.Create(ctx, "New Session")

	if _, err := svc.Rename(ctx, created.ID, "User Title"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	got, err := svc.SetGeneratedTitle(ctx, created.ID, "Auto Generated")
	if err != nil {
		t.Fatalf("SetGeneratedTitle: %v", err)
	}
	if got.Title != "User Title" {
		t.Errorf("title = %q, want %q (user title must survive)", got.Title, "User Title")
	}
	if !got.UserSetTitle {
		t.Error("user-titled flag must remain set")
	}
}

// TestSavePreservesUserSetTitle verifies the generic Save path (token/cost
// updates) does not clear the user-titled mark.
func TestSavePreservesUserSetTitle(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created, _ := svc.Create(ctx, "New Session")
	renamed, _ := svc.Rename(ctx, created.ID, "User Title")

	renamed.Cost = 1.23
	renamed.PromptTokens = 100
	if _, err := svc.Save(ctx, renamed); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, _ := svc.Get(ctx, created.ID)
	if got.Title != "User Title" || !got.UserSetTitle {
		t.Errorf("after Save = {%q, userSet=%v}, want {User Title, true}", got.Title, got.UserSetTitle)
	}
}

func TestRenamePublishesUpdatedEvent(t *testing.T) {
	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	created, _ := svc.Create(ctx, "New Session")

	sub := svc.Subscribe(ctx)
	drain(sub) // discard the CreatedEvent already buffered, if any

	if _, err := svc.Rename(ctx, created.ID, "Renamed"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	select {
	case ev := <-sub:
		if ev.Type != pubsub.UpdatedEvent || ev.Payload.Title != "Renamed" {
			t.Errorf("event = {%v, %q}, want {updated, Renamed}", ev.Type, ev.Payload.Title)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for UpdatedEvent after rename")
	}
}

func drain(ch <-chan pubsub.Event[Session]) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func TestSetAgentNameIf(t *testing.T) {
	tests := []struct {
		name       string
		stored     string
		expected   string
		wantOK     bool
		wantStored string
	}{
		{name: "first stamp from empty", expected: "", wantOK: true, wantStored: "hivemind"},
		{name: "matching previous value", stored: "coder", expected: "coder", wantOK: true, wantStored: "hivemind"},
		{name: "stale expectation leaves the stored value", stored: "coder", expected: "", wantOK: false, wantStored: "coder"},
		{name: "wrong previous value", stored: "coder", expected: "other", wantOK: false, wantStored: "coder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			created, err := svc.Create(ctx, "New Session")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if tt.stored != "" {
				if err := svc.SetAgentName(ctx, created.ID, tt.stored); err != nil {
					t.Fatalf("SetAgentName: %v", err)
				}
			}
			events := svc.Subscribe(ctx)
			drain(events)

			ok, err := svc.SetAgentNameIf(ctx, created.ID, tt.expected, "hivemind")
			if err != nil {
				t.Fatalf("SetAgentNameIf: %v", err)
			}
			if ok != tt.wantOK {
				t.Errorf("applied = %v, want %v", ok, tt.wantOK)
			}
			got, err := svc.Get(ctx, created.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.AgentName != tt.wantStored {
				t.Errorf("AgentName = %q, want %q", got.AgentName, tt.wantStored)
			}
			select {
			case <-events:
				if !tt.wantOK {
					t.Error("update event published for a write that did not apply")
				}
			case <-time.After(200 * time.Millisecond):
				if tt.wantOK {
					t.Error("no update event for an applied write")
				}
			}
		})
	}
}

func TestSetAgentNameRoundTrip(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	created, err := svc.Create(ctx, "New Session")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.AgentName != "" {
		t.Fatalf("new session AgentName = %q, want empty", created.AgentName)
	}

	events := svc.Subscribe(ctx)
	if err := svc.SetAgentName(ctx, created.ID, "hivemind"); err != nil {
		t.Fatalf("SetAgentName: %v", err)
	}
	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AgentName != "hivemind" {
		t.Errorf("AgentName = %q, want hivemind", got.AgentName)
	}
	select {
	case ev := <-events:
		if ev.Type != pubsub.UpdatedEvent || ev.Payload.AgentName != "hivemind" {
			t.Errorf("event = %v / %q, want an update carrying hivemind", ev.Type, ev.Payload.AgentName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no update event for SetAgentName")
	}

	// A token Save must not clobber the stored agent.
	got.PromptTokens = 7
	saved, err := svc.Save(ctx, got)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.AgentName != "hivemind" {
		t.Errorf("AgentName after Save = %q, want hivemind", saved.AgentName)
	}

	// List and ListChildren map the column too.
	list, err := svc.List(ctx)
	if err != nil || len(list) != 1 || list[0].AgentName != "hivemind" {
		t.Errorf("List = %+v, %v; want one session with AgentName hivemind", list, err)
	}
}
