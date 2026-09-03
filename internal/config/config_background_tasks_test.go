package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// TestConfig_BackgroundTasksViperRoundTrip locks in that
// `backgroundTasks.stallThreshold` survives the real loader path
// (viper.ReadInConfig + viper.Unmarshal). Viper folds keys to lowercase during
// JSON ingestion (`stallThreshold` -> `stallthreshold`); a change that stopped
// matching the folded key would silently drop the operator's value and fall
// back to the 30m default — which, for a deployment that raised
// callToolTimeoutSeconds above it, means killing healthy subagents.
func TestConfig_BackgroundTasksViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{"backgroundTasks": {"stallThreshold": "45m"}}`
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

	if cfg.BackgroundTasks == nil {
		t.Fatal("backgroundTasks was dropped by the loader")
	}
	if got := cfg.BackgroundTasks.StallThreshold; got != "45m" {
		t.Errorf("StallThreshold = %q, want %q", got, "45m")
	}
	if got := cfg.TaskStallThreshold(); got != 45*time.Minute {
		t.Errorf("TaskStallThreshold() = %v, want %v", got, 45*time.Minute)
	}
}

func TestConfig_TaskStallThreshold(t *testing.T) {
	tests := []struct {
		name string
		cfg  *BackgroundTasksConfig
		want time.Duration
	}{
		{"unset block uses the default", nil, DefaultTaskStallThreshold},
		{"empty value uses the default", &BackgroundTasksConfig{}, DefaultTaskStallThreshold},
		{"explicit override", &BackgroundTasksConfig{StallThreshold: "90m"}, 90 * time.Minute},
		// Non-positive is the documented escape hatch: detection off entirely.
		{"zero disables", &BackgroundTasksConfig{StallThreshold: "0s"}, 0},
		{"negative disables", &BackgroundTasksConfig{StallThreshold: "-5m"}, 0},
		// A typo must not silently disable protection.
		{"unparseable falls back to the default", &BackgroundTasksConfig{StallThreshold: "half an hour"}, DefaultTaskStallThreshold},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{BackgroundTasks: tt.cfg}
			if got := cfg.TaskStallThreshold(); got != tt.want {
				t.Errorf("TaskStallThreshold() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The default must clear the largest budget a healthy subagent can be silent
// inside, or the shipped configuration kills working tasks mid-tool-call. The
// ceilings are the bash foreground hard cap and the default MCP per-call budget.
func TestConfig_DefaultStallThresholdClearsToolCallCeilings(t *testing.T) {
	const bashForegroundHardCap = 10 * time.Minute // tools.MaxTimeout
	const mcpCallToolDefault = 5 * time.Minute     // agent.mcpCallToolTimeout

	for _, ceiling := range []time.Duration{bashForegroundHardCap, mcpCallToolDefault} {
		if DefaultTaskStallThreshold <= ceiling {
			t.Errorf("DefaultTaskStallThreshold (%v) must exceed the %v tool-call ceiling",
				DefaultTaskStallThreshold, ceiling)
		}
	}
}
