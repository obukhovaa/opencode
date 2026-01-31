package db

import (
	"database/sql"
	"fmt"

	"github.com/opencode-ai/opencode/internal/config"
)

// Provider defines the interface for database connection providers.
type Provider interface {
	// Connect establishes a connection to the database and returns a *sql.DB instance.
	// It should handle provider-specific connection setup, including:
	// - Connection string/DSN building
	// - Connection pooling configuration
	// - Provider-specific optimizations (pragmas, settings, etc.)
	// - Connection verification (ping)
	Connect() (*sql.DB, error)

	// Type returns the provider type (sqlite or mysql).
	Type() config.ProviderType

	// Dialect returns the SQL dialect name for migration purposes.
	Dialect() string
}

// NewProvider creates a new database provider based on the configuration.
func NewProvider(cfg *config.Config) (Provider, error) {
	providerType := cfg.SessionProvider.Type
	if providerType == "" {
		providerType = config.ProviderSQLite
	}

	switch providerType {
	case config.ProviderSQLite:
		return NewSQLiteProvider(cfg.Data.Directory), nil
	case config.ProviderMySQL:
		return NewMySQLProvider(cfg.SessionProvider.MySQL), nil
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}
}
