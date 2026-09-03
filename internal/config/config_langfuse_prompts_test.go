package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateAgentPromptSource(t *testing.T) {
	tests := []struct {
		name      string
		hasInline bool
		path      string
		label     string
		errHas    string
	}{
		{name: "inline only", hasInline: true},
		{name: "reference only", path: "agents/planner/system"},
		{name: "reference with a label", path: "agents/planner/system", label: "staging"},
		// Declaring neither is legal: a built-in agent falls back to its
		// compiled-in prompt, and an override that only changes the model
		// has no business restating one.
		{name: "neither is legal for an agent"},
		{
			name:      "both sources",
			hasInline: true,
			path:      "agents/planner/system",
			errHas:    "mutually exclusive",
		},
		// A label with no path is NOT rejected here. Each definition layer
		// is a partial override, so a JSON entry may legitimately re-label
		// a path declared by a markdown agent; only the merged entry can
		// tell an orphaned label from a re-label, and the agent registry
		// drops it there (normalisePromptSources) rather than failing boot.
		{name: "label without a path is judged after merging, not here", label: "staging"},
		{
			name:      "a whitespace-only path does not collide with an inline prompt",
			hasInline: true,
			path:      "   ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAgentPromptSource("planner", tt.hasInline, tt.path)
			if tt.errHas == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.errHas) {
				t.Errorf("error = %v, want one containing %q", err, tt.errHas)
			}
			if !strings.Contains(err.Error(), "planner") {
				t.Errorf("error = %v, want one naming the agent", err)
			}
		})
	}
}

func TestLangfusePromptsConfig_Durations(t *testing.T) {
	t.Run("empty means default", func(t *testing.T) {
		c := &LangfusePromptsConfig{}
		ttl, err := c.CacheTTLDuration()
		if err != nil || ttl != 0 {
			t.Errorf("CacheTTLDuration() = %v, %v — want 0, nil (the client applies the default)", ttl, err)
		}
		timeout, err := c.TimeoutDuration()
		if err != nil || timeout != 0 {
			t.Errorf("TimeoutDuration() = %v, %v — want 0, nil", timeout, err)
		}
	})

	t.Run("valid durations parse", func(t *testing.T) {
		c := &LangfusePromptsConfig{CacheTTL: "5m", Timeout: "2s"}
		ttl, err := c.CacheTTLDuration()
		if err != nil || ttl != 5*time.Minute {
			t.Errorf("CacheTTLDuration() = %v, %v", ttl, err)
		}
		timeout, err := c.TimeoutDuration()
		if err != nil || timeout != 2*time.Second {
			t.Errorf("TimeoutDuration() = %v, %v", timeout, err)
		}
	})

	t.Run("unparseable and non-positive values are rejected", func(t *testing.T) {
		for _, bad := range []string{"soon", "0s", "-1m"} {
			c := &LangfusePromptsConfig{CacheTTL: bad}
			if _, err := c.CacheTTLDuration(); err == nil {
				t.Errorf("CacheTTLDuration(%q) error = nil, want an error", bad)
			}
		}
	})

	t.Run("warmup defaults on", func(t *testing.T) {
		var nilCfg *LangfusePromptsConfig
		if !nilCfg.WarmupEnabled() {
			t.Error("nil config WarmupEnabled() = false, want true")
		}
		if !(&LangfusePromptsConfig{}).WarmupEnabled() {
			t.Error("absent warmup WarmupEnabled() = false, want true")
		}
		off := false
		if (&LangfusePromptsConfig{Warmup: &off}).WarmupEnabled() {
			t.Error("explicit warmup:false WarmupEnabled() = true, want false")
		}
	})
}

