package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"

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
	Attempts int
	// StructOutput is the wrap-up document, kept so the fallback step
	// inherits what the cut-off run managed to report (see
	// mergeStructOutputIntoArgs). Nil when the run produced none.
	StructOutput string
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
	return msg + "; its work is incomplete"
}

// TurnsExhausted lets errTurnsExhausted treat this alongside
// missingStructOutputError without either type knowing about the other.
func (e *turnsExhaustedError) TurnsExhausted() bool { return true }

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

// turnsExhaustedOutcome builds the step failure for a turn-exhausted run that
// still produced a usable struct_output. Returns nil when the step did not opt
// in (Fallback.on_turns_exhausted) or the run was not turn-exhausted, which is
// the caller's signal to complete the step as before.
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
	}
	return out
}
