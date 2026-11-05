package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/install"
	"github.com/opencode-ai/opencode/internal/permission"
)

func TestMCPPolicyAllowlistConversionPreservesScope(t *testing.T) {
	info := agentregistry.AgentInfo{AllowTools: []string{"read", "svc_*"}}
	enabled, _ := mcpPolicyMaps(info)
	for _, name := range []string{"read", "write", "svc_read", "unrelated_read"} {
		if permission.IsToolEnabled(name, enabled) != info.ToolEnabled(name) {
			t.Errorf("scope changed for %s", name)
		}
	}
	enabled["svc_*"] = false
	if permission.IsToolEnabled("svc_read", enabled) || !permission.IsToolEnabled("read", enabled) {
		t.Fatal("disable changed unrelated tools")
	}
	enabled["new_*"] = true
	if !permission.IsToolEnabled("new_read", enabled) || permission.IsToolEnabled("unrelated_read", enabled) {
		t.Fatal("enable widened unrelated scope")
	}
}

// The helper speaks LSP over stdio, exercising the same child-process transport
// as an installed server without downloading tools or touching the network.
func TestServiceControlsLSPHelper(t *testing.T) {
	if os.Getenv("OPENCODE_TEST_LSP_HELPER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		length := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				os.Exit(0)
			}
			if strings.TrimSpace(line) == "" {
				break
			}
			if strings.HasPrefix(line, "Content-Length:") {
				length, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
			}
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(reader, body); err != nil {
			os.Exit(0)
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &request)
		if request.Method == "exit" {
			os.Exit(0)
		}
		if len(request.ID) == 0 {
			continue
		}
		result := `null`
		if request.Method == "initialize" {
			result = `{"capabilities":{}}`
		}
		response := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, request.ID, result)
		fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(response), response)
	}
}

func TestLSPActivationAfterStartupChannelClosed(t *testing.T) {
	if config.Get() == nil {
		dir := t.TempDir()
		t.Setenv("HOME", dir)
		t.Setenv("XDG_CONFIG_HOME", dir)
		t.Setenv("OPENCODE_PROFILE", "")
		if _, err := config.Load(dir, false); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	svc := NewLspService().(*lspService)
	svc.startupDone = true
	close(svc.clientsCh)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer svc.Shutdown(context.Background())
	server := install.ResolvedServer{ID: "fixture", Env: map[string]string{"OPENCODE_TEST_LSP_HELPER": "1"}, Extensions: []string{".unmatched-test-extension"}}
	svc.createAndStartLSPClient(ctx, "fixture", server, executable, "-test.run=^TestServiceControlsLSPHelper$")
	client := svc.Clients()["fixture"]
	if client == nil || client.GetServerState() != lsp.StateReady {
		t.Fatal("live activation failed after startup channel closed")
	}

	path := filepath.Join(t.TempDir(), ".opencode.json")
	if err := os.WriteFile(path, []byte(`{"lsp":{"fixture":{"command":"fixture"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	previous := viper.ConfigFileUsed()
	viper.SetConfigFile(path)
	defer viper.SetConfigFile(previous)
	a := &App{LspService: svc}
	if err := a.DeactivateLSP("fixture"); err != nil {
		t.Fatal(err)
	}
	if svc.Clients()["fixture"] != nil {
		t.Fatal("deactivated client remained registered")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted config.Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.LSP["fixture"].Disabled || persisted.LSP["fixture"].Command != "fixture" {
		t.Fatal("deactivation did not preserve and disable server config")
	}
}

func TestMCPPolicyLoadingAgentRejectsBeforePersistenceOrDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".opencode.json")
	original := []byte(`{"agents":{"coder":{"model":"anthropic.claude-haiku-4-5"}}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	previous := viper.ConfigFileUsed()
	viper.SetConfigFile(path)
	defer viper.SetConfigFile(previous)
	a := &App{Registry: reviewFlowRegistry{}, PrimaryAgents: map[config.AgentName]agentpkg.Service{config.AgentCoder: &fakeAgent{}}}
	// Nil MCP dependencies prove no loading goroutine/reconnect is started.
	err := a.SetMCPPolicy(config.AgentCoder, "server", "disable")
	if err == nil || !strings.Contains(err.Error(), "still loading") {
		t.Fatalf("error=%v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("failed action persisted configuration")
	}
}

func TestMCPPolicyRejectsBetweenFlowSteps(t *testing.T) {
	a := &App{dynamicRuns: map[string]*dynamicRun{"root": {status: "running"}}}
	// No individual agent turn is active. The run itself still owns the frozen
	// tool contract and must exclude a policy change before its next step.
	err := a.SetMCPPolicy(config.AgentCoder, "server", "disable")
	if err == nil || !strings.Contains(err.Error(), "flow is active") {
		t.Fatalf("error=%v", err)
	}
}

type loadingPolicyRegistry struct {
	reviewFlowRegistry
	entered chan struct{}
	release chan struct{}
}

func (r loadingPolicyRegistry) Get(id string) (agentregistry.AgentInfo, bool) {
	close(r.entered)
	<-r.release
	return r.reviewFlowRegistry.Get(id)
}
func TestMCPPolicyDoesNotBlockFlowStatusDuringSlowRefresh(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	a := &App{Registry: loadingPolicyRegistry{entered: entered, release: release}, PrimaryAgents: map[config.AgentName]agentpkg.Service{config.AgentCoder: &fakeAgent{}}}
	done := make(chan error, 1)
	go func() { done <- a.SetMCPPolicy(config.AgentCoder, "server", "reconnect") }()
	<-entered
	queried := make(chan bool, 1)
	go func() { queried <- a.FlowBusy("root") }()
	select {
	case busy := <-queried:
		if busy {
			t.Error("nonexistent flow marked busy")
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("MCP refresh holds flow-status lock and freezes UI")
	}
	if _, _, _, err := a.beginFlow("root", "planning"); err == nil {
		t.Error("flow admitted during policy refresh")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("unfinished initial discovery unexpectedly accepted")
	}
	a.dynamicMu.Lock()
	changing := a.mcpPolicyChanging
	a.dynamicMu.Unlock()
	if changing {
		t.Fatal("policy reservation leaked after failure")
	}
}
