package flow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// structOutputRetryHistoryScan bounds how far back lastAssistantText reads for
// the prose that explains a missing-struct_output failure. The interesting
// message is the agent's final turn, so a short tail is enough; a full
// message.List on a 200-turn interactive session would be a large read on an
// already-failing path.
const structOutputRetryHistoryScan = 20

// lastAssistantTextMaxLen caps the assistant prose embedded in a step error.
// The error string lands in flow_states.output and on the /event SSE stream,
// so an unbounded model turn must not be pasted in whole.
const lastAssistantTextMaxLen = 1000

// missingStructOutputError is the terminal failure for a schema-bearing step
// whose agent produced neither a struct_output call nor any assistant text.
//
// It carries the diagnostic context that used to be discarded: whether the run
// died on turn exhaustion (so no re-prompt was worth a request), whether the
// bounded re-prompt already ran, and — crucially — the agent's last assistant
// message. That prose is normally the only artefact that explains WHY the agent
// stopped ("I have no tool that can list this client's products…"), and it
// never leaves the session otherwise.
type missingStructOutputError struct {
	StepID         string
	TurnsExhausted bool
	Retried        bool
	LastAssistant  string
}

func (e *missingStructOutputError) Error() string {
	// Keep the historical prefix verbatim — orchestrators and tests match on
	// "expects structured output but agent produced empty response".
	var b strings.Builder
	fmt.Fprintf(&b, "step %q expects structured output but agent produced empty response", e.StepID)
	switch {
	case e.TurnsExhausted:
		b.WriteString(" (turn budget exhausted; no re-prompt attempted)")
	case e.Retried:
		b.WriteString(" (re-prompted once for struct_output, still nothing)")
	}
	if e.LastAssistant != "" {
		fmt.Fprintf(&b, "; last assistant message: %q", e.LastAssistant)
	}
	return b.String()
}

// lastAssistantText returns the most recent non-empty assistant prose in the
// session, truncated to lastAssistantTextMaxLen. Returns "" when the message
// service is unavailable (unit tests wire it nil), the read fails, or the agent
// genuinely never said anything.
//
// The failing turn itself has no text by definition — that is what makes the
// step fail — so this deliberately scans BACKWARDS past it to the last turn
// that did say something.
func (s *service) lastAssistantText(ctx context.Context, sessionID string) string {
	if s.messages == nil || sessionID == "" {
		return ""
	}
	// A cancelled parent ctx (shutdown, step timeout) would fail the read; use
	// a fresh short deadline so the diagnostic still lands.
	readCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
	}
	msgs, err := s.messages.ListLatest(readCtx, sessionID, structOutputRetryHistoryScan)
	if err != nil {
		logging.Warn("Could not read session history for step failure diagnostics",
			"session_id", sessionID, "error", err)
		return ""
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != message.Assistant {
			continue
		}
		if text := strings.TrimSpace(m.Content().Text); text != "" {
			return truncateForError(text)
		}
	}
	return ""
}

func truncateForError(s string) string {
	// Collapse newlines so the error stays one legible line in logs and SSE.
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= lastAssistantTextMaxLen {
		return s
	}
	return s[:lastAssistantTextMaxLen] + "…"
}

