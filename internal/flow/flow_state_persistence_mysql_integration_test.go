//go:build mysql_integration

package flow

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/opencode-ai/opencode/internal/db"
)

// TestFlowStatePersistenceMySQL runs the flow-state persistence suite against
// the MySQL migrations, where fk_flow_states_session is always enforced. Needs
// TEST_MYSQL_DSN (`make test-mysql`, or the test-mysql CI workflow).
func TestFlowStatePersistenceMySQL(t *testing.T) {
	runFlowStatePersistenceSuite(t, func(t *testing.T) db.Querier {
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
		resetMySQLSchema(t, conn)
		migrate(t, conn, "mysql", "migrations/mysql")
		return db.NewMySQLQuerier(conn)
	})
}

// resetMySQLSchema drops every table in the test database so each subtest
// migrates from scratch. The tables are listed from information_schema rather
// than hard-coded, so a new migration cannot leave one behind.
func resetMySQLSchema(t *testing.T, conn *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// FOREIGN_KEY_CHECKS is session-scoped: the disable, the drops and the
	// re-enable must share one connection.
	pinned, err := conn.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire pinned conn: %v", err)
	}
	defer pinned.Close()

	rows, err := pinned.QueryContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("list tables: %v", err)
	}

	if _, err := pinned.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("disable fk checks: %v", err)
	}
	for _, tbl := range tables {
		if _, err := pinned.ExecContext(ctx, "DROP TABLE IF EXISTS `"+tbl+"`"); err != nil {
			t.Fatalf("drop %s: %v", tbl, err)
		}
	}
	if _, err := pinned.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
		t.Fatalf("re-enable fk checks: %v", err)
	}
}
