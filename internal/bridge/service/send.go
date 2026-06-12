package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/logging"
)

// sendWorkerCap caps the parallel-delivery worker pool used by
// SendBySessionID. The chat-bridge spec demands "bounded parallel worker
// pool (cap 4)" — one slow peer must not delay another's delivery, but
// runaway parallelism would amplify our outbound burst rate on the
// platform side. 4 strikes the balance.
const sendWorkerCap = 4

// SendBySessionID fans `out` (text + optional attachments) across every
// peer bound to sessionID. Returns the per-peer outcomes — callers can
// log/surface failures but the agent itself is NOT informed of per-peer
// errors (per the chat-bridge spec: "the agent is not told about per-peer
// failures — partial fan-out failure is a delivery concern, not a
// conversation concern").
//
// Failures on one peer never block delivery to others; per-peer errors
// are also accumulated into the corresponding adapter's status (visible
// in /router/health as lastError/lastFailureAt).
func (s *Service) SendBySessionID(ctx context.Context, sessionID string, out bridge.Outbound) ([]PerPeerSendResult, error) {
	if sessionID == "" {
		return nil, errors.New("bridge: SendBySessionID requires sessionID")
	}
	bindings, err := s.store.ListBindingsBySession(ctx, s.projectID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("bridge: ListBindingsBySession: %w", err)
	}
	if len(bindings) == 0 {
		return nil, fmt.Errorf("bridge: no bindings for session %s", sessionID)
	}

	// Strip any reviewer-attribution envelopes from the outbound text
	// before delivery. The prompt-builder is supposed to keep these out,
	// but agents do quote prior context — strip defensively at the
	// fan-out boundary so users never see "[<reviewer> via slack]: …"
	// appear in an agent message.
	out.Text = StripAttribution(out.Text)

	results := make([]PerPeerSendResult, len(bindings))
	worker := make(chan struct{}, sendWorkerCap)
	var wg sync.WaitGroup
	for i := range bindings {
		i, b := i, bindings[i]
		wg.Add(1)
		worker <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-worker }()
			defer func() {
				if r := recover(); r != nil {
					logging.Error("bridge: per-peer send panic",
						"session", sessionID, "peer", b.PeerID, "panic", r)
					results[i] = PerPeerSendResult{
						Binding:     b,
						Err:         fmt.Errorf("panic during send"),
						CompletedAt: time.Now().UnixMilli(),
					}
				}
			}()
			results[i] = s.sendToOnePeer(ctx, b, out)
		}()
	}
	wg.Wait()
	return results, nil
}

// PerPeerSendResult is one row of the SendBySessionID fan-out outcome.
type PerPeerSendResult struct {
	Binding   store.Binding
	Delivered bool
	Err       error
	// ResolvedPeer carries any platform-side resolution (e.g. Slack
	// channel→thread mutation, where the adapter returns the ts so the
	// orchestrator can mutate the binding's peer_id form).
	ResolvedPeer string
	CompletedAt  int64
}

