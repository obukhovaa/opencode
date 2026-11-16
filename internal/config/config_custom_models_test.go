package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/opencode-ai/opencode/internal/llm/models"
)

func validCustomModel() CustomModel {
	return CustomModel{
		ID:               "bedrock.acme-large",
		Provider:         "bedrock",
		Wire:             "openai-chat",
		APIModel:         "us.acme.large-v1",
		CostPer1MIn:      1,
		CostPer1MOut:     4,
		ContextWindow:    200_000,
		DefaultMaxTokens: 32_000,
	}
}

func TestValidateCustomModels(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(c *CustomModel)
		wantErr string // "" = valid
	}{
		{"minimal openai-chat", func(*CustomModel) {}, ""},
		{"full anthropic-messages", func(c *CustomModel) {
			c.Wire = "anthropic-messages"
			c.CanReason, c.SupportsAdaptiveThinking, c.SupportsXHighThinking, c.SupportsMaximumThinking = true, true, true, true
			c.SupportsTaskBudget, c.SupportsToolSearch, c.RejectsForcedToolChoice, c.SupportsAttachments = true, true, true, true
			c.LongContextThreshold, c.LongContextCostMultiplier, c.LongContextOutputCostMultiplier = 200_000, 2, 1.5
			c.DefaultReasoningEffort = "XHigh"
		}, ""},
		{"reasoning openai-chat with a default effort", func(c *CustomModel) {
			c.CanReason, c.SupportsMaximumThinking, c.DefaultReasoningEffort = true, true, "max"
		}, ""},
		{"id without the bedrock prefix", func(c *CustomModel) { c.ID = "acme-large" }, "id must match"},
		{"upper-case id", func(c *CustomModel) { c.ID = "bedrock.Acme" }, "id must match"},
		{"other provider", func(c *CustomModel) { c.Provider = "openai" }, "provider"},
		{"unknown wire", func(c *CustomModel) { c.Wire = "converse" }, "wire"},
		{"missing apiModel", func(c *CustomModel) { c.APIModel = " " }, "apiModel"},
		{"negative cache price", func(c *CustomModel) { c.CostPer1MOutCached = -1 }, "costPer1MOutCached"},
		{"free output", func(c *CustomModel) { c.CostPer1MOut = 0 }, "must be positive"},
		{"no context window", func(c *CustomModel) { c.ContextWindow = 0 }, "contextWindow"},
		{"output above the window", func(c *CustomModel) { c.DefaultMaxTokens = 300_000 }, "defaultMaxTokens"},
		{"thinking without canReason", func(c *CustomModel) { c.SupportsXHighThinking = true }, "canReason"},
		{"adaptive thinking on openai-chat", func(c *CustomModel) {
			c.CanReason, c.SupportsAdaptiveThinking = true, true
		}, "anthropic-messages wire only"},
		{"tool search on openai-chat", func(c *CustomModel) { c.SupportsToolSearch = true }, "anthropic-messages wire only"},
		{"threshold without multiplier", func(c *CustomModel) { c.LongContextThreshold = 100_000 }, "longContextThreshold"},
		{"output multiplier alone", func(c *CustomModel) { c.LongContextOutputCostMultiplier = 1.5 }, "longContextThreshold"},
		{"default effort the model cannot take", func(c *CustomModel) {
			c.CanReason, c.DefaultReasoningEffort = true, "xhigh"
		}, "defaultReasoningEffort"},
		{"default effort without reasoning", func(c *CustomModel) { c.DefaultReasoningEffort = "low" }, "defaultReasoningEffort"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validCustomModel()
			tt.mutate(&c)
			got, err := validateCustomModels([]CustomModel{c})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(got) != 1 || got[0].ID != models.ModelID(c.ID) || got[0].Provider != models.ProviderBedrock || got[0].Wire != models.Wire(c.Wire) {
					t.Fatalf("converted model = %+v", got)
				}
				return
			}
			if err == nil || !errors.Is(err, ErrInvalidCustomModel) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want ErrInvalidCustomModel mentioning %q", err, tt.wantErr)
			}
		})
	}

	t.Run("duplicate ids", func(t *testing.T) {
		if _, err := validateCustomModels([]CustomModel{validCustomModel(), validCustomModel()}); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("error = %v, want a duplicate-id error", err)
		}
	})
	t.Run("name defaults to the id and the effort is lower-cased", func(t *testing.T) {
		c := validCustomModel()
		c.CanReason, c.DefaultReasoningEffort = true, "HIGH"
		got, err := validateCustomModels([]CustomModel{c})
		if err != nil {
			t.Fatal(err)
		}
		if got[0].Name != c.ID || got[0].DefaultReasoningEffort != "high" {
			t.Fatalf("model = %+v", got[0])
		}
	})
}

