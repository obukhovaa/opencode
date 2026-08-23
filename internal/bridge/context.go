package bridge

import "context"

// ExternalQuestionContext carries the values the "external" channel's
// question relay frame needs that don't fit the fixed
// bridge.InteractiveQuestionSender signature: the question.Request id
// (the consumer answers against it — SendInteractiveQuestion has no
// requestId parameter) and whether the originating prompt allows
// multiple selections. The opencode sessionId is a SEPARATE context key
// (ContextWithSessionID) because it applies to every outbound send, not
// only questions.
//
// Rationale for propagating via context.Context rather than widening the
// Adapter/InteractiveQuestionSender interfaces: those interfaces are also
// implemented by the Slack and Mattermost adapters, and changing their
// signatures would touch both for a value only the "external" channel
// reads. Every other adapter simply never reads these context keys, so
// stamping them unconditionally at the call sites (service/send.go,
// service/question.go) is a no-op everywhere except this package.
type ExternalQuestionContext struct {
	RequestID string
	Multiple  bool
}

type ctxKeySessionID struct{}
type ctxKeyExternalQuestion struct{}

// ContextWithSessionID returns a copy of ctx carrying sessionID. Read back
// with SessionIDFromContext.
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, ctxKeySessionID{}, sessionID)
}

// SessionIDFromContext returns the sessionID stamped by
// ContextWithSessionID, if any.
func SessionIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeySessionID{}).(string)
	return v, ok
}

// ContextWithExternalQuestion returns a copy of ctx carrying q, read back
// with ExternalQuestionFromContext. Stamped by QuestionRouter.handleNewRequest
// ahead of calling SendInteractiveQuestion so the "external" adapter can
// recover the requestId and multiple-choice flag the fixed
// InteractiveQuestionSender signature has no room for.
func ContextWithExternalQuestion(ctx context.Context, q ExternalQuestionContext) context.Context {
	return context.WithValue(ctx, ctxKeyExternalQuestion{}, q)
}

// ExternalQuestionFromContext returns the ExternalQuestionContext stamped
// by ContextWithExternalQuestion, if any.
func ExternalQuestionFromContext(ctx context.Context) (ExternalQuestionContext, bool) {
	v, ok := ctx.Value(ctxKeyExternalQuestion{}).(ExternalQuestionContext)
	return v, ok
}
