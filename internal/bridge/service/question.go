package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
)

const (
	recentlyAnsweredTTL           = 30 * time.Second
	recentlyAnsweredSweepInterval = 10 * time.Second

	// questionNudgeDefaultInterval is the idle gap before the first "still
	// waiting" nudge, and the spacing between subsequent nudges, when the
	// operator hasn't overridden cfg.QuestionNudgeIntervalSeconds. Chosen so
	// a reviewer who stepped away is reminded a few times well inside a
	// typical job deadline, without pinging on every brief pause.
	questionNudgeDefaultInterval = 5 * time.Minute
	// questionNudgeDefaultMax caps nudges per pending question when the
	// operator hasn't overridden cfg.QuestionNudgeMax.
	questionNudgeDefaultMax = 3
	// questionNudgeSweepInterval is how often the nudger wakes to look for
	// questions that have gone quiet past the interval. Kept well below the
	// nudge interval so a due nudge fires promptly without busy-looping.
	questionNudgeSweepInterval = 30 * time.Second

	// interactiveInboundBufferCap bounds the per-session queue of reviewer
	// messages buffered while an interactive flow step has no question
	// pending (see BufferInbound). Drop-oldest once the cap is hit so a
	// reviewer firing many messages into the between-questions gap — or a
	// wedged step that never asks again — can't grow memory unbounded.
	// Tied to the dispatcher's inbound channel capacity so the two stay in
	// sync.
	interactiveInboundBufferCap = dispatchInboundCap
)

// answeredKey is the cache key for stale-click suppression. The triple
// (projectID, sessionID, label) lets a future multi-project Service stay
// collision-safe while keeping the lookup a single sync.Map hit.
type answeredKey struct {
	projectID string
	sessionID string
	label     string
}

// QuestionRouter watches the question.Service broker for new Request
// events, surfaces them to the chat surface for any session that has at
// least one bound peer, and routes reviewer replies back through
// question.Service.Reply.
//
// Per the chat-bridge spec ("Inbound dispatch via direct in-process
// calls"), the bridge MUST NOT use SSE event subscriptions or HTTP POST
// /question/{id}/reply for these flows — direct service-method calls
// only. This is the bridge's home for that contract.
type QuestionRouter struct {
	svc *Service

	mu             sync.Mutex
	pending        map[string]*pendingQuestion // sessionID → pending
	settledReplies map[string]settledQuestionReply
	sessionGuards  map[string]*questionSessionGuard

	// inbox holds reviewer messages that arrived for an interactive flow
	// session while no question was pending. dispatchInbound queues them
	// there (instead of dispatching to app.ActiveAgent(), which would
	// hijack the interactive step with the workspace default agent).
	// Each item's Meta is the original bridge.Inbound. In production it is
	// inbox.Default(), the box the agent loop drains; nil means a private
	// box created on first use (see box).
	inbox *inbox.Box

	// recentlyAnswered caches answered (projectID, sessionID, label) → time
	// for the stale-click suppression window. Populated on Reply, swept on
	// a 10s tick, evicting entries older than recentlyAnsweredTTL.
	// See spec: bridge-question-stale-click-suppression.
	recentlyAnswered sync.Map
}

// pendingQuestion is what the router remembers between Publish and
// reviewer-reply: the request ID (for question.Service.Reply), the prompt
// (for parsing numeric replies), and the bound peers (so the right
// inbound triggers the reply).
type pendingQuestion struct {
	requestID string
	prompts   []question.Prompt
	// askedAt is when the question was surfaced; lastNudge is when the
	// nudger last re-posted a "still waiting" reminder (zero until the
	// first one). nudges counts how many reminders have been sent so the
	// per-question cap is enforced. Guarded by QuestionRouter.mu.
	askedAt   time.Time
	lastNudge time.Time
	nudges    int
}

// NewQuestionRouter constructs a router and starts the subscriber
// goroutine. The router stays alive for the lifetime of Service.
func (s *Service) newQuestionRouter() *QuestionRouter {
	r := &QuestionRouter{
		svc:     s,
		pending: make(map[string]*pendingQuestion),
		inbox:   routerInbox(),
	}
	if s.app != nil && s.app.Questions != nil {
		s.launchSupervised("question-router", r.run)
		s.launchSupervised("question-stale-cache-sweeper", r.runSweeper)
		s.launchSupervised("question-nudger", r.runNudger)
	}
	return r
}

