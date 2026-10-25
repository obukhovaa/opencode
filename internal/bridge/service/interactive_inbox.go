package service

import (
	"context"
	"strings"
	"sync"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/logging"
)

// unansweredQuestionNote is the question tool result the agent reads when
// queued input closes its question (cd-5480 D3). The queued messages
// follow it as user messages.
const unansweredQuestionNote = "The user has not answered this question yet; they sent the messages that follow. " +
	"Ask again if you still need the answer."

// deliveredIDsPerPeerCap bounds the delivered-id memory of one peer. The
// oldest ids are forgotten first; a re-send of one that old is unlikely.
const deliveredIDsPerPeerCap = 4096

// deliveredIDs is the in-memory, per-run record of message ids that
// reached the agent (cd-5480 D8). It is keyed by bound peer; each entry
// remembers the job it was recorded for and is dropped when the pod's job
// identity changes. A pod restart loses it, which a durable store would
// fix and is deferred until a duplicate shows up. The zero value is ready.
type deliveredIDs struct {
	mu    sync.Mutex
	peers map[string]*peerDelivered
}

type peerDelivered struct {
	jobID string
	ids   map[string]struct{}
	order []string
}

func (d *deliveredIDs) has(peer bridge.PeerRef, id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.peers[jobPeerKey(peer)]
	if p == nil {
		return false
	}
	_, ok := p.ids[id]
	return ok
}

func (d *deliveredIDs) add(jobID string, peer bridge.PeerRef, ids ...string) {
	if len(ids) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.peers == nil {
		d.peers = make(map[string]*peerDelivered)
	}
	key := jobPeerKey(peer)
	p := d.peers[key]
	if p == nil || p.jobID != jobID {
		p = &peerDelivered{jobID: jobID, ids: make(map[string]struct{})}
		d.peers[key] = p
	}
	for _, id := range ids {
		if _, ok := p.ids[id]; ok {
			continue
		}
		p.ids[id] = struct{}{}
		p.order = append(p.order, id)
	}
	for len(p.order) > deliveredIDsPerPeerCap {
		delete(p.ids, p.order[0])
		p.order = p.order[1:]
	}
}

// prune forgets every peer recorded for a job other than jobID.
func (d *deliveredIDs) prune(jobID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, p := range d.peers {
		if p.jobID != jobID {
			delete(d.peers, k)
		}
	}
}

// inboxItem wraps one reviewer input for the session's inbox. Text is what
// the agent reads once drained (with the attribution envelope when the
// session has several peers); Meta keeps the raw inbound, whose Text is
// what a question answer is parsed from.
func inboxItem(id string, in bridge.Inbound, peerCount int) inbox.Item {
	return inbox.Item{
		ID:   id,
		Text: PrependAttributionIfMultiPeer(in.Peer, in.Text, peerCount),
		Meta: in,
	}
}

// itemInbound returns the raw inbound an inbox item was built from.
func itemInbound(it inbox.Item) bridge.Inbound {
	in, _ := it.Meta.(bridge.Inbound)
	return in
}

// sessionPeerCount is peerCountForSession with the lookup error logged and
// read as a single peer, so a store hiccup costs only the envelope.
func (s *Service) sessionPeerCount(ctx context.Context, sessionID string) int {
	if s == nil || s.store == nil {
		return 1
	}
	n, err := s.peerCountForSession(ctx, sessionID)
	if err != nil {
		logging.Warn("bridge: peerCount lookup", "session", sessionID, "err", err)
		return 1
	}
	return n
}

