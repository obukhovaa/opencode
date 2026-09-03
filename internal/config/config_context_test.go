package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// TestConfig_AgentContextViperRoundTrip locks in the contract for the
// agent `context` object through the real loader. Viper case-folds map
// keys on JSON load, so a mixed-case agent name arrives lowercased —
// the same hazard documented for DeferredTools — while the Context
// struct's fields (values, not map keys) must survive intact.
func TestConfig_AgentContextViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"agents": {
			"MyAgent": {
				"model": "claude-3.7-sonnet",
				"context": {
					"paths": ["AGENTS.runtime.md", "docs/"],
					"mode": "append",
					"nested": false
				}
			}
		},
		"contextDiscovery": {
			"enabled": false,
			"maxFiles": 7
		}
	}`
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

	if _, ok := cfg.Agents["MyAgent"]; ok {
		t.Log("viper now preserves agent-name case; the folded-key expectation below can be revisited")
	}
	agent, ok := cfg.Agents["myagent"]
	if !ok {
		keys := make([]AgentName, 0, len(cfg.Agents))
		for k := range cfg.Agents {
			keys = append(keys, k)
		}
		t.Fatalf("expected viper to lowercase the agent key to 'myagent'; got keys %v", keys)
	}
	if agent.Context == nil {
		t.Fatal("Context did not survive the viper round-trip")
	}
	if got, want := len(agent.Context.Paths), 2; got != want {
		t.Fatalf("Context.Paths length = %d, want %d (%v)", got, want, agent.Context.Paths)
	}
	if agent.Context.Paths[0] != "AGENTS.runtime.md" || agent.Context.Paths[1] != "docs/" {
		t.Errorf("Context.Paths mangled: %v", agent.Context.Paths)
	}
	if agent.Context.Mode != "append" {
		t.Errorf("Context.Mode = %q, want %q", agent.Context.Mode, "append")
	}
	if agent.Context.Nested == nil || *agent.Context.Nested {
		t.Errorf("Context.Nested = %v, want explicit false", agent.Context.Nested)
	}

	if cfg.ContextDiscovery == nil {
		t.Fatal("ContextDiscovery did not survive the viper round-trip")
	}
	if cfg.ContextDiscovery.Enabled {
		t.Error("ContextDiscovery.Enabled = true, want false")
	}
	if cfg.ContextDiscovery.MaxFiles != 7 {
		t.Errorf("ContextDiscovery.MaxFiles = %d, want 7", cfg.ContextDiscovery.MaxFiles)
	}
}
