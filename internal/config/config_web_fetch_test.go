package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// TestConfig_WebFetchViperRoundTrip locks in that `webFetch.maxOutputBytes`
// survives the real loader path (viper.ReadInConfig + viper.Unmarshal). Viper
// folds keys to lowercase during JSON ingestion (`maxOutputBytes` ->
// `maxoutputbytes`); a change that stopped matching the folded key would
// silently drop the operator's value and fall back to the 50KB default —
// including the negative value that is the documented way to keep whole pages
// inline, whose loss would be invisible until a context overflowed.
func TestConfig_WebFetchViperRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"positive override", `{"webFetch": {"maxOutputBytes": 4096}}`, 4096},
		{"negative disables the cap", `{"webFetch": {"maxOutputBytes": -1}}`, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(tt.body), 0o644); err != nil {
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

			if cfg.WebFetch == nil {
				t.Fatal("webFetch was dropped by the loader")
			}
			if got := cfg.WebFetch.MaxOutputBytes; got != tt.want {
				t.Errorf("MaxOutputBytes = %d, want %d", got, tt.want)
			}
		})
	}
}

// An absent block must stay absent rather than materializing as a zero value,
// so the tool can tell "unset" (use the default) from an explicit 0.
func TestConfig_WebFetchAbsentStaysNil(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(`{"debug": true}`), 0o644); err != nil {
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

	if cfg.WebFetch != nil {
		t.Errorf("webFetch = %+v, want nil when the block is absent", cfg.WebFetch)
	}
}
