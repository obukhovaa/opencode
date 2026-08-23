package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/question"
)

// ctxCapturingAdapter wraps stubAdapter to record what the
// internal/bridge context keys (SessionIDFromContext,
// ExternalQuestionFromContext) look like at Send /
// SendInteractiveQuestion time — the mechanism the "external" channel
// adapter relies on to recover sessionId/requestId/multiple, values
// that don't fit the fixed bridge.Adapter / InteractiveQuestionSender
// signatures (see internal/bridge/context.go). A fake adapter is
// sufficient to prove the SERVICE side (send.go's sendToOnePeer,
// question.go's handleNewRequest) stamps the context correctly, without
// exercising the real external.Adapter's HTTP relay — that's covered
// separately in internal/bridge/external/adapter_test.go.
type ctxCapturingAdapter struct {
	*stubAdapter

	mu            sync.Mutex
	gotSessionID  []string
	gotHadSession []bool
	questionCalls []capturedQuestionCtx
}

type capturedQuestionCtx struct {
	SessionID   string
	HadSession  bool
	RequestID   string
	Multiple    bool
	HadQuestion bool
}

func newCtxCapturingAdapter(channel, identity string) *ctxCapturingAdapter {
	return &ctxCapturingAdapter{stubAdapter: newStubAdapter(channel, identity)}
}

func (a *ctxCapturingAdapter) Send(ctx context.Context, out bridge.Outbound) bridge.SendResult {
	sid, ok := bridge.SessionIDFromContext(ctx)
	a.mu.Lock()
	a.gotSessionID = append(a.gotSessionID, sid)
	a.gotHadSession = append(a.gotHadSession, ok)
	a.mu.Unlock()
	return a.stubAdapter.Send(ctx, out)
}

func (a *ctxCapturingAdapter) SendInteractiveQuestion(ctx context.Context, _ bridge.PeerRef, _ string, _ []bridge.QuestionChoice) (string, error) {
	sid, sok := bridge.SessionIDFromContext(ctx)
	eq, eok := bridge.ExternalQuestionFromContext(ctx)
	a.mu.Lock()
	a.questionCalls = append(a.questionCalls, capturedQuestionCtx{
		SessionID:   sid,
		HadSession:  sok,
		RequestID:   eq.RequestID,
		Multiple:    eq.Multiple,
		HadQuestion: eok,
	})
	a.mu.Unlock()
	return "", nil
}

// TestSendToOnePeerStampsSessionIDOnContext proves send.go's
// sendToOnePeer (the SendBySessionID fan-out path) stamps the binding's
// sessionId onto ctx before calling adapter.Send, and that a normal
// (non-ack) send leaves Outbound.IsAck false.
func TestSendToOnePeerStampsSessionIDOnContext(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())

	ad := newCtxCapturingAdapter("external", "c3")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "external", Identity: "c3", PeerID: "aid1:flow1:run1"},
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "hi"}); err != nil {
		t.Fatalf("SendBySessionID: %v", err)
	}

	if len(ad.gotSessionID) != 1 {
		t.Fatalf("Send calls = %d, want 1", len(ad.gotSessionID))
	}
	if !ad.gotHadSession[0] || ad.gotSessionID[0] != "S1" {
		t.Errorf("sessionId on context = (%q, had=%v); want (\"S1\", true)", ad.gotSessionID[0], ad.gotHadSession[0])
	}
	if sends := ad.Sends(); len(sends) != 1 || sends[0].IsAck {
		t.Errorf("Sends = %+v; want exactly one non-ack send", sends)
	}
}

// TestHandleNewRequestStampsSessionAndQuestionContext proves
// question.go's handleNewRequest stamps both the sessionId and the
// requestId/multiple-choice flag onto ctx before calling
// SendInteractiveQuestion — the end-to-end proof of the H.1
// sessionId/requestId/multiple context-plumbing design.
func TestHandleNewRequestStampsSessionAndQuestionContext(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	svc.cfg.QuestionMode = "interactive"
	_ = svc.Start(context.Background())

	ad := newCtxCapturingAdapter("external", "c3")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "external", Identity: "c3", PeerID: "aid1:flow1:run1"},
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	router := &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{}}
	router.handleNewRequest(context.Background(), question.Request{
		ID:        "req-42",
		SessionID: "S1",
		Questions: []question.Prompt{{
			Question: "ship?",
			Multiple: true,
			Options:  []question.Option{{Label: "yes"}, {Label: "no"}},
		}},
	})

	time.Sleep(50 * time.Millisecond)

	if len(ad.questionCalls) != 1 {
		t.Fatalf("SendInteractiveQuestion calls = %d, want 1", len(ad.questionCalls))
	}
	got := ad.questionCalls[0]
	if !got.HadSession || got.SessionID != "S1" {
		t.Errorf("sessionId on context = (%q, had=%v); want (\"S1\", true)", got.SessionID, got.HadSession)
	}
	if !got.HadQuestion || got.RequestID != "req-42" || !got.Multiple {
		t.Errorf("question context = %+v; want requestId=req-42, multiple=true", got)
	}
}

// TestExternalChannelBindSendUnbind proves the binding layer (Bind /
// SendBySessionID / Unbind) requires zero channel-specific code for
// "external" — per design.md D2 "the channel is an opaque string on the
// binding wire". TestUnknownChannelInBindRejected already establishes
// this generically (an arbitrary "irc" channel string is rejected only
// for lacking a registered adapter, not for failing an enum check); this
// test additionally proves the full happy path — bind, deliver, unbind
// — works for the real "external" channel name once an adapter is
// registered under it.
func TestExternalChannelBindSendUnbind(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())

	ad := newStubAdapter("external", "c3")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	peer := bridge.PeerRef{Channel: "external", Identity: "c3", PeerID: "aid1:flow1:run1"}
	results, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{peer})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("Bind results = %+v", results)
	}

	if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "hi"}); err != nil {
		t.Fatalf("SendBySessionID: %v", err)
	}
	if len(ad.Sends()) != 1 {
		t.Fatalf("Sends = %d, want 1", len(ad.Sends()))
	}

	if err := svc.Unbind(context.Background(), "S1", peer); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "after unbind"}); err == nil {
		t.Errorf("SendBySessionID after Unbind: expected error (no bindings), got nil")
	}
}

// TestReplyToPeerSetsIsAckOnlyForAnswerAck proves replyToPeer's isAck
// parameter is the ONLY thing that sets Outbound.IsAck — a normal reply
// (the cron-output / run-failure call shape) leaves it false.
func TestReplyToPeerSetsIsAckOnlyForAnswerAck(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())

	ad := newStubAdapter("external", "c3")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	peer := bridge.PeerRef{Channel: "external", Identity: "c3", PeerID: "aid1:flow1:run1"}

	svc.replyToPeer(context.Background(), peer, "normal message", false)
	svc.replyToPeer(context.Background(), peer, "ack message", true)

	sends := ad.Sends()
	if len(sends) != 2 {
		t.Fatalf("sends = %d, want 2", len(sends))
	}
	if sends[0].IsAck {
		t.Errorf("first (non-ack) reply: IsAck = true, want false")
	}
	if !sends[1].IsAck {
		t.Errorf("second (ack) reply: IsAck = false, want true")
	}
}
