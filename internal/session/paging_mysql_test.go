//go:build mysql_integration

package session

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"

	"github.com/opencode-ai/opencode/internal/db"
)

func TestSessionPagingMySQL(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN required")
	}
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	goose.SetBaseFS(db.FS)
	if err = goose.SetDialect("mysql"); err != nil {
		t.Fatal(err)
	}
	if err = goose.Up(conn, "migrations/mysql"); err != nil {
		t.Fatal(err)
	}
	q := db.NewMySQLQuerier(conn)
	runPagingSuite(t, conn, q)
	runCleanupPagingSuite(t, conn, q)
}
