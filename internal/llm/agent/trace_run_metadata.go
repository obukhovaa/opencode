package agent

import (
	"context"
	"errors"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/langfuse"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
)

// Run-level trace metadata keys (GENAI-499), written bare and namespaced
// like every other custom key, so a run can be measured from the trace
// alone without fetching its observations.
const (
	traceKeyMaxTurns     = "max_turns"
	traceKeyTurnsUsed    = "turns_used"
	traceKeyFinishReason = "finish_reason"
)

// traceFinishTurnsExhausted is the finish_reason of a run that ran out of its
// turn budget. It has no message.FinishReason counterpart: the forced wrap-up
// turn itself finishes with end_turn (or a struct_output tool call).
const traceFinishTurnsExhausted = "turns_exhausted"

// startRunTrace creates the turn's Langfuse trace and stamps the turn budget
// on it. The budget is known up front, so it is stamped whatever path the run
// ends on. Like every attribute of the root span it reaches Langfuse when the
// span ends: a process killed mid-run exports none of them.
func (a *agent) startRunTrace(ctx context.Context, sess session.Session, input any, maxTurns int) context.Context {
	ctx = a.createLangfuseTrace(ctx, sess, input)
	stampTraceMetadata(ctx, map[string]any{traceKeyMaxTurns: maxTurns})
	return ctx
}

// stampTraceMetadata adds metadata to the trace on ctx after it started,
// under the configured telemetry.metadataNamespace. Unlike the trace output
// it is not gated by telemetry.generations: these are counters, not content.
// A subagent's values go on its own observation, never its parent's trace
// (see langfuse.SetTraceMetadata).
func stampTraceMetadata(ctx context.Context, metadata map[string]any) {
	if lf := langfuse.Get(); lf == nil || !lf.Enabled() {
		return
	}
	if cfg := config.Get(); cfg != nil && cfg.Telemetry != nil && cfg.Telemetry.MetadataNamespace != "" {
		metadata = langfuse.NamespaceMetadata(metadata, cfg.Telemetry.MetadataNamespace)
	}
	langfuse.SetTraceMetadata(ctx, metadata)
}

// traceFinishReason classifies how a RunWith call ended, from the event it
// returned and runErr, the run context's error at that moment. In order:
//
//   - an error event: canceled for a cancellation, error for anything else
//     (a step deadline included);
//   - turns_exhausted when a turn-budget gate ended the run, even if its
//     forced wrap-up call then failed or was cancelled;
//   - a cancelled or expired run context: canceled, or error for a deadline.
//     A run cut short this way can still return a result with no error: a
//     non-interactive run whose wait on background tasks is interrupted
//     returns its last natural result, and a tool call interrupted mid-run
//     finishes its message as canceled whatever stopped it;
//   - the final message's permission_denied, canceled or error;
//   - end_turn for every other finish (a text reply, an accepted
//     struct_output, a max_tokens stop).
func traceFinishReason(ev AgentEvent, runErr error) string {
	if ev.Error != nil {
		return errorFinishReason(ev.Error)
	}
	if ev.TurnsExhausted {
		return traceFinishTurnsExhausted
	}
	if runErr != nil {
		return errorFinishReason(runErr)
	}
	switch reason := ev.Message.FinishReason(); reason {
	case message.FinishReasonPermissionDenied, message.FinishReasonCanceled, message.FinishReasonError:
		return string(reason)
	}
	return string(message.FinishReasonEndTurn)
}

// errorFinishReason is canceled for a cancellation and error for any other
// error, a context deadline included.
func errorFinishReason(err error) string {
	if errors.Is(err, ErrRequestCancelled) || errors.Is(err, context.Canceled) {
		return string(message.FinishReasonCanceled)
	}
	return string(message.FinishReasonError)
}
