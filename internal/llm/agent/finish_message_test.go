package agent

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// recordingMessages captures Update calls and returns whatever the caller's
// ctx says — so a cancelled ctx propagates as ctx.Err() and a fresh ctx
// records a successful update.
type recordingMessages struct {
	message.Service
	updateCalls  int
	updateCtxErr []error // ctx.Err() at the moment of each Update call
	updatedMsg   message.Message
}

func (r *recordingMessages) Update(ctx context.Context, msg message.Message) error {
	r.updateCalls++
	r.updateCtxErr = append(r.updateCtxErr, ctx.Err())
	r.updatedMsg = msg
	return ctx.Err()
}

// TestFinishMessage_PersistsOnCancelledCtx is a regression guard for the
// `parts: []` / `finished_at: null` symptom observed in TPWEBAPP-62638's
// stuck analyze-issue session.
//
// Scenario: graceful shutdown / step-timeout cancels the parent ctx mid-stream,
// the agent loop tries to record a Canceled finish marker on its in-flight
// assistant message via finishMessage. With the pre-fix code, the same
// cancelled ctx was threaded into a.messages.Update, the DB driver short-
// circuited on ctx.Err(), and the finish marker never landed — leaving the
// row indistinguishable from "stream still in flight". The fix swaps in a
// fresh background context with a 5s deadline when the caller's ctx is
// already cancelled, so the Update actually executes.
func TestFinishMessage_PersistsOnCancelledCtx(t *testing.T) {
	rec := &recordingMessages{}
	a := &agent{
		Broker:   pubsub.NewBroker[AgentEvent](),
		messages: rec,
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	msg := message.Message{ID: "msg-1", Role: message.Assistant}
	a.finishMessage(cancelledCtx, &msg, message.FinishReasonCanceled)

	if rec.updateCalls != 1 {
		t.Fatalf("Update calls = %d, want 1", rec.updateCalls)
	}
	if rec.updateCtxErr[0] != nil {
		t.Errorf("Update was invoked with a cancelled ctx (err=%v) — fix did not engage", rec.updateCtxErr[0])
	}
	if !rec.updatedMsg.IsFinished() {
		t.Error("persisted message has no Finish part — AddFinish was not applied")
	}
	if rec.updatedMsg.FinishReason() != message.FinishReasonCanceled {
		t.Errorf("FinishReason = %q, want %q", rec.updatedMsg.FinishReason(), message.FinishReasonCanceled)
	}
}

// TestFinishMessage_UsesCallerCtxWhenHealthy: belt-and-braces guard that the
// fix doesn't unconditionally swap ctx — happy-path callers must still get
// their original ctx (so they keep any deadlines/values).
func TestFinishMessage_UsesCallerCtxWhenHealthy(t *testing.T) {
	rec := &recordingMessages{}
	a := &agent{
		Broker:   pubsub.NewBroker[AgentEvent](),
		messages: rec,
	}

	type ctxKey struct{}
	healthyCtx := context.WithValue(context.Background(), ctxKey{}, "sentinel")

	msg := message.Message{ID: "msg-2", Role: message.Assistant}
	a.finishMessage(healthyCtx, &msg, message.FinishReasonEndTurn)

	if rec.updateCalls != 1 {
		t.Fatalf("Update calls = %d, want 1", rec.updateCalls)
	}
	if rec.updateCtxErr[0] != nil {
		t.Errorf("Update on healthy ctx unexpectedly saw err=%v", rec.updateCtxErr[0])
	}
}
