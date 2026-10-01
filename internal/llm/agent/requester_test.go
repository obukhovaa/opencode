package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
)

func TestStampRequester(t *testing.T) {
	tests := []struct {
		name       string
		static     string
		ctxValue   string
		flowArg    string
		hasFlowArg bool
		want       string
		wantUnset  bool
	}{
		{name: "nothing known leaves metadata unset", wantUnset: true},
		{name: "static config is the fallback", static: "owner@example.com", want: "owner@example.com"},
		{name: "per-turn requester beats static config", static: "owner@example.com", ctxValue: "author@example.com", want: "author@example.com"},
		{name: "per-turn requester without static config", ctxValue: "U123", want: "U123"},
		{name: "flow arg beats both", static: "owner@example.com", ctxValue: "author@example.com", flowArg: "flow@example.com", hasFlowArg: true, want: "flow@example.com"},
		{name: "empty flow arg falls through to per-turn requester", ctxValue: "author@example.com", hasFlowArg: true, want: "author@example.com"},
		{name: "blank flow arg falls through to static config", static: "owner@example.com", flowArg: "  \t", hasFlowArg: true, want: "owner@example.com"},
		{name: "empty flow arg with nothing else known is dropped", hasFlowArg: true, wantUnset: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loadConfigIn(t, ".")
			config.Get().Telemetry = &config.TelemetryConfig{Requester: tt.static}

			metadata := map[string]any{}
			if tt.hasFlowArg {
				metadata["requester"] = tt.flowArg
			}
			ctx := tools.WithRequester(context.Background(), tt.ctxValue)
			stampRequester(ctx, metadata)

			got, ok := metadata["requester"]
			if tt.wantUnset {
				if ok {
					t.Fatalf("requester = %q, want unset", got)
				}
				return
			}
			if got != tt.want {
				t.Fatalf("requester = %q, want %q", got, tt.want)
			}
		})
	}
}

// traceMetadata returns the langfuse.trace.metadata.* attributes of the
// root span createLangfuseTrace put on ctx, keyed without the prefix. The
// span is never ended, so nothing is queued for export.
func traceMetadata(t *testing.T, ctx context.Context) map[string]string {
	t.Helper()
	span, ok := trace.SpanFromContext(ctx).(sdktrace.ReadOnlySpan)
	if !ok {
		t.Fatal("createLangfuseTrace put no recording span on the ctx")
	}
	out := map[string]string{}
	for _, kv := range span.Attributes() {
		if k, ok := strings.CutPrefix(string(kv.Key), "langfuse.trace.metadata."); ok {
			out[k] = kv.Value.AsString()
		}
	}
	return out
}

// The requester must reach the exported trace under the configured
// namespace, with flow arg > per-turn requester > telemetry.requester.
func TestCreateLangfuseTraceStampsNamespacedRequester(t *testing.T) {
	// Load once, before setLangfuseClient blanks LANGFUSE_*: viper keeps
	// config search paths across tests, so a sibling test's unisolated Load
	// can surface a langfuse-enabled home config whose validation needs
	// those vars. Each case then swaps in its own Telemetry.
	loadConfigIn(t, ".")
	setLangfuseClient(t, true)
	tests := []struct {
		name      string
		static    string
		ctxValue  string
		flowArg   string
		want      string
		wantUnset bool
	}{
		{name: "flow arg wins", static: "owner@example.com", ctxValue: "author@example.com", flowArg: "flow@example.com", want: "flow@example.com"},
		{name: "per-turn requester beats static config", static: "owner@example.com", ctxValue: "author@example.com", want: "author@example.com"},
		{name: "static config is the fallback", static: "owner@example.com", want: "owner@example.com"},
		{name: "nothing known omits the key", wantUnset: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.Get().Telemetry = &config.TelemetryConfig{
				MetadataNamespace: "piano",
				FlowArgs:          []string{"requester"},
				Requester:         tt.static,
			}

			ctx := tools.WithRequester(context.Background(), tt.ctxValue)
			if tt.flowArg != "" {
				ctx = context.WithValue(ctx, tools.FlowIDContextKey, "flow-1")
				ctx = context.WithValue(ctx, tools.FlowArgsContextKey, map[string]string{"requester": tt.flowArg})
			}
			a := &agent{agentID: "coder", provider: &stubProvider{}}
			md := traceMetadata(t, a.createLangfuseTrace(ctx, session.Session{ID: "s1"}, nil))

			if _, flat := md["requester"]; flat {
				t.Errorf("flat requester key present; want it namespaced: %v", md)
			}
			got, ok := md["piano.requester"]
			if tt.wantUnset {
				if ok {
					t.Fatalf("piano.requester = %q, want unset", got)
				}
				return
			}
			if got != tt.want {
				t.Fatalf("piano.requester = %q, want %q (metadata: %v)", got, tt.want, md)
			}
		})
	}
}

