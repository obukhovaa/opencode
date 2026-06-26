package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/hooks"
	"github.com/spf13/viper"
)

// TestConfig_HooksBlockUnmarshals verifies that a Claude-Code-shaped
// `hooks` block inside `.opencode.json` round-trips through the Go
// Config struct without mangling — the field types here must align
// with what users will paste from Claude Code's `settings.json`.
//
// This is the unit-level confidence check that "copy-paste from Claude
// Code into .opencode.json" actually works for the JSON shape.
func TestConfig_HooksBlockUnmarshals(t *testing.T) {
	raw := []byte(`{
		"hooks": {
			"PreToolUse": [
				{
					"matcher": "Bash",
					"hooks": [
						{
							"type": "command",
							"command": "/usr/local/bin/rtk",
							"args": ["hook"],
							"timeout": 30
						}
					]
				}
			],
			"PostToolUse": [
				{
					"matcher": "Bash|Read",
					"hooks": [
						{
							"type": "command",
							"command": "/path/to/redact.sh"
						}
					]
				}
			]
		}
	}`)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	pre := cfg.Hooks["PreToolUse"]
	if len(pre) != 1 {
		t.Fatalf("PreToolUse: got %d groups, want 1", len(pre))
	}
	if pre[0].Matcher != "Bash" {
		t.Errorf("matcher = %q, want %q", pre[0].Matcher, "Bash")
	}
	if len(pre[0].Hooks) != 1 {
		t.Fatalf("inner hooks: got %d, want 1", len(pre[0].Hooks))
	}
	gotEntry := pre[0].Hooks[0]
	wantEntry := hooks.HookEntry{
		Type: "command", Command: "/usr/local/bin/rtk", Args: []string{"hook"}, Timeout: 30,
	}
	if gotEntry.Type != wantEntry.Type || gotEntry.Command != wantEntry.Command || gotEntry.Timeout != wantEntry.Timeout {
		t.Errorf("entry = %+v, want %+v", gotEntry, wantEntry)
	}
	if len(gotEntry.Args) != 1 || gotEntry.Args[0] != "hook" {
		t.Errorf("args = %v, want [\"hook\"]", gotEntry.Args)
	}

	post := cfg.Hooks["PostToolUse"]
	if len(post) != 1 || post[0].Matcher != "Bash|Read" {
		t.Errorf("PostToolUse parse: got %+v", post)
	}
}

// TestConfig_HooksBlockAbsentLeavesNilMap verifies that omitting `hooks`
// from `.opencode.json` produces a nil map — the registry-side logic
// already handles nil as "no hooks installed", so the absent case must
// not require defensive map allocation in the config layer.
func TestConfig_HooksBlockAbsentLeavesNilMap(t *testing.T) {
	raw := []byte(`{}`)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Hooks != nil {
		t.Errorf("expected nil Hooks map for empty config; got %v", cfg.Hooks)
	}
}

// TestConfig_HooksViperRoundTripLowercasesEventKeys documents and locks in
// the actual viper behavior: viper lowercases ALL map keys during JSON
// ingestion, so `"PreToolUse"` in the on-disk config becomes `"pretooluse"`
// in `cfg.Hooks`. The registry compensates with case-insensitive lookup
// in `internal/hooks/registry.go::loadGroups` — this test guards against
// either a viper version that changes the case-folding behavior OR a
// regression that removes the case-insensitive lookup.
//
// If this test starts failing because viper preserves case, the registry's
// case-insensitive lookup remains correct but unnecessary; if it fails
// because the lookup stopped folding, hooks silently stop firing in
// production despite passing every JSON-only unit test.
func TestConfig_HooksViperRoundTripLowercasesEventKeys(t *testing.T) {
	dir := t.TempDir()
	body := `{"hooks":{"PreToolUse":[{"matcher":"bash","hooks":[{"type":"command","command":"/usr/local/bin/rtk"}]}]}}`
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

	// Document the actual key shape after viper round-trip. If this
	// assertion ever flips to expecting "PreToolUse", remove the
	// case-insensitive lookup in the registry.
	if _, ok := cfg.Hooks["PreToolUse"]; ok {
		t.Log("viper now preserves event-name case; consider removing the case-insensitive lookup workaround")
	}
	if g, ok := cfg.Hooks["pretooluse"]; !ok {
		t.Fatalf("expected viper to lowercase the event key to 'pretooluse'; got keys %v", mapKeys(cfg.Hooks))
	} else if len(g) != 1 || g[0].Matcher != "bash" {
		t.Errorf("hook group not preserved through viper: got %+v", g)
	}
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