// schemaFieldNames lists the schema's field names for the re-prompt: the
// `required` array when present (that is what the agent actually owes), else
// the `properties` keys, sorted for a stable prompt (a stable prompt keeps the
// provider-side cache prefix intact across retries).
func schemaFieldNames(schema map[string]any) []string {
	if schema == nil {
		return nil
	}
	if req, ok := schema["required"].([]any); ok && len(req) > 0 {
		out := make([]string, 0, len(req))
		for _, r := range req {
			if name, ok := r.(string); ok && name != "" {
				out = append(out, name)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if req, ok := schema["required"].([]string); ok && len(req) > 0 {
		return req
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return nil
	}
	out := make([]string, 0, len(props))
	for name := range props {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// structOutputRetryPrompt builds the nudge sent on the single bounded
// re-prompt. It names the missing primitive, names the schema fields the step
// owes, and — on an interactive step — spells out that `question` is the ONLY
// tool that waits for a human reply. That last line matters: an agent that
// stopped mid-conversation typically stopped because it "asked" in plain prose,
// which never reaches the peer and never blocks.
func structOutputRetryPrompt(step Step) string {
	var b strings.Builder
	b.WriteString("Your previous turn ended without calling the struct_output tool and without any message text, ")
	b.WriteString("so this step has produced no result and cannot continue.\n\n")
	b.WriteString("This step REQUIRES exactly one struct_output tool call to finish.")
	if fields := schemaFieldNames(step.Output.Schema); len(fields) > 0 {
		fmt.Fprintf(&b, " The schema fields are: %s.", strings.Join(fields, ", "))
	}
	b.WriteString("\n\n")
	if step.Interactive {
		b.WriteString("You are in a live conversation with a human reviewer over chat. ")
		b.WriteString("`question` is the only tool that actually reaches them and waits for a reply — ")
		b.WriteString("plain assistant text does not reach them and does not block, so it cannot be used to ask for anything.\n\n")
		b.WriteString("Choose one now:\n")
		b.WriteString("- If you still need information from the reviewer, call `question`.\n")
		b.WriteString("- If you are blocked and cannot proceed, call `struct_output` and say so in the schema fields.\n")
		b.WriteString("- Otherwise call `struct_output` with the result you already have.\n")
		return b.String()
	}
	b.WriteString("Call struct_output now, conforming exactly to the required schema. Do not reply with prose.")
	return b.String()
}

// retryStructOutputTurn issues exactly ONE bounded re-prompt for a
// schema-bearing step whose run returned no struct_output and no text, and
// returns the resulting event plus whether it is usable.
//
// This is the interactive counterpart to forceStructOutputTurn. The two differ
// in the only two ways that matter for a step with a human on the other end:
//
//   - It does NOT set ForceStructOutput. Forcing tool_choice=struct_output
//     would make `question` unreachable, so an agent that stopped because it
//     needed information from the reviewer would be forced to invent an answer
//     instead of asking for one.
//   - It gets the step's own turn budget and the step's own timeout rather than
//     maxTurns=1 / forceStructOutputMaxWait, because a question round-trip
//     needs at least two turns and a human needs longer than two minutes.
//
// Bridge lifetime: this runs inside runStep, and the interactive unbind is a
// `defer` on runStep's return (see the OnInteractiveStepStart block), so the
// session is still bound to its peers and still flagged as an interactive
// session for the question tool's auto-approve bypass. No rebind is needed —
// and a retry must never be moved out of runStep, or it would run unbound and
// fail again differently.
func (s *service) retryStructOutputTurn(
	ctx context.Context,
	agentSvc agentpkg.Service,
	sessionID string,
	step Step,
) (agentpkg.AgentEvent, bool) {
	// The main loop's step-scoped ctx was cancelled on its way out; derive a
	// fresh one so the re-prompt gets a real budget. ctx already carries the
	// flow telemetry values set in runStep.
	base, cancel := stepCtx(ctx, step)
	defer cancel()
	retryCtx := context.WithValue(base, tools.StepScopedContextKey, base)

	runOpts := agentpkg.RunOptions{NonInteractive: true}
	if step.Compact != nil && step.Compact.Threshold > 0 {
		runOpts.CompactionThreshold = step.Compact.Threshold
	}

	// agent.RunWith releases the session busy-lock in a deferred delete that
	// runs after it delivers the prior turn's terminal event, so an immediate
	// call here can observe ErrSessionBusy. Retry briefly before giving up.
	var (
		done <-chan agentpkg.AgentEvent
		err  error
	)
	for attempt := 0; attempt < 6; attempt++ {
		done, err = agentSvc.RunWith(retryCtx, sessionID, structOutputRetryPrompt(step), step.MaxTurns, runOpts)
		if err == nil {
			break
		}
		if !errors.Is(err, agentpkg.ErrSessionBusy) {
			logging.Warn("struct_output re-prompt failed to start", "step", step.ID, "error", err)
			return agentpkg.AgentEvent{}, false
		}
		select {
		case <-ctx.Done():
			return agentpkg.AgentEvent{}, false
		case <-time.After(50 * time.Millisecond):
		}
	}
	if err != nil {
		logging.Warn("struct_output re-prompt still session-busy after retries", "step", step.ID)
		return agentpkg.AgentEvent{}, false
	}

	ev := <-done
	if ev.Type == agentpkg.AgentEventTypeError {
		logging.Warn("struct_output re-prompt errored", "step", step.ID, "error", ev.Error)
		return agentpkg.AgentEvent{}, false
	}
	// Usable when it produced either a struct_output or prose: struct_output
	// completes the step, prose falls through the caller's existing
	// text-fallback path (and, failing that, lands in the step error).
	usableStruct := ev.StructOutput != nil && ev.StructOutput.Content != "" && !ev.StructOutput.IsError
	if !usableStruct && ev.Message.Content().Text == "" {
		logging.Warn("struct_output re-prompt produced nothing usable", "step", step.ID)
		return ev, false
	}
	return ev, true
}

// Reasons carried on a FlowStatusRetrying transition, so an orchestrator can
// tell the recoverable shapes apart on the wire.
const (
	retryReasonNoStructOutput = "agent turn ended without a struct_output call; re-prompting once"
	retryReasonRunFailed      = "agent run produced no usable struct_output; forcing one wrap-up turn"
)

// structOutputTurnsExhausted reports whether err is the missing-struct_output
// failure raised because the agent ran out of turns, as opposed to ending a
// turn without a qualifying tool call. Only the latter is worth a re-prompt.
func structOutputTurnsExhausted(err error) bool {
	var mse *missingStructOutputError
	if errors.As(err, &mse) {
		return mse.TurnsExhausted
	}
	return false
}

// publishStructOutputRetry emits the in-flight FlowStatusRetrying transition so
// the /event SSE stream carries a flow.step.retrying for the re-prompt. Like
// FlowStatusWaitingForInput it is deliberately NOT persisted — the step has not
// reached a terminal status and a `retrying` row in flow_states would confuse
// resume (see hasResumableWork, which only knows persisted statuses).
func (s *service) publishStructOutputRetry(
	sessionID string,
	rootSessionID string,
	flowID string,
	step Step,
	args map[string]any,
	iteration int,
	reason string,
	flowStates chan<- *FlowState,
) {
	st := &FlowState{
		SessionID:     sessionID,
		RootSessionID: rootSessionID,
		FlowID:        flowID,
		StepID:        step.ID,
		Status:        FlowStatusRetrying,
		Args:          args,
		Output:        reason,
		Iteration:     iteration,
		UpdatedAt:     time.Now().Unix(),
	}
	flowStates <- st
	s.Publish(pubsub.UpdatedEvent, *st)
}
