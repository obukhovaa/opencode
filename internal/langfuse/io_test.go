package langfuse

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func newTestClient() (*Client, *tracetest.SpanRecorder) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	return &Client{tracer: tp.Tracer(tracerName), provider: tp, enabled: true}, sr
}

func attrValue(spans []sdktrace.ReadOnlySpan, name, key string) (string, bool) {
	for _, s := range spans {
		if s.Name() != name {
			continue
		}
		for _, kv := range s.Attributes() {
			if string(kv.Key) == key {
				return kv.Value.AsString(), true
			}
		}
	}
	return "", false
}

func TestTraceInputOutputRoot(t *testing.T) {
	c, sr := newTestClient()

	ctx := c.TraceStart(context.Background(), TraceParams{Name: "turn", Input: "user asks a thing"})
	SetTraceOutput(ctx, "final answer")
	c.TraceEnd(ctx)

	spans := sr.Ended()
	if got, ok := attrValue(spans, "turn", "langfuse.trace.input"); !ok || got != "user asks a thing" {
		t.Fatalf("trace input = %q, ok=%v", got, ok)
	}
	if got, ok := attrValue(spans, "turn", "langfuse.trace.output"); !ok || got != "final answer" {
		t.Fatalf("trace output = %q, ok=%v", got, ok)
	}
}

func TestTraceInputOutputChildUsesObservationKeys(t *testing.T) {
	c, sr := newTestClient()

	parent := c.TraceStart(context.Background(), TraceParams{Name: "parent"})
	ctx := c.TraceStart(parent, TraceParams{Name: "subagent", Input: "task prompt", IsChild: true})
	SetTraceOutput(ctx, "subagent result")
	c.TraceEnd(ctx)
	c.TraceEnd(parent)

	spans := sr.Ended()
	if _, ok := attrValue(spans, "subagent", "langfuse.trace.input"); ok {
		t.Fatal("child trace must not set langfuse.trace.input — it would overwrite the parent's")
	}
	if _, ok := attrValue(spans, "subagent", "langfuse.trace.output"); ok {
		t.Fatal("child trace must not set langfuse.trace.output — it would overwrite the parent's")
	}
	if got, ok := attrValue(spans, "subagent", "langfuse.observation.input"); !ok || got != "task prompt" {
		t.Fatalf("child observation input = %q, ok=%v", got, ok)
	}
	if got, ok := attrValue(spans, "subagent", "langfuse.observation.output"); !ok || got != "subagent result" {
		t.Fatalf("child observation output = %q, ok=%v", got, ok)
	}
}

// Run facts recorded after the trace started land on the trace itself for a
// root, and on the observation for a child: a subagent's turn count must not
// replace its parent's.
func TestSetTraceMetadata(t *testing.T) {
	tests := []struct {
		name    string
		isChild bool
		want    string
		absent  string
	}{
		{name: "root writes trace metadata", want: "langfuse.trace.metadata.", absent: "langfuse.observation.metadata."},
		{name: "child writes observation metadata", isChild: true, want: "langfuse.observation.metadata.", absent: "langfuse.trace.metadata."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, sr := newTestClient()
			// A child nests in the calling run's trace, so start one.
			parent := c.TraceStart(context.Background(), TraceParams{Name: "parent"})
			ctx := c.TraceStart(parent, TraceParams{Name: "run", IsChild: tt.isChild})
			SetTraceMetadata(ctx, map[string]any{"piano.turns_used": 3, "piano.finish_reason": "end_turn"})
			c.TraceEnd(ctx)
			c.TraceEnd(parent)

			spans := sr.Ended()
			if got, ok := attrValue(spans, "run", tt.want+"piano.turns_used"); !ok || got != "3" {
				t.Errorf("%spiano.turns_used = %q, ok=%v", tt.want, got, ok)
			}
			if got, ok := attrValue(spans, "run", tt.want+"piano.finish_reason"); !ok || got != "end_turn" {
				t.Errorf("%spiano.finish_reason = %q, ok=%v", tt.want, got, ok)
			}
			if _, ok := attrValue(spans, "run", tt.absent+"piano.turns_used"); ok {
				t.Errorf("%spiano.turns_used set; want it absent", tt.absent)
			}
		})
	}
}

