package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// Per-session dispatch channel capacities, per the chat-bridge spec:
//
//   - inbound: 16, NEVER drop. Reviewers' messages MUST NOT be lost.
//     When the channel is full, messages spill into the per-session
//     overflow slice so the shared runInboundLoop is never stalled.
//   - parts:   64, drop-oldest. Part-event transitions can collapse
//     ("completed" supersedes "running" supersedes "pending"). Drops are
//     rate-limited to one warn log per session per minute.
const (
	dispatchInboundCap = 16
	dispatchPartsCap   = 64
	// partsDrainGrace is how long the parts subscription is kept open
	// after agent.Run terminates, so trailing transitions (a tool's
	// final "completed" transition that the agent emits AFTER the
	// terminal AgentEvent) can be flushed to the chat surface. Tuned
	// short — agents emit terminal-event-then-trailing-parts in the
	// same iteration of the event loop, so the queued events are
	// already in flight; we only need to give the broker time to push.
	partsDrainGrace = 100 * time.Millisecond

	// busyRetryBackoff is the sleep between ErrSessionBusy retries.
	// Matches the TUI drain worker's precedent (app/queue.go busyBackoff).
	busyRetryBackoff = 100 * time.Millisecond
)

// busyRetryBudget is the maximum time handleInbound will retry agent.Run on
// ErrSessionBusy before re-queuing the message via the overflow path.
// Cross-actor holders (flow steps, cron sentinels, task auto-resume) can hold a
// session for several minutes; 5 minutes gives them a generous window before
// the message is re-queued for another attempt. Content is never discarded.
// A variable rather than a const so tests can shrink it (see busyAckThreshold).
var busyRetryBudget = 5 * time.Minute

// busyAckThreshold is the minimum duration of ErrSessionBusy retrying before
// a queued-acknowledgement is sent to the peer (Decision 3: 2-second short-wait
// threshold). Hardcoded for v1; exported as a variable so tests can override it
// without an N×100 ms spin wait.
var busyAckThreshold = 2 * time.Second

// toolErrorPreviewRunes caps the failure reason appended to a ✗ tool
// line. Tool updates are compact by design (name + id + duration only);
// a failed call is the one case that carries body text, because an
// invisible failure is worse than an extra short line. The full result
// is always in the session store and Langfuse.
const toolErrorPreviewRunes = 200

// toolFullPreviewRunes caps the successful-result body included only
// under full verbosity. Matches the pre-compact behaviour.
const toolFullPreviewRunes = 1000

// sessionDispatch owns the single agent.Run callsite for one bound
// sessionID. It runs in its own goroutine; all inbound messages for the
// session route through its inbound channel, ensuring serialization (no
// ErrSessionBusy can escape) and ordering (parts-event demux is interleaved
// with inbound on the same select loop). Lifecycle is tied to bind/unbind:
// created on the first Bind for the session, torn down on the last Unbind
// or when the orchestrator observes the session row's session_id was
// NULL'd by FK ON DELETE SET NULL.
type sessionDispatch struct {
	svc       *Service
	sessionID string

	inbound chan bridge.Inbound
	parts   chan pubsub.Event[message.PartEvent]

	// mu guards overflowLog, overflow, and the non-blocking push/drain
	// interlock. MUST NOT be held across I/O or across calls that acquire
	// another lock.
	mu          sync.Mutex
	overflowLog time.Time
	// overflow holds inbound messages that could not fit into d.inbound
	// when the channel was full (non-starvation fix — see pushInbound).
	// Drained back into d.inbound by drainOverflowToInbound after each
	// handleInbound call. Guarded by mu.
	overflow []bridge.Inbound

	stop atomic.Bool

	// ownedSessions caches whether a given session ID is "ours" —
	// either this dispatcher's session or one of its descendants
	// (subagent sessions spawned via the `task` tool, which share
	// root_session_id with the parent). Per-event store lookups
	// would be too expensive in the hot path; this map amortises it.
	// Keys: session ID; values: bool. See isOwnedSession.
	ownedSessions sync.Map

	// toolCallStart records each tool call's wall-clock start so the
	// matching result emit can compute DurationMs. Keys are the raw
	// provider tool-call ID (e.g. toolu_01...); values are unix-millis.
	// Entries are evicted on consume by consumeToolCallDuration; a 5-min
	// sweep removes stale entries when a call never produces a paired
	// result (rare — usually a cancelled cycle).
	toolCallStart sync.Map // map[string]int64

	// liveAcks remembers the outstanding queued-ack token per peer so it
	// survives a busy-retry-budget re-queue. handleInbound's ack state is a
	// local and budget expiry returns from handleInbound — without this the
	// next 5-minute cycle would SEND a brand-new "⏳ queued" message instead
	// of editing the existing one, leaving one orphaned, never-resolved ack
	// per cycle in the reviewer's chat. Keys: peerAckKey(peer); values:
	// bridge.QueueAckToken. Entries are removed when the ack is resolved
	// (run started) or when an edit fails (message gone — send a fresh one).
	liveAcks sync.Map // map[string]bridge.QueueAckToken
}

