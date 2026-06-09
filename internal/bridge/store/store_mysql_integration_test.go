//go:build mysql_integration

package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"

	"github.com/opencode-ai/opencode/internal/config"
	opencodedb "github.com/opencode-ai/opencode/internal/db"
)

// newMySQLTestStore creates a fresh MySQL-backed Store. Tests under this
// build tag share a single TEST_MYSQL_DSN-pointed database; we drop all
// tables before each migration to keep tests independent.
func newMySQLTestStore(t *testing.T) (Store, *sql.DB) {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set; run via `make test-mysql`")
	}
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// SET FOREIGN_KEY_CHECKS is session-scoped; pin a single conn so
	// the disable + drops + re-enable share one session. Otherwise the
	// pool can serve a different connection for the DROPs, leaving FK
	// constraints in place and breaking the next migration run.
	pinned, err := conn.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire pinned conn: %v", err)
	}
	defer pinned.Close()

	if _, err := pinned.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("disable fk: %v", err)
	}
	for _, tbl := range []string{
		"bridge_allowlist",
		"bridge_sessions",
		"cron_jobs",
		"session_recaps",
		"flow_states",
		"messages",
		"files",
		"sessions",
		"goose_db_version",
	} {
		if _, err := pinned.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
			t.Fatalf("drop %s: %v", tbl, err)
		}
	}
	if _, err := pinned.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
		t.Fatalf("re-enable fk: %v", err)
	}

	goose.SetBaseFS(opencodedb.FS)
	if err := goose.SetDialect("mysql"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}
	if err := goose.UpContext(ctx, conn, "migrations/mysql"); err != nil {
		t.Fatalf("goose up: %v", err)
	}

	// Seed one session so FK-bearing tests don't have to.
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO sessions (id, project_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at)
		VALUES ('S1', 'proj', 't', 0, 0, 0, 0, UNIX_TIMESTAMP(), UNIX_TIMESTAMP())
	`); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	return New(conn, config.ProviderMySQL), conn
}

func TestMySQLUpsertAndGetBinding(t *testing.T) {
	s, _ := newMySQLTestStore(t)
	ctx := context.Background()

	out, err := s.UpsertBinding(ctx, Binding{
		ProjectID:     "proj",
		Channel:       "slack",
		IdentityID:    "default",
		PeerID:        "D012345",
		SessionID:     "S1",
		MentionHandle: "<@U01>",
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if out.MentionHandle != "<@U01>" {
		t.Errorf("MentionHandle = %q, want %q", out.MentionHandle, "<@U01>")
	}

	got, err := s.GetBinding(ctx, "proj", "slack", "default", "D012345")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SessionID != "S1" {
		t.Errorf("SessionID = %q, want S1", got.SessionID)
	}
}

func TestMySQLNotFoundReturnsErrNotFound(t *testing.T) {
	s, _ := newMySQLTestStore(t)
	_, err := s.GetBinding(context.Background(), "proj", "slack", "default", "GHOST")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestMySQLFKOnDeleteSetNull(t *testing.T) {
	s, db := newMySQLTestStore(t)
	ctx := context.Background()

	if _, err := s.UpsertBinding(ctx, Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default",
		PeerID: "D1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := db.Exec("DELETE FROM sessions WHERE id = 'S1'"); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	got, err := s.GetBinding(ctx, "proj", "slack", "default", "D1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SessionID != "" {
		t.Errorf("SessionID = %q, want empty (FK SET NULL on delete)", got.SessionID)
	}
}

func TestMySQLAllowlistRoundTrip(t *testing.T) {
	s, _ := newMySQLTestStore(t)
	ctx := context.Background()

	if err := s.AddAllowlistEntry(ctx, "proj", "telegram", "default", "12345"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Idempotent re-add (INSERT IGNORE).
	if err := s.AddAllowlistEntry(ctx, "proj", "telegram", "default", "12345"); err != nil {
		t.Fatalf("Add idempotent: %v", err)
	}

	ok, err := s.IsAllowlisted(ctx, "proj", "telegram", "default", "12345")
	if err != nil {
		t.Fatalf("IsAllowlisted: %v", err)
	}
	if !ok {
		t.Errorf("IsAllowlisted = false, want true")
	}
}

func TestMySQLMentionConsumedReResetsOnReBind(t *testing.T) {
	s, _ := newMySQLTestStore(t)
	ctx := context.Background()

	if _, err := s.UpsertBinding(ctx, Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default",
		PeerID: "D1", SessionID: "S1", MentionHandle: "<@U01>",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.MarkMentionConsumed(ctx, "proj", "slack", "default", "D1"); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	got, err := s.GetBinding(ctx, "proj", "slack", "default", "D1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MentionConsumedAt == 0 {
		t.Errorf("MentionConsumedAt should be set after Mark")
	}

	// Re-upsert resets mention_consumed_at to NULL.
	if _, err := s.UpsertBinding(ctx, Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default",
		PeerID: "D1", SessionID: "S1", MentionHandle: "<@U01>",
	}); err != nil {
		t.Fatalf("Upsert again: %v", err)
	}
	got, err = s.GetBinding(ctx, "proj", "slack", "default", "D1")
	if err != nil {
		t.Fatalf("Get after re-upsert: %v", err)
	}
	if got.MentionConsumedAt != 0 {
		t.Errorf("MentionConsumedAt = %d after re-bind, want 0", got.MentionConsumedAt)
	}
}
