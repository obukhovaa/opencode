package service

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/question"
)

const (
	boundReplyReceiptTTL = 5 * time.Minute
	boundReplyReceiptCap = 256
)

// Text uses the existing bridge codec inside the runner, so a caller can retry
// even after the authoritative pending request is gone. Exactly one form is allowed.
type questionReplyInput struct {
	SessionID string     `json:"sessionId"`
	Answers   [][]string `json:"answers,omitempty"`
	Text      *string    `json:"text,omitempty"`
}

type settledQuestionReply struct {
	sessionID string
	digest    [32]byte
	settledAt time.Time
}

// Guards a failed buffered answer against restoring input after ClearSession.
type questionSessionGuard struct{ active bool }

// replyRequest returns whether this is a replay, or a machine-readable refusal.
// Spec: bridge-http-api, "Request-bound question replies preserve router state".
func (r *QuestionRouter) replyRequest(requestID string, in questionReplyInput) (bool, string) {
	if strings.TrimSpace(in.SessionID) == "" || (in.Text == nil) == (in.Answers == nil) {
		return false, "answer_shape"
	}
	if in.Text != nil && strings.TrimSpace(*in.Text) == "" {
		return false, "invalid_answer"
	}
	if in.Answers != nil {
		if len(in.Answers) == 0 {
			return false, "answer_shape"
		}
		for _, row := range in.Answers {
			if len(row) == 0 {
				return false, "answer_shape"
			}
			for _, value := range row {
				if strings.TrimSpace(value) == "" {
					return false, "invalid_answer"
				}
			}
		}
	}
	// The receipt contains identities and a digest, never prompt or answer text.
	raw, _ := json.Marshal(in) // this struct contains only strings and slices
	digest := sha256.Sum256(raw)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.sweepReplyReceiptsLocked(now)
	if receipt, ok := r.settledReplies[requestID]; ok {
		if receipt.sessionID != in.SessionID {
			return false, "session_mismatch"
		}
		if receipt.digest != digest {
			return false, "reply_conflict"
		}
		return true, ""
	}
	if pending := r.pending[in.SessionID]; pending != nil && pending.requestID != requestID {
		return false, "superseded"
	}
	var live *question.Request
	for _, req := range r.svc.app.Questions.List() {
		if req.ID == requestID {
			copy := req
			live = &copy
			break
		}
	}
	if live == nil {
		return false, "question_not_pending"
	}
	if live.SessionID != in.SessionID {
		return false, "session_mismatch"
	}
	answers := in.Answers
	if in.Text != nil {
		if len(live.Questions) != 1 {
			return false, "answer_shape"
		}
		answers = parseQuestionAnswers(*in.Text, live.Questions)
		if !live.Questions[0].IsCustomEnabled() {
			count := 1
			if live.Questions[0].Multiple {
				count = len(splitCSV(*in.Text))
			}
			if len(answers[0]) != count {
				return false, "invalid_answer"
			}
		}
	}
	answers, refusal := normalizeQuestionReply(answers, live.Questions)
	if refusal != "" {
		return false, refusal
	}
	// Preserve cd-5480 D3 even before the Created event reaches this router.
	if r.boxLocked().Any(in.SessionID, func(it inbox.Item) bool { return it.ID != "" }) {
		if err := r.svc.app.Questions.Unanswered(requestID, unansweredQuestionNote); err != nil {
			return false, "question_not_pending"
		}
		delete(r.pending, in.SessionID)
		return false, "queued_input_pending"
	}
	if err := r.svc.app.Questions.Reply(requestID, answers); err != nil {
		return false, "question_not_pending"
	}
	delete(r.pending, in.SessionID)
	// Only choice labels suppress duplicate legacy clicks. A custom answer
	// must not swallow a later deliberate typed message with the same text.
	choices := make([][]string, len(answers))
	for i, row := range answers {
		for _, value := range row {
			for _, option := range live.Questions[i].Options {
				if value == option.Label {
					choices[i] = append(choices[i], value)
					break
				}
			}
		}
	}
	r.rememberAnswers(in.SessionID, choices)
	if r.settledReplies == nil {
		r.settledReplies = make(map[string]settledQuestionReply)
	}
	if len(r.settledReplies) >= boundReplyReceiptCap {
		var oldest string
		var at time.Time
		for id, receipt := range r.settledReplies {
			if oldest == "" || receipt.settledAt.Before(at) {
				oldest, at = id, receipt.settledAt
			}
		}
		delete(r.settledReplies, oldest)
	}
	r.settledReplies[requestID] = settledQuestionReply{sessionID: in.SessionID, digest: digest, settledAt: now}
	return false, ""
}

func normalizeQuestionReply(answers [][]string, prompts []question.Prompt) ([][]string, string) {
	if len(answers) != len(prompts) {
		return nil, "answer_shape"
	}
	out := make([][]string, len(answers))
	for i, row := range answers {
		if len(row) == 0 || (!prompts[i].Multiple && len(row) != 1) {
			return nil, "answer_shape"
		}
		for _, raw := range row {
			value := strings.TrimSpace(raw)
			if value == "" {
				return nil, "invalid_answer"
			}
			matched := false
			for _, option := range prompts[i].Options {
				if strings.EqualFold(value, option.Label) {
					value, matched = option.Label, true
					break
				}
			}
			if !matched && !prompts[i].IsCustomEnabled() {
				return nil, "invalid_answer"
			}
			out[i] = append(out[i], value)
		}
	}
	return out, ""
}

func (r *QuestionRouter) sweepReplyReceiptsLocked(now time.Time) {
	for id, receipt := range r.settledReplies {
		if now.Sub(receipt.settledAt) >= boundReplyReceiptTTL {
			delete(r.settledReplies, id)
		}
	}
}
