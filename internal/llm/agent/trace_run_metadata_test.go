package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
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
		name   string
		ev     AgentEvent
		runErr error
		want   string
	}{
		{name: "natural end of turn", ev: AgentEvent{Message: finishedWith(message.FinishReasonEndTurn)}, want: "end_turn"},
		{name: "accepted struct_output ends on a tool call", ev: AgentEvent{Message: finishedWith(message.FinishReasonToolUse)}, want: "end_turn"},
		{name: "max_tokens stop", ev: AgentEvent{Message: finishedWith(message.FinishReasonMaxTokens)}, want: "max_tokens"},
		{name: "exhausted flag outranks a truncated wrap-up", ev: AgentEvent{Message: finishedWith(message.FinishReasonMaxTokens), TurnsExhausted: true}, want: "turns_exhausted"},
		{name: "cancellation outranks a truncated reply", ev: AgentEvent{Message: finishedWith(message.FinishReasonMaxTokens)}, runErr: context.Canceled, want: "canceled"},
		{name: "unfinished message", ev: AgentEvent{Message: finishedWith("")}, want: "end_turn"},
		{name: "turn budget exhausted", ev: AgentEvent{Message: finishedWith(message.FinishReasonEndTurn), TurnsExhausted: true}, want: "turns_exhausted"},
		{name: "rejected permission prompt", ev: AgentEvent{Message: finishedWith(message.FinishReasonPermissionDenied)}, want: "permission_denied"},
		{name: "canceled during tool execution", ev: AgentEvent{Message: finishedWith(message.FinishReasonCanceled)}, runErr: context.Canceled, want: "canceled"},
		{name: "user cancellation error", ev: AgentEvent{Error: ErrRequestCancelled}, want: "canceled"},
		{name: "wrapped context cancellation", ev: AgentEvent{Error: fmt.Errorf("stream: %w", context.Canceled)}, want: "canceled"},
		{name: "deadline is an error, not a cancellation", ev: AgentEvent{Error: context.DeadlineExceeded}, want: "error"},
		{name: "provider error", ev: AgentEvent{Error: errors.New("failed to process events: boom")}, want: "error"},
		{name: "error outranks the exhausted flag", ev: AgentEvent{Error: errors.New("boom"), TurnsExhausted: true}, want: "error"},
		// A run cut short with no error event: a non-interactive run whose
		// background-task wait is interrupted returns its last result, and
		// an interrupted tool call finishes its message as canceled
		// whatever stopped it.
		{name: "cancelled while waiting on background tasks", ev: AgentEvent{Message: finishedWith(message.FinishReasonEndTurn)}, runErr: context.Canceled, want: "canceled"},
		{name: "step deadline while waiting on background tasks", ev: AgentEvent{Message: finishedWith(message.FinishReasonEndTurn)}, runErr: context.DeadlineExceeded, want: "error"},
		{name: "step deadline during tool execution", ev: AgentEvent{Message: finishedWith(message.FinishReasonCanceled)}, runErr: context.DeadlineExceeded, want: "error"},
		{name: "exhausted flag outranks a cancelled wrap-up", ev: AgentEvent{Message: finishedWith(message.FinishReasonEndTurn), TurnsExhausted: true}, runErr: context.Canceled, want: "turns_exhausted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := traceFinishReason(tt.ev, tt.runErr); got != tt.want {
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
		// A subagent run nests in its caller's trace, so its ctx carries it.
		parentCtx := a.startRunTrace(context.Background(), session.Session{ID: "s1"}, nil, 42)
		span := trace.SpanFromContext(a.startRunTrace(parentCtx, session.Session{ID: "s2", ParentSessionID: "s1"}, nil, 7))
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

// A non-interactive run whose wait on background tasks is cut short returns
// its last natural result with no error, so the event alone reads end_turn.
// The run context says why it stopped: a cancellation (DELETE /flow, an
// aborted parent) is canceled, a step deadline is error.
func TestProcessGenerationStampsAnInterruptedBackgroundWait(t *testing.T) {
	loadConfigIn(t, ".")
	setLangfuseClient(t, true)
	config.Get().Telemetry = &config.TelemetryConfig{MetadataNamespace: "piano"}

	tests := []struct {
		name string
		// interrupt returns the run ctx and arms what cuts its wait short
		// once the model's first call has been made.
		interrupt func(t *testing.T) (context.Context, func())
		want      string
	}{
		{
			name: "cancelled",
			interrupt: func(t *testing.T) (context.Context, func()) {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				return ctx, func() { time.AfterFunc(50*time.Millisecond, cancel) }
			},
			want: "canceled",
		},
		{
			name: "step deadline",
			interrupt: func(t *testing.T) (context.Context, func()) {
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				t.Cleanup(cancel)
				return ctx, func() {}
			},
			want: "error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := withFreshTaskRegistry(t)
			sess := "sess-wait-" + strings.ReplaceAll(tt.name, " ", "-")
			// Never finishes, so the run blocks in its background-task wait.
			registerDrainTask(t, reg, sess)

			ctx, arm := tt.interrupt(t)
			p := &spanRecordingProvider{scriptedProvider: &scriptedProvider{
				respond: func(int, bool) *provider.ProviderResponse { return endTurn() },
				onCall:  func(int) { arm() },
			}}
			a := newLoopAgentWithTools(t, p, nil)

			res := a.processGeneration(ctx, sess, "do work", 10, nil, RunOptions{NonInteractive: true})
			if res.Error != nil {
				t.Fatalf("processGeneration error = %v; this path returns the pre-wait result", res.Error)
			}
			md := spanMetadata(t, p.span, "langfuse.trace.metadata.")
			if got := md["piano.finish_reason"]; got != tt.want {
				t.Errorf("piano.finish_reason = %q, want %q", got, tt.want)
			}
			if got := md["piano.turns_used"]; got != "1" {
				t.Errorf("piano.turns_used = %q, want 1", got)
			}
		})
	}
}

// panickingProvider remembers the run's trace span, then panics inside the
// model call.
type panickingProvider struct {
	*scriptedProvider
	span trace.Span
}

func (p *panickingProvider) StreamResponse(ctx context.Context, _ []message.Message, _ []tools.BaseTool) <-chan provider.ProviderEvent {
	p.span = trace.SpanFromContext(ctx)
	panic("provider blew up")
}

// A panic leaves the run's result zero-valued, which would read as a natural
// end_turn. The trace must record an error, and the panic must still reach
// RunWith's recovery.
func TestProcessGenerationStampsAPanicAsError(t *testing.T) {
	loadConfigIn(t, ".")
	setLangfuseClient(t, true)
	config.Get().Telemetry = &config.TelemetryConfig{MetadataNamespace: "piano"}
	withFreshTaskRegistry(t)

	p := &panickingProvider{scriptedProvider: &scriptedProvider{}}
	a := newLoopAgentWithTools(t, p, nil)

	func() {
		defer func() {
			if r := recover(); r != "provider blew up" {
				t.Errorf("recovered %v, want the provider's panic to propagate", r)
			}
		}()
		a.processGeneration(context.Background(), "sess-panic", "do work", 10, nil, RunOptions{})
		t.Error("processGeneration returned; want the panic to propagate")
	}()

	if p.span == nil {
		t.Fatal("provider never saw a trace span")
	}
	md := spanMetadata(t, p.span, "langfuse.trace.metadata.")
	if got := md["piano.finish_reason"]; got != "error" {
		t.Errorf("piano.finish_reason = %q, want error", got)
	}
	if got := md["piano.turns_used"]; got != "1" {
		t.Errorf("piano.turns_used = %q, want 1", got)
	}
}

func maxTokensStop() *provider.ProviderResponse {
	return &provider.ProviderResponse{
		Content:      "a reply cut off mid-",
		FinishReason: message.FinishReasonMaxTokens,
	}
}

// A run whose last model call stopped on the output-token limit ends there:
// the loop does not continue a truncated reply, so the run reports
// max_tokens. A truncated reply the run re-entered past is not its finish.
func TestProcessGenerationStampsMaxTokens(t *testing.T) {
	loadConfigIn(t, ".")
	setLangfuseClient(t, true)
	config.Get().Telemetry = &config.TelemetryConfig{MetadataNamespace: "piano"}

	t.Run("terminal truncation", func(t *testing.T) {
		withFreshTaskRegistry(t)
		p := &spanRecordingProvider{scriptedProvider: &scriptedProvider{
			respond: func(int, bool) *provider.ProviderResponse { return maxTokensStop() },
		}}
		a := newLoopAgentWithTools(t, p, nil)

		res := a.processGeneration(context.Background(), "sess-max-tokens", "do work", 10, nil, RunOptions{NonInteractive: true})
		if res.Error != nil {
			t.Fatalf("processGeneration error: %v", res.Error)
		}
		md := spanMetadata(t, p.span, "langfuse.trace.metadata.")
		if got := md["piano.finish_reason"]; got != "max_tokens" {
			t.Errorf("piano.finish_reason = %q, want max_tokens", got)
		}
		if got := md["piano.turns_used"]; got != "1" {
			t.Errorf("piano.turns_used = %q, want 1", got)
		}
	})

	t.Run("truncation the run re-entered past", func(t *testing.T) {
		reg := withFreshTaskRegistry(t)
		const sess = "sess-max-tokens-reentry"
		taskID := registerDrainTask(t, reg, sess)
		p := &spanRecordingProvider{scriptedProvider: &scriptedProvider{
			respond: func(call int, _ bool) *provider.ProviderResponse {
				if call == 1 {
					return maxTokensStop()
				}
				return endTurn()
			},
			// The background task completes while the run waits on it after
			// the first call, so the run re-enters for a second call.
			onCall: func(call int) {
				if call == 1 {
					time.AfterFunc(50*time.Millisecond, func() {
						reg.MarkFinished(taskID, task.StateCompleted, nil)
					})
				}
			},
		}}
		a := newLoopAgentWithTools(t, p, nil)

		res := a.processGeneration(context.Background(), sess, "do work", 10, nil, RunOptions{NonInteractive: true})
		if res.Error != nil {
			t.Fatalf("processGeneration error: %v", res.Error)
		}
		if got := p.callCount(); got != 2 {
			t.Fatalf("provider calls = %d, want 2 (the re-entry after the wait)", got)
		}
		md := spanMetadata(t, p.span, "langfuse.trace.metadata.")
		if got := md["piano.finish_reason"]; got != "end_turn" {
			t.Errorf("piano.finish_reason = %q, want end_turn", got)
		}
		if got := md["piano.turns_used"]; got != "2" {
			t.Errorf("piano.turns_used = %q, want 2", got)
		}
	})
}

// modelProvider is a scriptedProvider serving a named model, so two agents'
// traces carry different model_id values.
type modelProvider struct {
	*scriptedProvider
	id models.ModelID
}

func (p modelProvider) Model() models.Model { return models.Model{ID: p.id} }

// A subagent's trace lives inside its parent's, so its agent_id, session_id,
// model_id, parent_session_id and requester must stay on its observation: the
// parent trace keeps its own values after a child run with different ones.
func TestChildTraceKeepsTheParentTracesMetadata(t *testing.T) {
	loadConfigIn(t, ".")
	setLangfuseClient(t, true)
	config.Get().Telemetry = &config.TelemetryConfig{MetadataNamespace: "piano"}

	parent := &agent{agentID: "coder", provider: modelProvider{&scriptedProvider{}, "model-a"}}
	child := &agent{agentID: "explorer", provider: modelProvider{&scriptedProvider{}, "model-b"}}

	parentCtx := parent.createLangfuseTrace(tools.WithRequester(context.Background(), "alice@example.com"),
		session.Session{ID: "parent-session"}, nil)
	childCtx := child.createLangfuseTrace(tools.WithRequester(parentCtx, "bob@example.com"),
		session.Session{ID: "child-session", ParentSessionID: "parent-session", RootSessionID: "parent-session"}, nil)
	parentSpan, childSpan := trace.SpanFromContext(parentCtx), trace.SpanFromContext(childCtx)
	if parentSpan.SpanContext().TraceID() != childSpan.SpanContext().TraceID() {
		t.Fatal("child trace is not nested in the parent's trace")
	}

	wantParent := map[string]string{
		"piano.agent_id":   "coder",
		"piano.session_id": "parent-session",
		"piano.model_id":   "model-a",
		"piano.requester":  "alice@example.com",
	}
	if got := spanMetadata(t, childSpan, "langfuse.trace.metadata."); len(got) != 0 {
		t.Errorf("child wrote trace-level metadata %v; it would overwrite the parent's", got)
	}
	parentMD := spanMetadata(t, parentSpan, "langfuse.trace.metadata.")
	for k, want := range wantParent {
		if got := parentMD[k]; got != want {
			t.Errorf("parent trace %s = %q, want %q", k, got, want)
		}
	}
	childMD := spanMetadata(t, childSpan, "langfuse.observation.metadata.")
	for k, want := range map[string]string{
		"piano.agent_id":          "explorer",
		"piano.session_id":        "child-session",
		"piano.model_id":          "model-b",
		"piano.parent_session_id": "parent-session",
		"piano.requester":         "bob@example.com",
	} {
		if got := childMD[k]; got != want {
			t.Errorf("child observation %s = %q, want %q", k, got, want)
		}
	}
}
