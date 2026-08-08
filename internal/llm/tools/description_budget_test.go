package tools

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/stretchr/testify/assert"
)

const (
	// defaultDescriptionBudget bounds every builtin tool description. Tool
	// descriptions are interface contracts, not tutorials — if a description
	// no longer fits, cut guidance the model doesn't need rather than raising
	// the budget (see openspec/specs/prompt-surface).
	defaultDescriptionBudget = 2048
	// bashDescriptionBudget is larger: bash carries the canonical
	// dedicated-tools routing list and the git/GitHub policy.
	bashDescriptionBudget = 4096
)

// TestToolDescriptionBudgets pins the size of every builtin tool description
// with a static or cheaply-constructed Info(). Dynamic descriptions that scale
// with user configuration (websearch providers, skill inventory, router_send
// channels, the task tool's agent list) are deliberately excluded: their size
// is a function of the user's config, not of this repo's prompt surface.
func TestToolDescriptionBudgets(t *testing.T) {
	// Config is loaded once by this package's test init (edit_test.go) —
	// bash/ls descriptions read the working directory from it. Do NOT
	// Reset it here: later tests rely on that same package-lifetime config.
	tools := []struct {
		tool   BaseTool
		budget int
	}{
		{NewBashTool(nil, nil), bashDescriptionBudget},
		{NewEditTool(nil, nil, nil, nil), defaultDescriptionBudget},
		{NewMultiEditTool(nil, nil, nil, nil), defaultDescriptionBudget},
		{NewReadTool(nil, nil, nil), defaultDescriptionBudget},
		{NewWriteTool(nil, nil, nil, nil), defaultDescriptionBudget},
		{NewGlobTool(nil, nil), defaultDescriptionBudget},
		{NewGrepTool(nil, nil), defaultDescriptionBudget},
		{NewLsTool(config.Get(), nil, nil), defaultDescriptionBudget},
		{NewDeleteTool(nil, nil, nil), defaultDescriptionBudget},
		{NewPatchTool(nil, nil, nil, nil), defaultDescriptionBudget},
		{NewViewImageTool(), defaultDescriptionBudget},
		{NewFetchTool(nil, nil), defaultDescriptionBudget},
		{NewSourcegraphTool(), defaultDescriptionBudget},
		{NewLspTool(nil), defaultDescriptionBudget},
		{NewStructOutputTool(map[string]any{}), defaultDescriptionBudget},
		{NewQuestionTool(nil, nil), defaultDescriptionBudget},
		{NewTodoWriteTool(nil), defaultDescriptionBudget},
		{NewTaskListTool(), defaultDescriptionBudget},
		{NewTaskStopTool(nil, nil), defaultDescriptionBudget},
		{NewMonitorTool(nil, nil), defaultDescriptionBudget},
		{NewCronCreateTool(nil, nil), defaultDescriptionBudget},
		{NewCronDeleteTool(nil), defaultDescriptionBudget},
		{NewCronListTool(nil, nil), defaultDescriptionBudget},
	}

	for _, tc := range tools {
		info := tc.tool.Info()
		t.Run(info.Name, func(t *testing.T) {
			assert.NotEmpty(t, info.Description)
			assert.LessOrEqual(t, len(info.Description), tc.budget,
				"%s description is %d bytes (budget %d) — trim it instead of raising the budget",
				info.Name, len(info.Description), tc.budget)
		})
	}
}