// newSessionDispatch constructs and launches the per-session dispatcher
// goroutines. There are TWO goroutines per dispatcher:
//
//   - run: drains d.inbound and calls handleInbound, which blocks for
//     the entire agent.Run lifetime (minutes for long tool sequences).
//   - runParts: drains d.parts and calls handlePartEvent, processing
//     tool-update events CONCURRENTLY with the in-flight inbound. The
//     spec mandates "Indicator emission MUST NOT block the inbound
//     dispatch loop"; folding parts into the same select as inbound
//     made the loop block for the entire run duration so tool icons
//     only arrived AFTER the final assistant reply. Splitting into two
//     goroutines fixes the ordering — parts now interleave with the
//     run in real time.
//
// Both exit when ctx is cancelled, the inbound channel is closed via
// close(), or Service.Stop tears the bridge down.
func (s *Service) newSessionDispatch(sessionID string) *sessionDispatch {
	d := &sessionDispatch{
		svc:       s,
		sessionID: sessionID,
		inbound:   make(chan bridge.Inbound, dispatchInboundCap),
		parts:     make(chan pubsub.Event[message.PartEvent], dispatchPartsCap),
	}
	s.launchSupervised("session-dispatch/"+sessionID, d.run)
	s.launchSupervised("session-dispatch-parts/"+sessionID, d.runParts)
	return d
}

// run is the dispatcher's inbound loop. Reads from d.inbound, calls
// handleInbound, repeats. handleInbound BLOCKS for the entire agent.Run
// lifetime — that's by design (the spec mandates one in-flight Run per
// session at a time), so this loop only processes one inbound at a
// time. Parts events are handled in parallel by runParts so they don't
// have to wait for the run to finish.
//
// After each handleInbound, overflow items are drained back into
// d.inbound (FIFO) so they are processed before any newly-arriving
// messages from runInboundLoop.
func (d *sessionDispatch) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case in, ok := <-d.inbound:
			if !ok {
				return
			}
			if d.stop.Load() {
				return
			}
			d.handleInbound(ctx, in)
			d.drainOverflowToInbound()
		}
	}
}

// drainOverflowToInbound transfers overflow items into d.inbound under
// mu so the transfer is atomic with concurrent pushInbound calls from
// runInboundLoop. Called by run() after each handleInbound.
//
// FIFO ordering: overflow items are older than any items that arrive
// concurrently from runInboundLoop. Transferring them into d.inbound
// (a FIFO channel) while holding mu ensures new arrivals see the channel
// full and go to overflow AFTER the existing overflow items — so read
// order is:
//
//	[items already in d.inbound] → [drained overflow] → [new arrivals]
func (d *sessionDispatch) drainOverflowToInbound() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for len(d.overflow) > 0 {
		select {
		case d.inbound <- d.overflow[0]:
			d.overflow = d.overflow[1:]
		default:
			// Channel still full; remaining overflow items stay and
			// will be drained on the next handleInbound cycle.
			return
		}
	}
}

// runParts drains d.parts on a separate goroutine so tool-update
// emission overlaps with the in-flight agent.Run. Without this split,
// tool icons (🔧 read / 🔧 grep / etc.) only reach the chat surface
// AFTER the final assistant reply, because handleInbound blocks the
// dispatch loop's select.
//
// The d.parts channel still acts as the back-pressure boundary with
// drop-oldest semantics (drainParts forwards from the broker into
// d.parts non-blockingly); runParts consumes that buffer at chat-
// surface speed.
func (d *sessionDispatch) runParts(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-d.parts:
			if !ok {
				return
			}
			if d.stop.Load() {
				return
			}
			d.handlePartEvent(ev)
		}
	}
}

