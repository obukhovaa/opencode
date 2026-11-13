package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/question"
)

// routeRequestQuestionReply consumes exact native callback identities even once
// their flow completed. A stale callback must never turn into an ordinary prompt
// or select the same label in a later question.
func (s *Service) routeRequestQuestionReply(ctx context.Context, conversation string, in bridge.Inbound) bool {
	tokens := strings.Split(in.Text, ",")
	encoded := len(tokens) > 0 && bridge.IsFlowQuestionValue(strings.TrimSpace(tokens[0]))
	requestID := in.QuestionRequestID
	if !encoded && requestID == "" {
		return false
	}
	indices := []int{}
	if encoded {
		for _, token := range tokens {
			id, index, err := bridge.DecodeFlowQuestionValue(strings.TrimSpace(token))
			if err != nil || requestID != "" && requestID != id {
				return true
			}
			requestID = id
			indices = append(indices, index)
		}
	}
	if s.app == nil || s.app.Questions == nil || s.questionRouter == nil {
		return true
	}
	if s.app.FlowRuns != nil {
		record, err := s.app.FlowRuns.FindQuestionByRequest(ctx, requestID)
		if err == nil {
			input := questionReplyInput{SessionID: record.SessionID, ConversationID: conversation, Peer: &in.Peer, JobID: s.RemoteJobID()}
			if encoded {
				var descriptor question.DurableDescriptor
				if json.Unmarshal(record.Descriptor, &descriptor) != nil || len(descriptor.Questions) != 1 {
					return true
				}
				labels := []string{}
				for _, index := range indices {
					if index >= len(descriptor.Questions[0].Options) {
						return true
					}
					labels = append(labels, descriptor.Questions[0].Options[index].Label)
				}
				input.Answers = [][]string{labels}
			} else {
				input.Text = &in.Text
			}
			handled, replayed, refusal := s.durableFlowReply(ctx, requestID, input)
			if !handled || refusal != "" {
				s.replyToPeer(ctx, in.Peer, "That question is no longer waiting for this answer.", true, conversation)
			} else if !replayed && in.Source == bridge.InboundSourceModal {
				s.replyToPeer(ctx, in.Peer, "Answer recorded.", true, conversation)
			}
			return true
		}
	}
	if encoded {
		return true
	}
	// Preserve exact custom-modal replies on ordinary, non-flow sessions too.
	for _, request := range s.app.Questions.List() {
		if request.ID == requestID && request.SessionID == conversation {
			_, refusal := s.questionRouter.replyRequest(requestID, questionReplyInput{SessionID: conversation, Text: &in.Text})
			if refusal != "" {
				s.replyToPeer(ctx, in.Peer, "That question is no longer waiting for this answer.", true, conversation)
			}
			return true
		}
	}
	return true
}
