package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	registry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
)

type describedCacheTool struct {
	tools.BaseTool
	description string
}

func (t describedCacheTool) Info() tools.ToolInfo {
	info := t.BaseTool.Info()
	info.Description = t.description
	return info
}
func cacheToolStream(all ...tools.BaseTool) <-chan tools.BaseTool {
	c := make(chan tools.BaseTool, len(all))
	for _, t := range all {
		c <- t
	}
	close(c)
	return c
}
func fallbackContracts(t *testing.T, all []tools.BaseTool, session string) string {
	t.Helper()
	var infos []tools.ToolInfo
	for _, tool := range tools.SerializableFor(session, all) {
		infos = append(infos, tool.Info())
	}
	b, err := json.Marshal(infos)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMCPReconnectPreservesUnchangedPrefixAndDiscoveries(t *testing.T) {
	seq := &atomic.Int64{}
	jira := tools.WrapDeferred(newMock("jira_read", false), seq)
	slack := tools.WrapDeferred(newMock("slack_send", false), seq)
	slack.Activate("session")
	jira.Activate("session")
	router := describedCacheTool{newMock("router_send", true), "original peer snapshot"}
	a := &agent{toolsCh: cacheToolStream(router, tools.NewToolSearchTool(), jira, slack)}
	before := fallbackContracts(t, a.resolveTools(), "session")
	newSeq := &atomic.Int64{}
	incoming := cacheToolStream(describedCacheTool{newMock("router_send", true), "different live peers"}, tools.NewToolSearchTool(), tools.WrapDeferred(newMock("jira_read", false), newSeq), tools.WrapDeferred(newMock("slack_send", false), newSeq))
	if err := ReplaceMCPToolset(a, incoming, "jira"); err != nil {
		t.Fatal(err)
	}
	if after := fallbackContracts(t, a.resolveTools(), "session"); before != after {
		t.Fatalf("no-op reconnect changed prefix\nbefore=%s\nafter=%s", before, after)
	}
	if other := fallbackContracts(t, a.resolveTools(), "other"); strings.Contains(other, "jira_read") || strings.Contains(other, "slack_send") {
		t.Fatal("discovery leaked between sessions")
	}
}

func TestMCPRefreshNewDiscoveryAppendsAfterRetainedActivations(t *testing.T) {
	old := tools.WrapDeferred(newMock("other_read", false), &atomic.Int64{})
	old.Activate("session")
	a := &agent{toolsCh: cacheToolStream(tools.NewToolSearchTool(), old)}
	a.resolveTools()
	nextSeq := &atomic.Int64{}
	added := tools.WrapDeferred(newMock("jira_read", false), nextSeq)
	incoming := cacheToolStream(tools.NewToolSearchTool(), tools.WrapDeferred(newMock("other_read", false), nextSeq), added)
	if err := ReplaceMCPToolset(a, incoming, "jira"); err != nil {
		t.Fatal(err)
	}
	added.Activate("session")
	names := toolNames(tools.SerializableFor("session", a.resolveTools()))
	if len(names) != 3 || names[1] != "other_read" || names[2] != "jira_read" {
		t.Fatalf("new discovery reordered cached prefix: %v", names)
	}
}

func TestMCPPolicyChangesAreNotHiddenFromModel(t *testing.T) {
	a := &agent{toolsCh: cacheToolStream(newMock("read", true), newMock("jira_read", false))}
	before := fallbackContracts(t, a.resolveTools(), "session")
	if err := ReplaceMCPToolset(a, cacheToolStream(newMock("read", true)), "jira"); err != nil {
		t.Fatal(err)
	}
	after := fallbackContracts(t, a.resolveTools(), "session")
	if before == after || strings.Contains(after, "jira_read") {
		t.Fatal("disabled tool remained advertised")
	}
	deferred := tools.WrapDeferred(newMock("jira_read", false), &atomic.Int64{})
	if err := ReplaceMCPToolset(a, cacheToolStream(newMock("read", true), tools.NewToolSearchTool(), deferred), "jira"); err != nil {
		t.Fatal(err)
	}
	if got := fallbackContracts(t, a.resolveTools(), "session"); strings.Contains(got, "jira_read") {
		t.Fatal("newly deferred tool was eagerly advertised")
	}
}

func TestDisabledMCPToolDeniesStaleInstance(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	cfg := config.Get()
	cfg.Agents["mcp-cache-test"] = config.Agent{Tools: map[string]bool{"jira_*": true}}
	registry.InvalidateRegistry()
	t.Cleanup(registry.InvalidateRegistry)
	reg := registry.GetRegistry()
	// This instance represents a stale cached flow-step toolset.
	old := newMCPTool("jira", mcp.NewTool("read"), nil, reg, nil)
	if !registry.SetRuntimeTools(reg, "mcp-cache-test", map[string]bool{"jira_*": false}, nil) {
		t.Fatal("policy update failed")
	}
	ctx := context.WithValue(context.Background(), tools.AgentIDContextKey, config.AgentName("mcp-cache-test"))
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, "session")
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "message")
	_, err := old.Run(ctx, tools.ToolCall{Name: "jira_read", Input: "{}"})
	if err == nil {
		t.Fatal("disabled stale tool instance executed")
	}
	// Nil transport/permission dependencies additionally prove neither was used.
}

