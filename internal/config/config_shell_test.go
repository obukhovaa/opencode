package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/viper"
)

// TestConfig_ShellViperRoundTrip covers the loader, not encoding/json. Viper is
// what actually reads .opencode.json in production, and it has surprised this
// codebase before (see TestConfig_HooksViperRoundTripLowercasesEventKeys): a
// pure json.Unmarshal test can pass while the real loader mangles the value.
func TestConfig_ShellViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{"shell":{"path":"/bin/zsh","args":["-l"],"interactive":["my-tool","Another-Tool"]}}`
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

	if cfg.Shell.Path != "/bin/zsh" {
		t.Errorf("shell.path = %q, want %q", cfg.Shell.Path, "/bin/zsh")
	}
	if !slices.Equal(cfg.Shell.Args, []string{"-l"}) {
		t.Errorf("shell.args = %v, want [-l]", cfg.Shell.Args)
	}

	// The list's element case must survive: entries are matched
	// case-insensitively by the classifier, but a loader that lowercased them
	// would still be a silent contract change worth catching here.
	want := []string{"my-tool", "Another-Tool"}
	if !slices.Equal(cfg.Shell.Interactive, want) {
		t.Errorf("shell.interactive = %v, want %v", cfg.Shell.Interactive, want)
	}
}

func TestConfig_ShellInteractiveAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(`{"shell":{"path":"/bin/zsh"}}`), 0o644); err != nil {
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

	if len(cfg.Shell.Interactive) != 0 {
		t.Errorf("shell.interactive = %v, want empty when unset", cfg.Shell.Interactive)
	}
}
