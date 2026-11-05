package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"

	"github.com/opencode-ai/opencode/internal/config"
)

func TestToolPolicyRejectsAnyBusySession(t *testing.T) {
	id := "service-control-test"
	if !acquireSessionSlot(id, cronLock{}) {
		t.Fatal("slot held")
	}
	defer releaseSessionSlot(id)
	called := false
	err := WithIdleToolPolicy(func() error { called = true; return nil })
	if err != ErrAgentBusy || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestReconnectRetiresOldConnection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("OPENCODE_PROFILE", "")
	path := filepath.Join(dir, ".opencode.json")
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	previous := viper.ConfigFileUsed()
	viper.SetConfigFile(path)
	t.Cleanup(func() { viper.SetConfigFile(previous) })
	factory := okFactory()
	reg := newFakeMCPRegistry(t, "test", config.MCPServer{Type: config.MCPStdio}, factory)
	cfg := config.ResolveMCPServers()["test"]
	old, err := reg.pool.acquire(context.Background(), "test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Discovery may yield zero tools; the important pool lifecycle remains valid.
	_ = ReconnectMCP(reg, "test")
	next, err := reg.pool.acquire(context.Background(), "test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.pool.release(next, false)
	if old == next || !old.retired {
		t.Fatal("reconnect reused retired connection")
	}
	reg.pool.release(old, false)
}