// sendToOnePeer dispatches one outbound delivery via the configured
// adapter, then performs any binding mutations required by router-initiated
// semantics (channel→thread peer_id rewrite, mention_consumed_at stamp).
//
// Mutation transaction: per the chat-bridge-router-initiated spec, the
// binding's peer_id and mention_consumed_at MUST be committed in the same
// transaction as the platform call returns thread metadata. We use a
// best-effort sequence: post first, then update; if the update fails the
// binding is left at the pre-mutation form and the next outbound creates
// a duplicate thread (acceptable degradation per the spec). Future work
// could wrap UpdateBindingPeerID + MarkMentionConsumed in a single SQL
// transaction; the current per-row UPDATEs are atomic at the row level.
func (s *Service) sendToOnePeer(ctx context.Context, b store.Binding, out bridge.Outbound) PerPeerSendResult {
	res := PerPeerSendResult{Binding: b}
	defer func() { res.CompletedAt = time.Now().UnixMilli() }()

	adapter := s.Adapter(b.Channel, b.IdentityID)
	if adapter == nil {
		res.Err = fmt.Errorf("bridge: no adapter for %s:%s", b.Channel, b.IdentityID)
		return res
	}

	// Decide whether the mention prefix is needed for THIS outbound: only
	// when the binding has a mention_handle and it hasn't been consumed
	// by a prior outbound. The adapter receives the mention via
	// Outbound.Mention and prepends it to the platform text.
	peerOut := out
	peerOut.Peer = bridge.PeerRef{
		Channel:  b.Channel,
		Identity: b.IdentityID,
		PeerID:   b.PeerID,
	}
	if b.MentionHandle != "" && b.MentionConsumedAt == 0 {
		peerOut.Mention = b.MentionHandle
	}

	// Rich-render path: if the caller supplied Outbound.Render AND the
	// adapter satisfies bridge.RichRenderer, route through Render so the
	// adapter produces platform-native UI. On ErrRenderUnsupported (kind
	// not yet implemented by this adapter), fall through to the text path
	// transparently. Other errors are treated as delivery failures.
	var platformResult bridge.SendResult
	if peerOut.Render != nil {
		if renderer, ok := adapter.(bridge.RichRenderer); ok {
			platformResult = renderer.Render(ctx, peerOut.Peer, peerOut.Render)
			if errors.Is(platformResult.Err, bridge.ErrRenderUnsupported) {
				// Adapter didn't implement this kind — fall through to text.
				platformResult = adapter.Send(ctx, peerOut)
			}
		} else {
			platformResult = adapter.Send(ctx, peerOut)
		}
	} else {
		platformResult = adapter.Send(ctx, peerOut)
	}
	res.Delivered = platformResult.Delivered
	res.Err = platformResult.Err
	res.ResolvedPeer = platformResult.ResolvedPeer

	if !platformResult.Delivered {
		return res
	}

	// Binding mutation: Slack channel→thread / Mattermost channel→rootPost.
	// adapter.Send returns ResolvedPeer = "<channel>|<thread>" on the
	// first outbound to a channel-only peer. We rewrite the binding's
	// peer_id so the NEXT outbound posts in-thread.
	if platformResult.ResolvedPeer != "" && platformResult.ResolvedPeer != b.PeerID {
		if err := s.store.UpdateBindingPeerID(
			ctx, s.projectID, b.Channel, b.IdentityID,
			b.PeerID, platformResult.ResolvedPeer,
		); err != nil {
			logging.Warn("bridge: binding peer_id mutation failed",
				"session", b.SessionID, "old", b.PeerID, "new", platformResult.ResolvedPeer, "err", err)
		}
	}

	// Mention consumed: stamp mention_consumed_at so subsequent outbounds
	// skip the prefix.
	if peerOut.Mention != "" {
		// Use the post-mutation peer_id key if mutation happened.
		key := b.PeerID
		if platformResult.ResolvedPeer != "" {
			key = platformResult.ResolvedPeer
		}
		if err := s.store.MarkMentionConsumed(
			ctx, s.projectID, b.Channel, b.IdentityID, key,
		); err != nil {
			logging.Warn("bridge: MarkMentionConsumed failed",
				"session", b.SessionID, "peer", key, "err", err)
		}
	}

	return res
}

// BoundPeersSnapshot returns the peers currently bound to any session
// in this project. Used by the router_send tool's dynamic description
// so the agent can address bound peers without learning new IDs. Best-
// effort — store errors are swallowed and the snapshot returns an
// empty slice.
//
// The result is a global snapshot across all sessions; the tool's
// description doesn't differentiate per-session because the agent
// invoking the tool already knows its own session.
func (s *Service) BoundPeersSnapshot(ctx context.Context) []bridge.PeerRef {
	// Snapshot the (channel, identity) pairs under the cfg read lock,
	// then release it before performing store I/O. Holding the cfg
	// mutex across the store call would block concurrent identity
	// upserts for the duration of the DB roundtrip.
	if s.cfg == nil {
		return nil
	}
	type chid struct{ ch, id string }
	var pairs []chid
	s.cfgMu.RLock()
	if t := s.cfg.Channels.Telegram; t != nil {
		for _, b := range t.Bots {
			pairs = append(pairs, chid{"telegram", b.ID})
		}
	}
	if sl := s.cfg.Channels.Slack; sl != nil {
		for _, a := range sl.Apps {
			pairs = append(pairs, chid{"slack", a.ID})
		}
	}
	if m := s.cfg.Channels.Mattermost; m != nil {
		for _, mm := range m.Instances {
			pairs = append(pairs, chid{"mattermost", mm.ID})
		}
	}
	s.cfgMu.RUnlock()

	var out []bridge.PeerRef
	for _, p := range pairs {
		bindings, err := s.store.ListBindingsByIdentity(ctx, s.projectID, p.ch, p.id)
		if err != nil {
			continue
		}
		for _, b := range bindings {
			out = append(out, b.AsPeerRef())
		}
	}
	return out
}

