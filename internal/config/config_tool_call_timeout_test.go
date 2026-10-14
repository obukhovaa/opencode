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
