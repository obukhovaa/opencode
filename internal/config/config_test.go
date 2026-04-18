package config

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
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

func TestValidateProviderMetadata(t *testing.T) {
	tests := []struct {
		name        string
		metadata    *ProviderMetadata
		expectError bool
	}{
		{
			name:        "nil metadata is valid",
			metadata:    nil,
			expectError: false,
		},
		{
			name: "valid metadata with both fields",
			metadata: &ProviderMetadata{
				SessionID: "session_id",
				UserID:    "user_id",
			},
			expectError: false,
		},
		{
			name: "valid metadata with sessionId only",
			metadata: &ProviderMetadata{
				SessionID: "my_session",
			},
			expectError: false,
		},
		{
			name: "valid metadata with userId only",
			metadata: &ProviderMetadata{
				UserID: "trace_user",
			},
			expectError: false,
		},
		{
			name:        "empty metadata struct is valid",
			metadata:    &ProviderMetadata{},
			expectError: false,
		},
		{
			name: "whitespace-only sessionId is invalid",
			metadata: &ProviderMetadata{
				SessionID: "   ",
			},
			expectError: true,
		},
		{
			name: "whitespace-only userId is invalid",
			metadata: &ProviderMetadata{
				UserID: "  ",
			},
			expectError: true,
		},
		{
			name: "valid metadata with tags",
			metadata: &ProviderMetadata{
				Tags: "labels",
			},
			expectError: false,
		},
		{
			name: "whitespace-only tags is invalid",
			metadata: &ProviderMetadata{
				Tags: "  ",
			},
			expectError: true,
		},
		{
			name: "valid metadata with all fields",
			metadata: &ProviderMetadata{
				SessionID: "session_id",
				UserID:    "user_id",
				Tags:      "tags",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProviderMetadata(models.ProviderAnthropic, tt.metadata)
			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestValidateTelemetryConfig(t *testing.T) {
	tests := []struct {
		name        string
		telemetry   *TelemetryConfig
		expectError bool
	}{
		{
			name:        "nil telemetry is valid",
			telemetry:   nil,
			expectError: false,
		},
		{
			name:        "empty telemetry is valid",
			telemetry:   &TelemetryConfig{},
			expectError: false,
		},
		{
			name: "supported default tag is valid",
			telemetry: &TelemetryConfig{
				DefaultTags: []string{"agent"},
			},
			expectError: false,
		},
		{
			name: "unsupported default tag is invalid",
			telemetry: &TelemetryConfig{
				DefaultTags: []string{"unknown"},
			},
			expectError: true,
		},
		{
			name: "mix of supported and unsupported is invalid",
			telemetry: &TelemetryConfig{
				DefaultTags: []string{"agent", "bad"},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTelemetryConfig(tt.telemetry)
			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestFlattenPermissionMap(t *testing.T) {
	// Simulate what viper does to {"read": {"~/.openai/*": "deny"}}:
	// it splits on "." producing {"read": {"~/": {"openai/*": "deny"}}}
	mangled := map[string]any{
		"read": map[string]any{
			"~/": map[string]any{
				"openai/*": "deny",
			},
		},
		"grep": map[string]any{
			"/proc/*": "allow",
		},
		"bash": "allow",
	}

	fixed := flattenPermissionMap(mangled)

	// "read" inner map should have the dot-joined key restored
	readVal, ok := fixed["read"].(map[string]any)
	if !ok {
		t.Fatalf("read should be map[string]any, got %T", fixed["read"])
	}
	if _, ok := readVal["~/.openai/*"]; !ok {
		t.Errorf("expected key '~/.openai/*' in read map, got keys: %v", readVal)
	}
	if v := readVal["~/.openai/*"]; v != "deny" {
		t.Errorf("expected 'deny', got %v", v)
	}

	// "grep" should be unchanged
	grepVal, ok := fixed["grep"].(map[string]any)
	if !ok {
		t.Fatalf("grep should be map[string]any, got %T", fixed["grep"])
	}
	if v := grepVal["/proc/*"]; v != "allow" {
		t.Errorf("expected 'allow', got %v", v)
	}

	// "bash" string should be unchanged
	if v := fixed["bash"]; v != "allow" {
		t.Errorf("expected 'allow', got %v", v)
	}
}

func TestFixPermissionKeys_Agents(t *testing.T) {
	cfg := &Config{
		Agents: map[AgentName]Agent{
			"explorer": {
				Permission: map[string]any{
					"read": map[string]any{
						"~/": map[string]any{
							"openai/*": "deny",
						},
					},
				},
			},
		},
	}

	fixPermissionKeys(cfg)

	readMap := cfg.Agents["explorer"].Permission["read"].(map[string]any)
	if _, ok := readMap["~/.openai/*"]; !ok {
		t.Errorf("expected '~/.openai/*' key after fix, got: %v", readMap)
	}
}

func TestFixPermissionKeys_GlobalRules(t *testing.T) {
	cfg := &Config{
		Agents: map[AgentName]Agent{},
		Permission: &PermissionConfig{
			Rules: map[string]any{
				"read": map[string]any{
					"~/": map[string]any{
						"ssh/*": "deny",
					},
				},
			},
		},
	}

	fixPermissionKeys(cfg)

	readMap := cfg.Permission.Rules["read"].(map[string]any)
	if _, ok := readMap["~/.ssh/*"]; !ok {
		t.Errorf("expected '~/.ssh/*' key after fix, got: %v", readMap)
	}
}
