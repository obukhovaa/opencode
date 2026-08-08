package prompt

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBasePromptBudgets pins the size of the builtin base prompts so the
// prompt surface cannot silently re-bloat (see openspec/specs/prompt-surface).
// If a prompt outgrows its budget, cut pedagogy — examples, repeated rules,
// tool-routing lists — rather than raising the number.
func TestBasePromptBudgets(t *testing.T) {
	budgets := []struct {
		name   string
		prompt string
		budget int
	}{
		{"coder", CoderPrompt(), 6144},
		{"workhorse", WorkhorsePrompt(models.ProviderAnthropic), 4096},
		{"hivemind", HivemindPrompt(models.ProviderAnthropic), 4096},
		{"explorer", ExplorerPrompt(models.ProviderAnthropic), 2048},
	}

	for _, tc := range budgets {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotEmpty(t, tc.prompt)
			assert.LessOrEqual(t, len(tc.prompt), tc.budget,
				"%s base prompt is %d bytes (budget %d)", tc.name, len(tc.prompt), tc.budget)
		})
	}
}

// TestEnvironmentInfoHasNoProjectTree pins the lean-prompts requirement that
// environment info carries only the <env> block: the recursive project
// file-tree dump (up to 1000 paths re-sent on every request, breaking prompt
// caching on any file change) must not come back.
func TestEnvironmentInfoHasNoProjectTree(t *testing.T) {
	tmpDir := t.TempDir()
	config.Reset()
	_, err := config.Load(tmpDir, false)
	require.NoError(t, err)
	t.Cleanup(config.Reset)

	got := getEnvironmentInfo()

	assert.Contains(t, got, "<env>")
	assert.Contains(t, got, "Working directory:")
	assert.NotContains(t, got, "<project>",
		"environment info must not embed the project file tree")
}
