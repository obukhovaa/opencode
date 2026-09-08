package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
)

// turnsExhaustedError is the step outcome for a run that ended on its turn
// budget while still producing a usable struct_output — the shape that used to
// complete the step silently.
//
// The wrap-up document is real (the agent runtime forces struct_output on the
// max-turns turn), but it describes work that was CUT OFF, not work that
// finished. On a step that leaves state behind — a clone, a branch, an
// uncommitted diff on a disposable pod — accepting it as success discards the
// only signal that anything is unfinished: the flow cannot retry for a fresh
// turn budget and cannot route to a salvage step, and the job is reported
// green while the pod takes the work with it (GENAI-296, job
// c1f9c6506caed6cd).
//
// It is a *retryable* error on purpose. Each fallback attempt re-enters the
// same session with a fresh turn budget on the same pod, so the retry is the
// cheap fix: the agent picks up where it stopped rather than starting over.
type turnsExhaustedError struct {
	StepID string
	// MaxTurns is the step's own override (Step.MaxTurns). Zero means the
	// step inherited the agent / global budget, which the flow layer does
	// not resolve — reported as "its turn budget" rather than a wrong number.
	MaxTurns int
	// Attempts counts the attempts that ended in exhaustion, NOT the loop
	// index: a step whose first attempt died on a provider error and whose
	// second exhausted has run two attempts but exhausted only once, and
	// saying "on all 2 attempts" of that would be a lie.
	Attempts int
	// StructOutput is the wrap-up document, kept so the fallback step
	// inherits what the cut-off run managed to report (see
	// mergeStructOutputIntoArgs). Empty when the run produced none.
	StructOutput string
	// LastAssistant is the cut-off run's closing prose, kept for the shape
	// that produces prose but no usable document — a provider that ignored
	// the forced tool_choice, or an end_turn text reply (see
	// agent.go's forced-wrap-up degradation path). Without it that run's
	// only account of what it had done ("3 commits on feat-X, not pushed")
	// would be dropped on the floor: the failed row's output is this
	// error's text, and the fallback step reads that row as its
	// "Previous step output". Empty when the run reported a document
	// instead, which travels through StructOutput.
	LastAssistant string
}

func (e *turnsExhaustedError) Error() string {
	budget := "its turn budget"
	if e.MaxTurns > 0 {
		budget = fmt.Sprintf("its %d-turn budget", e.MaxTurns)
	}
	msg := fmt.Sprintf("step %q was cut off after exhausting %s", e.StepID, budget)
	if e.Attempts > 1 {
		msg += fmt.Sprintf(" on all %d attempts", e.Attempts)
	}
	msg += "; its work is incomplete"
	if e.LastAssistant != "" {
		msg += fmt.Sprintf("; the run's last words were: %s", e.LastAssistant)
	}
	return msg
}

// errTurnsExhausted reports whether err describes a run that ran out of turn
// budget, in either of the two shapes that produce one: a
// missingStructOutputError raised on an exhausted run, or a
// turnsExhaustedError. Callers use it to skip work that needs turns to spend —
// a re-prompt or a forced wrap-up has nothing left to buy.
func errTurnsExhausted(err error) bool {
	if err == nil {
		return false
	}
	var te *turnsExhaustedError
	if errors.As(err, &te) {
		return true
	}
	return structOutputTurnsExhausted(err)
}

// retryReasonTurnsExhausted is the reason string on the in-flight
// FlowStatusRetrying transition published when a turn exhaustion consumes a
// fallback attempt. Sibling of retryReasonNoStructOutput / retryReasonRunFailed.
const retryReasonTurnsExhausted = "agent ran out of tool turns with work unfinished; retrying with a fresh turn budget"

