package db

import (
	"database/sql"
	"fmt"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"

	"github.com/pressly/goose/v3"
)

func Connect() (*sql.DB, error) {
	cfg := config.Get()

	// Create provider based on configuration
	provider, err := NewProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create database provider: %w", err)
	}

	// Connect to database
	db, err := provider.Connect()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Set up migrations
	goose.SetBaseFS(FS)

	if err := goose.SetDialect(provider.Dialect()); err != nil {
		logging.Error("Failed to set dialect", "error", err)
		db.Close()
		return nil, fmt.Errorf("failed to set dialect: %w", err)
	}

	// Run migrations
	if err := goose.Up(db, "migrations"); err != nil {
		logging.Error("Failed to apply migrations", "error", err)
		db.Close()
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}

	// Backfill project_id for existing sessions (SQLite only)
	// MySQL support is added AFTER project_id introduction, so MySQL should never have sessions without project_id
	if provider.Type() == config.ProviderSQLite {
		if err := backfillProjectID(db, cfg); err != nil {
			logging.Warn("Failed to backfill project_id", "error", err)
			// Don't fail the connection, just log the warning
		}
	}

	return db, nil
}

// backfillProjectID populates project_id for sessions that don't have one.
func backfillProjectID(db *sql.DB, cfg *config.Config) error {
	// Check if there are any sessions without project_id
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM sessions WHERE project_id IS NULL OR project_id = ''").Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to count sessions without project_id: %w", err)
	}

	if count == 0 {
		logging.Debug("No sessions need project_id backfill")
		return nil
	}

	logging.Info("Backfilling project_id for existing sessions", "count", count)

	// Determine project ID based on data directory location
	// For existing sessions, we use the data directory as the working directory
	// since we don't know which project they were created in
	projectID := GetProjectID(cfg.Data.Directory)

	// Update all sessions without project_id
	result, err := db.Exec("UPDATE sessions SET project_id = ? WHERE project_id IS NULL OR project_id = ''", projectID)
	if err != nil {
		return fmt.Errorf("failed to update sessions with project_id: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	logging.Info("Successfully backfilled project_id", "rows_affected", rowsAffected, "project_id", projectID)
	return nil
}
