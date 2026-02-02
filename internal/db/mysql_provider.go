package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
)

// MySQLProvider implements the Provider interface for MySQL databases.
type MySQLProvider struct {
	config config.MySQLConfig
}

// NewMySQLProvider creates a new MySQL provider instance.
func NewMySQLProvider(cfg config.MySQLConfig) *MySQLProvider {
	return &MySQLProvider{
		config: cfg,
	}
}

// Connect establishes a connection to the MySQL database.
func (p *MySQLProvider) Connect() (*sql.DB, error) {
	dsn := p.buildDSN()

	// Open the MySQL database
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open MySQL database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(p.config.MaxConnections)
	db.SetMaxIdleConns(p.config.MaxIdleConnections)
	db.SetConnMaxLifetime(5 * time.Minute) // Recycle connections every 5 minutes to prevent stale connections

	// Verify connection with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.config.ConnectionTimeout)*time.Second)
	defer cancel()

	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to MySQL database: %w", err)
	}

	logging.Info("Connected to MySQL database",
		"host", p.config.Host,
		"database", p.config.Database,
		"max_connections", p.config.MaxConnections)

	return db, nil
}

// buildDSN builds the MySQL Data Source Name (DSN) connection string.
func (p *MySQLProvider) buildDSN() string {
	// If DSN is provided directly, use it
	if p.config.DSN != "" {
		return p.config.DSN
	}

	// Build DSN from individual fields
	// Format: username:password@tcp(host:port)/database?parseTime=true
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true",
		p.config.Username,
		p.config.Password,
		p.config.Host,
		p.config.Port,
		p.config.Database,
	)
}

// Type returns the provider type.
func (p *MySQLProvider) Type() config.ProviderType {
	return config.ProviderMySQL
}

// Dialect returns the SQL dialect name for migrations.
func (p *MySQLProvider) Dialect() string {
	return "mysql"
}