// routerInbox picks the inbox a new QuestionRouter queues into. It is a
// variable so the package's tests can give every router its own box
// instead of sharing the process-wide one across tests.
var routerInbox = inbox.Default

// box returns the router's inbox, creating a private one for a router
// built without one (tests construct QuestionRouter literals).
func (r *QuestionRouter) box() *inbox.Box {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.boxLocked()
}

// boxLocked is box for a caller that holds r.mu.
func (r *QuestionRouter) boxLocked() *inbox.Box {
	if r.inbox == nil {
		r.inbox = inbox.New(interactiveInboundBufferCap)
	}
	return r.inbox
}

// runSweeper evicts recentlyAnswered entries older than the TTL. Stops
// when the service context cancels.
func (r *QuestionRouter) runSweeper(ctx context.Context) {
	ticker := time.NewTicker(recentlyAnsweredSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.sweepRecentlyAnswered(now, recentlyAnsweredTTL)
			r.mu.Lock()
			r.sweepReplyReceiptsLocked(now)
			r.mu.Unlock()
		}
	}
}

// sweepRecentlyAnswered deletes entries older than ttl as of now. Split
// from runSweeper so tests can drive eviction deterministically.
func (r *QuestionRouter) sweepRecentlyAnswered(now time.Time, ttl time.Duration) {
	cutoff := now.Add(-ttl)
	r.recentlyAnswered.Range(func(k, v any) bool {
		if ts, ok := v.(time.Time); ok && ts.Before(cutoff) {
			r.recentlyAnswered.Delete(k)
		}
		return true
	})
}

// rememberAnswers stores every label from a successful Reply into the
// stale-click cache so subsequent inbounds matching the same label get
// swallowed for the TTL window.
func (r *QuestionRouter) rememberAnswers(sessionID string, answers [][]string) {
	now := time.Now()
	for _, row := range answers {
		for _, label := range row {
			label = strings.TrimSpace(label)
			if label == "" {
				continue
			}
			r.recentlyAnswered.Store(answeredKey{
				projectID: r.svc.projectID,
				sessionID: sessionID,
				label:     label,
			}, now)
		}
	}
}

// wasRecentlyAnswered reports whether the inbound text matches a cached
// answer for the session within the TTL. Returns the cached entry's age
// for log context when true.
func (r *QuestionRouter) wasRecentlyAnswered(sessionID, text string) (time.Duration, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false
	}
	v, ok := r.recentlyAnswered.Load(answeredKey{
		projectID: r.svc.projectID,
		sessionID: sessionID,
		label:     text,
	})
	if !ok {
		return 0, false
	}
	ts, _ := v.(time.Time)
	age := time.Since(ts)
	if age > recentlyAnsweredTTL {
		return age, false
	}
	return age, true
}

// run subscribes to question.Service.Subscribe and forwards each new
// Request to the bound peers for the request's session. Replies arrive
// via inbound message dispatch (TryHandleQuestionReply, called from the
// inbound dispatch path).
func (r *QuestionRouter) run(ctx context.Context) {
	if r.svc.app == nil || r.svc.app.Questions == nil {
		return
	}
	sub := r.svc.app.Questions.Subscribe(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return
			}
			if ev.Type != pubsub.CreatedEvent {
				continue
			}
			r.handleNewRequest(ctx, ev.Payload)
		}
	}
}

