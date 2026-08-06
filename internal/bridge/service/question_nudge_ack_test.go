package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/question"
)

// --- custom-answer acknowledgment (GENAI-151) ---------------------------

// newAckRouter wires a router + a registered slack/default adapter so
// maybeAckAnswer's replyToPeer has somewhere to deliver.
func newAckRouter(t *testing.T) (*QuestionRouter, *stubAdapter) {
	t.Helper()
	svc, _ := newOrchestratorForTest(t)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ad := newStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	return &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{}}, ad
}

func TestMaybeAckAnswer_TypedAnswersAreAcknowledged(t *testing.T) {
	t.Parallel()
	for _, src := range []string{bridge.InboundSourceMessage, bridge.InboundSourceAppMention, bridge.InboundSourceModal} {
		r, ad := newAckRouter(t)
		in := bridge.Inbound{
			Peer:   bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "C1|170.5"},
			Text:   "the meta service README",
			Source: src,
		}
		r.maybeAckAnswer(context.Background(), in, [][]string{{"the meta service README"}})
		sends := ad.Sends()
		if len(sends) != 1 {
			t.Fatalf("source=%s: sends=%d, want 1 (typed answer must be acknowledged)", src, len(sends))
		}
		if !strings.Contains(sends[0].Text, "Got it") || !strings.Contains(sends[0].Text, "meta service README") {
			t.Errorf("source=%s: ack text = %q", src, sends[0].Text)
		}
		if sends[0].Peer.PeerID != "C1|170.5" {
			t.Errorf("source=%s: ack went to peer %q, want the answering peer C1|170.5", src, sends[0].Peer.PeerID)
		}
	}
}

func TestMaybeAckAnswer_ButtonAndUnknownAreNotAcknowledged(t *testing.T) {
	t.Parallel()
	for _, src := range []string{bridge.InboundSourceButton, "" /* older orchestrator */} {
		r, ad := newAckRouter(t)
		in := bridge.Inbound{
			Peer:   bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "C1|170.5"},
			Text:   "Yes, CD",
			Source: src,
		}
		r.maybeAckAnswer(context.Background(), in, [][]string{{"Yes, CD"}})
		if n := len(ad.Sends()); n != 0 {
			t.Errorf("source=%q: sends=%d, want 0 (button self-renders ✓ Answered; unknown suppressed to avoid double-ack)", src, n)
		}
	}
}

func TestFormatAnswerAck(t *testing.T) {
	t.Parallel()
	if got := formatAnswerAck(nil); got != "" {
		t.Errorf("empty answers → %q, want \"\"", got)
	}
	if got := formatAnswerAck([][]string{{"  ", ""}}); got != "" {
		t.Errorf("whitespace-only answers → %q, want \"\"", got)
	}
	got := formatAnswerAck([][]string{{"auth"}, {"billing"}})
	if !strings.Contains(got, "auth") || !strings.Contains(got, "billing") {
		t.Errorf("ack should list all recorded labels: %q", got)
	}
}

// --- idle "still waiting" nudge (GENAI-151) -----------------------------

// newNudgeRouter wires a router + adapter + a single bound peer for session
// S1, then plants a pending question asked `askedAgo` in the past.
func newNudgeRouter(t *testing.T, askedAgo time.Duration) (*QuestionRouter, *stubAdapter) {
	t.Helper()
	svc, _ := newOrchestratorForTest(t)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ad := newStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "C1|170.5"},
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	r := &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{}}
	r.pending["S1"] = &pendingQuestion{
		requestID: "q1",
		prompts:   []question.Prompt{{Question: "Where should the endpoint list live?"}},
		askedAt:   time.Now().Add(-askedAgo),
	}
	return r, ad
}

func TestNudgeDue_FiresAfterIntervalThenRespectsSpacing(t *testing.T) {
	t.Parallel()
	r, ad := newNudgeRouter(t, 10*time.Minute) // idle well past the 5m default
	now := time.Now()

	r.nudgeDue(context.Background(), now)
	if n := len(ad.Sends()); n != 1 {
		t.Fatalf("first nudge: sends=%d, want 1", n)
	}
	if !strings.Contains(ad.Sends()[0].Text, "Still waiting") ||
		!strings.Contains(ad.Sends()[0].Text, "endpoint list") {
		t.Errorf("nudge text = %q", ad.Sends()[0].Text)
	}

	// Immediately again: spacing not elapsed → no second nudge.
	r.nudgeDue(context.Background(), now)
	if n := len(ad.Sends()); n != 1 {
		t.Errorf("re-nudge within interval: sends=%d, want 1 (no double-ping)", n)
	}

	// After another interval: second nudge fires.
	r.nudgeDue(context.Background(), now.Add(6*time.Minute))
	if n := len(ad.Sends()); n != 2 {
		t.Errorf("nudge after interval elapsed: sends=%d, want 2", n)
	}
}

func TestNudgeDue_NotYetIdle(t *testing.T) {
	t.Parallel()
	r, ad := newNudgeRouter(t, 1*time.Minute) // only 1m idle, default is 5m
	r.nudgeDue(context.Background(), time.Now())
	if n := len(ad.Sends()); n != 0 {
		t.Errorf("sends=%d, want 0 (not idle long enough)", n)
	}
}

func TestNudgeDue_RespectsMaxCap(t *testing.T) {
	t.Parallel()
	r, ad := newNudgeRouter(t, 10*time.Minute)
	r.svc.cfg.QuestionNudgeMax = 1
	now := time.Now()
	r.nudgeDue(context.Background(), now)                       // #1
	r.nudgeDue(context.Background(), now.Add(1*time.Hour))      // capped
	r.nudgeDue(context.Background(), now.Add(2*time.Hour))      // capped
	if n := len(ad.Sends()); n != 1 {
		t.Errorf("sends=%d, want 1 (QuestionNudgeMax=1)", n)
	}
}

func TestNudgeDue_DisabledByNegativeInterval(t *testing.T) {
	t.Parallel()
	r, ad := newNudgeRouter(t, 1*time.Hour)
	r.svc.cfg.QuestionNudgeIntervalSeconds = -1 // disabled
	r.nudgeDue(context.Background(), time.Now())
	if n := len(ad.Sends()); n != 0 {
		t.Errorf("sends=%d, want 0 (nudging disabled)", n)
	}
}

func TestNudgeDue_HonoursCustomInterval(t *testing.T) {
	t.Parallel()
	r, ad := newNudgeRouter(t, 90*time.Second)
	r.svc.cfg.QuestionNudgeIntervalSeconds = 60 // 1m, below the 90s idle
	r.nudgeDue(context.Background(), time.Now())
	if n := len(ad.Sends()); n != 1 {
		t.Errorf("sends=%d, want 1 (custom 60s interval, 90s idle)", n)
	}
}

func TestRunNudgerStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	r, _ := newNudgeRouter(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.runNudger(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("nudger goroutine did not exit after ctx cancel")
	}
}

func TestFormatNudge(t *testing.T) {
	t.Parallel()
	if got := formatNudge(nil); got != "" {
		t.Errorf("no prompts → %q, want \"\"", got)
	}
	if got := formatNudge([]question.Prompt{{Question: "   "}}); got != "" {
		t.Errorf("blank question → %q, want \"\"", got)
	}
	got := formatNudge([]question.Prompt{{Question: "Pick a project"}})
	if !strings.Contains(got, "Still waiting") || !strings.Contains(got, "Pick a project") {
		t.Errorf("nudge = %q", got)
	}
}