func TestValidateTelemetryConfig_Prompts(t *testing.T) {
	// Keep the machine's own Langfuse environment out of these cases.
	t.Setenv("LANGFUSE_PUBLIC_KEY", "")
	t.Setenv("LANGFUSE_SECRET_KEY", "")

	withCreds := func(p *LangfusePromptsConfig) *TelemetryConfig {
		return &TelemetryConfig{Langfuse: &LangfuseConfig{
			PublicKey: "pk", SecretKey: "sk", Prompts: p,
		}}
	}

	t.Run("prompts need no tracing", func(t *testing.T) {
		// langfuse.enabled stays false — prompt management is its own
		// capability and must validate on its own.
		if err := validateTelemetryConfig(withCreds(&LangfusePromptsConfig{Enabled: true})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("prompts need credentials", func(t *testing.T) {
		cfg := &TelemetryConfig{Langfuse: &LangfuseConfig{
			Prompts: &LangfusePromptsConfig{Enabled: true},
		}}
		err := validateTelemetryConfig(cfg)
		if err == nil {
			t.Fatal("error = nil, want a missing-credentials error")
		}
		if !strings.Contains(err.Error(), "telemetry.langfuse.prompts") {
			t.Errorf("error = %v, want one naming the prompts block", err)
		}
	})

	t.Run("invalid durations are caught at load", func(t *testing.T) {
		err := validateTelemetryConfig(withCreds(&LangfusePromptsConfig{Enabled: true, CacheTTL: "soon"}))
		if err == nil {
			t.Fatal("error = nil, want a duration parse error")
		}
		if !strings.Contains(err.Error(), "cacheTTL") {
			t.Errorf("error = %v, want one naming cacheTTL", err)
		}
	})

	t.Run("a disabled prompts block is not validated", func(t *testing.T) {
		cfg := &TelemetryConfig{Langfuse: &LangfuseConfig{
			Prompts: &LangfusePromptsConfig{CacheTTL: "soon"},
		}}
		if err := validateTelemetryConfig(cfg); err != nil {
			t.Fatalf("unexpected error for a disabled block: %v", err)
		}
	})
}

// TestConfigLoad_LangfusePromptKeys proves the new keys survive the actual
// load path. Config is read through viper, which lowercases JSON keys, so
// camelCase fields only bind because mapstructure matches case-insensitively
// — worth pinning rather than assuming, since a silently-dropped key here
// would look exactly like "Langfuse is being ignored".
func TestConfigLoad_LangfusePromptKeys(t *testing.T) {
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk-test")
	t.Setenv("LANGFUSE_SECRET_KEY", "sk-test")
	// A configured agent needs a reachable provider or validation rejects
	// it before the fields under test are ever compared.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	dir := t.TempDir()
	cfgJSON := `{
  "agents": {
    "managed": {
      "langfusePromptPath": "agents/managed/system",
      "langfusePromptLabel": "staging"
    }
  },
  "telemetry": {
    "langfuse": {
      "prompts": {
        "enabled": true,
        "label": "canary",
        "cacheTTL": "5m",
        "timeout": "2s",
        "warmup": false
      }
    }
  }
}`
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	Reset()
	t.Cleanup(Reset)
	if _, err := Load(dir, false); err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	cfg := Get()

	agent, ok := cfg.Agents["managed"]
	if !ok {
		t.Fatal("agent 'managed' missing from the loaded config")
	}
	if agent.LangfusePromptPath != "agents/managed/system" {
		t.Errorf("LangfusePromptPath = %q", agent.LangfusePromptPath)
	}
	if agent.LangfusePromptLabel != "staging" {
		t.Errorf("LangfusePromptLabel = %q", agent.LangfusePromptLabel)
	}

	p := cfg.Telemetry.Langfuse.Prompts
	if p == nil {
		t.Fatal("telemetry.langfuse.prompts missing from the loaded config")
	}
	if !p.Enabled || p.Label != "canary" || p.CacheTTL != "5m" || p.Timeout != "2s" {
		t.Errorf("prompts = %+v", *p)
	}
	if p.WarmupEnabled() {
		t.Error("WarmupEnabled() = true, want false — explicit warmup:false must bind")
	}
}
