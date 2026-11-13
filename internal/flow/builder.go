package flow

import (
	_ "embed"
	"fmt"
)

//go:embed bundled/dynamic-flow.md
var builderGuide string

func BuilderPrompt(goal, primary, size string, subagents []string) string {
	return fmt.Sprintf(`Build a dynamic workflow for this user goal. You are planning only in this turn: call flow with operation=create and then finish your response. Do not execute the work until the workflow starts.
User goal: %s
Primary agent: %s. All primary steps MUST use exactly this agent. Available subagents: %v.
Limits (steps, parallel task agents, turns/step, iterations/step): XS=(3,1,20,1), S=(5,2,40,1), M=(8,3,80,2), L=(12,4,120,3), XL=(18,6,180,4), XXL=(24,8,240,6).
Complexity: %s (if auto choose XS/S/M/L/XL/XXL based on scope; the runtime enforces resource ceilings).
<skill_content name="flow-creator">
%s
</skill_content>`, goal, primary, subagents, size, builderGuide)
}