// loadIsolated runs the real Load against dir with an empty HOME, so neither
// the developer's global config nor a previous test's viper state leaks in.
func loadIsolated(t *testing.T, dir string) (*Config, error) {
	t.Helper()
	clearProviderEnv(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("AWS_REGION", "us-east-1")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	Reset()
	viper.Reset()
	t.Cleanup(func() { Reset(); viper.Reset() })
	return Load(dir, false)
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestConfig_CustomModelsViperRoundTrip drives customModels through the real
// loader: viper lowercases keys, so only the case-insensitive mapstructure
// match binds camelCase fields like costPer1MInCached — a json.Unmarshal test
// would pass while production silently dropped them.
func TestConfig_CustomModelsViperRoundTrip(t *testing.T) {
	dir := writeConfig(t, `{
  "providers": {"bedrock": {"apiKey": "test-key"}},
  "customModels": [{
    "id": "bedrock.acme-large", "name": "Acme Large", "provider": "bedrock", "wire": "openai-chat",
    "apiModel": "us.acme.large-v1", "costPer1MIn": 1.5, "costPer1MOut": 6, "costPer1MInCached": 1.875, "costPer1MOutCached": 0.15,
    "contextWindow": 400000, "defaultMaxTokens": 64000, "canReason": true, "supportsXHighThinking": true,
    "longContextThreshold": 200000, "longContextCostMultiplier": 2, "longContextOutputCostMultiplier": 1.5,
    "defaultReasoningEffort": "medium"
  }],
  "agents": {"coder": {"model": "bedrock.acme-large"}}
}`)
	cfg, err := loadIsolated(t, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := models.Model{
		ID: "bedrock.acme-large", Name: "Acme Large", Provider: models.ProviderBedrock, Wire: models.WireOpenAIChat,
		APIModel: "us.acme.large-v1", CostPer1MIn: 1.5, CostPer1MOut: 6, CostPer1MInCached: 1.875, CostPer1MOutCached: 0.15,
		ContextWindow: 400_000, DefaultMaxTokens: 64_000, CanReason: true, SupportsXHighThinking: true,
		LongContextThreshold: 200_000, LongContextCostMultiplier: 2, LongContextOutputCostMultiplier: 1.5,
		DefaultReasoningEffort: "medium",
	}
	if got := models.SupportedModels["bedrock.acme-large"]; got != want {
		t.Fatalf("registered model = %+v\nwant %+v", got, want)
	}
	// The agent validated against the custom model: its OpenAI-wire default
	// effort was applied.
	if coder := cfg.Agents[AgentCoder]; coder.Model != "bedrock.acme-large" || coder.ReasoningEffort != "medium" {
		t.Fatalf("coder = %+v", coder)
	}

	Reset()
	if _, ok := models.SupportedModels["bedrock.acme-large"]; ok {
		t.Fatal("Reset left the custom model registered")
	}
}

func TestConfig_CustomModelsLoadFailures(t *testing.T) {
	t.Run("invalid entry fails the load", func(t *testing.T) {
		dir := writeConfig(t, `{"customModels": [{"id": "bedrock.acme", "provider": "bedrock", "wire": "grpc", "apiModel": "x",
  "costPer1MIn": 1, "costPer1MOut": 1, "contextWindow": 1000, "defaultMaxTokens": 100}]}`)
		if _, err := loadIsolated(t, dir); err == nil || !errors.Is(err, ErrInvalidCustomModel) {
			t.Fatalf("Load error = %v, want ErrInvalidCustomModel", err)
		}
		if _, ok := models.SupportedModels["bedrock.acme"]; ok {
			t.Fatal("an invalid entry was registered")
		}
	})
	t.Run("a failed load unregisters what it registered", func(t *testing.T) {
		dir := writeConfig(t, `{"sessionProvider": {"type": "bogus"},
  "customModels": [{"id": "bedrock.acme", "provider": "bedrock", "wire": "anthropic-messages", "apiModel": "x",
  "costPer1MIn": 1, "costPer1MOut": 1, "contextWindow": 1000, "defaultMaxTokens": 100}]}`)
		if _, err := loadIsolated(t, dir); err == nil {
			t.Fatal("Load succeeded with an invalid session provider")
		}
		if _, ok := models.SupportedModels["bedrock.acme"]; ok {
			t.Fatal("the custom model outlived the failed load")
		}
	})
	t.Run("a built-in id keeps the built-in", func(t *testing.T) {
		builtin := models.SupportedModels[models.BedrockSonnet55]
		dir := writeConfig(t, `{"customModels": [{"id": "bedrock.claude-sonnet-5-5", "provider": "bedrock", "wire": "openai-chat",
  "apiModel": "x", "costPer1MIn": 99, "costPer1MOut": 99, "contextWindow": 1000, "defaultMaxTokens": 100}]}`)
		if _, err := loadIsolated(t, dir); err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := models.SupportedModels[models.BedrockSonnet55]; got != builtin {
			t.Fatalf("built-in replaced: %+v", got)
		}
		Reset()
		if _, ok := models.SupportedModels[models.BedrockSonnet55]; !ok {
			t.Fatal("Reset removed the built-in model")
		}
	})
}