// handleInbound is one full agent.Run turn for a single inbound message.
// Steps (per the chat-bridge spec subscribe-before-Run requirement):
//
//  1. Subscribe to messages.SubscribeParts BEFORE calling agent.Run. The
//     broker has a zero-subscribers fast path that drops events emitted
//     before any subscriber attaches — calling Run first would silently
//     lose the agent's first ToolCall pending events.
//  2. Translate bridge.Attachment slice into message.Attachment for
//     agent.Run (the bridge package and message package use distinct
//     attachment types because the bridge package can't import message
//     without creating an import cycle through internal/config).
//  3. Invoke agent.Run; drain the returned channel (one terminal event).
//  4. Demux parts from the local subscription into d.parts so the
//     dispatcher's select loop can fan them to outbound, but cap the
//     queue at 64 with drop-oldest (newest wins because per-tool
//     transitions are strictly ordered: pending → running → completed).
func (d *sessionDispatch) handleInbound(ctx context.Context, in bridge.Inbound) {
	defer func() {
		if r := recover(); r != nil {
			logging.Error("bridge: handleInbound panic", "session", d.sessionID, "panic", r)
		}
	}()

	// Named `ag`, not `agent`: the local must not shadow the agent package.
	ag := d.svc.app.ActiveAgent()
	if ag == nil {
		logging.Warn("bridge: no active agent; dropping inbound", "session", d.sessionID)
		d.svc.replyToPeer(ctx, in.Peer,
			"bridge: this session has no active agent — your message could not be processed. "+
				"Please try again once the agent is available.",
			false, d.sessionID)
		return
	}

	// Subscribe parts BEFORE Run — the broker's zero-subscriber fast path
	// will otherwise drop the agent's first ToolCall pending events.
	// On return, give the parts subscription a short grace window to
	// flush any trailing "completed" transitions that the agent emitted
	// AFTER the terminal AgentEvent. Without it, the partsCancel would
	// fire the moment handleInbound returns and drainParts could exit
	// before the tail events made it through the broker — leading to
	// stale "running" tool indicators on the chat surface.
	partsCtx, partsCancel := context.WithCancel(ctx)
	defer func() {
		select {
		case <-time.After(partsDrainGrace):
		case <-ctx.Done():
		}
		partsCancel()
	}()
	partsSub := d.svc.app.Messages.SubscribeParts(partsCtx)

	atts := translateAttachments(in.Attachments)

	// Bounded retry for ErrSessionBusy: the session-run ledger is
	// process-global (session-run-exclusivity spec). Cross-actor holders
	// — a flow step's own agent, a cron sentinel, a task auto-resume —
	// make agent.Run return ErrSessionBusy here. The bridge's single-
	// dispatcher serialization prevents bridge-vs-bridge collisions but
	// cannot prevent cross-actor ones. Retry with 100 ms backoff for up
	// to 5 minutes; on budget expiry, re-queue the message via the
	// overflow path so content is NEVER discarded.
	//
	// Queued-ack lifecycle (Decision 2 + Decision 3):
	//   - A 2-second short-wait timer arms on the first ErrSessionBusy.
	//   - If the timer fires while still retrying AND acks are enabled,
	//     the peer receives a "⏳ queued" message (SendQueuedAck).
	//   - The ack is updated in-place ONLY when the reported position
	//     actually changes (UpdateQueuedAck). Editing on every 100 ms retry
	//     would issue thousands of identical edits per queued message, which
	//     burns the platforms' edit rate limits and makes Telegram reject the
	//     call outright ("message is not modified").
	//   - When Run succeeds, the ack is resolved to "▶ Processing…"
	//     (UpdateQueuedAck, position=0).
	deadline := time.Now().Add(busyRetryBudget)
	ackThreshold := time.Now().Add(busyAckThreshold)
	ack := queueAckState{lastPosition: -1}
	if tok, ok := d.liveAcks.Load(peerAckKey(in.Peer)); ok {
		// A previous retry cycle for this peer already has an ack message in
		// chat (busy-retry budget expired and the inbound was re-queued).
		// Reuse it so the peer sees one ack that keeps updating.
		ack.token, _ = tok.(bridge.QueueAckToken)
	}
	var runCh <-chan agent.AgentEvent
	for {
		var err error
		runCh, err = ag.Run(ctx, d.sessionID, in.Text, 0, atts...)
		if err == nil {
			break
		}
		if !errors.Is(err, agent.ErrSessionBusy) {
			// Non-busy error: the run never started, so the ack must NOT be
			// resolved to "▶ Processing…" — that would contradict the failure
			// reply sent immediately after. Leave the "⏳ queued" text in place.
			logging.Warn("bridge: agent.Run failed", "session", d.sessionID, "err", err)
			d.svc.replyToPeer(ctx, in.Peer, runFailureMessage(err, d.sessionID), false, d.sessionID)
			return
		}
		// ErrSessionBusy from a cross-actor holder. Check budget.
		if time.Now().After(deadline) {
			// Do NOT resolve the ack here: the message is being re-queued, not
			// processed. Resolving would tell the peer "▶ Processing your
			// message now…" while it goes back to the tail of the retry cycle.
			logging.Warn("bridge: ErrSessionBusy budget expired; re-queuing inbound",
				"session", d.sessionID)
			d.pushInbound(in)
			return
		}
		// Check / send / update the queued-ack.
		d.tickQueueAck(ctx, in.Peer, &ack, &ackThreshold)
		select {
		case <-ctx.Done():
			return
		case <-time.After(busyRetryBackoff):
		}
	}
	// Run succeeded — resolve the ack before starting the run.
	d.resolveQueueAck(ctx, in.Peer, ack.token)

	// Fan part events into d.parts for outbound surface delivery (typing,
	// tool-update prints). Filter to this session's parts; broker is
	// process-wide and carries every session's events. The drainParts
	// goroutine runs under the supervised launcher so a panic inside
	// (e.g. a malformed PartEvent) cannot crash the orchestrator, and
	// s.wg tracks it across Service.Stop.
	d.svc.launchSupervisedCtx("dispatch-parts/"+d.sessionID, partsCtx, func(ctx context.Context) {
		d.drainParts(ctx, partsSub)
	})

	// Drain the agent's terminal event. The channel delivers exactly one
	// AgentEvent and closes (per agent.Run contract). We process its
	// outcome (text + struct-output) on the dispatcher goroutine so all
	// outbound work for this session remains serialized.
	for ev := range runCh {
		d.handleTerminalEvent(ctx, ev)
	}
}

// runFailureMessage builds the chat-surface text for an agent.Run that failed
// to start with a non-busy error. ErrSessionBusy is handled by the retry
// loop in handleInbound and never reaches this function.
func runFailureMessage(err error, sessionID string) string {
	// Cap the detail leaked to chat to the public-facing fields.
	return "bridge: agent run failed (" + err.Error() + "). " +
		"If this keeps happening, use /reset in chat to clear the session " +
		"or POST /session/" + sessionID + "/abort to release the busy lock."
}

// peerAckKey is the liveAcks map key for a peer. Channel+identity+peerID is
// the same tuple bindings are keyed on, so two identities in the same channel
// never share an ack slot.
func peerAckKey(p bridge.PeerRef) string {
	return p.Channel + "|" + p.Identity + "|" + p.PeerID
}

// queueAckState is handleInbound's local queued-ack bookkeeping: the platform
// token for in-place edits and the position last rendered into it. lastPosition
// starts at -1 ("nothing rendered yet") so position 0 can never be mistaken for
// an already-rendered value.
type queueAckState struct {
	token        bridge.QueueAckToken
	lastPosition int
}

