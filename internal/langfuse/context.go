package langfuse

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type contextKey string

const rootSpanKey contextKey = "langfuse_root_span"

// rootSpan holds the root trace span plus whether it is a child trace
// (a subagent observation nested inside a parent trace; see TraceStart).
// Trace-level attributes (langfuse.trace.*) must only be set on true roots —
// on a child they would overwrite the parent trace's values.
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

// metadataPrefix is the attribute prefix for a trace's own metadata: trace
// metadata on a root, observation metadata on a child trace (a subagent),
// whose trace-level keys would overwrite the parent trace's.
func metadataPrefix(isChild bool) string {
	if isChild {
		return "langfuse.observation.metadata."
	}
	return "langfuse.trace.metadata."
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
		attribute.String(key, payload(output, maxGenIOSize)),
	)
}

// SetTraceMetadata records metadata on the trace held in context after it
// started, for run facts known only later (turns used, finish reason). On a
// root trace it sets trace-level metadata; on a child trace (subagent) it
// sets the observation's metadata so the parent trace's own keys are not
// overwritten. Keys are written as given — namespacing is the caller's.
// No-op when context has no trace.
func SetTraceMetadata(ctx context.Context, metadata map[string]any) {
	v, ok := ctx.Value(rootSpanKey).(rootSpan)
	if !ok || v.span == nil || len(metadata) == 0 {
		return
	}
	prefix := metadataPrefix(v.isChild)
	attrs := make([]attribute.KeyValue, 0, len(metadata))
	for k, val := range metadata {
		attrs = append(attrs, attribute.String(prefix+k, redactString(fmt.Sprint(val))))
	}
	v.span.SetAttributes(attrs...)
}
