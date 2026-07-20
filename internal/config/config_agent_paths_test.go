package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// TestConfig_AgentPathsUnmarshals verifies that a top-level `agentPaths`
// array in `.opencode.json` round-trips through the Go Config struct via
// plain json.Unmarshal.
func TestConfig_AgentPathsUnmarshals(t *testing.T) {
	raw := []byte(`{"agentPaths":["~/.my-agents",".team/agents","/abs/agents"]}`)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []string{"~/.my-agents", ".team/agents", "/abs/agents"}
	if len(cfg.AgentPaths) != len(want) {
		t.Fatalf("AgentPaths = %v, want %v", cfg.AgentPaths, want)
	}
	for i, p := range want {
		if cfg.AgentPaths[i] != p {
			t.Errorf("AgentPaths[%d] = %q, want %q", i, cfg.AgentPaths[i], p)
		}
	}
}

// TestConfig_AgentPathsAbsentLeavesNilSlice verifies that omitting
// `agentPaths` produces a nil slice — the registry treats nil/empty as
// "no custom agent paths", so the absent case must not require defensive
// allocation in the config layer.
func TestConfig_AgentPathsAbsentLeavesNilSlice(t *testing.T) {
	raw := []byte(`{}`)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.AgentPaths != nil {
		t.Errorf("expected nil AgentPaths for empty config; got %v", cfg.AgentPaths)
	}
}

// TestConfig_AgentPathsViperRoundTrip locks in that `agentPaths` survives
// the real loader path (viper.ReadInConfig + viper.Unmarshal). Viper folds
// all keys to lowercase during JSON ingestion (`agentPaths` -> `agentpaths`);
// this test guards against a mapstructure/viper change that would stop
// matching the folded key to the `AgentPaths` struct field and silently
// drop the operator's custom agent paths in production.
func TestConfig_AgentPathsViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{"agentPaths":["~/.my-agents",".team/agents"]}`
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	v := viper.New()
	v.SetConfigName(".opencode")
	v.SetConfigType("json")
	v.AddConfigPath(dir)
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("read: %v", err)
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := []string{"~/.my-agents", ".team/agents"}
	if len(cfg.AgentPaths) != len(want) {
		t.Fatalf("AgentPaths = %v, want %v (viper may have dropped the folded key)", cfg.AgentPaths, want)
	}
	for i, p := range want {
		if cfg.AgentPaths[i] != p {
			t.Errorf("AgentPaths[%d] = %q, want %q", i, cfg.AgentPaths[i], p)
		}
	}
}