// tickQueueAck manages the queued-ack lifecycle on each ErrSessionBusy retry
// cycle. On first call after the short-wait threshold (busyAckThreshold), it
// sends the initial "⏳ queued" message if acks are enabled. On subsequent
// calls it updates the ack in-place ONLY when the position it would render has
// changed — the retry loop ticks every 100 ms, so editing unconditionally would
// issue up to 3000 identical edits per queued message (rate-limit exhaustion on
// Slack/Mattermost, and a hard "message is not modified" error on Telegram).
//
// ack is a pointer to handleInbound's local ack state.
// threshold is a pointer to the firing time.
func (d *sessionDispatch) tickQueueAck(ctx context.Context, peer bridge.PeerRef, ack *queueAckState, threshold *time.Time) {
	if d.svc.cfg == nil || !d.svc.cfg.QueueAcknowledgementsEnabled {
		return
	}
	adapter := d.svc.Adapter(peer.Channel, peer.Identity)
	acker, ok := adapter.(bridge.QueuedAcknowledger)
	if !ok {
		return
	}
	// position = 1 means the message is next-in-line once the current
	// cross-actor holder releases the slot.
	position := 1 + len(d.inbound)
	// Initial send: fires when threshold has elapsed AND we don't yet have a token.
	if ack.token == "" {
		if time.Now().Before(*threshold) {
			return
		}
		tok, err := acker.SendQueuedAck(ctx, peer, position)
		if err != nil {
			logging.Info("bridge: SendQueuedAck failed", "session", d.sessionID, "err", err)
			return
		}
		ack.token = tok
		ack.lastPosition = position
		d.liveAcks.Store(peerAckKey(peer), tok)
		return
	}
	// Update in-place only when the rendered text would actually change.
	if position == ack.lastPosition {
		return
	}
	if err := acker.UpdateQueuedAck(ctx, peer, ack.token, position); err != nil {
		logging.Info("bridge: UpdateQueuedAck failed", "session", d.sessionID, "err", err)
		// The ack message may be gone (deleted by the user, or a token from a
		// previous cycle that is no longer editable). Forget it so the next
		// tick sends a fresh one rather than editing into the void forever.
		d.liveAcks.Delete(peerAckKey(peer))
		ack.token = ""
		return
	}
	ack.lastPosition = position
}

// resolveQueueAck edits the ack message to "▶ Processing…" (position == 0).
// A no-op when ackToken is empty or acks are disabled.
func (d *sessionDispatch) resolveQueueAck(ctx context.Context, peer bridge.PeerRef, ackToken bridge.QueueAckToken) {
	if ackToken == "" {
		return
	}
	// The run is starting: this ack is done, whatever the edit's outcome.
	d.liveAcks.Delete(peerAckKey(peer))
	if d.svc.cfg == nil || !d.svc.cfg.QueueAcknowledgementsEnabled {
		return
	}
	adapter := d.svc.Adapter(peer.Channel, peer.Identity)
	acker, ok := adapter.(bridge.QueuedAcknowledger)
	if !ok {
		return
	}
	if err := acker.UpdateQueuedAck(ctx, peer, ackToken, 0); err != nil {
		logging.Info("bridge: resolveQueueAck failed", "session", d.sessionID, "err", err)
	}
}

// drainParts forwards parts for this session AND any of its descendant
// (subagent) sessions from the broker subscription to d.parts. Returns
// when partsCtx is cancelled (set by handleInbound after agent.Run
// completes).
//
// Subagent visibility: when the parent agent calls the `task` tool,
// opencode spawns a subagent on a NEW session whose root_session_id
// points back at the parent. Subagent tool activity (which can
// dominate the run — e.g. 15 minutes of Atlassian MCP calls inside one
// task) emits part events on the SUBAGENT's session, not the parent's.
// Without the descendant filter, the reviewer would see a single
// "🔧 task#<id>" line at the start and then silence for the entire
// subagent run.
// Including descendant events makes the chat surface reflect what the
// run is actually doing, so a hung MCP call is visible instead of
// looking like the bridge itself is stuck.
//
// Drop-oldest semantics are preserved — the consumer (runParts) drains
// d.parts in parallel with handleInbound, so backlog is rare; when it
// does happen, the oldest event is dropped first.
func (d *sessionDispatch) drainParts(partsCtx context.Context, sub <-chan pubsub.Event[message.PartEvent]) {
	for {
		select {
		case <-partsCtx.Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return
			}
			if !d.isOwnedSession(partsCtx, ev.Payload.SessionID) {
				continue
			}
			select {
			case d.parts <- ev:
			default:
				d.logOverflow()
				// Drop oldest: try once more (non-blocking). The
				// previously-oldest event was on the front of the
				// buffer; pop it by reading and discarding, then
				// retry the send.
				select {
				case <-d.parts:
				default:
				}
				select {
				case d.parts <- ev:
				default:
					// Still full — the consumer is wedged; surrender.
				}
			}
		}
	}
}

// isOwnedSession reports whether a part event's session_id is either
// this dispatcher's own session, or a descendant subagent session
// (root_session_id == d.sessionID). Results are cached in
// d.ownedSessions to amortise the per-event store lookup — a busy
// subagent can emit hundreds of events per minute, all from the same
// session ID, so we only want to look up once per discovered session.
//
// The cache is per-dispatcher and tied to its lifetime; no GC needed
// because a dispatcher is torn down when the binding is unbound.
func (d *sessionDispatch) isOwnedSession(ctx context.Context, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	if sessionID == d.sessionID {
		return true
	}
	if v, ok := d.ownedSessions.Load(sessionID); ok {
		return v.(bool)
	}
	// Cache miss — resolve via the session service. Unknown sessions
	// (deleted, not yet flushed) cache as false so we don't repeat
	// the lookup for every event in a flood.
	owned := false
	if d.svc.app != nil && d.svc.app.Sessions != nil {
		sess, err := d.svc.app.Sessions.Get(ctx, sessionID)
		if err == nil && sess.RootSessionID == d.sessionID {
			owned = true
		}
	}
	d.ownedSessions.Store(sessionID, owned)
	return owned
}

