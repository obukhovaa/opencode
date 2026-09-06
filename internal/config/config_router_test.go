package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// TestConfig_RouterQueueAcksViperRoundTrip locks in that
// `router.queueAcknowledgementsEnabled` survives the real loader path
// (viper.ReadInConfig + viper.Unmarshal). Viper case-folds keys to lowercase
// during JSON ingestion; a camelCase field whose name maps to something Viper
// case-folds unexpectedly would be silently dropped. This test catches that
// before the field ships — a pure json.Unmarshal test would pass while the
// real config loader mangles it in production.
func TestConfig_RouterQueueAcksViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{"router": {"queueAcknowledgementsEnabled": true}}`
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

	if cfg.Router == nil {
		t.Fatal("router config was dropped by the loader")
	}
	if !cfg.Router.QueueAcknowledgementsEnabled {
		t.Errorf("QueueAcknowledgementsEnabled = false after round-trip, want true")
	}
}

// TestConfig_RouterQueueAcksDefaultFalse verifies that the field defaults to
// false when omitted from .opencode.json (no-op for the absent key, but Viper
// must not set it to true on any implicit default path).
func TestConfig_RouterQueueAcksDefaultFalse(t *testing.T) {
	dir := t.TempDir()
	body := `{"router": {"toolUpdatesEnabled": true}}`
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

	if cfg.Router == nil {
		t.Fatal("router config was dropped by the loader")
	}
	if cfg.Router.QueueAcknowledgementsEnabled {
		t.Errorf("QueueAcknowledgementsEnabled = true when omitted, want false (default)")
	}
}