// handleNewRequest surfaces a fresh question.Request to every peer
// bound to its session. When `cfg.Router.QuestionMode == "interactive"`
// the router tries platform-native UI first (Slack blocks / Telegram
// inline keyboard) and falls back to numbered text per-peer if the
// adapter doesn't satisfy InteractiveQuestionSender or returns an error.
func (r *QuestionRouter) handleNewRequest(ctx context.Context, req question.Request) {
	bindings, err := r.svc.store.ListBindingsBySession(ctx, r.svc.projectID, req.SessionID)
	if err != nil {
		logging.Warn("bridge: question fan-out store lookup",
			"session", req.SessionID, "err", err)
		return
	}
	if len(bindings) == 0 {
		return
	}

	pend := &pendingQuestion{
		requestID: req.ID,
		prompts:   req.Questions,
		askedAt:   time.Now(),
	}
	r.mu.Lock()
	// A request-bound HTTP reply can settle the service before this Created
	// event is processed. Do not resurrect a settled question during fan-out.
	if r.svc.app != nil && r.svc.app.Questions != nil {
		live := false
		for _, current := range r.svc.app.Questions.List() {
			if current.ID == req.ID && current.SessionID == req.SessionID {
				live = true
				break
			}
		}
		if !live {
			r.mu.Unlock()
			return
		}
	}
	r.pending[req.SessionID] = pend
	if r.sessionGuards == nil {
		r.sessionGuards = make(map[string]*questionSessionGuard)
	}
	guard := r.sessionGuards[req.SessionID]
	if guard == nil {
		guard = &questionSessionGuard{active: true}
		r.sessionGuards[req.SessionID] = guard
	}
	// Queued input (messages with an id) never answers a question: the
	// question closes as unanswered without reaching the peers, and the
	// agent reads the queue right after its result (cd-5480 D3). Holding
	// the question open instead would deadlock a UI that keeps its answer
	// controls disabled until its queue has drained.
	if r.boxLocked().Any(req.SessionID, func(it inbox.Item) bool { return it.ID != "" }) &&
		r.svc.app != nil && r.svc.app.Questions != nil {
		delete(r.pending, req.SessionID)
		err := r.svc.app.Questions.Unanswered(pend.requestID, unansweredQuestionNote)
		if err == nil {
			r.mu.Unlock()
			logging.Info("bridge: question closed as unanswered — queued input is waiting",
				"session", req.SessionID, "reqID", pend.requestID)
			return
		}
		logging.Warn("bridge: closing question as unanswered failed — falling back to fan-out",
			"session", req.SessionID, "reqID", pend.requestID, "err", err)
		r.pending[req.SessionID] = pend
	}
	// Answer from the head of the plain reviewer messages buffered while no
	// question was pending (dispatchInbound → BufferInbound for interactive
	// sessions); the rest stay queued for the agent loop to drain after the
	// answer. Claim the just-registered pending entry in the SAME critical
	// section so a racing live reply cannot double-consume the question.
	bufferedItem, hasBuffered := r.boxLocked().PopFirst(req.SessionID)
	buffered, _ := bufferedItem.Meta.(bridge.Inbound)
	if hasBuffered {
		delete(r.pending, req.SessionID)
	}
	r.mu.Unlock()

	// Auto-answer THIS question from the buffered message, keeping the
	// reply inside the flow agent's turn instead of the workspace default
	// agent. On failure, preserve the input and restore pending only if that
	// exact request is still live; a competing bound reply must not resurrect it.
	if hasBuffered {
		answers := parseQuestionAnswers(buffered.Text, pend.prompts)
		if err := r.svc.app.Questions.Reply(pend.requestID, answers); err != nil {
			logging.Warn("bridge: auto-answer from buffered inbound failed — preserving input if the step still owns it",
				"session", req.SessionID, "reqID", pend.requestID, "err", err)
			r.mu.Lock()
			if !guard.active {
				r.mu.Unlock()
				return // ClearSession ended this step's ownership
			}
			live := false
			for _, current := range r.svc.app.Questions.List() {
				if current.ID == req.ID && current.SessionID == req.SessionID {
					live = true
					break
				}
			}
			evicted := r.boxLocked().PushFront(req.SessionID, bufferedItem)
			current := r.pending[req.SessionID]
			canRestore := live && (current == nil || current.requestID == req.ID)
			if canRestore {
				r.pending[req.SessionID] = pend
			}
			r.mu.Unlock()
			r.notifyEvicted(ctx, req.SessionID, evicted)
			if !canRestore {
				return // another reply settled it: no stale widget or nudge
			}
		} else {
			r.rememberAnswers(req.SessionID, answers)
			logging.Info("bridge: interactive question auto-answered from buffered reviewer message",
				"session", req.SessionID, "reqID", pend.requestID)
			return
		}
	}

	// The text the agent wrote before calling question must reach chat
	// before the widget does.
	r.svc.flushIntermediateText(ctx, req.SessionID)

	interactiveOK := r.shouldUseInteractive(req.Questions)
	text := renderQuestionPrompt(req.Questions)

	// Stamp sessionId (every send) and, when an interactive question is
	// about to be attempted, the requestId + multiple-choice flag. Both
	// are no-ops for Slack/Mattermost/Telegram — only the "external"
	// channel adapter reads them (see internal/bridge/context.go) since
	// its relay wire frame needs sessionId on every send and requestId
	// on question sends specifically.
	sendCtx := bridge.ContextWithSessionID(ctx, req.SessionID)
	questionCtx := sendCtx
	if interactiveOK {
		questionCtx = bridge.ContextWithExternalQuestion(sendCtx, bridge.ExternalQuestionContext{
			RequestID: req.ID,
			Multiple:  req.Questions[0].Multiple,
		})
	}

	for _, b := range bindings {
		peer := b.AsPeerRef()
		if interactiveOK {
			resolved, err := r.tryInteractiveSend(questionCtx, peer, req.Questions[0])
			if err == nil {
				// Mirror service.Send's binding mutation: when posting
				// the question opens a new thread (Slack's "<channel>|
				// <thread_ts>" form), rewrite the binding row's peer_id
				// so the reviewer's reply — which arrives keyed by the
				// composite — matches THIS session instead of falling
				// through resolveBinding's ErrNotFound branch.
				if resolved != "" && resolved != b.PeerID {
					if uerr := r.svc.store.UpdateBindingPeerID(
						ctx, r.svc.projectID, b.Channel, b.IdentityID,
						b.PeerID, resolved,
					); uerr != nil {
						logging.Warn("bridge: question binding peer_id mutation failed",
							"session", b.SessionID, "old", b.PeerID, "new", resolved, "err", uerr)
					}
				}
				continue
			} else {
				logging.Info("bridge: question interactive UI failed, falling back to text",
					"peer", peer, "err", err)
			}
		}
		_, _ = r.svc.Send(sendCtx, peer, text, "", nil)
	}
}

