package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// TestConfig_FlowPathsUnmarshals verifies that a top-level `flowPaths`
// array in `.opencode.json` round-trips through the Go Config struct via
// plain json.Unmarshal.
func TestConfig_FlowPathsUnmarshals(t *testing.T) {
	raw := []byte(`{"flowPaths":["~/.my-flows",".team/flows","/workspace/id/flows"]}`)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []string{"~/.my-flows", ".team/flows", "/workspace/id/flows"}
	if len(cfg.FlowPaths) != len(want) {
		t.Fatalf("FlowPaths = %v, want %v", cfg.FlowPaths, want)
	}
	for i, p := range want {
		if cfg.FlowPaths[i] != p {
			t.Errorf("FlowPaths[%d] = %q, want %q", i, cfg.FlowPaths[i], p)
		}
	}
}

// TestConfig_FlowPathsAbsentLeavesNilSlice verifies that omitting
// `flowPaths` produces a nil slice — the registry treats nil/empty as
// "no custom flow paths", so the absent case must not require defensive
// allocation in the config layer.
func TestConfig_FlowPathsAbsentLeavesNilSlice(t *testing.T) {
	raw := []byte(`{}`)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.FlowPaths != nil {
		t.Errorf("expected nil FlowPaths for empty config; got %v", cfg.FlowPaths)
	}
}

// TestConfig_FlowPathsViperRoundTrip locks in that `flowPaths` survives
// the real loader path (viper.ReadInConfig + viper.Unmarshal). Viper folds
// all keys to lowercase during JSON ingestion (`flowPaths` -> `flowpaths`);
// this test guards against a mapstructure/viper change that would stop
// matching the folded key to the `FlowPaths` struct field and silently
// drop the operator's custom flow paths in production.
func TestConfig_FlowPathsViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{"flowPaths":["~/.my-flows",".team/flows"]}`
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

	want := []string{"~/.my-flows", ".team/flows"}
	if len(cfg.FlowPaths) != len(want) {
		t.Fatalf("FlowPaths = %v, want %v (viper may have dropped the folded key)", cfg.FlowPaths, want)
	}
	for i, p := range want {
		if cfg.FlowPaths[i] != p {
			t.Errorf("FlowPaths[%d] = %q, want %q", i, cfg.FlowPaths[i], p)
		}
	}
}
