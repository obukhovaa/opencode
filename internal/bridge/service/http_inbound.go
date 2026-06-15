package service

import (
	"net/http"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// handleInbound is the orchestrator-mediated-inbound entry point. The
// c2-agent orchestrator (or any future inbound-mediating peer) POSTs a
// pre-built bridge.Inbound JSON body here; the handler pushes the value
// onto the shared inbound channel using the same path adapters use
// internally — runInboundLoop drains, dispatchInbound resolves the
// binding, and the per-session dispatcher forwards to the agent.
//
// Auth: the API server's authMiddleware (HTTP Basic, OPENCODE_SERVER_PASSWORD)
// wraps every /router/* route. This handler does NOT re-implement that
// check — when authMiddleware is bypassed because the server runs with no
// password (development), the bridge endpoint follows the same posture
// as every other /router/* route.
//
// Backpressure: the shared inboundCh is buffered (default 64). A normal
// adapter-driven inbound that finds the channel full back-pressures the
// adapter's pump goroutine (sendInboundWithBackpressure) — the platform's
// own retry then re-delivers. An HTTP forward has no such symmetry: if
// we blocked the request, the orchestrator's per-request worker would
// stall. So we offer the inbound non-blocking and return 429 on a full
// channel, instructing the orchestrator to retry. The orchestrator's
// forward path (Phase E) handles 5xx + 4xx + 429 uniformly with its
// single-retry policy.
func (s *Service) handleInbound(w http.ResponseWriter, r *http.Request) {
	var in bridge.Inbound
	if err := readJSON(r, &in); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Peer.Channel == "" || in.Peer.Identity == "" || in.Peer.PeerID == "" {
		writeAPIError(w, http.StatusBadRequest, "peer.channel, peer.identity, peer.peerId are required")
		return
	}
	if in.Text == "" && len(in.Attachments) == 0 {
		writeAPIError(w, http.StatusBadRequest, "inbound must carry text or attachments")
		return
	}
	if in.ReceivedAt == 0 {
		in.ReceivedAt = time.Now().UnixMilli()
	}

	select {
	case s.inboundCh <- in:
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
	case <-r.Context().Done():
		// Client cancelled mid-enqueue — don't write a body, the
		// response is moot.
		return
	default:
		writeAPIError(w, http.StatusTooManyRequests, "inbound dispatcher full; retry")
	}
}