// logOverflow emits a rate-limited warn log when the parts buffer
// overflows, per the chat-bridge spec ("once per session per minute").
func (d *sessionDispatch) logOverflow() {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if now.Sub(d.overflowLog) < time.Minute {
		return
	}
	d.overflowLog = now
	logging.Warn("bridge: part-queue overflow",
		"session", d.sessionID,
		"cap", dispatchPartsCap,
	)
}

// handleTerminalEvent is invoked once per agent.Run with the terminal
// AgentEvent. For text turns it fans agent output across all bound peers
// via Service.SendBySessionID; for error events it logs but still emits
// any text the agent had produced; struct-output events skip fan-out
// (the flow engine drains the structured result separately).
//
// Implementation note: fan-out to bound peers happens through
// Service.SendBySessionID which queries the store + dispatches to adapters
// in a bounded worker pool — this dispatcher does NOT do outbound IO
// directly so a slow chat platform does not stall the next agent turn.
func (d *sessionDispatch) handleTerminalEvent(ctx context.Context, ev agent.AgentEvent) {
	switch ev.Type {
	case agent.AgentEventTypeError:
		logging.Warn("bridge: agent run terminal error",
			"session", d.sessionID, "err", ev.Error)
		// Fall through to fan-out so any partial text the agent
		// emitted before erroring still reaches the chat surface.
	case agent.AgentEventTypeSummarize:
		// Summarization is internal — no chat-surface delivery.
		return
	}

	text := agentMessageText(ev.Message)
	mediaRoot, _ := d.svc.MediaDir()
	clean, atts, unsafe := ParseFileTokens(text, mediaRoot)
	if len(unsafe) > 0 {
		logging.Warn("bridge: dropped unsafe FILE: paths from agent output",
			"session", d.sessionID, "paths", unsafe)
	}
	if clean == "" && len(atts) == 0 {
		return
	}

	out := bridge.Outbound{Text: clean, Attachments: atts}
	results, err := d.svc.SendBySessionID(ctx, d.sessionID, out)
	if err != nil {
		logging.Warn("bridge: terminal-event fan-out failed",
			"session", d.sessionID, "err", err)
		return
	}
	for _, r := range results {
		if !r.Delivered && r.Err != nil {
			logging.Info("bridge: per-peer delivery failed",
				"session", d.sessionID, "peer", r.Binding.PeerID, "err", r.Err)
		}
	}
}

// agentMessageText concatenates every TextContent part in the agent's
// terminal message. ReasoningContent (the model's internal chain of
// thought) and ToolCall/ToolResult parts are skipped — they're not
// user-facing chat surface output. The TS bridge does the same.
func agentMessageText(m message.Message) string {
	var b strings.Builder
	first := true
	for _, p := range m.Parts {
		if tc, ok := p.(message.TextContent); ok {
			if !first {
				b.WriteString("\n")
			}
			b.WriteString(tc.Text)
			first = false
		}
	}
	return b.String()
}

// handlePartEvent forwards a single part transition to the outbound
// surface. When cfg.Router.ToolUpdatesEnabled is true, tool-call lifecycle
// transitions are summarized as short chat messages so the reviewer
// sees what the agent is doing in real time. Failures (ToolResult with
// IsError) are ALWAYS surfaced regardless of the flag — silent tool
// failures are too easy to miss otherwise.
//
// Emission defaults to COMPACT — one line per tool call, carrying only
// the status glyph, the tool name, the pairing id and (on completion)
// the elapsed time:
//   - ToolCall with Finished=true → "🔧 <name>#<id>"
//   - Successful completion       → "✓ <name>#<id> · <duration>"
//   - Failed completion           → "✗ <name>#<id> · <duration> · <reason>"
//
// In compact mode, tool ARGUMENTS and successful result BODIES are NOT
// sent to chat. A daemon-mode thread is a progress indicator, not a
// transcript: the full input/output of every call is already durably
// recorded in the session store (messages.parts) and in Langfuse, which
// is where an investigation belongs. The single exception is a failure
// reason, truncated to toolErrorPreviewRunes — an error the reviewer
// can't see at all is worse than one extra short line.
//
// Under `router.toolUpdateVerbosity: "full"` (or after `/verbosity full`)
// the argument summary and a truncated result body are included, for
// reviewers watching a single run closely.
//
// The #<id> suffix is a short stable hash of the tool_call_id so a
// reviewer watching parallel tool calls can pair each ✓/✗ result back
// to the originating 🔧 call. Without it, two concurrent `bash` calls
// would render as indistinguishable "🔧 bash" / "✓ bash" pairs.
//
// Per the chat-bridge spec the dispatcher MUST consume from d.parts
// even when the outbound is suppressed — otherwise drainParts back-
// pressures the broker subscription and stalls every other session.
func (d *sessionDispatch) handlePartEvent(ev pubsub.Event[message.PartEvent]) {
	if d.svc.cfg == nil {
		return
	}
	// Synthetic messages (cron-fired completions, background bash/task/
	// monitor completions injected via task.EnqueueTaskCompletion) must NOT
	// surface as tool-update indicators in chat. The agent's NEXT real
	// assistant message — its human-readable reaction to the synthetic
	// ToolResult — still flows to chat through the normal text path.
	if ev.Payload.Synthetic {
		return
	}
	tu := d.svc.cfg.ToolUpdatesEnabled
	full := d.svc.ToolVerbosity() == bridge.ToolUpdateVerbosityFull
	switch part := ev.Payload.Part.(type) {
	case message.ToolCall:
		// Streaming providers (Anthropic) publish each ToolCall up to
		// THREE times:
		//   1. EventToolUseStart — Finished=false, Input empty
		//   2. EventToolUseStop  — Finished=true,  Input STILL empty
		//      (the delta-accumulation path is commented out in
		//      agent.go, so Input isn't merged at this point)
		//   3. EventComplete     — Finished=true,  Input MERGED with
		//      the assembled args (via mergeToolCalls)
		// Non-streaming providers (OpenAI / Gemini) only fire #3.
		//
		// We want exactly one line per tool call, with the full args.
		// Filter on `Finished && Input != ""`:
		//   - #1 fails Finished                  → skip
		//   - #2 has empty Input                 → skip
		//   - #3 (the only useful one)           → emit
		// A genuinely-no-args tool (e.g. get_all_projects → "{}")
		// still passes because its Input is the literal "{}", not "".
		if !tu || !part.Finished || part.Input == "" {
			return
		}
		// Record the call's wall-clock start so the result emit can
		// compute DurationMs even when the adapter doesn't track timing
		// per-call.
		d.recordToolCallStart(part.ID)
		hint, fallback := toolCallRender(part.Name, callIDSuffix(part.ID), part.Input, full)
		d.emitToolRender(hint, fallback)
	case message.ToolResult:
		// Gate non-error results behind tu (matches today's behaviour).
		if !part.IsError && !tu {
			return
		}
		durationMs := d.consumeToolCallDuration(part.ToolCallID)
		hint, fallback := toolResultRender(
			part.Name, callIDSuffix(part.ToolCallID), part.IsError, part.Content, durationMs, full)
		d.emitToolRender(hint, fallback)
	}
}