// shouldUseInteractive reports whether the router should attempt
// platform-native rendering for the given prompts. Three conditions:
//
//  1. cfg.Router.QuestionMode == "interactive"
//  2. exactly one prompt in the request (multi-prompt requests don't
//     fit the block-actions widget shape)
//  3. the prompt has at least one option to render as a button
func (r *QuestionRouter) shouldUseInteractive(prompts []question.Prompt) bool {
	if r.svc == nil || r.svc.cfg == nil {
		return false
	}
	if r.svc.cfg.QuestionMode != "interactive" {
		return false
	}
	if len(prompts) != 1 {
		return false
	}
	return len(prompts[0].Options) > 0
}

// tryInteractiveSend asks the adapter to render the prompt with
// platform-native UI. Returns the resolved peer-id (e.g. Slack's
// "<channel>|<thread_ts>" when the post opens a new thread) so the
// caller can mutate the binding row, plus an error if the adapter
// doesn't satisfy InteractiveQuestionSender or if the platform call
// fails — the caller treats that as a fallback signal.
//
// When prompt.Multiple is true AND the adapter satisfies
// InteractiveMultiSelectSender AND len(options) >= 2, the multi-select
// path runs instead (single-submit widget that the adapter parses into
// a comma-separated reply matching parseQuestionAnswers' format).
func (r *QuestionRouter) tryInteractiveSend(ctx context.Context, peer bridge.PeerRef, prompt question.Prompt) (string, error) {
	adapter := r.svc.Adapter(peer.Channel, peer.Identity)
	if adapter == nil {
		return "", errors.New("no adapter")
	}
	custom := prompt.IsCustomEnabled()
	choices := make([]bridge.QuestionChoice, 0, len(prompt.Options))
	for _, opt := range prompt.Options {
		choices = append(choices, bridge.QuestionChoice{
			Label: opt.Label,
			// Value MUST be the canonical answer label so the
			// inbound-reply parser (parseQuestionAnswers) maps it
			// back to a choice without any extra decoding.
			Value:  opt.Label,
			Custom: custom,
		})
	}
	// Multi-select path: gated on prompt.Multiple + >= 2 options + adapter
	// support. Single-option multi-select degrades to single-select per
	// the spec (it's just a yes/no in disguise).
	if prompt.Multiple && len(prompt.Options) >= 2 {
		if multi, ok := adapter.(bridge.InteractiveMultiSelectSender); ok {
			return multi.SendInteractiveMultiSelect(ctx, peer, prompt.Question, choices)
		}
		// Adapter doesn't support multi-select widget — fall through to
		// single-select; parseQuestionAnswers still parses comma-separated
		// typed replies for Multiple prompts.
	}
	sender, ok := adapter.(bridge.InteractiveQuestionSender)
	if !ok {
		return "", errors.New("adapter does not support interactive UI")
	}
	return sender.SendInteractiveQuestion(ctx, peer, prompt.Question, choices)
}