// Send is the in-process entry point for the router_send agent tool and
// the POST /router/send HTTP handler. Unlike SendBySessionID it targets a
// single peer directly; no fan-out, no session lookup.
//
// peer can be in user-id form (Slack U<id>, Mattermost 26-char user id);
// the adapter resolves it to a DM channel via ResolveUserToDM before
// sending. Returns the platform-level SendResult; callers translate it
// into the tool/HTTP response shape.
func (s *Service) Send(ctx context.Context, peer bridge.PeerRef, text string, mention string, attachments []bridge.Attachment) (bridge.SendResult, error) {
	return s.SendWithRender(ctx, peer, text, mention, attachments, nil)
}

// SendWithRender is Send with an optional structured render hint. When
// hint != nil AND the adapter satisfies bridge.RichRenderer, the bridge
// routes through Render so the adapter produces platform-native UI; on
// ErrRenderUnsupported or non-RichRenderer adapter, falls back to the
// text path verbatim. Callers that don't want rich rendering use Send
// (which passes nil).
func (s *Service) SendWithRender(ctx context.Context, peer bridge.PeerRef, text string, mention string, attachments []bridge.Attachment, hint *bridge.RenderHint) (bridge.SendResult, error) {
	adapter := s.Adapter(peer.Channel, peer.Identity)
	if adapter == nil {
		return bridge.SendResult{}, fmt.Errorf("bridge: no adapter for %s:%s", peer.Channel, peer.Identity)
	}
	originalPeerID := peer.PeerID
	resolved, err := adapter.ResolveUserToDM(ctx, peer.PeerID)
	if err != nil {
		return bridge.SendResult{}, fmt.Errorf("bridge: resolve user-id: %w", err)
	}
	peer.PeerID = resolved
	out := bridge.Outbound{
		Peer:        peer,
		Text:        StripAttribution(text),
		Attachments: attachments,
		Mention:     mention,
		Render:      hint,
	}
	var result bridge.SendResult
	if hint != nil {
		if renderer, ok := adapter.(bridge.RichRenderer); ok {
			result = renderer.Render(ctx, peer, hint)
			if errors.Is(result.Err, bridge.ErrRenderUnsupported) {
				result = adapter.Send(ctx, out)
			}
		} else {
			result = adapter.Send(ctx, out)
		}
	} else {
		result = adapter.Send(ctx, out)
	}
	// Channel→thread mutation: if the adapter performed platform-side
	// resolution (Slack C<id> → C<id>|<ts>, Mattermost channel → root
	// post) and the resulting form differs from what we just sent
	// against, persist the mutation so subsequent sends on the same
	// binding land in the thread/post that was just created. This
	// mirrors the multi-peer fan-out path in sendToOnePeer — the HTTP
	// /router/send + router_send tool flow must NOT silently lose the
	// mutation just because they bypass SendBySessionID.
	if result.Delivered && result.ResolvedPeer != "" && result.ResolvedPeer != peer.PeerID {
		// We don't know the binding's session_id at this layer — the
		// store update is keyed by (channel, identity, peer_id). Use
		// the originalPeerID (the value the caller asked for) as the
		// match key: that's the value persisted in bridge_sessions
		// for a not-yet-mutated binding.
		if err := s.store.UpdateBindingPeerID(
			ctx, s.projectID, peer.Channel, peer.Identity,
			originalPeerID, result.ResolvedPeer,
		); err != nil {
			logging.Warn("bridge: channel→thread mutation persistence failed (send succeeded)",
				"channel", peer.Channel, "identity", peer.Identity,
				"old_peer", originalPeerID, "new_peer", result.ResolvedPeer, "err", err)
		}
	}
	return result, nil
}
