package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/pressly/goose/v3"

	opencodedb "github.com/opencode-ai/opencode/internal/db"
)

// goose.SetBaseFS and goose.SetDialect both write to package-global state
// in pressly/goose. Parallel tests calling them concurrently trigger
// -race. Serialize the setup via gooseInit so the globals are written
// exactly once, then reuse for every test.
var (
	gooseInitOnce sync.Once
	gooseInitErr  error
)

func ensureGooseGlobals() error {
	gooseInitOnce.Do(func() {
		goose.SetBaseFS(opencodedb.FS)
		gooseInitErr = goose.SetDialect("sqlite3")
	})
	return gooseInitErr
}

// gooseMu serializes goose.Up calls. goose holds a per-DB lock internally
// (via the goose_db_version table) but with `cache=shared` SQLite memory
// DSNs, multiple tests share the same underlying database file in the
// in-memory FS — without serialization, two t.Parallel() tests would
// hammer the same goose_db_version table concurrently. We use a unique
// dsn per t.Name() so each test has its own DB; but the global goose
// dialect state still must not race.
var gooseMu sync.Mutex

// newTestStore spins up an in-memory SQLite database, runs every opencode
// migration against it (so the bridge_sessions FK to sessions resolves), and
// returns a Store that the test can drive directly. Each call creates a
// distinct database so tests can parallelize.
func newTestStore(t *testing.T) (Store, *sql.DB) {
	t.Helper()

	// "file::memory:?cache=shared" with a per-test name keeps the database
	// in memory but allows multiple connections to see the same schema —
	// the *sql.DB layer pools connections by default and a plain
	// ":memory:" would give each connection its own database.
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		t.Fatalf("pragma: %v", err)
	}

	if err := ensureGooseGlobals(); err != nil {
		t.Fatalf("ensureGooseGlobals: %v", err)
	}
	gooseMu.Lock()
	err = goose.Up(conn, "migrations/sqlite")
	gooseMu.Unlock()
	if err != nil {
		t.Fatalf("goose up: %v", err)
	}

	// Seed a sessions row so the FK accepts a SessionID. The bridge
	// scenarios that exercise FK-on-delete-set-null insert their own
	// session rows; this is just the baseline.
	if _, err := conn.Exec(`
		INSERT INTO sessions (id, project_id, title, message_count,
		                      prompt_tokens, completion_tokens, cost,
		                      updated_at, created_at)
		VALUES ('S1', 'proj', 't', 0, 0, 0, 0, strftime('%s','now'), strftime('%s','now'))
	`); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	return &sqliteStore{queries: opencodedb.New(conn), db: conn}, conn
}

func TestUpsertAndGetBinding(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	ctx := context.Background()

	in := Binding{
		ProjectID:     "proj",
		Channel:       "slack",
		IdentityID:    "default",
		PeerID:        "D012345",
		SessionID:     "S1",
		MentionHandle: "<@U01ABC>",
	}
	out, err := s.UpsertBinding(ctx, in)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if out.SessionID != "S1" || out.MentionHandle != "<@U01ABC>" {
		t.Errorf("Upsert returned %+v", out)
	}

	got, err := s.GetBinding(ctx, "proj", "slack", "default", "D012345")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SessionID != "S1" {
		t.Errorf("Get returned SessionID=%q, want S1", got.SessionID)
	}
	if got.MentionConsumedAt != 0 {
		t.Errorf("Get returned MentionConsumedAt=%d, want 0 (fresh binding)", got.MentionConsumedAt)
	}
}

