package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/flowrun"
	"github.com/opencode-ai/opencode/internal/question"
)

// flowQuestionBindings resolves a verified child run association without
// rebinding the user's conversation to a transient step session.
func (r *QuestionRouter) flowQuestionBindings(ctx context.Context, req question.Request) ([]store.Binding, string, bool, error) {
	c := r.svc.flowController()
	if c == nil || r.svc.app == nil || r.svc.app.FlowRuns == nil {
		return nil, "", false, nil
	}
	live := false
	if r.svc.app.Questions != nil {
		for _, pending := range r.svc.app.Questions.List() {
			if pending.ID == req.ID && pending.SessionID == req.SessionID {
				live = true
				break
			}
		}
	}
	if !live {
		return nil, "", true, nil
	}
	conversation, owned, err := c.SessionFlowConversation(ctx, req.SessionID)
	if err != nil {
		return nil, "", false, err
	}
	if !owned {
		candidate, e := c.CurrentFlow(ctx, req.SessionID)
		if e != nil || req.FlowRunID == "" || req.FlowRunID != candidate.ID || candidate.ConversationID != req.SessionID || flowIsTerminal(candidate.Status) {
			return nil, "", false, nil
		}
		conversation = req.SessionID
	}
	run, err := c.CurrentFlow(ctx, conversation)
	if err != nil {
		return nil, "", false, err
	}
	lease, ok := c.FlowLease(run.ID)
	if !ok {
		return nil, "", false, flowrun.ErrLeaseLost
	}
	descriptor, err := json.Marshal(question.DurableDescriptor{Questions: req.Questions, ToolCallID: req.ToolCallID})
	if err != nil {
		return nil, "", false, err
	}
	records, err := r.svc.app.FlowRuns.Questions(ctx, run.ID)
	if err != nil {
		return nil, "", false, err
	}
	logicalID := uuid.NewString()
	if req.ToolCallID != "" {
		sum := sha256.Sum256([]byte(run.ID + "\x00" + req.SessionID + "\x00" + req.ToolCallID))
		logicalID = hex.EncodeToString(sum[:])
	}
	record := flowrun.Question{ID: logicalID, RunID: run.ID, SessionID: req.SessionID, RequestID: req.ID, Descriptor: descriptor, Status: "pending"}
	for _, prior := range records {
		if prior.SessionID == req.SessionID && bytes.Equal(prior.Descriptor, descriptor) {
			record = prior
			record.RequestID = req.ID
			break
		}
	}
	record, err = r.svc.app.FlowRuns.PutQuestion(ctx, lease, record)
	if err != nil {
		return nil, "", false, err
	}
	if record.Status == "answered" || record.Status == "delivered" || record.Status == "interrupted" {
		var answer question.DurableAnswer
		if err = json.Unmarshal(record.Answer, &answer); err != nil {
			return nil, "", false, err
		}
		if answer.Unanswered != "" {
			err = r.svc.app.Questions.Unanswered(req.ID, answer.Unanswered)
		} else if answer.Rejected {
			err = r.svc.app.Questions.Reject(req.ID)
		} else {
			err = r.svc.app.Questions.Reply(req.ID, answer.Answers)
		}
		if err != nil {
			return nil, "", false, err
		}
		return nil, record.ID, true, nil
	}
	if r.deferFlowQuestion(conversation, req) {
		return nil, record.ID, true, nil
	}
	bindings, err := r.svc.store.ListBindingsBySession(ctx, r.svc.projectID, conversation)
	return bindings, record.ID, false, err
}

func (r *QuestionRouter) replyFlowQuestion(ctx context.Context, requestID string, answers [][]string) error {
	if r.svc.app.FlowRuns != nil {
		record, err := r.svc.app.FlowRuns.FindQuestionByRequest(ctx, requestID)
		if err == nil {
			payload, err := json.Marshal(question.DurableAnswer{Answers: answers})
			if err != nil {
				return err
			}
			sum := sha256.Sum256(payload)
			if _, _, err = r.svc.app.FlowRuns.AnswerQuestion(ctx, record.RunID, record.ID, hex.EncodeToString(sum[:]), payload); err != nil {
				return err
			}
		} else if !errors.Is(err, flowrun.ErrNotFound) {
			return err
		}
	}
	err := r.svc.app.Questions.Reply(requestID, answers)
	if err == nil {
		go r.advanceFlowQuestions(context.WithoutCancel(ctx))
	}
	return err
}

