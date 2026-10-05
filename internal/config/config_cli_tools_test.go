package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// TestConfig_CLIToolsViperRoundTrip locks in that the `cliTools` block
// survives the real loader path (viper.ReadInConfig + viper.Unmarshal), whose
// key folding is what the pure json.Unmarshal tests cannot exercise.
func TestConfig_CLIToolsViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{"cliTools": {"paths": ["~/tools", "./team/tools"], "disabled": true,
		"timeout": "90s", "maxTimeout": 300, "maxOutputBytes": -1}}`
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
	if cfg.CLITools == nil {
		t.Fatal("cliTools was dropped by the loader")
	}
	if len(cfg.CLITools.Paths) != 2 || cfg.CLITools.Paths[0] != "~/tools" || !cfg.CLITools.Disabled {
		t.Errorf("cliTools = %+v", cfg.CLITools)
	}
	// Durations arrive as strings; a bare JSON number (seconds) is weakly
	// typed into the string the manifest-style parser understands.
	if cfg.CLITools.Timeout != "90s" || cfg.CLITools.MaxTimeout != "300" || cfg.CLITools.MaxOutputBytes != -1 {
		t.Errorf("cliTools limits = %+v", cfg.CLITools)
	}
}

// An absent block stays nil so discovery can tell "unset" from "disabled".
func TestConfig_CLIToolsAbsentStaysNil(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(`{"debug": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	v := viper.New()
	v.SetConfigName(".opencode")
	v.SetConfigType("json")
	v.AddConfigPath(dir)
	if err := v.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.CLITools != nil {
		t.Errorf("cliTools should stay nil when absent, got %+v", cfg.CLITools)
	}
}
