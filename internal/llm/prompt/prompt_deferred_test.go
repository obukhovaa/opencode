package prompt

import (
	"strings"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/stretchr/testify/assert"
)

func TestIsBuiltinDeferralName(t *testing.T) {
	// Builtins covered by the static prompt block — the agent delta must
	// skip these (incl. the non-baseline monitor/tasklist/taskstop, whose
	// double-announcement this predicate exists to prevent).
	for _, n := range []string{"ls", "bash", "monitor", "tasklist", "taskstop", "task", "websearch", "lsp"} {
		assert.True(t, IsBuiltinDeferralName(n), "%s should be a builtin deferral name", n)
	}
	// MCP / dynamic tools are NOT builtins → announced via the delta.
	for _, n := range []string{"mcp__gitlab__get_issue", "jira_add_comment", "toolsearch"} {
		assert.False(t, IsBuiltinDeferralName(n), "%s should not be a builtin deferral name", n)
	}
}

func TestDeferredToolsPrompt(t *testing.T) {
	t.Run("absent without config", func(t *testing.T) {
		reg := &mockRegistry{agents: map[string]agentregistry.AgentInfo{
			"a": {ID: "a"},
		}}
		assert.Empty(t, deferredToolsPrompt("a", reg))
	})

	t.Run("lists deferred builtins", func(t *testing.T) {
		reg := &mockRegistry{agents: map[string]agentregistry.AgentInfo{
			"a": {ID: "a", DeferredTools: map[string]bool{"websearch": true, "sourcegraph": true}},
		}}
		got := deferredToolsPrompt("a", reg)
		assert.Contains(t, got, "<system-reminder>")
		assert.Contains(t, got, "- sourcegraph")
		assert.Contains(t, got, "- websearch")
		assert.NotContains(t, got, "- bash")
	})

	t.Run("MCP-only patterns still announce", func(t *testing.T) {
		reg := &mockRegistry{agents: map[string]agentregistry.AgentInfo{
			"a": {ID: "a", DeferredTools: map[string]bool{"jira_*": true}},
		}}
		got := deferredToolsPrompt("a", reg)
		assert.Contains(t, got, "<system-reminder>", "explainer must appear even when no builtin matches")
		assert.NotContains(t, got, "Deferred builtin tools:")
		assert.Contains(t, got, "announced in <system-reminder> messages as they become available")
	})

	t.Run("toolsearch disabled fails open", func(t *testing.T) {
		reg := &mockRegistry{agents: map[string]agentregistry.AgentInfo{
			"a": {
				ID:            "a",
				DeferredTools: map[string]bool{"websearch": true},
				Tools:         map[string]bool{"toolsearch": false},
			},
		}}
		assert.Empty(t, deferredToolsPrompt("a", reg))
	})

	t.Run("byte-stable across calls", func(t *testing.T) {
		reg := &mockRegistry{agents: map[string]agentregistry.AgentInfo{
			"a": {ID: "a", DeferredTools: map[string]bool{"websearch": true, "lsp": true}},
		}}
		first := deferredToolsPrompt("a", reg)
		for range 5 {
			assert.Equal(t, first, deferredToolsPrompt("a", reg))
		}
		assert.Equal(t, 1, strings.Count(first, "Deferred builtin tools:"))
	})
}
