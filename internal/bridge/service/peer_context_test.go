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

// A free-text turn runs with the args of the job the process is running, so
// its MCP calls stamp the same flowArgHeaders as the job's steps; a daemon
// with no job runs it with none.
func TestHandleInbound_RunCtxCarriesProcessJobArgs(t *testing.T) {
	ag := &jobArgsCaptureAgent{}
	svc, _ := newDispatchTestSvc(t, ag)
	d := newBareDispatch(svc, "S1")

	t.Cleanup(func() { tools.SetProcessJobArgs(nil) })
	tools.SetProcessJobArgs(map[string]any{"tenant": "acme"})
	d.handleInbound(context.Background(), testInbound("with a job"))
	tools.SetProcessJobArgs(nil)
	d.handleInbound(context.Background(), testInbound("daemon"))

	if len(ag.jobArgs) != 2 {
		t.Fatalf("Run calls = %d, want 2", len(ag.jobArgs))
	}
	if ag.jobArgs[0]["tenant"] != "acme" {
		t.Errorf("turn of a job: job args = %v, want tenant=acme", ag.jobArgs[0])
	}
	if ag.jobArgs[1] != nil {
		t.Errorf("daemon turn: job args = %v, want none", ag.jobArgs[1])
	}
}
