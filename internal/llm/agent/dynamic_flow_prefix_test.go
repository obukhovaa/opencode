package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

func TestDynamicPrimarySerializedPrefixMatchesOrdinaryTurn(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "fallback"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) { testDynamicPrimarySerializedPrefix(t, native) })
	}
}
func testDynamicPrimarySerializedPrefix(t *testing.T, native bool) {
	original := models.SupportedModels[models.Claude45Haiku]
	model := original
	model.SupportsToolSearch = native
	models.SupportedModels[models.Claude45Haiku] = model
	t.Cleanup(func() { models.SupportedModels[models.Claude45Haiku] = original })
	requests := make(chan map[string]json.RawMessage, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var payload map[string]json.RawMessage
		if err = json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
		}
		requests <- payload
		// Nonretryable local response: this test asserts the real serialized request,
		// not provider generation. No external service or model is contacted.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"capture complete"}}`))
	}))
	defer server.Close()
	work := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte("stable project context"), 0600))
	loadConfigIn(t, work)
	cfg := config.Get()
	cfg.ContextPaths = []string{"AGENTS.md"}
	cfg.Providers[models.ProviderAnthropic] = config.Provider{APIKey: "test-key", BaseURL: server.URL}
	for _, id := range []config.AgentName{config.AgentDescriptor, config.AgentSummarizer} {
		entry := cfg.Agents[id]
		entry.Model = models.Claude45Haiku
		cfg.Agents[id] = entry
	}
	cfg.Agents["cache-primary"] = config.Agent{Model: models.Claude45Haiku, Mode: config.AgentModeAgent, Prompt: "Stable primary prompt", DynamicFlow: &config.DynamicFlow{Enabled: true}, SelfCompact: &config.SelfCompact{Enabled: true, MinTokens: 1000}, DeferredTools: map[string]bool{"read": true, "glob": true}}
	cfg.Agents["cache-specialist"] = config.Agent{Model: models.Claude45Haiku, Mode: config.AgentModeSubagent, Prompt: "Different specialist prompt"}
	agentregistry.InvalidateRegistry()
	t.Cleanup(agentregistry.InvalidateRegistry)
	factory := &agentFactory{registry: agentregistry.GetRegistry(), permissions: permission.NewPermissionService(), mcpRegistry: seamStubMCPRegistry{Broker: pubsub.NewBroker[MCPServerEvent]()}, stepCache: map[stepCacheKey]Service{}}
	primarySvc, err := factory.NewAgent(context.Background(), "cache-primary", nil, "", false, nil, nil, contextfile.TemplateVars{}, ModelOverride{})
	require.NoError(t, err)
	primary := primarySvc.(*agent)
	primary.resolveTools()
	for _, tool := range primary.Tools() {
		if w, ok := tool.(*tools.DeferredWrapper); ok {
			if tool.Info().Name == "read" {
				w.Activate("root")
			} else if tool.Info().Name == "glob" {
				w.Activate("unrelated")
			}
		}
	}
	require.False(t, hasStructOutputTool(primary.Tools()), "reserved tool must not force schema completion on ordinary turns")
	request := func(a *agent, sid, prompt string) map[string]json.RawMessage {
		ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, sid)
		a.flowPrefix.activate(sid)
		for range a.provider.StreamResponse(ctx, []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: prompt}}}}, a.Tools()) {
		}
		return <-requests
	}
	ordinary := request(primary, "root", "ordinary task")
	builder := request(primary, "root", "build a dynamic workflow")
	require.Equal(t, string(ordinary["tools"]), string(builder["tools"]))
	require.Equal(t, string(ordinary["system"]), string(builder["system"]))
	ctx := WithPrimaryFlowPrefix(context.Background(), primary, "root")
	expected := ordinary
	// Changes on disk or to future managed prompts must not alter the selected
	// primary's prefix halfway through a generated run.
	require.NoError(t, os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte("context changed during task"), 0600))
	for i, field := range []string{"plan", "verdict", ""} {
		var schema map[string]any
		if field != "" {
			schema = map[string]any{"type": "object", "properties": map[string]any{field: map[string]any{"type": "string"}}, "required": []any{field}}
		}
		svc, err := factory.NewAgent(ctx, "cache-primary", schema, field+"-step", false, nil, nil, contextfile.TemplateVars{FlowID: "dynamic", FlowStep: field}, ModelOverride{})
		require.NoError(t, err)
		built := svc.(*agent)
		for _, stepTool := range built.Tools() {
			if stepTool.Info().Name == tools.ToolSearchToolName || stepTool.Info().Name == tools.CompactSessionToolName {
				for _, rootTool := range primary.Tools() {
					if rootTool.Info().Name == stepTool.Info().Name {
						require.NotSame(t, rootTool, stepTool, "bound callbacks must remain agent-local")
					}
				}
			}
		}
		req := request(built, field+"-session", "step work")
		require.Equal(t, string(expected["tools"]), string(req["tools"]), "step %d tools prefix drift", i)
		require.Equal(t, string(expected["system"]), string(req["system"]), "step %d system prefix drift", i)
		require.Equal(t, field != "", hasStructOutputTool(built.Tools()))
		if schema != nil {
			require.Contains(t, built.structOutputEnvelope, field)
		}
		reused, err := factory.NewAgent(ctx, "cache-primary", schema, field+"-step", false, nil, nil, contextfile.TemplateVars{}, ModelOverride{})
		require.NoError(t, err)
		require.Same(t, svc, reused)
		if i == 0 {
			for _, tool := range built.Tools() {
				if w, ok := tool.(*tools.DeferredWrapper); ok && tool.Info().Name == "glob" {
					w.Activate(field + "-session")
				}
			}
			expected = request(built, field+"-session", "after discovery")
			built.flowPrefix.captureActivations(built.Tools(), field+"-session")
		}
	}
	returned := request(primary, "root", "ordinary turn after flow")
	require.Equal(t, string(expected["tools"]), string(returned["tools"]), "returning to ordinary chat dropped tools learned inside the flow")
	require.Equal(t, string(expected["system"]), string(returned["system"]))
	// A resumed run captures a new primary contract. Cached step agents from
	// the preceding run must not silently retain a now-disabled tool.
	resumedCtx := WithPrimaryFlowPrefix(context.Background(), primary, "root")
	resumedPrefix := primaryFlowPrefixFrom(resumedCtx, "cache-primary")
	filtered := resumedPrefix.tools[:0]
	for _, tool := range resumedPrefix.tools {
		if tool.Info().Name != "read" {
			filtered = append(filtered, tool)
		}
	}
	resumedPrefix.tools = filtered
	resumed, err := factory.NewAgent(resumedCtx, "cache-primary", nil, "plan-step", false, nil, nil, contextfile.TemplateVars{}, ModelOverride{})
	require.NoError(t, err)
	for _, tool := range resumed.(*agent).Tools() {
		require.NotEqual(t, "read", tool.Info().Name, "stale step cache reused prior primary tool contract")
	}
	specialist, err := factory.NewAgent(ctx, "cache-specialist", nil, "specialist", false, nil, nil, contextfile.TemplateVars{}, ModelOverride{})
	require.NoError(t, err)
	specialist.(*agent).resolveTools()
	require.Nil(t, specialist.(*agent).flowPrefix, "explicit specialist must retain its own prefix")
}