// subagentStub is the async subagent: it records the requester its Run ctx
// carries and finishes at once, so runAsync's waiter goroutine completes.
type subagentStub struct {
	Service // nil — only Run is called
	got     chan string
}

func (s *subagentStub) Run(ctx context.Context, _, _ string, _ int, _ ...message.Attachment) (<-chan AgentEvent, error) {
	s.got <- tools.RequesterFromContext(ctx)
	done := make(chan AgentEvent, 1)
	done <- AgentEvent{Type: AgentEventTypeResponse}
	close(done)
	return done, nil
}

// resumeRecorder is the task.Deps the async completion goes through; the
// resume is the waiter goroutine's last step.
type resumeRecorder struct{ resumed chan string }

func (r *resumeRecorder) WritePair(context.Context, string, task.SyntheticPair) error { return nil }
func (r *resumeRecorder) IsSessionBusy(string) bool                                   { return false }
func (r *resumeRecorder) ResumeSession(_, requester string)                           { r.resumed <- requester }

// An async subagent runs on a ctx detached from the spawning turn, and its
// completion auto-resumes the parent later; both must keep the turn's
// requester.
func TestRunAsyncCarriesRequester(t *testing.T) {
	withFreshTaskRegistry(t)
	deps := &resumeRecorder{resumed: make(chan string, 1)}
	t.Cleanup(task.SetDeps(deps))
	sub := &subagentStub{got: make(chan string, 1)}
	b := &agentTool{sessions: &memSessions{}}

	ctx := tools.WithRequester(context.Background(), "alice@")
	resp, err := b.runAsync(ctx, tools.ToolCall{ID: "call-async"}, TaskParams{TaskTitle: "bg"},
		"PARENT", "explorer", agentregistry.AgentInfo{}, session.Session{ID: "CHILD"}, false, sub, "do it")
	if err != nil || resp.IsError {
		t.Fatalf("runAsync: err=%v resp=%q", err, resp.Content)
	}

	if got := <-sub.got; got != "alice@" {
		t.Errorf("subagent run requester = %q, want alice@", got)
	}
	select {
	case got := <-deps.resumed:
		if got != "alice@" {
			t.Errorf("parent resume requester = %q, want alice@", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("async completion never resumed the parent session")
	}
}

// titleRequesterSpy records the requester on the title generation ctx.
type titleRequesterSpy struct {
	*stubProvider
	got chan string
}

func (s *titleRequesterSpy) SendMessages(ctx context.Context, _ []message.Message, _ []tools.BaseTool) (*provider.ProviderResponse, error) {
	s.got <- tools.RequesterFromContext(ctx)
	// Failing ends generateTitle right here: no session write, and no
	// further reads of globals a later test may reset.
	return nil, errors.New("stop after capture")
}

// The first turn's title is generated on a ctx detached from the turn; its
// trace must still be attributed to the turn's requester, including a flow
// step's `requester` arg, which the detached ctx would otherwise drop.
func TestTitleGenerationCarriesRequester(t *testing.T) {
	withFlowArg := func(ctx context.Context, v string) context.Context {
		return context.WithValue(ctx, tools.FlowArgsContextKey, map[string]string{"requester": v})
	}
	turnCtx := tools.WithRequester(context.Background(), "alice@")
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "per-turn requester", ctx: turnCtx, want: "alice@"},
		{name: "flow step's requester arg", ctx: withFlowArg(context.Background(), "flow@"), want: "flow@"},
		{name: "flow arg beats the per-turn requester, as on the trace", ctx: withFlowArg(turnCtx, "flow@"), want: "flow@"},
		{name: "blank flow arg falls through to the per-turn requester", ctx: withFlowArg(turnCtx, " "), want: "alice@"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withFreshTaskRegistry(t)
			a := newLoopAgent(t, &scriptedProvider{respond: func(int, bool) *provider.ProviderResponse { return endTurn() }})
			spy := &titleRequesterSpy{stubProvider: &stubProvider{}, got: make(chan string, 1)}
			a.titleProvider = spy

			if res := a.processGeneration(tt.ctx, "sess-title", "first message", 0, nil, RunOptions{NonInteractive: true}); res.Error != nil {
				t.Fatalf("processGeneration: %v", res.Error)
			}

			select {
			case got := <-spy.got:
				if got != tt.want {
					t.Fatalf("title generation requester = %q, want %q", got, tt.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("title generation never ran")
			}
		})
	}
}
