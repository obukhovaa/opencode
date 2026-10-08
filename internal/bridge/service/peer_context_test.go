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