// dispatchBatch routes an inbound that carries a messages batch. On an
// interactive session the batch goes to the session's inbox through the
// question router. Otherwise the fresh messages are joined into one prompt
// for the session's dispatcher, as a single text inbound would be.
func (s *Service) dispatchBatch(ctx context.Context, sessionID string, in bridge.Inbound) {
	if s.questionRouter != nil && s.app != nil && s.app.Permissions != nil &&
		s.app.Permissions.IsInteractiveSession(sessionID) {
		s.questionRouter.EnqueueBatch(ctx, sessionID, in)
		return
	}
	if s.poolMode {
		logging.Warn("bridge: dropping inbound batch for a session no live interactive step owns",
			"session", sessionID, "peer", in.Peer.PeerID)
		return
	}
	var texts, fresh, repeats []string
	for _, m := range in.Messages {
		if s.delivered.has(in.Peer, m.ID) {
			repeats = append(repeats, m.ID)
			continue
		}
		texts = append(texts, m.Text)
		fresh = append(fresh, m.ID)
	}
	s.sendDeliveryAcks(ctx, sessionID, repeats)
	if len(fresh) == 0 {
		return
	}
	in.Messages = nil
	in.Text = PrependAttributionIfMultiPeer(in.Peer, strings.Join(texts, "\n\n"), s.sessionPeerCount(ctx, sessionID))
	disp := s.dispatcherFor(sessionID)
	disp.pushInbound(in)
	disp.preemptHeartbeat()
	s.delivered.add(s.RemoteJobID(), in.Peer, fresh...)
	s.sendDeliveryAcks(ctx, sessionID, fresh)
}

// EnqueueBatch puts a messages batch into an interactive session's inbox
// as one unit. Ids already delivered in this run are skipped and
// acknowledged again; ids still waiting in the inbox are skipped silently,
// since their acknowledgement is still to come. When at least one message
// is new, a pending question is closed as unanswered in the same critical
// section, so the batch is what the agent reads right after the question's
// result. A batch of repeats only leaves the question pending.
func (r *QuestionRouter) EnqueueBatch(ctx context.Context, sessionID string, in bridge.Inbound) {
	peerCount := r.svc.sessionPeerCount(ctx, sessionID)
	var repeats []string
	var evicted []inbox.Item
	r.mu.Lock()
	box := r.boxLocked()
	seen := make(map[string]bool, len(in.Messages))
	fresh := make([]inbox.Item, 0, len(in.Messages))
	for _, m := range in.Messages {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		id := m.ID
		// Check pending before delivered: a drain can move the id into the
		// delivered cache and release its reservation between these checks.
		if box.AnyPending(sessionID, func(it inbox.Item) bool {
			return it.ID == id && jobPeerKey(itemInbound(it).Peer) == jobPeerKey(in.Peer)
		}) {
			continue
		}
		if r.svc.delivered.has(in.Peer, m.ID) {
			repeats = append(repeats, m.ID)
			continue
		}
		single := in
		single.Messages = nil
		single.Text = m.Text
		fresh = append(fresh, inboxItem(m.ID, single, peerCount))
	}
	if len(fresh) > 0 {
		evicted = box.Push(sessionID, r.svc.onInboxDelivered, fresh...)
		if p, ok := r.pending[sessionID]; ok {
			delete(r.pending, sessionID)
			if r.svc.app != nil && r.svc.app.Questions != nil {
				if err := r.svc.app.Questions.Unanswered(p.requestID, unansweredQuestionNote); err != nil {
					logging.Warn("bridge: closing question as unanswered failed",
						"session", sessionID, "reqID", p.requestID, "err", err)
				} else {
					logging.Info("bridge: queued input closed the pending question as unanswered",
						"session", sessionID, "reqID", p.requestID, "messages", len(fresh))
				}
			}
		}
	}
	r.mu.Unlock()

	r.svc.sendDeliveryAcks(ctx, sessionID, repeats)
	r.notifyEvicted(ctx, sessionID, evicted)
	logging.Info("bridge: queued inbound batch for interactive session",
		"session", sessionID, "fresh", len(fresh), "repeats", len(repeats))
}

