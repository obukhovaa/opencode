package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestConfig_ToolCallTimeoutViperRoundTrip(t *testing.T) {
	dir := t.TempDir()
	body := `{"toolCallTimeoutSeconds":600,"agents":{"Neo":{"model":"claude-4-sonnet","toolCallTimeoutSeconds":0},"coder":{"model":"claude-4-sonnet"}}}`
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
	var loaded Config
	if err := v.Unmarshal(&loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	prev := cfg
	cfg = &loaded
	t.Cleanup(func() { cfg = prev })

	if got := ToolCallTimeout("neo"); got != 0 {
		t.Errorf("neo = %s, want 0 (explicitly disabled)", got)
	}
	if got := ToolCallTimeout(AgentCoder); got != 600*time.Second {
		t.Errorf("coder = %s, want the top-level 600s", got)
	}
	loaded.ToolCallTimeoutSeconds = nil
	if got := ToolCallTimeout(AgentCoder); got != DefaultToolCallTimeoutSeconds*time.Second {
		t.Errorf("coder = %s, want the default", got)
	}
}

func TestToolCallTimeoutForRegistryValue(t *testing.T) {
	intp := func(v int) *int { return &v }
	tests := []struct {
		name     string
		top      *int
		agentCfg *int
		registry *int
		want     time.Duration
	}{
		{name: "default", want: DefaultToolCallTimeoutSeconds * time.Second},
		{name: "frontmatter value beats the top level", top: intp(600), registry: intp(300), want: 300 * time.Second},
		{name: "frontmatter zero disables", top: intp(600), registry: intp(0), want: 0},
		{name: "agent config beats frontmatter", registry: intp(300), agentCfg: intp(60), want: 60 * time.Second},
		{name: "negative frontmatter value is ignored", top: intp(600), registry: intp(-1), want: 600 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := cfg
			cfg = &Config{ToolCallTimeoutSeconds: tt.top, Agents: map[AgentName]Agent{"neo": {ToolCallTimeoutSeconds: tt.agentCfg}}}
			t.Cleanup(func() { cfg = prev })
			if got := ToolCallTimeoutFor("neo", tt.registry); got != tt.want {
				t.Errorf("ToolCallTimeoutFor = %s, want %s", got, tt.want)
			}
		})
	}
}
