//go:build mysql_integration

package flowrun

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"

	"github.com/opencode-ai/opencode/internal/db"
)

// Uses the shared repository test DSN, never a production connection. Each
// scenario gets a separate project and conversation; no table/data deletion.
func TestStoreMySQL(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN required")
	}
	runStoreSuite(t, func(t *testing.T) *Store {
		conn, err := sql.Open("mysql", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		migrateStore(t, conn, "mysql")
		return New(conn, mysqlQueries{db.NewMySQLQuerier(conn)}, "project-"+uuid.NewString(), "mysql")
	})
}