// toolCallRender builds the compact pending-call render: a status glyph,
// the tool name and the pairing id — nothing else. Params are nil in
// compact mode so RichRenderer adapters skip their params block and the
// chat line stays one line (see handlePartEvent's contract note); in
// full mode the argument summary is attached.
func toolCallRender(name, callID, input string, full bool) (*bridge.RenderHint, string) {
	label := name + callID
	if !full {
		return bridge.NewToolCallHint(name, callID, nil), fmt.Sprintf("🔧 %s", label)
	}
	fallback := fmt.Sprintf("🔧 %s", label)
	if params := formatToolParams(name, input); params != "" {
		fallback += " · " + params
	}
	return bridge.NewToolCallHint(name, callID, formatToolParamMap(name, input)), fallback
}

// toolResultRender builds the compact completion render. Successful
// calls carry NO body — only the glyph, name, pairing id and elapsed
// time. Failures append a rune-capped reason so a broken call is
// actionable from chat alone; the full result stays in the session
// store and Langfuse. In full mode a successful call also carries a
// truncated result body.
func toolResultRender(name, callID string, isError bool, content string, durationMs int64, full bool) (*bridge.RenderHint, string) {
	status := "ok"
	glyph := "✓"
	preview := ""
	if isError {
		status = "error"
		glyph = "✗"
		preview = truncateRunes(oneLine(content), toolErrorPreviewRunes)
	} else if full {
		preview = truncateRunes(oneLine(content), toolFullPreviewRunes)
	}
	fallback := fmt.Sprintf("%s %s%s", glyph, name, callID)
	if durationMs > 0 {
		fallback += " · " + formatDurationMs(durationMs)
	}
	if preview != "" {
		fallback += " · " + truncateRunes(preview, toolErrorPreviewRunes)
	}
	return bridge.NewToolResultHint(name, callID, status, preview, durationMs), fallback
}

// callIDSuffix renders a short stable suffix derived from the tool
// call ID so reviewers can pair "🔧 bash#abcd" with its "✓ bash#abcd"
// (or "✗ bash#abcd") result. Empty input yields an empty suffix.
//
// The ID is truncated to the trailing 6 chars — provider-issued IDs
// are typically opaque strings like "toolu_01ABC..." or "call_xyz...".
// The trailing portion is more entropic than the prefix (which is
// often a fixed scheme prefix) and short enough to keep chat lines
// compact.
func callIDSuffix(id string) string {
	if id == "" {
		return ""
	}
	const n = 6
	if len(id) > n {
		id = id[len(id)-n:]
	}
	return "#" + id
}

// truncateRunes returns s capped to maxRunes codepoints. Slicing a
// UTF-8 string at a byte index can land mid-codepoint and produce
// invalid UTF-8; counting runes guarantees the cut is at a codepoint
// boundary. Appends an ellipsis when truncation occurred.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i] + "…"
		}
		count++
	}
	return s
}

// formatDurationMs renders a millisecond duration as a compact
// "850ms" / "1.4s" / "1m2s" for the plain-text fallback line. Adapters
// that satisfy bridge.RichRenderer format the duration themselves from
// RenderHint.DurationMs; this is only used when the render hint can't
// be honoured.
func formatDurationMs(ms int64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		return fmt.Sprintf("%dm%ds", ms/60_000, (ms%60_000)/1000)
	}
}