// A subagent's trace is an observation inside its parent's trace, so its
// metadata must not be written at trace level: the parent trace keeps its own
// agent_id, session_id, model_id and requester after a child run with
// different values, and the child's values land on its observation.
func TestChildTraceMetadataDoesNotOverwriteTheParents(t *testing.T) {
	c, sr := newTestClient()

	parentMeta := map[string]any{
		"piano.agent_id":   "coder",
		"piano.session_id": "parent-session",
		"piano.model_id":   "model-a",
		"piano.requester":  "alice@example.com",
	}
	childMeta := map[string]any{
		"piano.agent_id":          "explorer",
		"piano.session_id":        "child-session",
		"piano.model_id":          "model-b",
		"piano.parent_session_id": "parent-session",
		"piano.requester":         "bob@example.com",
	}
	parentCtx := c.TraceStart(context.Background(), TraceParams{Name: "parent", Metadata: parentMeta})
	childCtx := c.TraceStart(parentCtx, TraceParams{Name: "child", Metadata: childMeta, IsChild: true})
	c.TraceEnd(childCtx)
	c.TraceEnd(parentCtx)

	spans := sr.Ended()
	if len(spans) != 2 || spans[0].SpanContext().TraceID() != spans[1].SpanContext().TraceID() {
		t.Fatalf("want the child nested in the parent's trace, got %d span(s)", len(spans))
	}
	// Every trace-level metadata key written by any span of the trace is
	// the parent's: Langfuse applies trace attributes from any span.
	for _, s := range spans {
		for _, kv := range s.Attributes() {
			k, ok := strings.CutPrefix(string(kv.Key), "langfuse.trace.metadata.")
			if !ok {
				continue
			}
			if want, isParents := parentMeta[k]; !isParents || kv.Value.AsString() != want {
				t.Errorf("span %q writes trace metadata %s = %q; only the parent's values may reach the trace", s.Name(), k, kv.Value.AsString())
			}
		}
	}
	for k, want := range parentMeta {
		if got, ok := attrValue(spans, "parent", "langfuse.trace.metadata."+k); !ok || got != want {
			t.Errorf("parent trace metadata %s = %q (ok=%v), want %q", k, got, ok, want)
		}
	}
	for k, want := range childMeta {
		if got, ok := attrValue(spans, "child", "langfuse.observation.metadata."+k); !ok || got != want {
			t.Errorf("child observation metadata %s = %q (ok=%v), want %q", k, got, ok, want)
		}
	}
}

// A child session run on a detached ctx (an async subagent, an auto-resumed
// subagent session) has no parent trace in ctx: OTel starts a new trace for
// it, so it is that trace's root and keeps trace-level name, input and
// metadata, exactly like a root run.
func TestDetachedChildTraceIsItsOwnRoot(t *testing.T) {
	c, sr := newTestClient()

	parent := c.TraceStart(context.Background(), TraceParams{Name: "parent"})
	c.TraceEnd(parent)
	ctx := c.TraceStart(context.Background(), TraceParams{
		Name: "async-subagent", Input: "task prompt", IsChild: true,
		Metadata: map[string]any{"piano.agent_id": "explorer"},
	})
	SetTraceMetadata(ctx, map[string]any{"piano.turns_used": 2})
	c.TraceEnd(ctx)

	spans := sr.Ended()
	if spans[0].SpanContext().TraceID() == spans[1].SpanContext().TraceID() {
		t.Fatal("a detached child must start a trace of its own")
	}
	for key, want := range map[string]string{
		"langfuse.trace.name":                      "async-subagent",
		"langfuse.trace.input":                     "task prompt",
		"langfuse.trace.metadata.piano.agent_id":   "explorer",
		"langfuse.trace.metadata.piano.turns_used": "2",
	} {
		if got, ok := attrValue(spans, "async-subagent", key); !ok || got != want {
			t.Errorf("%s = %q (ok=%v), want %q", key, got, ok, want)
		}
	}
}

func TestSetTraceMetadataNoTraceIsNoop(t *testing.T) {
	SetTraceMetadata(context.Background(), map[string]any{"k": "v"}) // must not panic
}

