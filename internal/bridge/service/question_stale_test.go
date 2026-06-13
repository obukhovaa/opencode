package service

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// newRouterForCacheTest builds a router bound to the test orchestrator's
// projectID — sufficient for the cache surface, no app injection needed.
func newRouterForCacheTest(t *testing.T) *QuestionRouter {
	t.Helper()
	svc, _ := newOrchestratorForTest(t)
	return &QuestionRouter{
		svc:     svc,
		pending: map[string]*pendingQuestion{},
	}
}

func TestRememberAnswersStoresAllLabelsAcrossRows(t *testing.T) {
	t.Parallel()
	r := newRouterForCacheTest(t)

	answers := [][]string{
		{"Yes"},
		{"auth", "billing"},
		{"", "  "}, // empty + whitespace-only entries are skipped
	}
	r.rememberAnswers("S1", answers)

	for _, label := range []string{"Yes", "auth", "billing"} {
		if _, hit := r.wasRecentlyAnswered("S1", label); !hit {
			t.Errorf("label %q expected to be cached", label)
		}
	}
	if _, hit := r.wasRecentlyAnswered("S1", ""); hit {
		t.Errorf("empty label should not be cached")
	}
}

func TestWasRecentlyAnsweredScopesBySession(t *testing.T) {
	t.Parallel()
	r := newRouterForCacheTest(t)
	r.rememberAnswers("S1", [][]string{{"Yes"}})

	if _, hit := r.wasRecentlyAnswered("S1", "Yes"); !hit {
		t.Fatal("hit expected for own session")
	}
	if _, hit := r.wasRecentlyAnswered("S2", "Yes"); hit {
		t.Fatal("hit must not leak across sessions")
	}
}

func TestWasRecentlyAnsweredExpiresAfterTTL(t *testing.T) {
	t.Parallel()
	r := newRouterForCacheTest(t)
	r.recentlyAnswered.Store(answeredKey{
		projectID: r.svc.projectID,
		sessionID: "S1",
		label:     "Yes",
	}, time.Now().Add(-2*recentlyAnsweredTTL))

	if _, hit := r.wasRecentlyAnswered("S1", "Yes"); hit {
		t.Errorf("expired entry must not be reported as hit")
	}
}

func TestSweepDeletesExpiredEntries(t *testing.T) {
	t.Parallel()
	r := newRouterForCacheTest(t)
	now := time.Now()
	r.recentlyAnswered.Store(answeredKey{r.svc.projectID, "S1", "old"}, now.Add(-2*time.Minute))
	r.recentlyAnswered.Store(answeredKey{r.svc.projectID, "S1", "fresh"}, now)

	r.sweepRecentlyAnswered(now, recentlyAnsweredTTL)

	if _, ok := r.recentlyAnswered.Load(answeredKey{r.svc.projectID, "S1", "old"}); ok {
		t.Errorf("expired entry should be deleted by sweeper")
	}
	if _, ok := r.recentlyAnswered.Load(answeredKey{r.svc.projectID, "S1", "fresh"}); !ok {
		t.Errorf("fresh entry should survive sweep")
	}
}

func TestSweeperGoroutineStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	r := newRouterForCacheTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.runSweeper(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sweeper goroutine did not exit after ctx cancel")
	}
}

func TestTryHandleQuestionReply_NoPending_CachedHit_Swallowed(t *testing.T) {
	t.Parallel()
	r := newRouterForCacheTest(t)
	// Pre-warm cache as if a previous Reply consumed "Yes".
	r.rememberAnswers("S1", [][]string{{"Yes"}})

	handled := r.TryHandleQuestionReply(context.Background(), "S1", bridge.Inbound{Text: "Yes"})
	if !handled {
		t.Fatal("cached stale-click should be suppressed (handled=true)")
	}
}

func TestTryHandleQuestionReply_NoPending_NoCache_Passthrough(t *testing.T) {
	t.Parallel()
	r := newRouterForCacheTest(t)
	handled := r.TryHandleQuestionReply(context.Background(), "S1", bridge.Inbound{Text: "Yes"})
	if handled {
		t.Fatal("no pending question + no cache → should not consume the inbound")
	}
}

func TestTryHandleQuestionReply_NoPending_DifferentLabel_Passthrough(t *testing.T) {
	t.Parallel()
	r := newRouterForCacheTest(t)
	r.rememberAnswers("S1", [][]string{{"Yes"}})

	handled := r.TryHandleQuestionReply(context.Background(), "S1", bridge.Inbound{Text: "No"})
	if handled {
		t.Fatal("clicking a DIFFERENT button after answering must still flow through")
	}
}
