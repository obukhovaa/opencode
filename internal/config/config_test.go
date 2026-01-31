package config

import (
	"testing"
)

func TestValidateSessionProvider(t *testing.T) {
	tests := []struct {
		name        string
		config      SessionProviderConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "Valid SQLite configuration",
			config: SessionProviderConfig{
				Type: ProviderSQLite,
			},
			expectError: false,
		},
		{
			name: "Valid MySQL configuration with DSN",
			config: SessionProviderConfig{
				Type: ProviderMySQL,
				MySQL: MySQLConfig{
					DSN: "user:pass@tcp(localhost:3306)/dbname",
				},
			},
			expectError: false,
		},
		{
			name: "Valid MySQL configuration with individual fields",
			config: SessionProviderConfig{
				Type: ProviderMySQL,
				MySQL: MySQLConfig{
					Host:     "localhost",
					Port:     3306,
					Database: "opencode",
					Username: "user",
					Password: "pass",
				},
			},
			expectError: false,
		},
		{
			name: "MySQL without DSN or host",
			config: SessionProviderConfig{
				Type: ProviderMySQL,
				MySQL: MySQLConfig{
					Database: "opencode",
					Username: "user",
					Password: "pass",
				},
			},
			expectError: true,
			errorMsg:    "MySQL host is required",
		},
		{
			name: "MySQL without database",
			config: SessionProviderConfig{
				Type: ProviderMySQL,
				MySQL: MySQLConfig{
					Host:     "localhost",
					Username: "user",
					Password: "pass",
				},
			},
			expectError: true,
			errorMsg:    "MySQL database is required",
		},
		{
			name: "MySQL without username",
			config: SessionProviderConfig{
				Type: ProviderMySQL,
				MySQL: MySQLConfig{
					Host:     "localhost",
					Database: "opencode",
					Password: "pass",
				},
			},
			expectError: true,
			errorMsg:    "MySQL username is required",
		},
		{
			name: "MySQL without password",
			config: SessionProviderConfig{
				Type: ProviderMySQL,
				MySQL: MySQLConfig{
					Host:     "localhost",
					Database: "opencode",
					Username: "user",
				},
			},
			expectError: true,
			errorMsg:    "MySQL password is required",
		},
		{
			name: "Invalid provider type",
			config: SessionProviderConfig{
				Type: "postgres",
			},
			expectError: true,
			errorMsg:    "invalid session provider type",
		},
		{
			name:        "Empty type defaults to SQLite",
			config:      SessionProviderConfig{},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up a temporary config
			cfg = &Config{
				SessionProvider: tt.config,
			}

			err := validateSessionProvider()

			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if tt.expectError && err != nil && tt.errorMsg != "" {
				if !contains(err.Error(), tt.errorMsg) {
					t.Errorf("Error message %q does not contain %q", err.Error(), tt.errorMsg)
				}
			}

			// Clean up
			cfg = nil
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
