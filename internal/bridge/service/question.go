package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
)

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
		svc:     s,
		pending: make(map[string]*pendingQuestion),
	}
	if s.app != nil && s.app.Questions != nil {
		s.launchSupervised("question-router", r.run)
	}
	return r
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

	r.mu.Lock()
	r.pending[req.SessionID] = &pendingQuestion{
		requestID: req.ID,
		prompts:   req.Questions,
	}
	r.mu.Unlock()

	interactiveOK := r.shouldUseInteractive(req.Questions)
	text := renderQuestionPrompt(req.Questions)

	for _, b := range bindings {
		peer := b.AsPeerRef()
		if interactiveOK {
			if err := r.tryInteractiveSend(ctx, peer, req.Questions[0]); err == nil {
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
// platform-native UI. Returns an error if the adapter doesn't satisfy
// the InteractiveQuestionSender contract or if the platform call fails
// — the caller treats that as a fallback signal.
func (r *QuestionRouter) tryInteractiveSend(ctx context.Context, peer bridge.PeerRef, prompt question.Prompt) error {
	adapter := r.svc.Adapter(peer.Channel, peer.Identity)
	if adapter == nil {
		return errors.New("no adapter")
	}
	sender, ok := adapter.(bridge.InteractiveQuestionSender)
	if !ok {
		return errors.New("adapter does not support interactive UI")
	}
	choices := make([]bridge.QuestionChoice, 0, len(prompt.Options))
	for _, opt := range prompt.Options {
		choices = append(choices, bridge.QuestionChoice{
			Label: opt.Label,
			// Value MUST be the canonical answer label so the
			// inbound-reply parser (parseQuestionAnswers) maps it
			// back to a choice without any extra decoding.
			Value: opt.Label,
		})
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
	if !ok {
		r.mu.Unlock()
		return false
	}
	delete(r.pending, sessionID)
	r.mu.Unlock()

	if r.svc.app == nil || r.svc.app.Questions == nil {
		return false
	}
	answers := parseQuestionAnswers(in.Text, pending.prompts)
	if err := r.svc.app.Questions.Reply(pending.requestID, answers); err != nil {
		logging.Warn("bridge: question reply",
			"session", sessionID, "reqID", pending.requestID, "err", err)
		return false
	}
	return true
}

// renderQuestionPrompt formats one or more question.Prompt entries as
// numbered-option chat text. Format mirrors the TS bridge's fallback
// rendering so the user experience is unchanged.
func renderQuestionPrompt(prompts []question.Prompt) string {
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
			buf.WriteString("\n(or type a custom answer)")
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