func TestDynamicPrimaryCopiesOnlyInitiatingSessionDeferredActivation(t *testing.T) {
	seq := &atomic.Int64{}
	first := tools.WrapDeferred(newMock("first", false), seq)
	second := tools.WrapDeferred(newMock("second", false), seq)
	other := tools.WrapDeferred(newMock("other", false), seq)
	second.Activate("root")
	first.Activate("root")
	other.Activate("unrelated")
	p := &primaryFlowPrefix{activated: []*tools.DeferredWrapper{second, first}}
	p.activate("step")
	got := tools.SerializableFor("step", []tools.BaseTool{first, second, other})
	require.Len(t, got, 2)
	require.Equal(t, "second", got[0].Info().Name)
	require.Equal(t, "first", got[1].Info().Name)
	_, active := other.ActivatedAt("step")
	require.False(t, active)
}

func TestDynamicPrimaryDoesNotReserveUnsupportedSchemaTool(t *testing.T) {
	for _, mode := range []string{"gemini", "explicit-tool"} {
		t.Run(mode, func(t *testing.T) {
			loadConfigIn(t, t.TempDir())
			model := models.Claude45Haiku
			delivery := "tool"
			if mode == "gemini" {
				delivery = "message"
				for id, m := range models.SupportedModels {
					if m.Provider == models.ProviderGemini {
						model = id
						break
					}
				}
			}
			config.Get().Agents["cache-policy"] = config.Agent{Model: model, Mode: config.AgentModeAgent, DynamicFlow: &config.DynamicFlow{Enabled: true}, StructOutputSchemaDelivery: delivery}
			agentregistry.InvalidateRegistry()
			t.Cleanup(agentregistry.InvalidateRegistry)
			reg := agentregistry.GetRegistry()
			info, ok := reg.Get("cache-policy")
			require.True(t, ok)
			perms := permission.NewPermissionService()
			mcp := seamStubMCPRegistry{Broker: pubsub.NewBroker[MCPServerEvent]()}
			factory := &agentFactory{registry: reg, permissions: perms, mcpRegistry: mcp}
			var ordinary []tools.BaseTool
			for tool := range NewToolSet(&info, reg, perms, nil, nil, nil, nil, mcp, factory, "") {
				ordinary = append(ordinary, tool)
				require.NotEqual(t, tools.StructOutputToolName, tool.Info().Name)
			}
			p := &primaryFlowPrefix{tools: ordinary}
			info.Output = &agentregistry.Output{Schema: map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}}}
			found := false
			for tool := range p.toolsFor(&info, "", true) {
				if tool.Info().Name == tools.StructOutputToolName {
					found = true
					require.Contains(t, tool.Info().Parameters, "answer")
				}
			}
			require.True(t, found, "fallback must still register active schema tool")
		})
	}
}