// TryHandleQuestionReply checks whether the inbound text is a response
// to a pending question for the resolved session. Returns true when the
// reply was consumed (i.e. the inbound should NOT be passed to the
// dispatcher as a regular prompt). Numeric replies map to choice indexes;
// other text is treated as a custom answer if the question allows it.
func (r *QuestionRouter) TryHandleQuestionReply(ctx context.Context, sessionID string, in bridge.Inbound) bool {
	r.mu.Lock()
	pending, ok := r.pending[sessionID]
	if ok {
		delete(r.pending, sessionID)
	}
	r.mu.Unlock()

	if !ok {
		// No pending question — check the stale-click cache. A reviewer
		// who clicks the same button twice (or whose adapter delivers a
		// buffered callback after Reply consumed the question) lands here.
		if age, hit := r.wasRecentlyAnswered(sessionID, in.Text); hit {
			logging.Info("bridge: stale answer suppressed",
				"session", sessionID, "label", strings.TrimSpace(in.Text), "age", age)
			return true
		}
		return false
	}

	if r.svc.app == nil || r.svc.app.Questions == nil {
		return false
	}
	answers := parseQuestionAnswers(in.Text, pending.prompts)
	if err := r.svc.app.Questions.Reply(pending.requestID, answers); err != nil {
		logging.Warn("bridge: question reply",
			"session", sessionID, "reqID", pending.requestID, "err", err)
		return false
	}
	r.rememberAnswers(sessionID, answers)
	r.maybeAckAnswer(ctx, in, answers, sessionID)
	return true
}

// maybeAckAnswer sends a short confirmation for a typed/custom answer that
// got NO transport-side feedback. A button click already self-renders a
// "✓ Answered" widget (InboundSourceButton, or an unstamped inbound from an
// older orchestrator — treated conservatively as already-acked), so it is
// skipped; a free-text @mention / DM / modal answer otherwise leaves the
// reviewer with no sign the agent received it. Sent to the answering peer
// via the pod's own outbound adapter, so it works in both daemon and
// orchestrator-mediated deployments. Best-effort — a delivery failure never
// blocks the (already-committed) Reply.
func (r *QuestionRouter) maybeAckAnswer(ctx context.Context, in bridge.Inbound, answers [][]string, sessionID string) {
	if in.AnswerWasAcknowledgedByTransport() {
		return
	}
	if ack := formatAnswerAck(answers); ack != "" {
		r.svc.replyToPeer(ctx, in.Peer, ack, true, sessionID)
	}
}

// formatAnswerAck renders the short confirmation sent back to a reviewer
// who typed/mentioned a custom answer. Lists the recorded answer(s) so it's
// unambiguous what the agent captured; returns "" when there's nothing to
// echo (defensive — the caller then skips the send).
func formatAnswerAck(answers [][]string) string {
	var picks []string
	for _, row := range answers {
		for _, a := range row {
			if a = strings.TrimSpace(a); a != "" {
				picks = append(picks, a)
			}
		}
	}
	if len(picks) == 0 {
		return ""
	}
	return "👍 Got it — recorded your answer: " + strings.Join(picks, ", ") + ". Working on it…"
}

