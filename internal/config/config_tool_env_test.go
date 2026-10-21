package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/opencode-ai/opencode/internal/toolenv"
)

// TestConfigLoad_ToolEnvDeny proves toolEnv.deny survives the real load path
// (viper, not json.Unmarshal: viper lowercases keys, and only the
// case-insensitive mapstructure match binds the camelCase `toolEnv`), keeps
// the element case (env names are case-sensitive), and reaches the filter
// every spawn site uses.
func TestConfigLoad_ToolEnvDeny(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv(toolenv.EnvVar, "")

	dir := t.TempDir()
	body := `{"toolEnv":{"deny":["MYSQL_DSN","Mixed_Case","AWS_SECRET_*"]}}`
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	Reset()
	t.Cleanup(Reset)
	if _, err := Load(dir, false); err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	want := []string{"MYSQL_DSN", "Mixed_Case", "AWS_SECRET_*"}
	if got := Get().ToolEnv.Deny; !slices.Equal(got, want) {
		t.Fatalf("toolEnv.deny = %q, want %q", got, want)
	}

	deny := toolenv.DenyList()
	for _, name := range []string{"OPENCODE_SERVER_PASSWORD", "MYSQL_DSN", "Mixed_Case", "AWS_SECRET_ACCESS_KEY"} {
		if !toolenv.Denied(name, deny) {
			t.Errorf("%s is not denied after Load", name)
		}
	}
	if toolenv.Denied("mixed_case", deny) {
		t.Error("deny entries must stay case-sensitive")
	}

	// Reset clears the configured entries; the built-ins remain.
	Reset()
	deny = toolenv.DenyList()
	if toolenv.Denied("MYSQL_DSN", deny) {
		t.Error("MYSQL_DSN still denied after Reset")
	}
	if !toolenv.Denied("OPENCODE_SERVER_PASSWORD", deny) {
		t.Error("built-in lost after Reset")
	}
}