func TestGetBindingNotFound(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetBinding(ctx, "proj", "slack", "default", "GHOST")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestManyToOneBindingPerSession(t *testing.T) {
	t.Parallel()
	s, db := newTestStore(t)
	ctx := context.Background()

	// Seed two sessions; bind 3 peers across them.
	if _, err := db.Exec(`
		INSERT INTO sessions (id, project_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at)
		VALUES ('S2', 'proj', 't2', 0, 0, 0, 0, strftime('%s','now'), strftime('%s','now'))
	`); err != nil {
		t.Fatalf("seed S2: %v", err)
	}

	for _, b := range []Binding{
		{ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1"},
		{ProjectID: "proj", Channel: "telegram", IdentityID: "default", PeerID: "111", SessionID: "S1"},
		{ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D2", SessionID: "S2"},
	} {
		if _, err := s.UpsertBinding(ctx, b); err != nil {
			t.Fatalf("Upsert %s: %v", b.PeerID, err)
		}
	}

	bindings, err := s.ListBindingsBySession(ctx, "proj", "S1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(bindings) != 2 {
		t.Errorf("len = %d, want 2 (many-to-one fan-out source)", len(bindings))
	}
}

func TestFKOnDeleteSetNull(t *testing.T) {
	t.Parallel()
	s, db := newTestStore(t)
	ctx := context.Background()

	if _, err := s.UpsertBinding(ctx, Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default",
		PeerID: "D1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Delete the parent session — FK ON DELETE SET NULL should fire.
	if _, err := db.Exec(`DELETE FROM sessions WHERE id = 'S1'`); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	got, err := s.GetBinding(ctx, "proj", "slack", "default", "D1")
	if err != nil {
		t.Fatalf("Get after parent delete: %v", err)
	}
	if got.SessionID != "" {
		t.Errorf("SessionID = %q, want empty (FK should null it out)", got.SessionID)
	}
}

func TestUpdateBindingPeerIDMutation(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := s.UpsertBinding(ctx, Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default",
		PeerID: "C0DEF456", SessionID: "S1",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Simulate Slack channel→thread mutation: first outbound captures ts,
	// the binding key mutates to channel|ts.
	if err := s.UpdateBindingPeerID(ctx, "proj", "slack", "default", "C0DEF456", "C0DEF456|1700000123.000200"); err != nil {
		t.Fatalf("UpdateBindingPeerID: %v", err)
	}

	// Old key should be gone.
	if _, err := s.GetBinding(ctx, "proj", "slack", "default", "C0DEF456"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old key still present: err=%v", err)
	}
	// New key should resolve.
	got, err := s.GetBinding(ctx, "proj", "slack", "default", "C0DEF456|1700000123.000200")
	if err != nil {
		t.Fatalf("Get new key: %v", err)
	}
	if got.SessionID != "S1" {
		t.Errorf("SessionID = %q, want S1", got.SessionID)
	}
}

func TestMentionConsumedTimestamp(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := s.UpsertBinding(ctx, Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default",
		PeerID: "D1", SessionID: "S1", MentionHandle: "<@U01>",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.MarkMentionConsumed(ctx, "proj", "slack", "default", "D1"); err != nil {
		t.Fatalf("MarkMentionConsumed: %v", err)
	}
	got, err := s.GetBinding(ctx, "proj", "slack", "default", "D1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MentionConsumedAt == 0 {
		t.Errorf("MentionConsumedAt = 0 after Mark — expected non-zero")
	}

	// Re-bind clears mention_consumed_at per the spec — repeated upsert
	// for the same key resets so the new binding gets the prefix again.
	if _, err := s.UpsertBinding(ctx, Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default",
		PeerID: "D1", SessionID: "S1", MentionHandle: "<@U01>",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err = s.GetBinding(ctx, "proj", "slack", "default", "D1")
	if err != nil {
		t.Fatalf("Get after re-upsert: %v", err)
	}
	if got.MentionConsumedAt != 0 {
		t.Errorf("MentionConsumedAt = %d after re-bind, want 0", got.MentionConsumedAt)
	}
}

func TestPartialAndFullDelete(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	ctx := context.Background()

	for _, peer := range []string{"D1", "D2", "D3"} {
		if _, err := s.UpsertBinding(ctx, Binding{
			ProjectID: "proj", Channel: "slack", IdentityID: "default",
			PeerID: peer, SessionID: "S1",
		}); err != nil {
			t.Fatalf("Upsert %s: %v", peer, err)
		}
	}

	// Partial: drop just D2.
	if err := s.DeleteBindingByPeer(ctx, "proj", "slack", "default", "D2"); err != nil {
		t.Fatalf("DeleteByPeer: %v", err)
	}
	got, err := s.ListBindingsBySession(ctx, "proj", "S1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("after partial: len=%d, want 2", len(got))
	}

	// Full: drop everything for S1.
	if err := s.DeleteBindingsBySession(ctx, "proj", "S1"); err != nil {
		t.Fatalf("DeleteBySession: %v", err)
	}
	got, err = s.ListBindingsBySession(ctx, "proj", "S1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("after full: len=%d, want 0", len(got))
	}
}

func TestAllowlistRoundTrip(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.AddAllowlistEntry(ctx, "proj", "telegram", "default", "12345"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Idempotent re-add must not error.
	if err := s.AddAllowlistEntry(ctx, "proj", "telegram", "default", "12345"); err != nil {
		t.Fatalf("Add (idempotent): %v", err)
	}

	ok, err := s.IsAllowlisted(ctx, "proj", "telegram", "default", "12345")
	if err != nil {
		t.Fatalf("IsAllowlisted: %v", err)
	}
	if !ok {
		t.Errorf("IsAllowlisted = false, want true after Add")
	}

	entries, err := s.ListAllowlist(ctx, "proj", "telegram", "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("List len = %d, want 1", len(entries))
	}

	if err := s.RemoveAllowlistEntry(ctx, "proj", "telegram", "default", "12345"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	ok, err = s.IsAllowlisted(ctx, "proj", "telegram", "default", "12345")
	if err != nil {
		t.Fatalf("IsAllowlisted after remove: %v", err)
	}
	if ok {
		t.Errorf("IsAllowlisted = true after Remove")
	}
}

func TestProjectIDIsolation(t *testing.T) {
	t.Parallel()
	s, db := newTestStore(t)
	ctx := context.Background()

	if _, err := db.Exec(`
		INSERT INTO sessions (id, project_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at)
		VALUES ('Sother', 'projOther', 't', 0, 0, 0, 0, strftime('%s','now'), strftime('%s','now'))
	`); err != nil {
		t.Fatalf("seed other session: %v", err)
	}

	// Two workspaces with the SAME (channel, identity, peer) but distinct
	// project_id must not collide on the PK.
	for _, b := range []Binding{
		{ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1"},
		{ProjectID: "projOther", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "Sother"},
	} {
		if _, err := s.UpsertBinding(ctx, b); err != nil {
			t.Fatalf("Upsert %s: %v", b.ProjectID, err)
		}
	}

	a, err := s.GetBinding(ctx, "proj", "slack", "default", "D1")
	if err != nil {
		t.Fatalf("Get proj: %v", err)
	}
	bOther, err := s.GetBinding(ctx, "projOther", "slack", "default", "D1")
	if err != nil {
		t.Fatalf("Get projOther: %v", err)
	}
	if a.SessionID == bOther.SessionID {
		t.Errorf("project_id isolation broken: both rows share SessionID %q", a.SessionID)
	}
}

func TestCountBindingsByIdentity(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	ctx := context.Background()

	for _, peer := range []string{"D1", "D2", "D3"} {
		if _, err := s.UpsertBinding(ctx, Binding{
			ProjectID: "proj", Channel: "slack", IdentityID: "default",
			PeerID: peer, SessionID: "S1",
		}); err != nil {
			t.Fatalf("Upsert %s: %v", peer, err)
		}
	}
	n, err := s.CountBindingsByIdentity(ctx, "proj", "slack", "default")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Errorf("Count = %d, want 3", n)
	}
}
