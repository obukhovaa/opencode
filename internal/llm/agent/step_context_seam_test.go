package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// seamStubMCPRegistry satisfies MCPRegistry without any background
// discovery: the real registry's LoadTools goroutine outlives the test
// and would race the config.Reset cleanup.
type seamStubMCPRegistry struct {
	*pubsub.Broker[MCPServerEvent]
}

func (seamStubMCPRegistry) LoadTools(*MCPRegistryFiler) <-chan tools.BaseTool {
	ch := make(chan tools.BaseTool)
	close(ch)
	return ch
}

func (seamStubMCPRegistry) StartClient(context.Context, string) (*client.Client, error) {
	return nil, errors.New("no MCP in the seam test")
}

func (seamStubMCPRegistry) SetDiscoveryAuth(map[string]string) {}
func (seamStubMCPRegistry) LoadedServers() map[string]bool     { return nil }
func (seamStubMCPRegistry) ServerTools(string) []string        { return nil }

// TestFactoryNewAgent_StepContextReachesProviderSystemPrompt is the seam
// test for the flow-step context delivery chain. The two existing tests
// cover only its ENDS: TestRunStep_ThreadsStepContextIntoNewAgent stops at
// a stubbed AgentFactory, and TestGetAgentPromptWithOptions_StepContext
// starts at AgentPromptOptions. This one drives the REAL middle —
// agentFactory.NewAgent → infoCopy.StepContext / flowVars.Agent →
// newAgent's withStepContext/withContextVars → createAgentProvider →
// provider.WithSystemMessage — and asserts on the provider's actual
// system prompt. Deleting `withStepContext(...)` or `withContextVars(...)`
// in newAgent (agent.go) makes this test fail; before this test the whole
// suite stayed green under that mutation.
func TestFactoryNewAgent_StepContextReachesProviderSystemPrompt(t *testing.T) {
	workDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "AGENTS.md"),
		[]byte("GLOBAL-CTX-MARKER: root instructions\n"), 0o644))
	// The step file name carries a ${flow.step} token so the test also
	// pins the ContextVars leg: without the template values the entry is
	// skipped and the marker never appears.
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "STEP.step-y.md"),
		[]byte("STEP-CTX-MARKER: step instructions\n"), 0o644))

	loadConfigIn(t, workDir)
	cfg := config.Get()
	cfg.WorkingDir = workDir
	cfg.ContextPaths = []string{"AGENTS.md"}
	cfg.Providers[models.ProviderAnthropic] = config.Provider{APIKey: "test-key"}
	cfg.Agents["ctx-seam"] = config.Agent{
		Model:  models.Claude45Haiku,
		Mode:   config.AgentModeSubagent,
		Prompt: "You are the seam test agent.",
	}
	agentregistry.InvalidateRegistry()
	t.Cleanup(agentregistry.InvalidateRegistry)

	perms := permission.NewPermissionService()
	reg := agentregistry.GetRegistry()
	factory := &agentFactory{
		registry:    reg,
		permissions: perms,
		mcpRegistry: seamStubMCPRegistry{Broker: pubsub.NewBroker[MCPServerEvent]()},
		stepCache:   map[string]Service{},
	}

	stepCtx := &contextfile.StepContext{Paths: []string{"STEP.${flow.step}.md"}, Mode: "replace"}
	svc, err := factory.NewAgent(context.Background(), "ctx-seam", nil, "", false, nil,
		stepCtx, contextfile.TemplateVars{FlowID: "flow-x", FlowStep: "step-y"})
	require.NoError(t, err)

	built, ok := svc.(*agent)
	require.True(t, ok, "factory must return the concrete agent")
	// Join the background toolset resolution before the test's config
	// cleanup runs, so no NewToolSet goroutine observes a reset config.
	built.resolveTools()
	sysProvider, ok := built.provider.(interface{ SystemMessage() string })
	require.True(t, ok, "provider must expose its system message for this seam test")
	sys := sysProvider.SystemMessage()

	assert.Contains(t, sys, "STEP-CTX-MARKER",
		"the flow step's context file must reach the provider's system prompt")
	assert.NotContains(t, sys, "GLOBAL-CTX-MARKER",
		"step mode=replace must exclude the global context file")
	assert.Contains(t, sys, "You are the seam test agent.")
}