// BufferInbound queues a reviewer message that arrived for an
// interactive flow session while no question was pending. The agent loop
// drains the queue into the session as user messages at its next tool or
// turn boundary; a question asked first is auto-answered from the head of
// the queue (handleNewRequest).
//
// Without this, dispatchInbound would fall through to the per-session
// dispatcher and start a SEPARATE app.ActiveAgent() run — the workspace
// default agent — on the flow's session. That agent lacks the flow
// agent's manager tools (router_send) and the step's struct_output
// schema, so it can never complete the interactive step: the step hangs
// on running/NULL while the reviewer talks to the wrong agent. Buffering
// keeps every reply inside the flow agent's turn.
//
// FIFO with a drop-oldest cap so a reviewer firing many messages into
// the between-questions gap can't grow memory unbounded. When eviction
// occurs, the evicted peer is notified so their message is not silently
// lost.
func (r *QuestionRouter) BufferInbound(ctx context.Context, sessionID string, in bridge.Inbound) {
	item := inboxItem("", in, r.svc.sessionPeerCount(ctx, sessionID))
	var hook inbox.DeliveredFunc
	if r.svc != nil {
		hook = r.svc.onInboxDelivered
	}
	r.mu.Lock()
	evicted := r.boxLocked().Push(sessionID, hook, item)
	r.mu.Unlock()
	r.notifyEvicted(ctx, sessionID, evicted)
}

// popBufferedLocked removes and returns the oldest buffered inbound for
// the session. Caller MUST hold r.mu.
func (r *QuestionRouter) popBufferedLocked(sessionID string) (bridge.Inbound, bool) {
	it, ok := r.boxLocked().PopFirst(sessionID)
	if !ok {
		return bridge.Inbound{}, false
	}
	in, _ := it.Meta.(bridge.Inbound)
	return in, true
}

// ClearSession drops the pending question and any buffered inbounds for
// the session and returns the inbounds that never reached the agent.
// Called from Unbind at interactive-step completion so a later step (or a
// re-used session id) never inherits stale state.
func (r *QuestionRouter) ClearSession(sessionID string) []inbox.Item {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, sessionID)
	if guard := r.sessionGuards[sessionID]; guard != nil {
		guard.active = false
		delete(r.sessionGuards, sessionID)
	}
	return r.boxLocked().Clear(sessionID)
}

// runNudger periodically re-surfaces questions that have gone unanswered
// past the nudge interval, re-posting a short "still waiting" status to the
// session's bound peers. This turns a silently-lost answer (e.g. a bridge
// reply misrouted to another Socket Mode consumer, GENAI-151) into a
// visible, recoverable prompt instead of an interactive step that hangs to
// the job's hard deadline. Stops when the service context cancels.
func (r *QuestionRouter) runNudger(ctx context.Context) {
	ticker := time.NewTicker(questionNudgeSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.nudgeDue(ctx, now)
		}
	}
}

// nudgeConfig resolves the effective nudge settings from cfg, applying
// defaults for zero values. A negative interval disables nudging; a
// negative max means unlimited (bounded in practice by the job deadline).
func (r *QuestionRouter) nudgeConfig() (interval time.Duration, max int, enabled bool) {
	interval = questionNudgeDefaultInterval
	max = questionNudgeDefaultMax
	if r.svc != nil && r.svc.cfg != nil {
		switch s := r.svc.cfg.QuestionNudgeIntervalSeconds; {
		case s < 0:
			return 0, 0, false
		case s > 0:
			interval = time.Duration(s) * time.Second
		}
		if m := r.svc.cfg.QuestionNudgeMax; m != 0 {
			max = m
		}
	}
	return interval, max, true
}

// nudgeDue sends a reminder for every pending question idle past the
// interval and under the per-question cap. The pending map is scanned under
// the lock to collect the due-list — updating each entry's counters in the
// SAME critical section so a concurrent reply/sweep can't double-send — then
// the chat I/O happens after the lock is released.
func (r *QuestionRouter) nudgeDue(ctx context.Context, now time.Time) {
	interval, max, enabled := r.nudgeConfig()
	if !enabled {
		return
	}
	type dueNudge struct {
		sessionID string
		prompts   []question.Prompt
	}
	var todo []dueNudge
	r.mu.Lock()
	for sid, p := range r.pending {
		// Reference clock: the last nudge, or the ask time if none sent yet.
		last := p.lastNudge
		if last.IsZero() {
			last = p.askedAt
		}
		if now.Sub(last) < interval {
			continue
		}
		if max >= 0 && p.nudges >= max {
			continue
		}
		p.nudges++
		p.lastNudge = now
		todo = append(todo, dueNudge{sessionID: sid, prompts: p.prompts})
	}
	r.mu.Unlock()

	for _, d := range todo {
		text := formatNudge(d.prompts)
		if text == "" {
			continue
		}
		if _, err := r.svc.SendBySessionID(ctx, d.sessionID, bridge.Outbound{Text: text}); err != nil {
			logging.Warn("bridge: question nudge send failed",
				"session", d.sessionID, "err", err)
			continue
		}
		logging.Info("bridge: question nudge sent", "session", d.sessionID)
	}
}

