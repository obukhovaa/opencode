package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestActiveProfile(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"", ""},
		{"piano", "piano"},
		{" home ", "home"},
		{"bfg-2", "bfg-2"},
		{"../evil", ""}, // path escape
		{"a/b", ""},     // separator
		{"a b", ""},     // whitespace inside
		{"-lead", ""},   // must start alnum
	} {
		t.Setenv(ProfileEnv, tc.value)
		if got := ActiveProfile(); got != tc.want {
			t.Errorf("OPENCODE_PROFILE=%q: got %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestProfileAgentsDirName(t *testing.T) {
	t.Setenv(ProfileEnv, "")
	if got := ProfileAgentsDirName(); got != ".agents" {
		t.Errorf("no profile: got %q, want .agents", got)
	}
	t.Setenv(ProfileEnv, "piano")
	if got := ProfileAgentsDirName(); got != ".agents-piano" {
		t.Errorf("piano profile: got %q, want .agents-piano", got)
	}
}

func TestProfileConfigDirs(t *testing.T) {
	t.Setenv(ProfileEnv, "")
	if got := ProfileConfigDirs(); got != nil {
		t.Fatalf("no profile: got %v, want nil", got)
	}
	t.Setenv(ProfileEnv, "piano")
	t.Setenv("XDG_CONFIG_HOME", "")
	got := ProfileConfigDirs()
	if len(got) != 1 || got[0] != filepath.Join("$HOME", ".config", "opencode-piano") {
		t.Fatalf("no XDG: got %v", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	got = ProfileConfigDirs()
	if len(got) != 2 || got[0] != "$XDG_CONFIG_HOME/opencode-piano" { // viper expands $VARS in paths
		t.Fatalf("with XDG: got %v", got)
	}
}

func loadWithProfile(t *testing.T, home, profile string) *Config {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(ProfileEnv, profile)
	viper.Reset()
	t.Cleanup(viper.Reset)
	Reset()
	t.Cleanup(Reset)
	cfg, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestProfileSelectsProfileGlobalConfig(t *testing.T) {
	home := t.TempDir()
	writeFile := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(filepath.Join(home, ".opencode.json"), `{"maxTurns": 11}`)
	writeFile(filepath.Join(home, ".config", "opencode-piano", ".opencode.json"), `{"maxTurns": 99}`)

	if cfg := loadWithProfile(t, home, ""); cfg.MaxTurns != 11 {
		t.Errorf("no profile: maxTurns=%d, want 11 (base config)", cfg.MaxTurns)
	}
	if cfg := loadWithProfile(t, home, "piano"); cfg.MaxTurns != 99 {
		t.Errorf("piano profile: maxTurns=%d, want 99 (profile config)", cfg.MaxTurns)
	}
	// profile without its own file falls back to the base global config
	if cfg := loadWithProfile(t, home, "bfg"); cfg.MaxTurns != 11 {
		t.Errorf("bfg profile (no file): maxTurns=%d, want 11 (fallback)", cfg.MaxTurns)
	}
}

func TestUpdateCfgFileDefaultsToProfilePath(t *testing.T) {
	home := t.TempDir()
	cfg := loadWithProfile(t, home, "piano") // no config files anywhere
	cfg.MaxTurns = 7
	if err := UpdateCfgFile(func(c *Config) { c.MaxTurns = 7 }); err != nil {
		t.Fatalf("UpdateCfgFile: %v", err)
	}
	want := filepath.Join(home, ".config", "opencode-piano", ".opencode.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("profile config not written at %q: %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".opencode.json")); !os.IsNotExist(err) {
		t.Fatal("base ~/.opencode.json must not be created for a profile write")
	}
}
