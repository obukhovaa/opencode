package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/spf13/viper"
)

// selfCompact must survive the real loader path: viper case-folds the agent
// key and the nested object's keys.
func TestConfig_SelfCompactViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{"agents":{"Daemon":{"model":"claude-4-sonnet","selfCompact":{"enabled":true,"minTokens":150000}},"coder":{"model":"claude-4-sonnet"}}}`
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

	daemon, ok := cfg.Agents["daemon"]
	if !ok {
		t.Fatalf("agent 'daemon' not loaded under its case-folded key; agents: %v", cfg.Agents)
	}
	if sc := daemon.SelfCompact; sc == nil || !sc.Enabled || sc.MinTokens != 150000 {
		t.Errorf("daemon selfCompact = %+v, want enabled with minTokens 150000", sc)
	}
	if sc := cfg.Agents[AgentCoder].SelfCompact; sc != nil {
		t.Errorf("coder selfCompact = %+v, want nil when unset", sc)
	}
}

func TestValidateAgentSelfCompact(t *testing.T) {
	cases := []struct {
		name string
		in   *SelfCompact
		want *SelfCompact
	}{
		{"unset stays unset", nil, nil},
		{"valid kept", &SelfCompact{Enabled: true, MinTokens: 1000}, &SelfCompact{Enabled: true, MinTokens: 1000}},
		{"negative floor zeroed", &SelfCompact{Enabled: true, MinTokens: -5}, &SelfCompact{Enabled: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderEnv(t)
			c := &Config{
				Agents: map[AgentName]Agent{
					AgentCoder: {Model: models.KimiK3, SelfCompact: tc.in},
				},
				Providers: map[models.ModelProvider]Provider{
					models.ProviderKimi: {APIKey: "test-key"},
				},
			}
			if err := validateAgent(c, AgentCoder, c.Agents[AgentCoder]); err != nil {
				t.Fatalf("validateAgent: %v", err)
			}
			got := c.Agents[AgentCoder].SelfCompact
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Errorf("selfCompact = %+v, want %+v", got, tc.want)
			}
		})
	}
}
