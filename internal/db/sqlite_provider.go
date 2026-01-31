package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
)

// SQLiteProvider implements the Provider interface for SQLite databases.
type SQLiteProvider struct {
	dataDir string
}

// NewSQLiteProvider creates a new SQLite provider instance.
func NewSQLiteProvider(dataDir string) *SQLiteProvider {
	return &SQLiteProvider{
		dataDir: dataDir,
	}
}

// Connect establishes a connection to the SQLite database.
func (p *SQLiteProvider) Connect() (*sql.DB, error) {
	if p.dataDir == "" {
		return nil, fmt.Errorf("data directory is not set")
	}

	if err := os.MkdirAll(p.dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	dbPath := filepath.Join(p.dataDir, "opencode.db")

	// Open the SQLite database
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Verify connection
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Set pragmas for better performance
	pragmas := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA page_size = 4096;",
		"PRAGMA cache_size = -8000;",
		"PRAGMA synchronous = NORMAL;",
	}

	for _, pragma := range pragmas {
		if _, err = db.Exec(pragma); err != nil {
			logging.Error("Failed to set pragma", pragma, err)
		} else {
			logging.Debug("Set pragma", "pragma", pragma)
		}
	}

	return db, nil
}

// Type returns the provider type.
func (p *SQLiteProvider) Type() config.ProviderType {
	return config.ProviderSQLite
}

// Dialect returns the SQL dialect name for migrations.
func (p *SQLiteProvider) Dialect() string {
	return "sqlite3"
}
