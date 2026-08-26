package langfuse

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type contextKey string

const rootSpanKey contextKey = "langfuse_root_span"

// rootSpan holds the root trace span plus whether it is a child trace
// (a subagent observation inside a parent trace). Trace-level attributes
// (langfuse.trace.*) must only be set on true roots — on a child they
// would overwrite the parent trace's values.
type rootSpan struct {
	span    trace.Span
	isChild bool
}

// withRootSpan stores the root trace span in context so child spans
// (generations, tools) can be created as siblings under the same trace.
func withRootSpan(ctx context.Context, span trace.Span, isChild bool) context.Context {
	return context.WithValue(ctx, rootSpanKey, rootSpan{span: span, isChild: isChild})
}

// getRootSpan returns the root trace span from context, or nil.
func getRootSpan(ctx context.Context) trace.Span {
	if v, ok := ctx.Value(rootSpanKey).(rootSpan); ok {
		return v.span
	}
	return nil
}

// SetTraceOutput records the final output of the trace held in context.
// On a root trace it sets the trace-level output; on a child trace
// (subagent) it sets the observation output so the parent trace's own
// output is not overwritten. No-op when context has no trace.
func SetTraceOutput(ctx context.Context, output any) {
	v, ok := ctx.Value(rootSpanKey).(rootSpan)
	if !ok || v.span == nil {
		return
	}
	key := "langfuse.trace.output"
	if v.isChild {
		key = "langfuse.observation.output"
	}
	v.span.SetAttributes(
		attribute.String(key, truncate(marshalAny(output), maxGenIOSize)),
	)
}
