package agent

import (
	"errors"
	"fmt"

	"github.com/opencode-ai/opencode/internal/permission"
)

// A tool call denied by a configured permission rule (permission.RuleDeniedError)
// is reported to the model as an error tool result and the run continues, so
// a workspace can deny a command without its first attempt ending the agent.
// A human rejecting a prompt still ends the run. The breaker below bounds a
// model that keeps reaching for denied calls instead of adapting.
const (
	// maxConsecutiveRuleDeniedTurns ends the run after this many turns in a
	// row in which no tool call ran except denied ones, starting from a turn
	// with a denial: the model has seen the denial and made no other
	// progress. Counted per turn, not per
	// call, so one turn of parallel calls that all hit the same rule — sent
	// before the model saw any denial — does not end the run on its own.
	maxConsecutiveRuleDeniedTurns = 3
	// maxRuleDenialsPerRun ends the run once this many tool calls of one run
	// were denied by rules, however they interleave with other calls.
	maxRuleDenialsPerRun = 10
)

// ruleDenialBreaker counts one run's rule denials. It lives on the run's
// callTracker, so it starts fresh with every agent.Run.
type ruleDenialBreaker struct {
	consecutiveTurns int
	total            int
}

// observeTurn records one turn's tool calls: denied of them were denied by a
// rule, progressed ran and were not denied. It returns whether the run must
// end, and why. A turn whose calls all went unexecuted (loop detected, tool
// not found) is no progress either: a model that keeps repeating a denied
// call identically gets loop-detected from its third try on, and that must
// not reset the streak.
func (b *ruleDenialBreaker) observeTurn(denied, progressed int) (bool, string) {
	b.total += denied
	switch {
	case progressed > 0:
		b.consecutiveTurns = 0
	case denied > 0 || b.consecutiveTurns > 0:
		b.consecutiveTurns++
	}
	switch {
	case b.consecutiveTurns >= maxConsecutiveRuleDeniedTurns:
		return true, fmt.Sprintf("%d consecutive turns with no tool call but denied or blocked ones", b.consecutiveTurns)
	case b.total >= maxRuleDenialsPerRun:
		return true, fmt.Sprintf("%d tool calls denied in this run", b.total)
	}
	return false, ""
}

// ruleDeniedMessage is the tool result the model gets for a rule denial. It
// names what was denied and says the action itself is forbidden: "try
// something else" alone invites reaching the same effect through another
// command or tool, which the rule is there to prevent.
func ruleDeniedMessage(rd *permission.RuleDeniedError) string {
	target := ""
	if rd.Input != "" {
		target = fmt.Sprintf(" on %q", rd.Input)
	}
	return fmt.Sprintf(
		"Permission denied by a configured permission rule: the %s tool may not run%s. "+
			"This is a policy decision, not a transient error; retrying will be denied again. "+
			"The rule forbids the action itself, so do not reach the same effect another way "+
			"(a different command, tool or script). Continue with the parts of the task that do not need it, "+
			"or report that this rule blocks you.",
		rd.ToolName, target,
	)
}

func countTrue(flags []bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}

// modelCallError marks an error from the provider's response stream, as
// opposed to one after the turn's tools ran (saving their results). Error and
// Unwrap pass the provider's error through unchanged.
type modelCallError struct{ err error }

func (e *modelCallError) Error() string { return e.err.Error() }
func (e *modelCallError) Unwrap() error { return e.err }

func isModelCallError(err error) bool {
	var mce *modelCallError
	return errors.As(err, &mce)
}
