package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
)

func finishedWith(reason message.FinishReason) message.Message {
	msg := message.Message{Role: message.Assistant}
	if reason != "" {
		msg.AddFinish(reason)
	}
	return msg
}

func TestTraceFinishReason(t *testing.T) {
	tests := []struct {
		name string
		ev   AgentEvent
		want string
	}{
		{name: "natural end of turn", ev: AgentEvent{Message: finishedWith(message.FinishReasonEndTurn)}, want: "end_turn"},
		{name: "accepted struct_output ends on a tool call", ev: AgentEvent{Message: finishedWith(message.FinishReasonToolUse)}, want: "end_turn"},
		{name: "max_tokens stop is a natural finish", ev: AgentEvent{Message: finishedWith(message.FinishReasonMaxTokens)}, want: "end_turn"},
		{name: "unfinished message", ev: AgentEvent{Message: finishedWith("")}, want: "end_turn"},
		{name: "turn budget exhausted", ev: AgentEvent{Message: finishedWith(message.FinishReasonEndTurn), TurnsExhausted: true}, want: "turns_exhausted"},
		{name: "rejected permission prompt", ev: AgentEvent{Message: finishedWith(message.FinishReasonPermissionDenied)}, want: "permission_denied"},
		{name: "canceled during tool execution", ev: AgentEvent{Message: finishedWith(message.FinishReasonCanceled)}, want: "canceled"},
		{name: "user cancellation error", ev: AgentEvent{Error: ErrRequestCancelled}, want: "canceled"},
		{name: "wrapped context cancellation", ev: AgentEvent{Error: fmt.Errorf("stream: %w", context.Canceled)}, want: "canceled"},
		{name: "deadline is an error, not a cancellation", ev: AgentEvent{Error: context.DeadlineExceeded}, want: "error"},
		{name: "provider error", ev: AgentEvent{Error: errors.New("failed to process events: boom")}, want: "error"},
		{name: "error outranks the exhausted flag", ev: AgentEvent{Error: errors.New("boom"), TurnsExhausted: true}, want: "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := traceFinishReason(tt.ev); got != tt.want {
				t.Errorf("traceFinishReason = %q, want %q", got, tt.want)
			}
		})
	}
}

// spanMetadata returns the attributes of span under prefix, keyed without it.
func spanMetadata(t *testing.T, span trace.Span, prefix string) map[string]string {
	t.Helper()
	ro, ok := span.(sdktrace.ReadOnlySpan)
	if !ok {
		t.Fatal("no recording span to read")
	}
	out := map[string]string{}
	for _, kv := range ro.Attributes() {
		if k, ok := strings.CutPrefix(string(kv.Key), prefix); ok {
			out[k] = kv.Value.AsString()
		}
	}
	return out
}

// The turn budget goes on the trace when it is created, namespaced like every
// other custom key. A subagent's budget goes on its own observation: written
// as trace metadata it would replace the parent's.
func TestStartRunTraceStampsMaxTurns(t *testing.T) {
	loadConfigIn(t, ".")
	setLangfuseClient(t, true)
	config.Get().Telemetry = &config.TelemetryConfig{MetadataNamespace: "piano"}

	a := &agent{agentID: "coder", provider: &stubProvider{}}

	t.Run("root trace", func(t *testing.T) {
		span := trace.SpanFromContext(a.startRunTrace(context.Background(), session.Session{ID: "s1"}, nil, 42))
		if got := spanMetadata(t, span, "langfuse.trace.metadata.")["piano.max_turns"]; got != "42" {
			t.Errorf("piano.max_turns = %q, want 42", got)
		}
	})
	t.Run("child trace", func(t *testing.T) {
		span := trace.SpanFromContext(a.startRunTrace(context.Background(), session.Session{ID: "s2", ParentSessionID: "s1"}, nil, 7))
		if got, ok := spanMetadata(t, span, "langfuse.trace.metadata.")["piano.max_turns"]; ok {
			t.Errorf("child wrote trace-level piano.max_turns = %q; it would overwrite the parent's", got)
		}
		if got := spanMetadata(t, span, "langfuse.observation.metadata.")["piano.max_turns"]; got != "7" {
			t.Errorf("child observation piano.max_turns = %q, want 7", got)
		}
	})
}

// spanRecordingProvider remembers the span current on the first model call's
// ctx — the run's root trace span — so a test can read what processGeneration
// stamped on it once the run is over.
type spanRecordingProvider struct {
	*scriptedProvider
	once sync.Once
	span trace.Span
}

func (p *spanRecordingProvider) StreamResponse(ctx context.Context, msgs []message.Message, ts []tools.BaseTool) <-chan provider.ProviderEvent {
	p.once.Do(func() { p.span = trace.SpanFromContext(ctx) })
	return p.scriptedProvider.StreamResponse(ctx, msgs, ts)
}

// The run's end stamps how many model calls it made and why it stopped.
func TestProcessGenerationStampsRunMetadata(t *testing.T) {
	loadConfigIn(t, ".")
	setLangfuseClient(t, true)
	config.Get().Telemetry = &config.TelemetryConfig{MetadataNamespace: "piano"}

	tests := []struct {
		name     string
		maxTurns int
		respond  func(call int, forced bool) *provider.ProviderResponse
		want     map[string]string
	}{
		{
			name:     "model ends its own turn",
			maxTurns: 10,
			respond:  func(int, bool) *provider.ProviderResponse { return endTurn() },
			want:     map[string]string{"piano.max_turns": "10", "piano.turns_used": "1", "piano.finish_reason": "end_turn"},
		},
		{
			// One budgeted turn, then the forced wrap-up: both are model
			// calls, so an exhausted run reports max_turns+1.
			name:     "turn budget runs out",
			maxTurns: 1,
			respond: func(call int, forced bool) *provider.ProviderResponse {
				if forced {
					return endTurn()
				}
				return structOutputCall(fmt.Sprintf("call-%d", call), `{}`)
			},
			want: map[string]string{"piano.max_turns": "1", "piano.turns_used": "2", "piano.finish_reason": "turns_exhausted"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withFreshTaskRegistry(t)
			p := &spanRecordingProvider{scriptedProvider: &scriptedProvider{respond: tt.respond}}
			a := newLoopAgentWithTools(t, p, []tools.BaseTool{testStructOutputTool()})

			res := a.processGeneration(context.Background(), "sess-"+tt.name, "do work", tt.maxTurns, nil, RunOptions{NonInteractive: true})
			if res.Error != nil {
				t.Fatalf("processGeneration error: %v", res.Error)
			}
			if p.span == nil {
				t.Fatal("provider never saw a trace span")
			}
			md := spanMetadata(t, p.span, "langfuse.trace.metadata.")
			for k, want := range tt.want {
				if got := md[k]; got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
		})
	}
}
