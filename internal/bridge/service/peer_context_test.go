package service

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

// peerCaptureAgent records the peer and the text each Run receives.
type peerCaptureAgent struct {
	agentpkg.Service // nil — only Run is called
	peers            []tools.Peer
	texts            []string
}

func (a *peerCaptureAgent) Run(ctx context.Context, _, text string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	p, _ := tools.PeerFromContext(ctx)
	a.peers = append(a.peers, p)
	a.texts = append(a.texts, text)
	ch := make(chan agentpkg.AgentEvent, 1)
	ch <- agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeResponse}
	close(ch)
	return ch, nil
}

// The turn runs with the peer the bridge resolved its session from, and the
// text the agent gets is exactly what came in — attribution never rides on
// the message itself.
func TestHandleInbound_RunCtxCarriesInboundPeer(t *testing.T) {
	ag := &peerCaptureAgent{}
	svc, _ := newDispatchTestSvc(t, ag)
	d := newBareDispatch(svc, "S1")

	in := testInbound("how is my cancel flow doing?")
	in.Peer = bridge.PeerRef{Channel: "external", Identity: "default", PeerID: "app1:d1:c1"}
	d.handleInbound(context.Background(), in)

	if len(ag.peers) != 1 {
		t.Fatalf("Run calls = %d, want 1", len(ag.peers))
	}
	want := tools.Peer{Channel: "external", Identity: "default", PeerID: "app1:d1:c1"}
	if ag.peers[0] != want {
		t.Errorf("peer = %+v, want %+v", ag.peers[0], want)
	}
	if ag.texts[0] != "how is my cancel flow doing?" {
		t.Errorf("text = %q, want the inbound text unchanged", ag.texts[0])
	}
}

type jobArgsCaptureAgent struct {
	agentpkg.Service // nil — only Run is called
	jobArgs          []map[string]any
}

func (a *jobArgsCaptureAgent) Run(ctx context.Context, _, _ string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	args, _ := tools.JobArgsFromContext(ctx)
	a.jobArgs = append(a.jobArgs, args)
	ch := make(chan agentpkg.AgentEvent, 1)
	ch <- agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeResponse}
	close(ch)
	return ch, nil
}

func runTurnJobArgs(t *testing.T, svc *Service, ag *jobArgsCaptureAgent, in bridge.Inbound) map[string]any {
	t.Helper()
	before := len(ag.jobArgs)
	newBareDispatch(svc, "S1").handleInbound(context.Background(), in)
	if len(ag.jobArgs) != before+1 {
		t.Fatalf("Run calls = %d, want %d", len(ag.jobArgs), before+1)
	}
	return ag.jobArgs[before]
}

// A free-text turn stamps the job args only when it is provably a turn of the
// job whose args the process holds: its session or peer was bound by that
// job. A daemon turn, a turn of another or unknown job, and a late turn of a
// finished job carry none.
func TestHandleInbound_JobArgsOnlyForTurnsOfTheCurrentJob(t *testing.T) {
	t.Cleanup(func() { tools.SetProcessJobArgs("", nil) })
	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}

	t.Run("daemon mode attaches none", func(t *testing.T) {
		ag := &jobArgsCaptureAgent{}
		svc, _ := newDispatchTestSvc(t, ag)
		svc.heartbeat = &heartbeatState{}
		svc.SetRemoteJobID("job-1")
		svc.markJobTurns("job-1", "S1", peer)
		tools.SetProcessJobArgs("job-1", map[string]any{"tenant": "acme"})
		if got := runTurnJobArgs(t, svc, ag, testInbound("hi")); got != nil {
			t.Errorf("daemon turn: job args = %v, want none", got)
		}
	})

	t.Run("turn with no job attaches none", func(t *testing.T) {
		ag := &jobArgsCaptureAgent{}
		svc, _ := newDispatchTestSvc(t, ag)
		svc.SetRemoteJobID("job-1")
		tools.SetProcessJobArgs("job-1", map[string]any{"tenant": "acme"})
		if got := runTurnJobArgs(t, svc, ag, testInbound("who are you")); got != nil {
			t.Errorf("unrelated turn: job args = %v, want none", got)
		}
	})

	t.Run("turn of a different job attaches none", func(t *testing.T) {
		ag := &jobArgsCaptureAgent{}
		svc, _ := newDispatchTestSvc(t, ag)
		svc.SetRemoteJobID("job-1")
		svc.markJobTurns("job-1", "S1", peer)
		// The pod moved on: job-2 holds the args now. job-1's records are
		// pruned when the identity changes.
		svc.SetRemoteJobID("job-2")
		tools.SetProcessJobArgs("job-2", map[string]any{"tenant": "other"})
		if got := runTurnJobArgs(t, svc, ag, testInbound("late")); got != nil {
			t.Errorf("late turn of job-1: job args = %v, want none", got)
		}
		// Even if the stale record survived, the ids differ.
		svc.markJobTurns("job-1", "S1", peer)
		if got := runTurnJobArgs(t, svc, ag, testInbound("late again")); got != nil {
			t.Errorf("turn bound by job-1 while job-2 holds the args: job args = %v, want none", got)
		}
	})

	t.Run("holder published without a job identity attaches none", func(t *testing.T) {
		ag := &jobArgsCaptureAgent{}
		svc, _ := newDispatchTestSvc(t, ag)
		svc.SetRemoteJobID("job-1")
		svc.markJobTurns("job-1", "S1", peer)
		tools.SetProcessJobArgs("", map[string]any{"tenant": "acme"})
		if got := runTurnJobArgs(t, svc, ag, testInbound("hi")); got != nil {
			t.Errorf("job args = %v, want none", got)
		}
	})

	t.Run("session of the current job attaches the args", func(t *testing.T) {
		ag := &jobArgsCaptureAgent{}
		svc, _ := newDispatchTestSvc(t, ag)
		svc.SetRemoteJobID("job-1")
		svc.markJobTurns("job-1", "S1")
		tools.SetProcessJobArgs("job-1", map[string]any{"tenant": "acme"})
		in := testInbound("hi")
		in.Peer.PeerID = "someone-else"
		if got := runTurnJobArgs(t, svc, ag, in); got["tenant"] != "acme" {
			t.Errorf("job args = %v, want tenant=acme", got)
		}
	})

	t.Run("reviewer peer of the current job attaches the args", func(t *testing.T) {
		ag := &jobArgsCaptureAgent{}
		svc, _ := newDispatchTestSvc(t, ag)
		svc.SetRemoteJobID("job-1")
		svc.markJobTurns("job-1", "S-step", peer)
		tools.SetProcessJobArgs("job-1", map[string]any{"tenant": "acme"})
		if got := runTurnJobArgs(t, svc, ag, testInbound("hi")); got["tenant"] != "acme" {
			t.Errorf("job args = %v, want tenant=acme", got)
		}
	})

	t.Run("records are dropped when the job identity is cleared", func(t *testing.T) {
		ag := &jobArgsCaptureAgent{}
		svc, _ := newDispatchTestSvc(t, ag)
		svc.SetRemoteJobID("job-1")
		svc.markJobTurns("job-1", "S1", peer)
		svc.SetRemoteJobID("")
		tools.SetProcessJobArgs("job-1", map[string]any{"tenant": "acme"})
		if got := runTurnJobArgs(t, svc, ag, testInbound("hi")); got != nil {
			t.Errorf("job args = %v, want none", got)
		}
	})
}
