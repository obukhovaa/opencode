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
// on it, so the budget is on the trace even when the run never returns.
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
// returned: canceled, error, turns_exhausted, permission_denied, or
// end_turn for every natural finish (a text reply, an accepted
// struct_output, a max_tokens stop).
func traceFinishReason(ev AgentEvent) string {
	if ev.Error != nil {
		if errors.Is(ev.Error, ErrRequestCancelled) || errors.Is(ev.Error, context.Canceled) {
			return string(message.FinishReasonCanceled)
		}
		return string(message.FinishReasonError)
	}
	if ev.TurnsExhausted {
		return traceFinishTurnsExhausted
	}
	switch reason := ev.Message.FinishReason(); reason {
	case message.FinishReasonPermissionDenied, message.FinishReasonCanceled, message.FinishReasonError:
		return string(reason)
	}
	return string(message.FinishReasonEndTurn)
}
