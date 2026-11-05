package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

func TestOrdinaryFlowToolRejectsStandaloneRootStep(t *testing.T) {
	for _, scope := range []string{"root", "outer-flow-root"} {
		t.Run(scope, func(t *testing.T) {
			// No dependencies are installed: rejecting recursion must not query
			// sessions, resolve OpenSpec, persist a manifest or start execution.
			a := &App{}
			t.Setenv("PATH", t.TempDir())
			ctx := tools.WithFlowAgentScope(context.Background(), scope)
			ctx = context.WithValue(ctx, tools.SessionIDContextKey, "root")
			ctx = context.WithValue(ctx, tools.AgentIDContextKey, config.AgentCoder)
			input, err := json.Marshal(tools.FlowRequest{
				Operation: "create", Complexity: "XS",
				Definition: "flow:\n  steps:\n    - id: work\n      prompt: work\n",
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := tools.NewFlowTool(a.submitOrdinaryFlow).Run(ctx, tools.ToolCall{Input: string(input)})
			if err != nil || !result.IsError || !strings.Contains(result.Content, "recursive dynamic flows are forbidden") {
				t.Fatalf("flow-scoped root accepted: result=%+v err=%v", result, err)
			}
			if a.FlowBusy("root") {
				t.Fatal("recursive submission acquired run ownership")
			}
		})
	}
}
