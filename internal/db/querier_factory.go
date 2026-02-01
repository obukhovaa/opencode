package db

import (
	"database/sql"

	"github.com/opencode-ai/opencode/internal/config"
)

// NewQuerier creates a new Querier based on the configured provider type
func NewQuerier(db *sql.DB) Querier {
	cfg := config.Get()

	provider, err := NewProvider(cfg)
	if err != nil {
		// Fallback to SQLite if provider creation fails
		return New(db)
	}

	if provider.Type() == config.ProviderMySQL {
		return NewMySQLQuerier(db)
	}

	return New(db)
}
