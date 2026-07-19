package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/spf13/viper"
)

// clearProviderEnv blanks every environment signal the provider-default
// resolution looks at, so tests control exactly which provider "exists".
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"VERTEXAI_PROJECT", "VERTEXAI_LOCATION",
		"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_REGION", "GOOGLE_CLOUD_LOCATION",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_PROFILE",
		"AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"YANDEXCLOUD_API_KEY", "YANDEXCLOUD_FOLDER_ID",
		"MOONSHOT_API_KEY", "KIMI_API_KEY",
	} {
		t.Setenv(v, "")
	}
}

func TestKimiAPIKeyFromEnv(t *testing.T) {
	clearProviderEnv(t)

	t.Setenv("MOONSHOT_API_KEY", "moon-key")
	t.Setenv("KIMI_API_KEY", "alias-key")
	if got := kimiAPIKeyFromEnv(); got != "moon-key" {
		t.Fatalf("MOONSHOT_API_KEY must win, got %q", got)
	}

	t.Setenv("MOONSHOT_API_KEY", "")
	if got := kimiAPIKeyFromEnv(); got != "alias-key" {
		t.Fatalf("expected KIMI_API_KEY fallback, got %q", got)
	}

	t.Setenv("KIMI_API_KEY", "")
	if got := kimiAPIKeyFromEnv(); got != "" {
		t.Fatalf("expected empty resolution, got %q", got)
	}
}

func TestGetProviderAPIKeyKimi(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("MOONSHOT_API_KEY", "moon-key")
	if got := getProviderAPIKey(models.ProviderKimi); got != "moon-key" {
		t.Fatalf("getProviderAPIKey(kimi) = %q, want moon-key", got)
	}
}

func TestSetProviderDefaultsKimiOnly(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("MOONSHOT_API_KEY", "moon-key")

	viper.Reset()
	t.Cleanup(viper.Reset)
	setProviderDefaults()

	if got := viper.GetString("providers.kimi.apiKey"); got != "moon-key" {
		t.Fatalf("providers.kimi.apiKey default = %q, want moon-key", got)
	}
	// Defaults are stored as typed models.ModelID values (read in
	// production via Unmarshal/mapstructure), so compare via Get.
	if got := viper.Get("agents.coder.model"); got != models.KimiK3 {
		t.Fatalf("agents.coder.model default = %v, want %v", got, models.KimiK3)
	}
}

func TestSetProviderDefaultsKimiLosesToAnthropic(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "ant-key")
	t.Setenv("MOONSHOT_API_KEY", "moon-key")

	viper.Reset()
	t.Cleanup(viper.Reset)
	setProviderDefaults()

	// Both providers get keys, but agent defaults resolve to the
	// higher-priority provider.
	if got := viper.GetString("providers.kimi.apiKey"); got != "moon-key" {
		t.Fatalf("providers.kimi.apiKey default = %q, want moon-key", got)
	}
	if got := viper.Get("agents.coder.model"); got == models.KimiK3 {
		t.Fatalf("agents.coder.model must not default to kimi when anthropic key present")
	}
}

// TestConfig_KimiProviderViperRoundTrip exercises the full on-disk-JSON →
// viper → Config path for the providers map (per the repo contract: viper
// case-folds map keys, so pure json.Unmarshal tests can pass while the
// loader mangles in production). "kimi" is already lowercase, so this locks
// in that the key survives ingestion and lands under models.ProviderKimi.
func TestConfig_KimiProviderViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{"providers":{"kimi":{"apiKey":"file-key","baseURL":"http://proxy.local/anthropic"}},"agents":{"coder":{"model":"kimi.kimi-k3"}}}`
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
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	p, ok := c.Providers[models.ProviderKimi]
	if !ok {
		t.Fatalf("providers.kimi lost in viper round-trip; got keys %v", c.Providers)
	}
	if p.APIKey != "file-key" || p.BaseURL != "http://proxy.local/anthropic" {
		t.Fatalf("provider fields mangled: %+v", p)
	}
	if c.Agents[AgentCoder].Model != models.KimiK3 {
		t.Fatalf("agent model mangled: %+v", c.Agents[AgentCoder])
	}
}

func TestValidateAgentKimiEffortDefaultsToMax(t *testing.T) {
	clearProviderEnv(t)
	c := &Config{
		Agents: map[AgentName]Agent{
			AgentCoder: {Model: models.KimiK3},
		},
		Providers: map[models.ModelProvider]Provider{
			models.ProviderKimi: {APIKey: "test-key"},
		},
	}

	if err := validateAgent(c, AgentCoder, c.Agents[AgentCoder]); err != nil {
		t.Fatalf("validateAgent: %v", err)
	}
	if got := c.Agents[AgentCoder].ReasoningEffort; got != "max" {
		t.Fatalf("kimi agent effort = %q, want max", got)
	}
}

func TestValidateAgentKimiEffortExplicitPassesThrough(t *testing.T) {
	clearProviderEnv(t)
	c := &Config{
		Agents: map[AgentName]Agent{
			AgentCoder: {Model: models.KimiK3, ReasoningEffort: "max"},
		},
		Providers: map[models.ModelProvider]Provider{
			models.ProviderKimi: {APIKey: "test-key"},
		},
	}

	if err := validateAgent(c, AgentCoder, c.Agents[AgentCoder]); err != nil {
		t.Fatalf("validateAgent: %v", err)
	}
	if got := c.Agents[AgentCoder].ReasoningEffort; got != "max" {
		t.Fatalf("explicit effort mangled: %q", got)
	}
}
