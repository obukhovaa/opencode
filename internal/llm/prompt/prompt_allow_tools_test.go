package prompt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

const (
	parallelToolUseMarker = "You can call multiple tools in a single response."
	lspSectionHeader      = "# LSP Information"
	structOutputMarker    = "You MUST use the struct_output tool"
)

// loadAgents boots a config + registry carrying the given agent definitions.
func loadAgents(t *testing.T, agents map[config.AgentName]config.Agent, mutate func(*config.Config)) {
	t.Helper()
	tmpDir := t.TempDir()
	config.Reset()
	_, err := config.Load(tmpDir, false)
	require.NoError(t, err)
	cfg := config.Get()
	for name, a := range agents {
		cfg.Agents[name] = a
	}
	if mutate != nil {
		mutate(cfg)
	}
	agentregistry.InvalidateRegistry()
	t.Cleanup(func() {
		config.Reset()
		agentregistry.InvalidateRegistry()
	})
}

// An allowlisted agent has tools by definition, so it must keep the
// parallel-tool-use and background-task sections. Before the HasTools fix the
// equivalent deny-list workaround ({"*": false} + grants) lost both.
func TestGetAgentPrompt_AllowlistedAgentKeepsToolSections(t *testing.T) {
	loadAgents(t, map[config.AgentName]config.Agent{
		"narrow": {
			Prompt:     "You are the narrow agent.",
			AllowTools: []string{"read", "grep"},
		},
	}, nil)

	got := GetAgentPrompt("narrow", models.ProviderAnthropic)

	assert.Contains(t, got, parallelToolUseMarker, "allowlisted agent has tools")
	assert.Contains(t, got, noPollHeader, "allowlisted agent has tools")
}

// The deny-list workaround the allowlist replaces: it must now be read as
// "has tools" too, otherwise migrating an agent silently changes its prompt.
func TestGetAgentPrompt_StarFalseWithGrantsKeepsToolSections(t *testing.T) {
	loadAgents(t, map[config.AgentName]config.Agent{
		"workaround": {
			Prompt: "You are the workaround agent.",
			Tools:  map[string]bool{"*": false, "read": true},
		},
	}, nil)

	got := GetAgentPrompt("workaround", models.ProviderAnthropic)

	assert.Contains(t, got, parallelToolUseMarker)
	assert.Contains(t, got, noPollHeader)
}

// The LSP prompt block follows the lsp TOOL, not the LSP config: an agent that
// cannot call lsp must not be told about diagnostics it will never see. This is
// the CD-4973-shaped leak the allowlist exists to prevent.
func TestGetAgentPrompt_LSPSectionFollowsAllowlist(t *testing.T) {
	withLSP := func(cfg *config.Config) {
		cfg.LSP = map[string]config.LSPConfig{"go": {Command: "gopls"}}
	}

	t.Run("lsp not listed", func(t *testing.T) {
		loadAgents(t, map[config.AgentName]config.Agent{
			"no-lsp": {
				Prompt:     "You are the no-lsp agent.",
				AllowTools: []string{"read"},
			},
		}, withLSP)

		got := GetAgentPrompt("no-lsp", models.ProviderAnthropic)
		assert.NotContains(t, got, lspSectionHeader)
	})

	t.Run("lsp listed", func(t *testing.T) {
		loadAgents(t, map[config.AgentName]config.Agent{
			"with-lsp": {
				Prompt:     "You are the with-lsp agent.",
				AllowTools: []string{"read", "lsp"},
			},
		}, withLSP)

		got := GetAgentPrompt("with-lsp", models.ProviderAnthropic)
		assert.Contains(t, got, lspSectionHeader)
	})
}

// Allowlist mode has no implicit grants: an output schema does not conjure
// struct_output, and the prompt must not order the agent to call a tool it
// will not be given.
func TestGetAgentPrompt_StructOutputSectionFollowsAllowlist(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"summary": map[string]any{"type": "string"}},
	}

	t.Run("struct_output not listed", func(t *testing.T) {
		loadAgents(t, map[config.AgentName]config.Agent{
			"silent": {
				Prompt:     "You are the silent agent.",
				AllowTools: []string{"read"},
				Output:     &config.AgentOutput{Schema: schema},
			},
		}, nil)

		got := GetAgentPrompt("silent", models.ProviderAnthropic)
		assert.NotContains(t, got, structOutputMarker)
	})

	t.Run("struct_output listed", func(t *testing.T) {
		loadAgents(t, map[config.AgentName]config.Agent{
			"structured": {
				Prompt:     "You are the structured agent.",
				AllowTools: []string{"read", "struct_output"},
				Output:     &config.AgentOutput{Schema: schema},
			},
		}, nil)

		got := GetAgentPrompt("structured", models.ProviderAnthropic)
		assert.Contains(t, got, structOutputMarker)
	})
}

// Cron guidance is gated on the explicit-opt-in predicate, which a bare "*"
// deliberately does not satisfy — the deny-list default never granted cron
// either.
func TestGetAgentPrompt_CronGuidanceNeedsExplicitAllowEntry(t *testing.T) {
	t.Run("star does not opt in", func(t *testing.T) {
		loadAgents(t, map[config.AgentName]config.Agent{
			"star": {
				Prompt:     "You are the star agent.",
				Mode:       config.AgentModeAgent,
				AllowTools: []string{"*"},
			},
		}, nil)

		got := GetAgentPrompt("star", models.ProviderAnthropic)
		assert.NotContains(t, got, cronCreateToolName+"`")
	})

	t.Run("named cron tool opts in", func(t *testing.T) {
		loadAgents(t, map[config.AgentName]config.Agent{
			"scheduler": {
				Prompt:     "You are the scheduler agent.",
				Mode:       config.AgentModeAgent,
				AllowTools: []string{"read", cronCreateToolName},
			},
		}, nil)

		got := GetAgentPrompt("scheduler", models.ProviderAnthropic)
		assert.True(t, strings.Contains(got, cronCreateToolName),
			"an agent that listed croncreate should get the cron guidance")
	})
}
