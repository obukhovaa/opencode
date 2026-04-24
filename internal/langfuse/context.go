package langfuse

import "context"

type contextKey string

const (
	traceIDKey   contextKey = "langfuse_trace_id"
	sessionIDKey contextKey = "langfuse_session_id"
)

// WithTraceID stores the current Langfuse trace ID in context.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// GetTraceID returns the Langfuse trace ID from context, or empty string.
func GetTraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

// WithSessionID stores the Langfuse session ID (root session) in context.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// GetSessionID returns the Langfuse session ID from context, or empty string.
func GetSessionID(ctx context.Context) string {
	if v, ok := ctx.Value(sessionIDKey).(string); ok {
		return v
	}
	return ""
}
