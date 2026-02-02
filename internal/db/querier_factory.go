package db

import (
	"database/sql"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
)

// NewQuerier creates a new Querier based on the configured provider type
func NewQuerier(db *sql.DB) Querier {
	cfg := config.Get()

	provider, err := NewProvider(cfg)
	if err != nil {
		// Fallback to SQLite if provider creation fails
		logging.Error("Failed to create database provider, falling back to SQLite", "error", err)
		return New(db)
	}

	if provider.Type() == config.ProviderMySQL {
		return NewMySQLQuerier(db)
	}

	return New(db)
}
