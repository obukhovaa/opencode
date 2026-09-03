package agent

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/langfuse"
)

// initTestPromptClient points the process-global prompt client at a test
// server that serves `text` for any name, and restores a disabled client
// afterwards so a later test cannot resolve against a closed server.
func initTestPromptClient(t *testing.T, text string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/public/v2/prompts/")
		_, _ = w.Write([]byte(`{"name":"` + name + `","version":4,"type":"text","prompt":"` + text + `"}`))
	}))
	t.Cleanup(srv.Close)

	require.True(t, langfuse.InitPrompts("pk-test", "sk-test", srv.URL, langfuse.PromptOptions{}),
		"test prompt client did not enable")
	t.Cleanup(func() {
		// Empty credentials yield a disabled client, which is the closest
		// thing to "uninitialised" the package exposes.
		langfuse.InitPrompts("", "", "", langfuse.PromptOptions{})
	})
}

// setTestAgents installs agent config overrides and rebuilds the registry.
//
// It goes through loadConfigIn rather than calling config.Load directly, and
// that is load-bearing: config.Reset() clears only the Config struct, while
// viper's state is process-global and keeps whatever a previous Load merged
// into it. A Load against the real $HOME therefore pulls the developer's
// ~/.opencode.json into viper and leaves it there for every later test in
// this package. loadConfigIn points HOME and XDG_CONFIG_HOME at an empty
// directory, which is what makes these tests hermetic.
func setTestAgents(t *testing.T, agents map[config.AgentName]config.Agent) {
	t.Helper()
	loadConfigIn(t, t.TempDir())
	cfg := config.Get()
	maps.Copy(cfg.Agents, agents)
	agentregistry.InvalidateRegistry()
	t.Cleanup(agentregistry.InvalidateRegistry)
}

// TestResolveManagedPrompt covers the single place an agent's prompt source
// is turned into text.
func TestResolveManagedPrompt(t *testing.T) {
	t.Run("an inline prompt is returned verbatim", func(t *testing.T) {
		got, err := resolveManagedPrompt("planner", agentregistry.AgentInfo{Prompt: "inline text"})
		require.NoError(t, err)
		assert.Equal(t, "inline text", got)
	})

	t.Run("no prompt at all yields empty, which the builder reads as the built-in", func(t *testing.T) {
		got, err := resolveManagedPrompt("planner", agentregistry.AgentInfo{})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("a reference is fetched", func(t *testing.T) {
		initTestPromptClient(t, "managed text")
		got, err := resolveManagedPrompt("planner", agentregistry.AgentInfo{
			LangfusePromptPath: "agents/planner/system",
		})
		require.NoError(t, err)
		assert.Equal(t, "managed text", got)
	})

	t.Run("a whitespace-only path is not a reference", func(t *testing.T) {
		// No prompt client is initialised here on purpose: an untrimmed
		// check would call Resolve and fail with ErrPromptsDisabled.
		got, err := resolveManagedPrompt("planner", agentregistry.AgentInfo{
			Prompt:             "inline text",
			LangfusePromptPath: "   ",
		})
		require.NoError(t, err)
		assert.Equal(t, "inline text", got)
	})

	// The whole point of BasePrompt: a referencing agent that cannot reach
	// Langfuse must fail loudly, not run on the built-in prompt for its
	// name. That failure reads as "Langfuse is being ignored", which is far
	// harder to trace than a construction error naming the path.
	t.Run("an unresolvable reference is an error, never a fallback", func(t *testing.T) {
		got, err := resolveManagedPrompt("planner", agentregistry.AgentInfo{
			LangfusePromptPath: "agents/planner/system",
		})
		require.Error(t, err, "prompt management is not enabled here, so this must fail")
		assert.Empty(t, got)
		assert.Contains(t, err.Error(), "agents/planner/system", "the error must name the path")
		assert.Contains(t, err.Error(), "planner", "the error must name the agent")
	})
}

// TestResolveRegisteredPrompt_HelperAgents pins that the two built-in helper
// agents honour a langfusePromptPath.
//
// newAgent builds their providers by NAME rather than through NewAgent, so
// their prompt source has to be resolved separately. Without it a
// langfusePromptPath on summarizer/descriptor was silently ignored — while
// an inline `prompt` on the very same agent worked — so compaction and title
// generation kept running the compiled-in prompt with no warning.
func TestResolveRegisteredPrompt_HelperAgents(t *testing.T) {
	for _, name := range []config.AgentName{config.AgentSummarizer, config.AgentDescriptor} {
		t.Run(string(name)+" resolves its reference", func(t *testing.T) {
			initTestPromptClient(t, "managed helper prompt")
			setTestAgents(t, map[config.AgentName]config.Agent{
				name: {LangfusePromptPath: "agents/" + string(name) + "/system"},
			})

			got, err := resolveRegisteredPrompt(agentregistry.GetRegistry(), name)
			require.NoError(t, err)
			assert.Equal(t, "managed helper prompt", got,
				"a langfusePromptPath on %s must reach the provider, not be dropped", name)
		})

		t.Run(string(name)+" with an inline prompt is unchanged", func(t *testing.T) {
			setTestAgents(t, map[config.AgentName]config.Agent{
				name: {Prompt: "inline helper prompt"},
			})

			got, err := resolveRegisteredPrompt(agentregistry.GetRegistry(), name)
			require.NoError(t, err)
			assert.Equal(t, "inline helper prompt", got)
		})
	}

	t.Run("an unknown agent name yields empty rather than an error", func(t *testing.T) {
		setTestAgents(t, nil)
		got, err := resolveRegisteredPrompt(agentregistry.GetRegistry(), "no-such-agent")
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}
