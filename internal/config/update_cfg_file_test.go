package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/spf13/viper"
)

// withTestConfigFile points viper at a fresh .opencode.json under t.TempDir
// and primes the package-global cfg singleton. The caller's closure can then
// drive UpdateCfgFile against a clean filesystem environment.
func withTestConfigFile(t *testing.T, initialContent string) (configPath string, restore func()) {
	t.Helper()

	dir := t.TempDir()
	configPath = filepath.Join(dir, ".opencode.json")
	if initialContent != "" {
		if err := os.WriteFile(configPath, []byte(initialContent), 0o644); err != nil {
			t.Fatalf("seed config write: %v", err)
		}
	}

	prevCfg := cfg
	prevConfigFile := viper.ConfigFileUsed()
	cfg = &Config{}
	viper.SetConfigFile(configPath)

	restore = func() {
		cfg = prevCfg
		// viper.Reset() is too heavy; just restore the config file pointer
		// to whatever was there before. If prev was empty, SetConfigFile("")
		// is the documented way to clear it.
		viper.SetConfigFile(prevConfigFile)
	}
	t.Cleanup(restore)
	return configPath, restore
}

func TestUpdateCfgFileAtomicReplaceLeavesValidJSON(t *testing.T) {
	configPath, _ := withTestConfigFile(t, `{"tui":{"theme":"old"}}`)

	err := UpdateCfgFile(func(c *Config) {
		c.TUI.Theme = "new"
	})
	if err != nil {
		t.Fatalf("UpdateCfgFile: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if got.TUI.Theme != "new" {
		t.Errorf("Theme = %q, want %q", got.TUI.Theme, "new")
	}
}

func TestUpdateCfgFileDoesNotLeaveTempFile(t *testing.T) {
	configPath, _ := withTestConfigFile(t, `{}`)

	if err := UpdateCfgFile(func(c *Config) {
		c.TUI.Theme = "x"
	}); err != nil {
		t.Fatalf("UpdateCfgFile: %v", err)
	}

	dir := filepath.Dir(configPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		// .tmp-XXXX is the pattern from os.CreateTemp; ensure none survive.
		if name := e.Name(); name != filepath.Base(configPath) && !e.IsDir() {
			t.Errorf("unexpected leftover file: %q", name)
		}
	}
}

func TestUpdateCfgFileForces0o600WhenTokenPresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode semantics differ on Windows")
	}
	configPath, _ := withTestConfigFile(t, `{}`)
	// Pre-create file at world-readable mode to confirm the upgrade.
	if err := os.WriteFile(configPath, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := UpdateCfgFile(func(c *Config) {
		c.Router = &bridge.Config{
			Channels: bridge.ChannelsConfig{
				Slack: &bridge.SlackChannelConfig{
					Enabled: true,
					Apps:    []bridge.SlackIdentity{{ID: "default", BotToken: "xoxb-x"}},
				},
			},
		}
	})
	if err != nil {
		t.Fatalf("UpdateCfgFile: %v", err)
	}

	st, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %v, want 0o600 (token-bearing config must tighten mode)", got)
	}
}

func TestUpdateCfgFilePreservesTightModeWhenNoTokens(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode semantics differ on Windows")
	}
	configPath, _ := withTestConfigFile(t, "")
	if err := os.WriteFile(configPath, []byte(`{}`), 0o400); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := UpdateCfgFile(func(c *Config) {
		c.TUI.Theme = "dark"
	})
	if err != nil {
		t.Fatalf("UpdateCfgFile: %v", err)
	}

	st, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o400 {
		t.Errorf("mode = %v, want 0o400 (no tokens — preserve operator mode)", got)
	}
}

func TestUpdateCfgFilePreservesLaxModeWhenNoTokens(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode semantics differ on Windows")
	}
	configPath, _ := withTestConfigFile(t, "")
	if err := os.WriteFile(configPath, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := UpdateCfgFile(func(c *Config) {
		c.TUI.Theme = "dark"
	}); err != nil {
		t.Fatalf("UpdateCfgFile: %v", err)
	}

	st, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o644 {
		t.Errorf("mode = %v, want 0o644 (no tokens, no operator hardening — preserve)", got)
	}
}