// Dynamic questions retain exact requesting-session identity. Legacy platform
// buttons contain labels rather than IDs, so route only an unambiguous matching
// choice (or explicit button) to a child; ordinary prose remains steering.
func (s *Service) tryFlowQuestionReply(ctx context.Context, conversation string, in bridge.Inbound) bool {
	if s.questionRouter == nil || s.app == nil || s.app.Questions == nil {
		return false
	}
	c := s.flowController()
	if c == nil {
		return false
	}
	// A submitting ordinary turn may still ask a legacy root question after
	// recording its plan request. It must finish before controller planning starts.
	for _, req := range s.app.Questions.List() {
		if req.SessionID == conversation && req.FlowRunID == "" {
			return false
		}
	}
	// Managed native buttons/modal submissions must carry exact identity and
	// are consumed earlier by routeRequestQuestionReply. Never reinterpret an
	// old label-only widget as a choice in the current question.
	if in.Source == bridge.InboundSourceButton || in.Source == bridge.InboundSourceModal {
		return true
	}
	candidates := []question.Request{}
	for _, req := range s.app.Questions.List() {
		conv, owned, err := c.SessionFlowConversation(ctx, req.SessionID)
		if !owned && req.SessionID == conversation && req.FlowRunID != "" {
			if current, e := c.CurrentFlow(ctx, conversation); e == nil && current.ID == req.FlowRunID {
				conv, owned = conversation, true
			}
		}
		if err != nil || !owned || conv != conversation || !s.questionRouter.flowQuestionVisible(conversation, req.ID) {
			continue
		}
		matches := false
		for _, prompt := range req.Questions {
			tokens := questionAnswerTokens(in.Text, prompt)
			known := len(tokens) > 0
			for _, token := range tokens {
				found := false
				for i, option := range prompt.Options {
					if token == option.Label || strings.TrimSpace(token) == strconv.Itoa(i+1) {
						found = true
						break
					}
				}
				if !found {
					known = false
					break
				}
			}
			if known {
				matches = true
			}
		}
		if matches {
			candidates = append(candidates, req)
		}
	}
	if len(candidates) != 1 {
		return false
	}
	if !s.questionRouter.TryHandleQuestionReply(ctx, candidates[0].SessionID, in) {
		s.replyToPeer(ctx, in.Peer, "That question is no longer waiting for an answer; queued input may already be in progress.", true, conversation)
	}
	return true
}

