package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// TestConfig_SkillsDefaultsComeFromSetDefaults pins the two defaults to the
// production wiring, not to viper's behaviour. A test that re-declares the
// SetDefault calls itself keeps passing after setDefaults stops making them —
// and the failure that hides is silent: an operator whose config carries any
// `skills` section (a `paths` list is the common case) would get
// MaxDescriptionChars 0, i.e. truncation off, for exactly the large-inventory
// workspace the cap exists to protect. skillLimitsFromConfig cannot recover,
// because it only falls back to the default when the whole section is nil.
func TestConfig_SkillsDefaultsComeFromSetDefaults(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	setDefaults(false)

	viper.SetConfigType("json")
	if err := viper.ReadConfig(strings.NewReader(`{"skills":{"paths":["team/skills"]}}`)); err != nil {
		t.Fatalf("read: %v", err)
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Skills == nil {
		t.Fatal("Skills is nil; setDefaults should have materialised the section")
	}
	if cfg.Skills.MaxDescriptionChars != DefaultSkillMaxDescriptionChars {
		t.Errorf("MaxDescriptionChars = %d, want the default %d — setDefaults must register the key",
			cfg.Skills.MaxDescriptionChars, DefaultSkillMaxDescriptionChars)
	}
	if cfg.Skills.MaxListingChars != 0 {
		t.Errorf("MaxListingChars = %d, want 0 — a hard listing cap must be opt-in",
			cfg.Skills.MaxListingChars)
	}
	if len(cfg.Skills.Paths) != 1 || cfg.Skills.Paths[0] != "team/skills" {
		t.Errorf("Paths = %v, want [team/skills]; a partial section must not shadow its siblings",
			cfg.Skills.Paths)
	}
}

// TestConfig_SkillsExplicitZeroDisablesTruncation guards the semantics of an
// explicit 0: an operator who writes `maxDescriptionChars: 0` wants full
// descriptions, not the default cap.
func TestConfig_SkillsExplicitZeroDisablesTruncation(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	setDefaults(false)

	viper.SetConfigType("json")
	if err := viper.ReadConfig(strings.NewReader(`{"skills":{"maxDescriptionChars":0}}`)); err != nil {
		t.Fatalf("read: %v", err)
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Skills == nil || cfg.Skills.MaxDescriptionChars != 0 {
		t.Fatalf("explicit 0 did not survive the loader: %+v", cfg.Skills)
	}
}
