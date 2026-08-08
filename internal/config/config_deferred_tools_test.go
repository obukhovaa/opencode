package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/spf13/viper"
)

// TestConfig_DeferredToolsViperRoundTripCaseFolding locks in the contract
// between viper's key case-folding and permission.IsToolDeferred's
// case-insensitive matching. Viper lowercases ALL map keys during JSON
// ingestion, so `"mcp_Slack_*"` on disk becomes `"mcp_slack_*"` in
// cfg.Agents[...].DeferredTools — while MCP tool names at runtime keep
// their original case (`<server>_<toolname>`). IsToolDeferred compensates
// by lowering both sides; this test fails if either half of that contract
// regresses (viper stops folding is fine; IsToolDeferred going
// case-sensitive strands JSON-configured deferrals in production while
// JSON-only unit tests keep passing).
func TestConfig_DeferredToolsViperRoundTripCaseFolding(t *testing.T) {
	dir := t.TempDir()
	body := `{"agents":{"coder":{"model":"claude-4-sonnet","deferredTools":{"mcp_Slack_*":true,"Sourcegraph":true}}}}`
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
	var cfg config.Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	agent, ok := cfg.Agents["coder"]
	if !ok {
		t.Fatalf("agent 'coder' not loaded; agents: %v", cfg.Agents)
	}
	if len(agent.DeferredTools) != 2 {
		t.Fatalf("expected 2 deferredTools patterns, got %v", agent.DeferredTools)
	}

	// Document actual key shape: viper lowercases the pattern keys.
	if _, ok := agent.DeferredTools["mcp_slack_*"]; !ok {
		if _, preserved := agent.DeferredTools["mcp_Slack_*"]; preserved {
			t.Log("viper now preserves map-key case; IsToolDeferred's folding remains correct but is no longer load-bearing")
		} else {
			t.Fatalf("expected lowered key 'mcp_slack_*'; got %v", agent.DeferredTools)
		}
	}

	// The end-to-end contract: runtime tool names with their original case
	// still match the viper-folded patterns.
	for _, name := range []string{"mcp_Slack_send_message", "mcp_slack_list_channels", "Sourcegraph", "sourcegraph"} {
		if !permission.IsToolDeferred(name, agent.DeferredTools) {
			t.Errorf("IsToolDeferred(%q) = false after viper round-trip; config %v", name, agent.DeferredTools)
		}
	}
	if permission.IsToolDeferred("bash", agent.DeferredTools) {
		t.Error("IsToolDeferred(\"bash\") = true; non-matching tool must not be deferred")
	}
	// Hard exclusions hold even under a match-all config.
	for _, name := range []string{"toolsearch", "struct_output"} {
		if permission.IsToolDeferred(name, map[string]bool{"*": true}) {
			t.Errorf("IsToolDeferred(%q) = true; hard exclusion violated", name)
		}
	}
}
