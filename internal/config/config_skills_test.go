package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// TestConfig_SkillsListingBudgetUnmarshals verifies the listing-budget fields
// round-trip through the Go struct via plain json.Unmarshal.
func TestConfig_SkillsListingBudgetUnmarshals(t *testing.T) {
	raw := []byte(`{"skills":{"paths":["composer/skills"],"maxDescriptionChars":250,"maxListingChars":16000}}`)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Skills == nil {
		t.Fatal("Skills is nil")
	}
	if cfg.Skills.MaxDescriptionChars != 250 {
		t.Errorf("MaxDescriptionChars = %d, want 250", cfg.Skills.MaxDescriptionChars)
	}
	if cfg.Skills.MaxListingChars != 16000 {
		t.Errorf("MaxListingChars = %d, want 16000", cfg.Skills.MaxListingChars)
	}
	if len(cfg.Skills.Paths) != 1 || cfg.Skills.Paths[0] != "composer/skills" {
		t.Errorf("Paths = %v, want [composer/skills]", cfg.Skills.Paths)
	}
}

// TestConfig_SkillsListingBudgetViperRoundTrip locks in that the budget
// survives the real loader path (viper.ReadInConfig + viper.Unmarshal). Viper
// folds keys to lowercase during JSON ingestion (`maxDescriptionChars` ->
// `maxdescriptionchars`); a mapstructure change that stopped matching the
// folded key would silently revert operators to the built-in default while
// their config file still said otherwise.
func TestConfig_SkillsListingBudgetViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{"skills":{"paths":["bdata/skills"],"maxDescriptionChars":300,"maxListingChars":12000}}`
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

	if cfg.Skills == nil {
		t.Fatal("Skills is nil after viper round-trip")
	}
	if cfg.Skills.MaxDescriptionChars != 300 {
		t.Errorf("MaxDescriptionChars = %d, want 300 (viper may have dropped the folded key)",
			cfg.Skills.MaxDescriptionChars)
	}
	if cfg.Skills.MaxListingChars != 12000 {
		t.Errorf("MaxListingChars = %d, want 12000 (viper may have dropped the folded key)",
			cfg.Skills.MaxListingChars)
	}
	if len(cfg.Skills.Paths) != 1 || cfg.Skills.Paths[0] != "bdata/skills" {
		t.Errorf("Paths = %v, want [bdata/skills]", cfg.Skills.Paths)
	}
}

// TestConfig_SkillsDefaultsThroughViper pins the two defaults set in
// setDefaults: descriptions are capped (truncation loses nothing) while the
// whole-block budget stays off (it drops skills from the listing, so it is
// opt-in).
func TestConfig_SkillsDefaultsThroughViper(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	v := viper.New()
	v.SetConfigName(".opencode")
	v.SetConfigType("json")
	v.AddConfigPath(dir)
	v.SetDefault("skills.maxDescriptionChars", DefaultSkillMaxDescriptionChars)
	v.SetDefault("skills.maxListingChars", 0)
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("read: %v", err)
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Skills == nil {
		t.Fatal("Skills is nil; the defaults should have materialised the section")
	}
	if cfg.Skills.MaxDescriptionChars != DefaultSkillMaxDescriptionChars {
		t.Errorf("MaxDescriptionChars = %d, want the default %d",
			cfg.Skills.MaxDescriptionChars, DefaultSkillMaxDescriptionChars)
	}
	if cfg.Skills.MaxListingChars != 0 {
		t.Errorf("MaxListingChars = %d, want 0 — a hard listing cap must be opt-in",
			cfg.Skills.MaxListingChars)
	}
}

// TestConfig_SkillsExplicitZeroDisablesTruncation guards the semantics of an
// explicit 0: an operator who writes `maxDescriptionChars: 0` wants full
// descriptions, not the default cap.
func TestConfig_SkillsExplicitZeroDisablesTruncation(t *testing.T) {
	dir := t.TempDir()
	body := `{"skills":{"maxDescriptionChars":0}}`
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	v := viper.New()
	v.SetConfigName(".opencode")
	v.SetConfigType("json")
	v.AddConfigPath(dir)
	v.SetDefault("skills.maxDescriptionChars", DefaultSkillMaxDescriptionChars)
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("read: %v", err)
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Skills == nil || cfg.Skills.MaxDescriptionChars != 0 {
		t.Fatalf("explicit 0 did not survive the loader: %+v", cfg.Skills)
	}
}