// durableFlowReply accepts stable logical question IDs and prior ephemeral IDs.
// A late reply is committed even while no in-process waiter exists, and the
// controller can deliver it when the owning step is restored.
func (s *Service) durableFlowReply(ctx context.Context, requestID string, in questionReplyInput) (handled, replayed bool, refusal string) {
	if s.app == nil || s.app.FlowRuns == nil {
		return false, false, ""
	}
	record, err := s.app.FlowRuns.FindQuestionByRequest(ctx, requestID)
	if errors.Is(err, flowrun.ErrNotFound) {
		return false, false, ""
	}
	if err != nil {
		return true, false, "question_store_unavailable"
	}
	if record.SessionID != in.SessionID {
		return true, false, "session_mismatch"
	}
	c := s.flowController()
	if c == nil {
		return true, false, "question_router_unavailable"
	}
	conv, owned, err := c.SessionFlowConversation(ctx, record.SessionID)
	savedRun, runErr := s.app.FlowRuns.Get(ctx, record.RunID)
	if !owned && runErr == nil && record.SessionID == savedRun.ConversationID {
		conv, owned = record.SessionID, true
	}
	if err != nil || runErr != nil || !owned || conv != savedRun.ConversationID {
		return true, false, "session_mismatch"
	}
	if in.Peer != nil {
		if in.ConversationID != conv || s.ValidateSessionFlowOrigin(ctx, conv, *in.Peer, in.JobID) != nil {
			return true, false, "session_mismatch"
		}
	} else if in.ConversationID != "" && in.ConversationID != conv {
		return true, false, "session_mismatch"
	}
	bindings, err := s.store.ListBindingsBySession(ctx, s.projectID, conv)
	if err != nil || len(bindings) == 0 {
		return true, false, "session_mismatch"
	}
	for _, binding := range bindings {
		if binding.Channel == "external" && in.Peer == nil {
			return true, false, "session_mismatch"
		}
	}
	var descriptor question.DurableDescriptor
	if json.Unmarshal(record.Descriptor, &descriptor) != nil {
		return true, false, "question_store_unavailable"
	}
	prompts := descriptor.Questions
	if (in.Text == nil) == (in.Answers == nil) {
		return true, false, "answer_shape"
	}
	answers := in.Answers
	if in.Text != nil {
		if len(prompts) != 1 {
			return true, false, "answer_shape"
		}
		answers = parseQuestionAnswers(*in.Text, prompts)
	}
	answers, refusal = normalizeQuestionReply(answers, prompts)
	if refusal != "" {
		return true, false, refusal
	}
	payload, _ := json.Marshal(question.DurableAnswer{Answers: answers})
	sum := sha256.Sum256(payload)
	_, fresh, err := s.app.FlowRuns.AnswerQuestion(ctx, record.RunID, record.ID, hex.EncodeToString(sum[:]), payload)
	if errors.Is(err, flowrun.ErrInputPending) {
		return true, false, "queued_input_pending"
	}
	if err != nil {
		return true, false, "reply_conflict"
	}
	if s.app.Questions != nil {
		_ = s.app.Questions.Reply(record.RequestID, answers)
	}
	if s.questionRouter != nil {
		s.questionRouter.mu.Lock()
		if pending := s.questionRouter.pending[record.SessionID]; pending != nil && pending.requestID == record.RequestID {
			delete(s.questionRouter.pending, record.SessionID)
		}
		s.questionRouter.mu.Unlock()
	}
	if s.questionRouter != nil {
		go s.questionRouter.advanceFlowQuestions(context.WithoutCancel(ctx))
	}
	return true, !fresh, ""
}

func (r *QuestionRouter) deferFlowQuestion(conversation string, req question.Request) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.flowVisible == nil {
		r.flowVisible = map[string]string{}
		r.flowQueued = map[string][]question.Request{}
	}
	if visible := r.flowVisible[conversation]; visible == "" || visible == req.ID {
		r.flowVisible[conversation] = req.ID
		return false
	}
	for _, pending := range r.flowQueued[conversation] {
		if pending.ID == req.ID {
			return true
		}
	}
	r.flowQueued[conversation] = append(r.flowQueued[conversation], req)
	return true
}
func (r *QuestionRouter) advanceFlowQuestions(ctx context.Context) {
	if r.svc.app == nil || r.svc.app.Questions == nil {
		return
	}
	// Only retire entries observed before fetching service state. A new Ask
	// registered between the snapshot and lock must not be mistaken for canceled.
	r.mu.Lock()
	observed := map[string]string{}
	eligible := map[string]bool{}
	for conversation, id := range r.flowVisible {
		observed[conversation] = id
		eligible[id] = true
		for _, req := range r.flowQueued[conversation] {
			eligible[req.ID] = true
		}
	}
	r.mu.Unlock()
	live := map[string]bool{}
	for _, req := range r.svc.app.Questions.List() {
		live[req.ID] = true
	}
	ready := []question.Request{}
	r.mu.Lock()
	for conversation, id := range observed {
		if r.flowVisible[conversation] != id || live[id] {
			continue
		}
		delete(r.flowVisible, conversation)
		for session, pending := range r.pending {
			if pending.requestID == id {
				delete(r.pending, session)
			}
		}
		for len(r.flowQueued[conversation]) > 0 {
			req := r.flowQueued[conversation][0]
			if !eligible[req.ID] {
				// Keep a marker so the next sweep can consider this newly queued request.
				r.flowVisible[conversation] = id
				break
			}
			r.flowQueued[conversation] = r.flowQueued[conversation][1:]
			if live[req.ID] {
				r.flowVisible[conversation] = req.ID
				ready = append(ready, req)
				break
			}
		}
	}
	r.mu.Unlock()
	for _, req := range ready {
		r.handleNewRequest(ctx, req)
	}
}
func (r *QuestionRouter) flowQuestionVisible(conversation, requestID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flowVisible[conversation] == requestID
}
