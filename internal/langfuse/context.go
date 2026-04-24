package langfuse

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

type contextKey string

const rootSpanKey contextKey = "langfuse_root_span"

// withRootSpan stores the root trace span in context so child spans
// (generations, tools) can be created as siblings under the same trace.
func withRootSpan(ctx context.Context, span trace.Span) context.Context {
	return context.WithValue(ctx, rootSpanKey, span)
}

// getRootSpan returns the root trace span from context, or nil.
func getRootSpan(ctx context.Context) trace.Span {
	if v, ok := ctx.Value(rootSpanKey).(trace.Span); ok {
		return v
	}
	return nil
}
