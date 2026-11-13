package agent

import (
	"context"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

type dynamicFlowRegistry struct {
	agentregistry.Registry
	deny bool
}

func (r dynamicFlowRegistry) IsToolEnabled(_, name string) bool {
	return !r.deny && name == tools.FlowToolName
}
func (dynamicFlowRegistry) IsToolExplicitlyEnabled(string, string) bool { return false }
func (dynamicFlowRegistry) HasTools(string) bool                        { return true }
func TestDynamicFlowToolRegistration(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	for _, tc := range []struct {
		name                string
		mode                config.AgentMode
		enabled, deny, want bool
	}{
		{"primary", config.AgentModeAgent, true, false, true},
		{"subagent", config.AgentModeSubagent, true, false, false},
		{"disabled", config.AgentModeAgent, false, false, false},
		{"excluded", config.AgentModeAgent, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := agentregistry.AgentInfo{ID: "planner", Mode: tc.mode, DynamicFlow: &config.DynamicFlow{Enabled: tc.enabled}}
			found := false
			for tool := range NewToolSet(&info, dynamicFlowRegistry{deny: tc.deny}, nil, nil, nil, nil, nil, nil, &lateHeartbeatFactory{}, "") {
				found = found || tool.Info().Name == tools.FlowToolName
			}
			if found != tc.want {
				t.Fatalf("registered=%v want %v", found, tc.want)
			}
		})
	}
}
func TestDynamicFlowCacheScope(t *testing.T) {
	a := &cachedFlowAgent{}
	b := &cachedFlowAgent{}
	factory := &agentFactory{stepCache: map[stepCacheKey]Service{
		{scope: "root-a", stepID: "review", agentID: "coder"}: a,
		{scope: "root-b", stepID: "review", agentID: "coder"}: b,
	}}
	for _, tc := range []struct {
		root string
		want Service
	}{{"root-a", a}, {"root-b", b}} {
		got, err := factory.NewAgent(tools.WithFlowAgentScope(context.Background(), tc.root), "coder", nil, "review", false, nil, nil, contextfile.TemplateVars{}, ModelOverride{})
		if err != nil || got != tc.want {
			t.Fatalf("root %s cached wrong agent: %v %v", tc.root, got, err)
		}
	}
}

type cachedFlowAgent struct{ Service }