// Full-verbosity helpers below. They are only reached when the live
// verbosity is "full" (router.toolUpdateVerbosity or /verbosity full);
// compact mode never calls them.
// formatToolParamMap is the structured analogue of formatToolParams —
// returns the same priority-keyed primary + secondary params as a map
// so a RenderHint can carry them for adapters that render fields
// natively (Slack Block Kit context, Mattermost attachment.fields).
// Returns nil for unknown tools or malformed JSON so adapters fall
// back to a header-only render.
func formatToolParamMap(name, input string) map[string]string {
	if input == "" {
		return nil
	}
	const maxParamInputBytes = 64 * 1024
	if len(input) > maxParamInputBytes {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(input), &raw); err != nil {
		return nil
	}
	type keySpec struct {
		primary   string
		secondary []string
	}
	keys := map[string]keySpec{
		"bash":        {primary: "command"},
		"read":        {primary: "file_path", secondary: []string{"limit", "offset"}},
		"write":       {primary: "file_path"},
		"edit":        {primary: "file_path"},
		"multiedit":   {primary: "file_path", secondary: []string{"edits"}},
		"delete":      {primary: "path"},
		"ls":          {primary: "path"},
		"grep":        {primary: "pattern", secondary: []string{"path", "include"}},
		"glob":        {primary: "pattern", secondary: []string{"path"}},
		"view_image":  {primary: "file_path"},
		"webfetch":    {primary: "url"},
		"websearch":   {primary: "query", secondary: []string{"max_results"}},
		"sourcegraph": {primary: "query"},
		"task":        {primary: "prompt", secondary: []string{"subagent_type"}},
		"router_send": {primary: "peerId", secondary: []string{"channel", "identity"}},
	}
	spec, known := keys[name]
	if !known {
		return nil
	}
	out := map[string]string{}
	if v := stringField(raw, spec.primary); v != "" {
		out[spec.primary] = truncateRunes(oneLine(v), 200)
	}
	for _, k := range spec.secondary {
		if v := stringField(raw, k); v != "" {
			out[k] = truncateRunes(oneLine(v), 200)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// formatToolParams extracts a short informative summary from a
// ToolCall.Input (a JSON string). Common params for known tools
// (bash command, file path, grep pattern, etc.) are surfaced
// verbatim; unknown tools fall back to a single-line dump of the
// raw input. Result is rune-truncated to 200 chars so a long bash
// command or file path can't dominate a chat line.
//
// Implementation note: this duplicates a subset of the TUI's
// renderToolParams logic but uses anonymous JSON decoding instead
// of importing each tool's typed param struct. That keeps the
// bridge package free of a dependency on internal/llm/tools.
func formatToolParams(name, input string) string {
	if input == "" {
		return ""
	}
	// Guard against a runaway agent or malformed tool surface that
	// could emit a multi-megabyte JSON input. Per-event allocation
	// is a hot path here (handlePartEvent runs once per tool
	// transition), and json.Unmarshal allocates proportionally to
	// the input size. Realistic tool inputs are < 4 KB; the cap
	// gives generous headroom while bounding worst case.
	const maxParamInputBytes = 64 * 1024
	if len(input) > maxParamInputBytes {
		return truncateRunes(strings.ReplaceAll(input, "\n", " "), 200)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(input), &raw); err != nil {
		// Fall back to the raw input minus newlines so the line stays
		// single-line.
		return truncateRunes(strings.ReplaceAll(input, "\n", " "), 200)
	}
	// Priority key per known tool. The first key in the list that's
	// present and non-empty wins. The "params" tail appends any
	// secondary keys for context (e.g. read offset/limit).
	type keySpec struct {
		primary   string
		secondary []string
	}
	keys := map[string]keySpec{
		"bash":        {primary: "command"},
		"read":        {primary: "file_path", secondary: []string{"limit", "offset"}},
		"write":       {primary: "file_path"},
		"edit":        {primary: "file_path"},
		"multiedit":   {primary: "file_path", secondary: []string{"edits"}},
		"delete":      {primary: "path"},
		"ls":          {primary: "path"},
		"grep":        {primary: "pattern", secondary: []string{"path", "include"}},
		"glob":        {primary: "pattern", secondary: []string{"path"}},
		"view_image":  {primary: "file_path"},
		"webfetch":    {primary: "url"},
		"websearch":   {primary: "query", secondary: []string{"max_results"}},
		"sourcegraph": {primary: "query"},
		"task":        {primary: "prompt", secondary: []string{"subagent_type"}},
		"router_send": {primary: "peerId", secondary: []string{"channel", "identity"}},
	}
	spec, known := keys[name]
	var parts []string
	if known {
		if v := stringField(raw, spec.primary); v != "" {
			parts = append(parts, oneLine(v))
		}
		for _, k := range spec.secondary {
			if v := stringField(raw, k); v != "" {
				parts = append(parts, k+"="+oneLine(v))
			}
		}
	}
	if len(parts) == 0 {
		// Unknown tool or known-tool with no recognised primary key —
		// fall back to a compact representation of the whole input.
		return truncateRunes(strings.ReplaceAll(input, "\n", " "), 200)
	}
	return truncateRunes(strings.Join(parts, " "), 200)
}

// stringField returns map[k] coerced to a single-line string. Numbers
// and booleans are formatted via %v; arrays/objects show their length
// (e.g. "edits=3") because dumping their full content rarely improves
// readability on a chat line.
func stringField(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// JSON numbers always decode to float64; render as integer
		// when possible so "offset=10" doesn't read "offset=10.000000".
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case bool:
		return fmt.Sprintf("%v", t)
	case []any:
		return fmt.Sprintf("%d", len(t))
	case map[string]any:
		return fmt.Sprintf("(%d keys)", len(t))
	default:
		return fmt.Sprintf("%v", v)
	}
}

// oneLine strips newlines so a multi-line bash command or prompt
// doesn't break chat formatting.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// emitToolUpdate sends a short status line to every peer currently
// bound to this dispatcher's session. Runs in a separate goroutine so
// platform-call latency cannot stall the per-session dispatcher loop
// (per the chat-bridge spec "Indicator emission MUST NOT block the
// inbound dispatch loop").
func (d *sessionDispatch) emitToolUpdate(text string) {
	ctx := d.svc.ctx
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Warn("bridge: emitToolUpdate panic", "session", d.sessionID, "panic", r)
			}
		}()
		_, err := d.svc.SendBySessionID(ctx, d.sessionID, bridge.Outbound{Text: text})
		if err != nil {
			logging.Warn("bridge: tool-update fan-out failed", "session", d.sessionID, "err", err)
		}
	}()
}

