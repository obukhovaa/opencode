package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConfigLoad_LangfuseEnvironment proves telemetry.langfuse.environment
// survives the actual load path (viper lowercases JSON keys; the camelCase
// siblings only bind because mapstructure matches case-insensitively). The
// value is carried verbatim: env:VAR_NAME is resolved, and the value checked
// against Langfuse's naming rule, by the langfuse package where it is used,
// so neither a reference nor an unexpanded "$VAR" placeholder fails the load.
func TestConfigLoad_LangfuseEnvironment(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "literal", value: "staging"},
		{name: "env reference is not resolved here", value: "env:AGENT_ENV"},
		{name: "placeholder does not fail the load", value: "$AGENT_ENV"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LANGFUSE_PUBLIC_KEY", "pk-test")
			t.Setenv("LANGFUSE_SECRET_KEY", "sk-test")
			t.Setenv("LANGFUSE_TRACING_ENVIRONMENT", "")
			t.Setenv("AGENT_ENV", "prod")
			// Keep provider validation from rejecting the config before the
			// field under test is compared.
			t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

			dir := t.TempDir()
			cfgJSON := `{
  "telemetry": {
    "langfuse": {
      "enabled": true,
      "environment": "` + tt.value + `"
    }
  }
}`
			if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(cfgJSON), 0o644); err != nil {
				t.Fatalf("writing config: %v", err)
			}

			Reset()
			t.Cleanup(Reset)
			if _, err := Load(dir, false); err != nil {
				t.Fatalf("Load() error: %v", err)
			}

			cfg := Get()
			if cfg.Telemetry == nil || cfg.Telemetry.Langfuse == nil {
				t.Fatal("telemetry.langfuse missing from the loaded config")
			}
			if got := cfg.Telemetry.Langfuse.Environment; got != tt.value {
				t.Errorf("Environment = %q, want %q", got, tt.value)
			}
		})
	}
}
