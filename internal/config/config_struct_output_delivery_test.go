package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/spf13/viper"
)

func loadConfigFrom(t *testing.T, body string) config.Config {
	t.Helper()
	dir := t.TempDir()
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
	return cfg
}

// structOutputSchemaDelivery is the revert switch for the message-delivery
// change, which makes it exactly the field that must not be quietly mangled on
// the way in. Viper case-folds map KEYS during JSON ingestion; this pins that
// it leaves the VALUE alone, at both the top level and inside the agents map —
// ParseSchemaDelivery is case-sensitive, so a folded or re-cased value would
// silently fall back to the default and the escape hatch would look like it
// does nothing.
func TestConfig_StructOutputSchemaDeliveryViperRoundTrip(t *testing.T) {
	cfg := loadConfigFrom(t, `{
		"structOutputSchemaDelivery": "tool",
		"agents": {
			"coder": {"model": "claude-4-sonnet", "structOutputSchemaDelivery": "message"},
			"Architect": {"model": "claude-4-sonnet", "structOutputSchemaDelivery": "tool"}
		}
	}`)

	if cfg.StructOutputSchemaDelivery != "tool" {
		t.Fatalf("top-level = %q, want %q", cfg.StructOutputSchemaDelivery, "tool")
	}
	if mode, ok := tools.ParseSchemaDelivery(cfg.StructOutputSchemaDelivery); !ok || mode != tools.SchemaDeliveryTool {
		t.Fatalf("top-level value did not survive the round-trip: mode=%q ok=%v", mode, ok)
	}

	coder, ok := cfg.Agents["coder"]
	if !ok {
		t.Fatalf("agent 'coder' not loaded; agents: %v", cfg.Agents)
	}
	if coder.StructOutputSchemaDelivery != "message" {
		t.Errorf("agents.coder = %q, want %q", coder.StructOutputSchemaDelivery, "message")
	}

	// Agent map keys fold; the value must not.
	architect, ok := cfg.Agents["architect"]
	if !ok {
		t.Fatalf("agent 'Architect' not loaded under a folded key; agents: %v", cfg.Agents)
	}
	if architect.StructOutputSchemaDelivery != "tool" {
		t.Errorf("agents.architect = %q, want %q — viper folded the value, not just the key",
			architect.StructOutputSchemaDelivery, "tool")
	}
}

// An absent field must read as "unset", which ParseSchemaDelivery turns into
// the default. A config that never mentions the field is the overwhelmingly
// common case and must not warn.
func TestConfig_StructOutputSchemaDeliveryDefaultsToMessage(t *testing.T) {
	cfg := loadConfigFrom(t, `{"agents":{"coder":{"model":"claude-4-sonnet"}}}`)

	if cfg.StructOutputSchemaDelivery != "" {
		t.Errorf("top-level = %q, want empty (unset)", cfg.StructOutputSchemaDelivery)
	}
	mode, ok := tools.ParseSchemaDelivery(cfg.StructOutputSchemaDelivery)
	if !ok {
		t.Error("an unset field must not be reported as an unrecognized value")
	}
	if mode != tools.SchemaDeliveryMessage {
		t.Errorf("unset resolved to %q, want %q", mode, tools.SchemaDeliveryMessage)
	}
}