// onInboxDelivered is the inbox delivery hook: the agent loop stored items
// as user messages. Their ids are remembered for the run and acknowledged
// to the session's peers. Items without an id need neither.
func (s *Service) onInboxDelivered(sessionID string, items []inbox.Item) {
	var ids []string
	jobID := s.RemoteJobID()
	for _, it := range items {
		if it.ID == "" {
			continue
		}
		ids = append(ids, it.ID)
		s.delivered.add(jobID, itemInbound(it).Peer, it.ID)
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	s.sendDeliveryAcks(ctx, sessionID, ids)
}

// sendDeliveryAcks reports ids to every peer bound to the session whose
// adapter implements bridge.DeliveryAcker. The bindings are read on the
// caller's goroutine, so an acknowledgement for a step's last drain still
// finds them; the sends themselves are fire-and-forget, because the caller
// is the agent loop or the shared inbound loop.
func (s *Service) sendDeliveryAcks(ctx context.Context, sessionID string, ids []string) {
	if len(ids) == 0 || s.store == nil {
		return
	}
	bindings, err := s.store.ListBindingsBySession(ctx, s.projectID, sessionID)
	if err != nil {
		logging.Warn("bridge: delivery ack binding lookup", "session", sessionID, "err", err)
		return
	}
	sendCtx := bridge.ContextWithSessionID(context.WithoutCancel(ctx), sessionID)
	for _, b := range bindings {
		peer := b.AsPeerRef()
		da, ok := s.Adapter(peer.Channel, peer.Identity).(bridge.DeliveryAcker)
		if !ok {
			continue
		}
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					logging.Warn("bridge: delivery ack panic", "session", sessionID, "panic", rec)
				}
			}()
			if err := da.SendDelivered(sendCtx, peer, ids); err != nil {
				logging.Warn("bridge: delivery ack failed", "session", sessionID, "peer", peer, "err", err)
			}
		}()
	}
}

// notifyEvicted tells the sender of each overflowed item without an id
// that it was lost. Items with an id get no notice: they are never
// acknowledged, so their sender re-sends them. Fire-and-forget, because
// the caller runs on the shared inbound loop.
func (r *QuestionRouter) notifyEvicted(ctx context.Context, sessionID string, evicted []inbox.Item) {
	if len(evicted) == 0 || r.svc == nil {
		return
	}
	logging.Warn("bridge: interactive inbound buffer full — dropping oldest",
		"session", sessionID, "cap", interactiveInboundBufferCap, "dropped", len(evicted))
	for _, it := range evicted {
		if it.ID != "" {
			continue
		}
		r.sendNoticeAsync(ctx, sessionID, itemInbound(it).Peer,
			"bridge: your earlier message was lost because too many messages were buffered "+
				"while the interactive step had no pending question. "+
				"Please resend it once the current step completes.")
	}
}

// notifyNotDelivered tells the sender of each leftover item without an id
// that it never reached the agent (cd-5480 D9). Replies go to the
// inbound's own peer, which on Slack and Mattermost is its thread.
func (r *QuestionRouter) notifyNotDelivered(ctx context.Context, sessionID string, leftovers []inbox.Item) {
	for _, it := range leftovers {
		if it.ID != "" {
			continue
		}
		in := itemInbound(it)
		r.sendNoticeAsync(ctx, sessionID, in.Peer,
			"Your message didn't reach the agent before this step ended — please resend it.\n> "+
				noticeExcerpt(in.Text))
	}
}

// noticeExcerptMax bounds the quoted excerpt in a not-delivered notice.
const noticeExcerptMax = 200

func noticeExcerpt(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if runeCount(text) > noticeExcerptMax {
		return truncateRunes(text, noticeExcerptMax)
	}
	return text
}

// sendNoticeAsync sends a bridge notice to peer on a detached goroutine.
// WithoutCancel keeps the caller's values (session scope, trace ids) while
// detaching the lifetime, so the send is not cancelled the moment the
// caller returns.
func (r *QuestionRouter) sendNoticeAsync(ctx context.Context, sessionID string, peer bridge.PeerRef, text string) {
	sendCtx := context.WithoutCancel(ctx)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				logging.Warn("bridge: inbox notice panic", "session", sessionID, "panic", rec)
			}
		}()
		r.svc.replyToPeer(sendCtx, peer, text, false, sessionID)
	}()
}