// emitToolRender fans out a structured RenderHint to every bound peer
// for the dispatcher's session. Mirrors emitToolUpdate's
// fire-and-forget posture (per the spec: "Indicator emission MUST NOT
// block the inbound dispatch loop") but routes through Outbound.Render
// so adapters that satisfy bridge.RichRenderer produce platform-native
// UI; non-rich adapters fall back to the supplied text via Outbound.Text.
func (d *sessionDispatch) emitToolRender(hint *bridge.RenderHint, fallbackText string) {
	ctx := d.svc.ctx
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Warn("bridge: emitToolRender panic", "session", d.sessionID, "panic", r)
			}
		}()
		_, err := d.svc.SendBySessionID(ctx, d.sessionID, bridge.Outbound{
			Text:   fallbackText,
			Render: hint,
		})
		if err != nil {
			logging.Warn("bridge: tool-render fan-out failed", "session", d.sessionID, "err", err)
		}
	}()
}

// recordToolCallStart stamps the wall-clock start time for a tool
// call so the matching ToolResult event can report DurationMs. Called
// at the ToolCall (Finished=true, Input!="") emit point.
func (d *sessionDispatch) recordToolCallStart(toolCallID string) {
	if toolCallID == "" {
		return
	}
	d.toolCallStart.Store(toolCallID, time.Now().UnixMilli())
}

// consumeToolCallDuration returns the elapsed milliseconds since the
// matching ToolCall start was recorded, then deletes the entry. Returns
// 0 if no start was recorded (rare — usually means the call was
// emitted under !ToolUpdatesEnabled so the start path was skipped).
func (d *sessionDispatch) consumeToolCallDuration(toolCallID string) int64 {
	if toolCallID == "" {
		return 0
	}
	v, ok := d.toolCallStart.LoadAndDelete(toolCallID)
	if !ok {
		return 0
	}
	start, ok := v.(int64)
	if !ok {
		return 0
	}
	return time.Now().UnixMilli() - start
}

// translateAttachments converts the bridge-domain Attachment slice into
// the message-domain Attachment slice agent.Run expects. The two types
// are field-compatible; the indirection exists solely because
// internal/bridge cannot import internal/message (transitive cycle through
// internal/config — see bridge.go's package docstring).
func translateAttachments(in []bridge.Attachment) []message.Attachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]message.Attachment, len(in))
	for i, a := range in {
		out[i] = message.Attachment{
			FilePath: a.FilePath,
			FileName: a.FileName,
			MimeType: a.MimeType,
			Content:  a.Content,
		}
	}
	return out
}

// close marks the dispatcher as stopped, drains queued messages for
// shutdown-loss logging, and closes d.inbound. Caller MUST hold
// s.dispatchMu.
//
// Draining AND the close itself are protected by d.mu so they serialize
// against concurrent drainOverflowToInbound / pushInbound calls: pushInbound
// re-checks d.stop under the same mutex, so no send can land on the closed
// channel (a send on a closed channel panics, taking down the dispatcher
// goroutine). Items that run() has already received (one possible item after
// stop is set) are not logged — that is an accepted race at shutdown per
// Decision 5.
func (d *sessionDispatch) close() {
	if !d.stop.CompareAndSwap(false, true) {
		return
	}
	// Collect any queued inbound messages for WARN logging before closing.
	d.mu.Lock()
	lost := make([]bridge.Inbound, 0, len(d.overflow)+len(d.inbound))
	lost = append(lost, d.overflow...)
	d.overflow = nil
drainLoop:
	for {
		select {
		case item := <-d.inbound:
			lost = append(lost, item)
		default:
			break drainLoop
		}
	}
	close(d.inbound)
	d.mu.Unlock()

	for _, item := range lost {
		logging.Warn("bridge: shutdown lost queued inbound",
			"session", d.sessionID,
			"peer", item.Peer.PeerID)
	}
	if n := len(lost); n > 0 {
		logging.Warn("bridge: shutdown dropped queued messages",
			"session", d.sessionID,
			"count", n)
	}
}

// pushInbound enqueues an inbound message. The push is NON-BLOCKING:
// if d.inbound is full, the message is appended to the per-session
// overflow slice instead of blocking the caller. This prevents a single
// session from stalling the shared runInboundLoop (cross-session
// non-starvation fix).
//
// Both the channel send and the overflow append are done under d.mu to
// serialize with drainOverflowToInbound calls in run(), preserving
// per-session FIFO order (overflow items are served before new arrivals).
// The same mutex makes the d.stop re-check safe: close() sets stop and closes
// d.inbound under d.mu, so a push that observes !stop can never send on a
// closed channel.
func (d *sessionDispatch) pushInbound(in bridge.Inbound) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop.Load() {
		// Dispatcher already torn down (unbind or shutdown). Dropping is
		// audible rather than silent, and never a panic.
		logging.Warn("bridge: dropped inbound for stopped dispatcher",
			"session", d.sessionID, "peer", in.Peer.PeerID)
		return
	}
	select {
	case d.inbound <- in:
	default:
		d.overflow = append(d.overflow, in)
	}
}
