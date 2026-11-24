package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func loadListingScopeConfig(t *testing.T, body string) Config {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	v := viper.NewWithOptions(configDecoderOption())
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
	return cfg
}

// Run-arg tokens are dotted, so environment-reference expansion must leave
// them for the flow to resolve per step.
func TestConfig_ListingScopeViperRoundTrip(t *testing.T) {
	t.Setenv("LISTING_SCOPE_ROOT", "shared")
	cfg := loadListingScopeConfig(t, `{"listingScope": {
		"skills": [".agents/skills/**", "${args.team}/skills/**", "${LISTING_SCOPE_ROOT}/skills/**"],
		"subagents": [".agents/types/**", "${args.team}/types/**"]}}`)
	if cfg.ListingScope == nil {
		t.Fatal("listingScope was dropped by the loader")
	}
	wantSkills := []string{".agents/skills/**", "${args.team}/skills/**", "shared/skills/**"}
	if len(cfg.ListingScope.Skills) != len(wantSkills) {
		t.Fatalf("skills = %q", cfg.ListingScope.Skills)
	}
	for i, want := range wantSkills {
		if cfg.ListingScope.Skills[i] != want {
			t.Errorf("skills[%d] = %q, want %q", i, cfg.ListingScope.Skills[i], want)
		}
	}
	if len(cfg.ListingScope.Subagents) != 2 || cfg.ListingScope.Subagents[1] != "${args.team}/types/**" {
		t.Errorf("subagents = %q", cfg.ListingScope.Subagents)
	}
}

func TestConfig_ListingScopeAbsentStaysNil(t *testing.T) {
	cfg := loadListingScopeConfig(t, `{"debug": true}`)
	if cfg.ListingScope != nil {
		t.Errorf("listingScope should stay nil when absent, got %+v", cfg.ListingScope)
	}
}
