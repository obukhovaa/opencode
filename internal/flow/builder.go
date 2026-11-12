package flow

import (
	_ "embed"
	"fmt"
)

//go:embed bundled/flow-creator.md
var builderGuide string

func BuilderPrompt(goal, primary, size string, subagents []string) string {
	return fmt.Sprintf(`Build a dynamic workflow for this user goal. You are planning only in this turn: call flow with operation=create and then finish your response. Do not execute the work until the workflow starts.
User goal: %s
Primary agent: %s. All primary steps MUST use exactly this agent. Available subagents: %v.
Limits (steps, parallel task agents, turns/step, iterations/step): XS=(3,1,20,1), S=(5,2,40,1), M=(8,3,80,2), L=(12,4,120,3), XL=(18,6,180,4), XXL=(24,8,240,6).
Complexity: %s (if auto choose XS/S/M/L/XL/XXL based on scope; the runtime enforces resource ceilings).
Prefer a compact sequential OpenSpec routine: propose, review, implement, review, end-to-end test, archive. Use task fanout within reviews when useful. Simplify for small tasks. Steps use regular question for user input; do not set interactive/interaction (those are bridge-only). No model/context/compaction overrides, includes, extends, external schema references or nested flows. Never put executable shell markup in generated prompts; use the permission-checked bash tool inside the step instead. Specify bounded maxTurns/maxIterations. Every step must return a result. Add an explicit rules: [{then: next-step-id}] edge between sequential steps; omit rules on terminal steps. The special destination "end" is not accepted. Persist requested artifacts in the repository. Use normal tools and configured subagent roles.
Submit self-contained YAML to flow; validation errors are actionable, fix and retry before ending.
<skill_content name="flow-creator">
%s
</skill_content>`, goal, primary, subagents, size, builderGuide)
}