func TestGenerationInputAndOutput(t *testing.T) {
	c, sr := newTestClient()

	ctx := c.TraceStart(context.Background(), TraceParams{Name: "turn"})
	gen := c.GenerationStart(ctx, GenerationParams{
		Name:  "coder/model",
		Model: "model",
		Input: []map[string]string{{"role": "user", "content": "hello"}},
	})
	gen.SetGenerationOutput(map[string]string{"role": "assistant", "content": "world"})
	gen.End()
	c.TraceEnd(ctx)

	spans := sr.Ended()
	if got, ok := attrValue(spans, "coder/model", "langfuse.observation.input"); !ok || got != `[{"content":"hello","role":"user"}]` {
		t.Fatalf("generation input = %q, ok=%v", got, ok)
	}
	if got, ok := attrValue(spans, "coder/model", "langfuse.observation.output"); !ok || got != `{"content":"world","role":"assistant"}` {
		t.Fatalf("generation output = %q, ok=%v", got, ok)
	}
}

func TestSetTraceOutputNoTraceIsNoop(t *testing.T) {
	SetTraceOutput(context.Background(), "ignored") // must not panic
}

// TestTruncateKeepsValidUTF8 guards the OTLP export: the protobuf encoder
// rejects invalid UTF-8 in a string field and fails the whole batch, so a cut
// that lands mid-rune must back off to the rune boundary.
func TestTruncateKeepsValidUTF8(t *testing.T) {
	// "€" is 3 bytes, so a cut at 1..2 bytes past "ab" lands mid-rune.
	s := "ab€€€"
	for max := 0; max <= len(s); max++ {
		got := truncate(s, max)
		if !utf8.ValidString(got) {
			t.Errorf("truncate(%q, %d) = %q: invalid UTF-8", s, max, got)
		}
		if len(got) > max+len("...[truncated]") {
			t.Errorf("truncate(%q, %d) = %q: exceeds cap", s, max, got)
		}
	}
	if got := truncate(s, len(s)); got != s {
		t.Errorf("truncate at exact length = %q, want unchanged", got)
	}
}

// marshalAny's string fast path skips json.Marshal (which substitutes U+FFFD),
// so it must sanitize itself: trace input/output are raw user/model strings, and
// invalid UTF-8 in a proto3 string field fails the OTLP marshal and drops the
// entire export batch.
func TestMarshalAnySanitizesInvalidUTF8(t *testing.T) {
	raw := "prompt \x80\xff tail" // lone continuation + invalid byte
	if utf8.ValidString(raw) {
		t.Fatal("fixture is supposed to be invalid UTF-8")
	}

	got := marshalAny(raw)
	if !utf8.ValidString(got) {
		t.Errorf("marshalAny(%q) = %q: still invalid UTF-8", raw, got)
	}
	if !strings.Contains(got, "prompt ") || !strings.Contains(got, " tail") {
		t.Errorf("marshalAny(%q) = %q: dropped surrounding text", raw, got)
	}

	// The struct path goes through json.Marshal, which sanitizes already.
	viaJSON := marshalAny(map[string]string{"content": raw})
	if !utf8.ValidString(viaJSON) {
		t.Errorf("marshalAny(struct) = %q: invalid UTF-8", viaJSON)
	}
}

// SetOutput and SetGenerationOutput share one body; only the cap differs.
func TestSetOutputCaps(t *testing.T) {
	c, sr := newTestClient()
	ctx := c.TraceStart(context.Background(), TraceParams{Name: "turn"})

	big := strings.Repeat("x", maxGenIOSize+100)
	tool := c.ToolStart(ctx, ToolParams{Name: "tool"})
	tool.SetOutput(big)
	tool.End()

	gen := c.GenerationStart(ctx, GenerationParams{Name: "gen"})
	gen.SetGenerationOutput(big)
	gen.End()
	c.TraceEnd(ctx)

	spans := sr.Ended()
	toolOut, ok := attrValue(spans, "tool", "langfuse.observation.output")
	if !ok || len(toolOut) > maxIOSize+len("...[truncated]") {
		t.Errorf("tool output len = %d, want <= %d", len(toolOut), maxIOSize)
	}
	genOut, ok := attrValue(spans, "gen", "langfuse.observation.output")
	if !ok || len(genOut) <= maxIOSize {
		t.Errorf("generation output len = %d, want the larger generation cap", len(genOut))
	}
	if len(genOut) > maxGenIOSize+len("...[truncated]") {
		t.Errorf("generation output len = %d exceeds cap %d", len(genOut), maxGenIOSize)
	}
}
