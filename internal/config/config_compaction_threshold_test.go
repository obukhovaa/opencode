package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/spf13/viper"
)

// Viper case-folds map keys, so an agent named in mixed case must still carry
// its compactionThreshold after the real loader path, not just json.Unmarshal.
func TestConfig_CompactionThresholdViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{"agents":{"Neo":{"model":"claude-4-sonnet","compactionThreshold":0.4},"coder":{"model":"claude-4-sonnet"}}}`
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

	neo, ok := cfg.Agents["neo"]
	if !ok {
		t.Fatalf("agent 'neo' not loaded under its case-folded key; agents: %v", cfg.Agents)
	}
	if neo.CompactionThreshold != 0.4 {
		t.Errorf("neo compactionThreshold = %v, want 0.4", neo.CompactionThreshold)
	}
	if got := cfg.Agents[AgentCoder].CompactionThreshold; got != 0 {
		t.Errorf("coder compactionThreshold = %v, want 0 (unset inherits the default)", got)
	}
}

func TestValidateAgentCompactionThreshold(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"unset stays unset", 0, 0},
		{"in range kept", 0.4, 0.4},
		{"upper bound kept", 1, 1},
		{"negative zeroed", -0.2, 0},
		{"above one zeroed", 1.5, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderEnv(t)
			c := &Config{
				Agents: map[AgentName]Agent{
					AgentCoder: {Model: models.KimiK3, CompactionThreshold: tc.in},
				},
				Providers: map[models.ModelProvider]Provider{
					models.ProviderKimi: {APIKey: "test-key"},
				},
			}
			if err := validateAgent(c, AgentCoder, c.Agents[AgentCoder]); err != nil {
				t.Fatalf("validateAgent: %v", err)
			}
			if got := c.Agents[AgentCoder].CompactionThreshold; got != tc.want {
				t.Errorf("compactionThreshold = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfig_SummarizerMaxInputTokens(t *testing.T) {
	dir := t.TempDir()
	body := `{"agents":{"Neo":{"model":"claude-4-sonnet","summarizerMaxInputTokens":300000},"summarizer":{"model":"claude-4-sonnet","summarizerMaxInputTokens":200000},"coder":{"model":"claude-4-sonnet"}}}`
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
	if got := cfg.Agents["neo"].SummarizerMaxInputTokens; got != 300_000 {
		t.Errorf("neo summarizerMaxInputTokens = %v, want 300000", got)
	}
	if got := cfg.Agents[AgentSummarizer].SummarizerMaxInputTokens; got != 200_000 {
		t.Errorf("summarizer summarizerMaxInputTokens = %v, want 200000", got)
	}
	if got := cfg.Agents[AgentCoder].SummarizerMaxInputTokens; got != 0 {
		t.Errorf("coder summarizerMaxInputTokens = %v, want 0 (unset: no cap of its own)", got)
	}

	for _, tc := range []struct {
		in, want int64
	}{{0, 0}, {300_000, 300_000}, {-1, 0}} {
		clearProviderEnv(t)
		c := &Config{
			Agents:    map[AgentName]Agent{AgentCoder: {Model: models.KimiK3, SummarizerMaxInputTokens: tc.in}},
			Providers: map[models.ModelProvider]Provider{models.ProviderKimi: {APIKey: "test-key"}},
		}
		if err := validateAgent(c, AgentCoder, c.Agents[AgentCoder]); err != nil {
			t.Fatalf("validateAgent: %v", err)
		}
		if got := c.Agents[AgentCoder].SummarizerMaxInputTokens; got != tc.want {
			t.Errorf("validate(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