// turnsExhaustedRetryPrompt frames a retry that follows a turn exhaustion as a
// continuation rather than a fresh start.
//
// The session this lands in ends with the runtime's forced max-turns wrap-up
// ("Call the struct_output tool now … Do not reply with prose") and the
// document the agent produced in response. Re-sending the step's original
// prompt into that context is genuinely ambiguous — the agent has just been
// told it was finished — and the two readings it invites are both wrong: redo
// the task from the top, or restate the document it just wrote. The second is
// the dangerous one, because it returns a NON-exhausted result and so
// completes the step, carrying the same unfinished work the opt-in exists to
// catch.
//
// So the nudge is not cosmetic: it is what makes "the retry continues where it
// stopped" a property of the system rather than a hope about the model. The
// original prompt is kept underneath for the goal and its parameters, since a
// step's prompt carries the args substitution the agent still needs.
func turnsExhaustedRetryPrompt(originalPrompt string) string {
	var b strings.Builder
	b.WriteString("CONTINUE the work already in progress in this session. ")
	b.WriteString("Your previous turn did not finish the task — it ran out of tool turns, ")
	b.WriteString("and the wrap-up summary you were forced to write describes work that is still incomplete.\n\n")
	b.WriteString("You now have a fresh turn budget, in the SAME workspace: the repository, branch, ")
	b.WriteString("commits and uncommitted changes from your previous turns are all still on disk exactly as you left them.\n\n")
	b.WriteString("Before anything else, establish what is actually done — inspect the working tree ")
	b.WriteString("(for a code task: `git status`, `git log`, `git diff`) rather than trusting your summary. ")
	b.WriteString("Then finish ONLY what remains. Do NOT redo completed work, and do not create duplicate ")
	b.WriteString("commits, branches or merge requests.\n\n")
	b.WriteString("Pay particular attention to the finishing steps that are easiest to lose to a turn budget: ")
	b.WriteString("pushing the branch, opening the merge request, and reporting the resulting links.\n\n")
	b.WriteString("The original task follows, for the goal and its parameters — treat it as context for what ")
	b.WriteString("remains, not as an instruction to start over.\n\n")
	b.WriteString("--- ORIGINAL TASK ---\n")
	b.WriteString(originalPrompt)
	return b.String()
}

// mergeStructOutputIntoArgs copies a struct_output JSON document's top-level
// fields into args, the same shallow merge the completion path performs before
// handing args to the next step. Used on the turn-exhaustion failure path so a
// salvage step still receives what the cut-off run reported (its summary, the
// links it did create) instead of running blind on the inbound args alone.
//
// Best-effort: a non-object or unparseable document leaves args untouched.
func mergeStructOutputIntoArgs(args map[string]any, structOutput string) {
	if args == nil || structOutput == "" {
		return
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(structOutput), &data); err != nil {
		return
	}
	maps.Copy(args, data)
}

// turnsExhaustedOutcome builds the step failure for a run the agent runtime cut
// off at its turn budget. Returns nil when the step did not opt in
// (Fallback.on_turns_exhausted) or the run was not turn-exhausted, which is the
// caller's signal to complete the step as before.
//
// It deliberately does NOT require a usable struct_output. Exhaustion means the
// work is unfinished whatever the run managed to say about it, and the shapes
// without a document are the ones with the LEAST to show for themselves: a step
// with no output schema at all, or a forced wrap-up the provider answered with
// prose. Gating on a document would let precisely those complete silently — the
// bug this exists to close. What the run did report, in whichever form, is
// carried off the run here so the failure and the fallback step can both use
// it: StructOutput for a document, LastAssistant for prose.
func turnsExhaustedOutcome(step Step, result agentpkg.AgentEvent, attempts int) *turnsExhaustedError {
	if !result.TurnsExhausted || !step.FailsOnTurnsExhausted() {
		return nil
	}
	out := &turnsExhaustedError{
		StepID:   step.ID,
		MaxTurns: step.MaxTurns,
		Attempts: attempts,
	}
	if su := usableStructOutput(result); su != nil {
		out.StructOutput = su.Content
	} else {
		out.LastAssistant = result.Message.Content().Text
	}
	return out
}
