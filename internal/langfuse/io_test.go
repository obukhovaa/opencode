package langfuse

import (
	"context"
	"testing"

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

	ctx := c.TraceStart(context.Background(), TraceParams{Name: "subagent", Input: "task prompt", IsChild: true})
	SetTraceOutput(ctx, "subagent result")
	c.TraceEnd(ctx)

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
