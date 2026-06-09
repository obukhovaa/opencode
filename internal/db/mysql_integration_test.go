//go:build mysql_integration

// Package db's mysql_integration tests run only when the `mysql_integration`
// build tag is set (via `make test-mysql`). They require a running MySQL 8
// instance reachable via the TEST_MYSQL_DSN environment variable —
// docker-compose.test-mysql.yml is the canonical way to provision one.
//
// The test verifies the three things Success Criterion 12.2 demands of the
// MySQL provider: (1) we can connect, (2) goose applies every migration
// cleanly, and (3) a basic insert/select round-trip works through the
// sqlc-generated MySQL queries.
package db_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"

	"github.com/opencode-ai/opencode/internal/db"
	mysqldb "github.com/opencode-ai/opencode/internal/db/mysql"
)

func openTestMySQL(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set; run via `make test-mysql`")
	}

	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open mysql: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Acquire a single pinned connection from the pool — SET
	// FOREIGN_KEY_CHECKS = 0 is a SESSION variable, so the disable +
	// drops + re-enable MUST share one connection. Previously each
	// ExecContext checked out an arbitrary pool member, leaving FK
	// checks at their default on some drops and silently leaving FK
	// constraints around (which cascaded into the next test's
	// migration as "Failed to open the referenced table" /
	// "Duplicate key").
	pinned, err := conn.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire pinned conn: %v", err)
	}
	defer pinned.Close()

	// List every table we know the schema produces. Anything not on the
	// list will fail to drop and surface as a bug.
	tables := []string{
		"bridge_allowlist",
		"bridge_sessions",
		"cron_jobs",
		"session_recaps",
		"flow_states",
		"messages",
		"files",
		"sessions",
		"goose_db_version",
	}

	if _, err := pinned.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("disable fk checks: %v", err)
	}
	for _, tbl := range tables {
		if _, err := pinned.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
			t.Fatalf("drop %s: %v", tbl, err)
		}
	}
	if _, err := pinned.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
		t.Fatalf("re-enable fk checks: %v", err)
	}

	goose.SetBaseFS(db.FS)
	if err := goose.SetDialect("mysql"); err != nil {
		t.Fatalf("set dialect: %v", err)
	}
	if err := goose.UpContext(ctx, conn, "migrations/mysql"); err != nil {
		t.Fatalf("goose up: %v", err)
	}
	return conn
}

func TestMySQLProviderConnectAndMigrate(t *testing.T) {
	conn := openTestMySQL(t)

	// A trivial sanity-check round-trip — the migrations above just ran,
	// so the bridge_sessions and bridge_allowlist tables MUST exist.
	for _, tbl := range []string{"sessions", "messages", "bridge_sessions", "bridge_allowlist"} {
		var n int
		if err := conn.QueryRow("SELECT COUNT(*) FROM " + tbl).Scan(&n); err != nil {
			t.Errorf("count %s: %v", tbl, err)
		}
	}
}

func TestMySQLSessionRoundTrip(t *testing.T) {
	conn := openTestMySQL(t)
	q := mysqldb.New(conn)
	ctx := context.Background()

	_, err := q.CreateSession(ctx, mysqldb.CreateSessionParams{
		ID:               "S-roundtrip",
		ProjectID:        sql.NullString{String: "proj", Valid: true},
		ParentSessionID:  sql.NullString{},
		RootSessionID:    sql.NullString{},
		Title:            "round trip",
		MessageCount:     0,
		PromptTokens:     0,
		CompletionTokens: 0,
		Cost:             0,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := q.GetSessionByID(ctx, "S-roundtrip")
	if err != nil {
		t.Fatalf("GetSessionByID: %v", err)
	}
	if got.Title != "round trip" {
		t.Errorf("Title = %q, want %q", got.Title, "round trip")
	}
}
