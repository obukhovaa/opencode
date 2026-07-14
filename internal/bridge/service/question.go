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
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
)

const (
	recentlyAnsweredTTL           = 30 * time.Second
	recentlyAnsweredSweepInterval = 10 * time.Second

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

	mu      sync.Mutex
	pending map[string]*pendingQuestion // sessionID → pending

	// buffered holds reviewer messages that arrived for an interactive
	// flow session while no question was pending. dispatchInbound queues
	// them here (instead of dispatching to app.ActiveAgent(), which would
	// hijack the interactive step with the workspace default agent);
	// handleNewRequest drains the head into the next question the flow
	// agent asks. FIFO, guarded by mu, capped at
	// interactiveInboundBufferCap.
	buffered map[string][]bridge.Inbound // sessionID → queued inbounds

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
}

// NewQuestionRouter constructs a router and starts the subscriber
// goroutine. The router stays alive for the lifetime of Service.
func (s *Service) newQuestionRouter() *QuestionRouter {
	r := &QuestionRouter{
		svc:      s,
		pending:  make(map[string]*pendingQuestion),
		buffered: make(map[string][]bridge.Inbound),
	}
	if s.app != nil && s.app.Questions != nil {
		s.launchSupervised("question-router", r.run)
		s.launchSupervised("question-stale-cache-sweeper", r.runSweeper)
	}
	return r
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
	}
	r.mu.Lock()
	r.pending[req.SessionID] = pend
	// Drain any reviewer message buffered while no question was pending
	// (dispatchInbound → BufferInbound for interactive sessions). Claim
	// the just-registered pending entry in the SAME critical section so a
	// racing live reply cannot double-consume the question.
	buffered, hasBuffered := r.popBufferedLocked(req.SessionID)
	if hasBuffered {
		delete(r.pending, req.SessionID)
	}
	r.mu.Unlock()

	// Auto-answer THIS question from the buffered message, keeping the
	// reply inside the flow agent's turn instead of the workspace default
	// agent. On Reply failure, restore the pending entry and fall through
	// to the normal fan-out so the reviewer can still respond live.
	if hasBuffered {
		answers := parseQuestionAnswers(buffered.Text, pend.prompts)
		if err := r.svc.app.Questions.Reply(pend.requestID, answers); err != nil {
			logging.Warn("bridge: auto-answer from buffered inbound failed — falling back to fan-out",
				"session", req.SessionID, "reqID", pend.requestID, "err", err)
			r.mu.Lock()
			r.pending[req.SessionID] = pend
			r.mu.Unlock()
		} else {
			r.rememberAnswers(req.SessionID, answers)
			logging.Info("bridge: interactive question auto-answered from buffered reviewer message",
				"session", req.SessionID, "reqID", pend.requestID)
			return
		}
	}

	interactiveOK := r.shouldUseInteractive(req.Questions)
	text := renderQuestionPrompt(req.Questions)

	for _, b := range bindings {
		peer := b.AsPeerRef()
		if interactiveOK {
			resolved, err := r.tryInteractiveSend(ctx, peer, req.Questions[0])
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
		_, _ = r.svc.Send(ctx, peer, text, "", nil)
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
	return true
}

// BufferInbound queues a reviewer message that arrived for an
// interactive flow session while no question was pending. The next
// question the flow agent asks (handleNewRequest) is auto-answered from
// the head of this queue.
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
// the between-questions gap can't grow memory unbounded.
func (r *QuestionRouter) BufferInbound(sessionID string, in bridge.Inbound) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.buffered[sessionID]
	if len(q) >= interactiveInboundBufferCap {
		logging.Warn("bridge: interactive inbound buffer full — dropping oldest",
			"session", sessionID, "cap", interactiveInboundBufferCap)
		q = q[1:]
	}
	r.buffered[sessionID] = append(q, in)
}

// popBufferedLocked removes and returns the oldest buffered inbound for
// the session. Caller MUST hold r.mu.
func (r *QuestionRouter) popBufferedLocked(sessionID string) (bridge.Inbound, bool) {
	q := r.buffered[sessionID]
	if len(q) == 0 {
		return bridge.Inbound{}, false
	}
	in := q[0]
	if len(q) == 1 {
		delete(r.buffered, sessionID)
	} else {
		r.buffered[sessionID] = q[1:]
	}
	return in, true
}

// ClearSession drops the pending question and any buffered inbounds for
// the session. Called from Unbind at interactive-step completion so a
// later step (or a re-used session id) never inherits stale state.
func (r *QuestionRouter) ClearSession(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, sessionID)
	delete(r.buffered, sessionID)
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
// no numeric match and Custom is enabled).
//
// Multi-question replies in a single message are not supported — the
// reply applies to the first Prompt. Multi-answer prompts (Multiple=true)
// accept comma-separated numeric indexes.
func parseQuestionAnswers(text string, prompts []question.Prompt) [][]string {
	out := make([][]string, len(prompts))
	if len(prompts) == 0 {
		return out
	}
	p := prompts[0]
	answer := strings.TrimSpace(text)

	tokens := []string{answer}
	if p.Multiple {
		tokens = splitCSV(answer)
	}

	picks := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if idx, err := strconv.Atoi(tok); err == nil && idx >= 1 && idx <= len(p.Options) {
			picks = append(picks, p.Options[idx-1].Label)
			continue
		}
		// Try exact label match.
		matched := false
		for _, opt := range p.Options {
			if strings.EqualFold(opt.Label, tok) {
				picks = append(picks, opt.Label)
				matched = true
				break
			}
		}
		if matched {
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
