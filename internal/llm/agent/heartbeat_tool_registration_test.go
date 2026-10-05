package agent

import (
	"context"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/heartbeat"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// heartbeatOptInRegistry enables nothing but, when optIn is set, an
// explicit `"heartbeat": true`.
type heartbeatOptInRegistry struct {
	agentregistry.Registry
	optIn bool
}

func (r heartbeatOptInRegistry) IsToolEnabled(string, string) bool { return false }

func (r heartbeatOptInRegistry) IsToolExplicitlyEnabled(_, name string) bool {
	return r.optIn && name == tools.HeartbeatToolName
}

func (r heartbeatOptInRegistry) HasTools(string) bool { return true }

type lateHeartbeatFactory struct {
	AgentFactory
	c tools.HeartbeatConfigurer
}

func (f *lateHeartbeatFactory) HeartbeatConfigurer() tools.HeartbeatConfigurer { return f.c }

type recordingConfigurer struct{ applied bool }

func (r *recordingConfigurer) HeartbeatStatus(context.Context, string) (string, error) {
	return "status", nil
}

func (r *recordingConfigurer) ApplyHeartbeat(context.Context, string, heartbeat.Command) (string, error) {
	r.applied = true
	return "applied", nil
}

func heartbeatToolOf(t *testing.T, reg agentregistry.Registry, f AgentFactory) tools.BaseTool {
	t.Helper()
	info := &agentregistry.AgentInfo{ID: "daemon", Mode: config.AgentModeAgent}
	var found tools.BaseTool
	for tool := range NewToolSet(info, reg, nil, nil, nil, nil, nil, nil, f, "") {
		if tool.Info().Name == tools.HeartbeatToolName {
			found = tool
		}
	}
	return found
}

func TestHeartbeatToolIsOptIn(t *testing.T) {
	if tool := heartbeatToolOf(t, heartbeatOptInRegistry{}, &lateHeartbeatFactory{}); tool != nil {
		t.Fatal("the heartbeat tool must not register without an explicit opt-in")
	}
}

// TestHeartbeatToolReachesALateConfigurer mirrors serve: the primary
// agent's tool set is built before the bridge installs its configurer.
func TestHeartbeatToolReachesALateConfigurer(t *testing.T) {
	f := &lateHeartbeatFactory{}
	tool := heartbeatToolOf(t, heartbeatOptInRegistry{optIn: true}, f)
	if tool == nil {
		t.Fatal("the heartbeat tool did not register for an opted-in primary agent")
	}
	c := &recordingConfigurer{}
	f.c = c // installed after the tool set was built

	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, "S1")
	resp, err := tool.Run(ctx, tools.ToolCall{Input: `{"state":"on"}`})
	if err != nil || resp.IsError || !c.applied {
		t.Fatalf("late configurer not reached: %+v %v", resp, err)
	}
}
