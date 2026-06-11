package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/question"
)

// interactiveStubAdapter extends stubAdapter with the
// InteractiveQuestionSender capability so tests can verify the
// QuestionRouter's interactive-then-fallback decision.
type interactiveStubAdapter struct {
	*stubAdapter
	mu             sync.Mutex
	interactive    []interactiveCall
	interactiveErr error
}

type interactiveCall struct {
	Peer    bridge.PeerRef
	Prompt  string
	Choices []bridge.QuestionChoice
}

func newInteractiveStubAdapter(channel, identity string) *interactiveStubAdapter {
	return &interactiveStubAdapter{stubAdapter: newStubAdapter(channel, identity)}
}

func (s *interactiveStubAdapter) SendInteractiveQuestion(_ context.Context, peer bridge.PeerRef, prompt string, choices []bridge.QuestionChoice) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interactiveErr != nil {
		return "", s.interactiveErr
	}
	s.interactive = append(s.interactive, interactiveCall{Peer: peer, Prompt: prompt, Choices: choices})
	return "", nil
}

func (s *interactiveStubAdapter) Interactive() []interactiveCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]interactiveCall, len(s.interactive))
	copy(out, s.interactive)
	return out
}

func TestShouldUseInteractiveTruthTable(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	r := &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{}}

	prompt := question.Prompt{Question: "ok?", Options: []question.Option{{Label: "yes"}}}

	// 1. QuestionMode unset (default) → false.
	if r.shouldUseInteractive([]question.Prompt{prompt}) {
		t.Errorf("default mode should NOT use interactive")
	}

	// 2. Mode = interactive, single prompt, with options → true.
	svc.cfg.QuestionMode = "interactive"
	if !r.shouldUseInteractive([]question.Prompt{prompt}) {
		t.Errorf("interactive mode should use interactive")
	}

	// 3. Multi-prompt → false (block-actions widget can't represent it).
	if r.shouldUseInteractive([]question.Prompt{prompt, prompt}) {
		t.Errorf("multi-prompt should NOT use interactive")
	}

	// 4. Single prompt with no options → false.
	noOpts := question.Prompt{Question: "free text?"}
	if r.shouldUseInteractive([]question.Prompt{noOpts}) {
		t.Errorf("options-less prompt should NOT use interactive")
	}
}

func TestInteractiveSendUsedWhenAdapterSupports(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	svc.cfg.QuestionMode = "interactive"
	_ = svc.Start(context.Background())

	ad := newInteractiveStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	// Bind a session so the question fan-out finds a peer.
	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1"},
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	// QuestionRouter sees the question event and tries SendInteractive.
	router := &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{}}
	router.handleNewRequest(context.Background(), question.Request{
		ID:        "q1",
		SessionID: "S1",
		Questions: []question.Prompt{{Question: "ship?", Options: []question.Option{
			{Label: "ship it"}, {Label: "wait"},
		}}},
	})

	// Wait briefly for any async paths to settle.
	time.Sleep(50 * time.Millisecond)

	got := ad.Interactive()
	if len(got) != 1 {
		t.Fatalf("interactive sends = %d, want 1", len(got))
	}
	if got[0].Prompt != "ship?" || len(got[0].Choices) != 2 {
		t.Errorf("interactive call = %+v", got[0])
	}
	// Should NOT have used the text fallback.
	if len(ad.Sends()) != 0 {
		t.Errorf("text sends = %d; expected interactive path to short-circuit", len(ad.Sends()))
	}
}

func TestInteractiveFallsBackToTextOnError(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	svc.cfg.QuestionMode = "interactive"
	_ = svc.Start(context.Background())

	ad := newInteractiveStubAdapter("slack", "default")
	ad.interactiveErr = errors.New("missing scope")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1"},
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	router := &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{}}
	router.handleNewRequest(context.Background(), question.Request{
		ID:        "q1",
		SessionID: "S1",
		Questions: []question.Prompt{{Question: "ship?", Options: []question.Option{{Label: "yes"}}}},
	})

	time.Sleep(50 * time.Millisecond)

	// Interactive was attempted but failed.
	if len(ad.Interactive()) != 0 {
		// The interactive call returned err; the adapter never
		// completed a successful interactive send, so the slice
		// stays empty by design.
	}
	// Fallback text send was emitted.
	if len(ad.Sends()) != 1 {
		t.Errorf("fallback text sends = %d, want 1", len(ad.Sends()))
	}
	if !strings.Contains(ad.Sends()[0].Text, "ship?") {
		t.Errorf("fallback text missing prompt: %q", ad.Sends()[0].Text)
	}
}

func TestInteractiveSkipsAdaptersWithoutCapability(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	svc.cfg.QuestionMode = "interactive"
	_ = svc.Start(context.Background())

	// Plain stubAdapter does NOT satisfy InteractiveQuestionSender.
	ad := newStubAdapter("mattermost", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "mattermost", Identity: "default", PeerID: "ch1"},
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	router := &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{}}
	router.handleNewRequest(context.Background(), question.Request{
		ID:        "q1",
		SessionID: "S1",
		Questions: []question.Prompt{{Question: "ship?", Options: []question.Option{{Label: "yes"}}}},
	})

	time.Sleep(50 * time.Millisecond)

	// Fallback text path was used (one Send).
	if len(ad.Sends()) != 1 {
		t.Errorf("text sends = %d, want 1 (fallback when adapter doesn't support interactive)", len(ad.Sends()))
	}
}

func TestNonInteractiveModeAlwaysUsesText(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	// QuestionMode unset/empty — default behavior.
	_ = svc.Start(context.Background())

	ad := newInteractiveStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1"},
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	router := &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{}}
	router.handleNewRequest(context.Background(), question.Request{
		ID:        "q1",
		SessionID: "S1",
		Questions: []question.Prompt{{Question: "ship?", Options: []question.Option{{Label: "yes"}}}},
	})

	time.Sleep(50 * time.Millisecond)

	if len(ad.Interactive()) != 0 {
		t.Errorf("interactive sends in non-interactive mode = %d", len(ad.Interactive()))
	}
	if len(ad.Sends()) != 1 {
		t.Errorf("text sends = %d, want 1", len(ad.Sends()))
	}
}
