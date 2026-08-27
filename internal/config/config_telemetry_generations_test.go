package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// TestConfig_TelemetryGenerationsViperRoundTrip locks in that
// `telemetry.generations` survives the real loader path
// (viper.ReadInConfig + viper.Unmarshal). Viper folds keys to lowercase during
// JSON ingestion (`logInput` -> `loginput`); a mapstructure/viper change that
// stopped matching the folded key to the struct field would silently drop the
// operator's opt-in and leave prompts uncaptured (or, worse for a future
// default flip, uncontrolled).
func TestConfig_TelemetryGenerationsViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{
	  "telemetry": {
	    "generations": {
	      "enabled": true,
	      "logInput": ["workhorse", "sub*"],
	      "logOutput": ["*"]
	    }
	  }
	}`
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

	if cfg.Telemetry == nil || cfg.Telemetry.Generations == nil {
		t.Fatal("telemetry.generations was dropped by the loader")
	}
	gen := cfg.Telemetry.Generations
	if !gen.Enabled {
		t.Error("Enabled = false, want true")
	}
	if got, want := gen.LogInput, []string{"workhorse", "sub*"}; !equalStrings(got, want) {
		t.Errorf("LogInput = %v, want %v", got, want)
	}
	if got, want := gen.LogOutput, []string{"*"}; !equalStrings(got, want) {
		t.Errorf("LogOutput = %v, want %v", got, want)
	}
}

// TestConfig_TelemetryGenerationsDefaultsOff guards the privacy default: a
// config that enables Langfuse but says nothing about generations must leave
// prompt/completion capture off.
func TestConfig_TelemetryGenerationsDefaultsOff(t *testing.T) {
	dir := t.TempDir()
	body := `{"telemetry":{"langfuse":{"enabled":true},"tools":{"enabled":true,"logInput":["*"]}}}`
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

	if cfg.Telemetry == nil {
		t.Fatal("telemetry was dropped by the loader")
	}
	if cfg.Telemetry.Generations != nil {
		t.Errorf("Generations = %+v, want nil (capture must be opt-in)", cfg.Telemetry.Generations)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