func TestMCPReconnectPreservesSerializedProviderPrefix(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	for _, native := range []bool{false, true} {
		name := "fallback"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			requests := make(chan map[string]json.RawMessage, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("request: %v", err)
					w.WriteHeader(400)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":1}}`))
			}))
			defer server.Close()
			model := models.AnthropicModels[models.Claude45Haiku]
			model.SupportsToolSearch = native
			p, err := provider.NewProvider(models.ProviderAnthropic, provider.WithAPIKey("test-key"), provider.WithBaseURL(server.URL), provider.WithModel(model), provider.WithMaxTokens(1024), provider.WithSystemMessage("stable primary agent"))
			if err != nil {
				t.Fatal(err)
			}
			seq := &atomic.Int64{}
			deferred := tools.WrapDeferred(newMock("jira_read", false), seq)
			deferred.Activate("session")
			a := &agent{toolsCh: cacheToolStream(newMock("read", true), tools.NewToolSearchTool(), deferred)}
			ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, "session")
			msgs := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}
			if _, err = p.SendMessages(ctx, msgs, a.resolveTools()); err != nil {
				t.Fatal(err)
			}
			before := <-requests
			incoming := cacheToolStream(newMock("read", true), tools.NewToolSearchTool(), tools.WrapDeferred(newMock("jira_read", false), &atomic.Int64{}))
			if err = ReplaceMCPToolset(a, incoming, "jira"); err != nil {
				t.Fatal(err)
			}
			if _, err = p.SendMessages(ctx, msgs, a.resolveTools()); err != nil {
				t.Fatal(err)
			}
			after := <-requests
			for _, field := range []string{"tools", "system"} {
				if string(before[field]) != string(after[field]) {
					t.Fatalf("%s cache block changed across no-op reconnect\nbefore=%s\nafter=%s", field, before[field], after[field])
				}
			}
			if !strings.Contains(string(after["tools"]), "cache_control") {
				t.Fatal("cache breakpoint missing")
			}
		})
	}
}

type allowCacheRegistry struct{ registry.Registry }

func (allowCacheRegistry) EvaluatePermission(string, string, string) permission.Action {
	return permission.ActionAllow
}

func TestRetainedMCPWrapperUsesReconnectedTransport(t *testing.T) {
	factory := &fakeMCPFactory{make: func(n int) *fakeMCPClient {
		return &fakeMCPClient{result: textResult("ok"), tools: []mcp.Tool{mcp.NewTool("read")}}
	}}
	reg := newFakeMCPRegistry(t, "test", config.MCPServer{Type: config.MCPStdio}, factory)
	original := newMCPTool("test", mcp.NewTool("read"), nil, allowCacheRegistry{}, reg)
	deferred := tools.WrapDeferred(original, &atomic.Int64{})
	deferred.Activate("session")
	a := &agent{toolsCh: cacheToolStream(tools.NewToolSearchTool(), deferred)}
	a.resolveTools()
	ctx := context.WithValue(context.Background(), tools.AgentIDContextKey, config.AgentCoder)
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, "session")
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "message")
	if _, err := original.Run(ctx, tools.ToolCall{Name: "test_read", Input: "{}"}); err != nil {
		t.Fatal(err)
	}
	first := factory.client(0)
	if err := ReconnectMCP(reg, "test"); err != nil {
		t.Fatal(err)
	}
	incoming := tools.WrapDeferred(newMCPTool("test", mcp.NewTool("read"), nil, allowCacheRegistry{}, reg), &atomic.Int64{})
	if err := ReplaceMCPToolset(a, cacheToolStream(tools.NewToolSearchTool(), incoming), "test"); err != nil {
		t.Fatal(err)
	}
	var retained tools.BaseTool
	for _, tool := range a.resolveTools() {
		if tool.Info().Name == "test_read" {
			retained = tool
		}
	}
	if retained != deferred {
		t.Fatal("equivalent reconnect discarded discovered wrapper")
	}
	if _, err := retained.Run(ctx, tools.ToolCall{Name: "test_read", Input: "{}"}); err != nil {
		t.Fatal(err)
	}
	if first.callCalls.Load() != 1 || factory.client(1).callCalls.Load() != 1 {
		t.Fatal("retained schema kept using retired transport")
	}
}