// formatNudge renders the short "still waiting" status. Includes the first
// prompt's question text so the reviewer knows exactly what's outstanding;
// returns "" when there's nothing to echo (caller skips the send).
func formatNudge(prompts []question.Prompt) string {
	if len(prompts) == 0 {
		return ""
	}
	q := strings.TrimSpace(prompts[0].Question)
	if q == "" {
		return ""
	}
	return "⏳ Still waiting on your answer to continue:\n> " + q +
		"\nReply with a button/number above, or @mention me with your answer."
}

// renderQuestionPrompt formats one or more question.Prompt entries as
// numbered-option chat text. Format mirrors the TS bridge's fallback
// rendering so the user experience is unchanged.
//
// When at least one prompt has Custom enabled, the text ends with a
// trailing clause that surfaces the "type your own answer" affordance
// per bridge-question-custom-answer-hint (text-fallback path).
func renderQuestionPrompt(prompts []question.Prompt) string {
	anyCustom := false
	var buf strings.Builder
	for i, p := range prompts {
		if i > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(p.Question)
		for j, opt := range p.Options {
			buf.WriteString("\n")
			buf.WriteString(fmt.Sprintf("%d) %s", j+1, opt.Label))
			if opt.Description != "" {
				buf.WriteString(" — " + opt.Description)
			}
		}
		if p.IsCustomEnabled() {
			anyCustom = true
		}
	}
	// Trailing instruction clause. Adapt the wording to whether typed
	// custom answers are accepted on any of the prompts.
	if len(prompts) > 0 && len(prompts[0].Options) > 0 {
		buf.WriteString("\n\n")
		if anyCustom {
			buf.WriteString("Reply with the number of your choice — or type your own answer.")
		} else {
			buf.WriteString("Reply with the number of your choice.")
		}
	}
	return buf.String()
}

// parseQuestionAnswers converts a reviewer's reply text into the
// `[][]string` shape question.Service.Reply expects. One row per Prompt;
// the row's strings are the selected option labels (or the raw text if
// it matches no label or index and Custom is enabled).
//
// Multi-question replies in a single message are not supported — the
// reply applies to the first Prompt. Multi-answer prompts (Multiple=true)
// accept comma-separated labels or numeric indexes.
//
// A label match wins over an index: buttons send their option's label,
// so a label such as "3" must select itself, not the third option.
func parseQuestionAnswers(text string, prompts []question.Prompt) [][]string {
	out := make([][]string, len(prompts))
	if len(prompts) == 0 {
		return out
	}
	p := prompts[0]
	tokens := questionAnswerTokens(text, p)

	picks := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if label, ok := matchOptionLabel(p, tok); ok {
			picks = append(picks, label)
			continue
		}
		if idx, err := strconv.Atoi(tok); err == nil && idx >= 1 && idx <= len(p.Options) {
			picks = append(picks, p.Options[idx-1].Label)
			continue
		}
		if p.IsCustomEnabled() {
			picks = append(picks, tok)
		}
	}

	out[0] = picks
	for i := 1; i < len(prompts); i++ {
		// Unanswered subsequent prompts get an empty answer slice.
		out[i] = nil
	}
	return out
}

// questionAnswerTokens splits a reply into the tokens parseQuestionAnswers
// reads. A Multiple prompt's reply is comma-separated, unless the whole
// reply is one option's label: a selected label containing a comma stays whole.
func questionAnswerTokens(text string, p question.Prompt) []string {
	answer := strings.TrimSpace(text)
	if _, ok := matchOptionLabel(p, answer); ok || !p.Multiple {
		if answer == "" {
			return nil
		}
		return []string{answer}
	}
	return splitCSV(answer)
}

// matchOptionLabel returns the canonical label of the option whose label
// equals tok, ignoring case.
func matchOptionLabel(p question.Prompt, tok string) (string, bool) {
	for _, opt := range p.Options {
		if strings.EqualFold(opt.Label, tok) {
			return opt.Label, true
		}
	}
	return "", false
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
