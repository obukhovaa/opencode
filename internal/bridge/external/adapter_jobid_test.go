package external

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// The external adapter stamps the orchestrator's job identity on every
// relay frame, and the orchestrator rejects a frame whose jobId is empty
// with 400. A per-Job pod gets that identity from OPENCODE_BRIDGE_JOB_ID
// at boot; a pool pod serves many jobs from one process and has no
// boot-time job at all, so the identity has to be replaceable per run
// (openspec change agent-pod-pool-runtime, design D9).

// TestSetJobIDStampsSubsequentFrames covers the pool pod's sequence:
// boot with no identity, receive one per run, and have the next run
// replace it.
func TestSetJobIDStampsSubsequentFrames(t *testing.T) {
	t.Setenv("OPENCODE_BRIDGE_JOB_ID", "")
	relay, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)
	ctx := bridge.ContextWithSessionID(context.Background(), "sess-1")

	// Boot state: no job. This is what a pool pod looked like before the
	// fix, for every run it ever served.
	a.Send(ctx, bridge.Outbound{Peer: testPeer(), Text: "before"})
	if got := relay.last()["jobId"]; got != "" {
		t.Errorf("jobId before SetJobID = %v, want empty", got)
	}

	a.SetJobID("job-1")
	a.Send(ctx, bridge.Outbound{Peer: testPeer(), Text: "run 1"})
	if got := relay.last()["jobId"]; got != "job-1" {
		t.Errorf("jobId during run 1 = %v, want job-1", got)
	}

	a.SetJobID("job-2")
	a.Send(ctx, bridge.Outbound{Peer: testPeer(), Text: "run 2"})
	if got := relay.last()["jobId"]; got != "job-2" {
		t.Errorf("jobId during run 2 = %v, want job-2 (run 1's identity must not leak)", got)
	}

	// Terminal clears it, so a late frame is not attributed to a
	// finished run.
	a.SetJobID("")
	a.Send(ctx, bridge.Outbound{Peer: testPeer(), Text: "after"})
	if got := relay.last()["jobId"]; got != "" {
		t.Errorf("jobId after clear = %v, want empty", got)
	}
}

// TestSetJobIDAppliesToInteractiveQuestions: the question frame is the
// one that actually matters — an interactive step whose question frame
// is rejected means the reviewer never sees the prompt at all.
func TestSetJobIDAppliesToInteractiveQuestions(t *testing.T) {
	t.Setenv("OPENCODE_BRIDGE_JOB_ID", "")
	relay, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)

	a.SetJobID("job-42")
	ctx := bridge.ContextWithSessionID(context.Background(), "sess-9")
	ctx = bridge.ContextWithExternalQuestion(ctx, bridge.ExternalQuestionContext{RequestID: "req-1"})
	_, err := a.SendInteractiveQuestion(ctx, testPeer(), "ship it?", []bridge.QuestionChoice{
		{Label: "Yes", Value: "Yes"},
	})
	if err != nil {
		t.Fatalf("SendInteractiveQuestion: %v", err)
	}
	if got := relay.last()["jobId"]; got != "job-42" {
		t.Errorf("question frame jobId = %v, want job-42", got)
	}
}

// TestBootEnvSeedsJobID pins the per-Job pod's unchanged path: with the
// env var set and nobody calling SetJobID, frames carry the boot value.
func TestBootEnvSeedsJobID(t *testing.T) {
	t.Setenv("OPENCODE_BRIDGE_JOB_ID", "boot-job")
	relay, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)

	a.Send(bridge.ContextWithSessionID(context.Background(), "s"), bridge.Outbound{Peer: testPeer(), Text: "x"})
	if got := relay.last()["jobId"]; got != "boot-job" {
		t.Errorf("jobId = %v, want the boot env value", got)
	}
}

// TestAdapterSatisfiesJobScoped is a compile-time-ish guard: the bridge
// service discovers this capability by type assertion, so a rename would
// silently stop propagating the identity.
func TestAdapterSatisfiesJobScoped(t *testing.T) {
	var _ bridge.JobScopedAdapter = (*Adapter)(nil)
}
