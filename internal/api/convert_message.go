package api

import (
	"encoding/json"
	"fmt"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
)

// ConvertMessageInfo converts the metadata of an internal Message to the API format.
// Token and cost information is not stored per-message internally, so those fields
// are left at zero values. Callers can populate them separately if needed.
func ConvertMessageInfo(msg message.Message) APIMessageInfo {
	var providerID, modelID string
	if msg.Model != "" {
		modelID = string(msg.Model)
		if m, ok := models.SupportedModels[msg.Model]; ok {
			providerID = string(m.Provider)
		}
	}

	return APIMessageInfo{
		ID:         msg.ID,
		SessionID:  msg.SessionID,
		Role:       string(msg.Role),
		ProviderID: providerID,
		ModelID:    modelID,
		Tokens:     APIMessageTokens{},
		Cost:       0,
		Time: APIMessageTime{
			Created: msg.CreatedAt,
			Updated: msg.UpdatedAt,
		},
	}
}

// ConvertMessages converts a full list of session messages into API responses.
// It merges ToolCall parts from assistant messages with their corresponding
// ToolResult parts from subsequent user/tool messages into unified "tool" APIParts.
func ConvertMessages(msgs []message.Message) []APIMessageResponse {
	// Build a lookup of toolCallID -> ToolResult across all messages so that
	// tool calls on assistant messages can be merged with their results.
	resultMap := buildToolResultMap(msgs)

	responses := make([]APIMessageResponse, 0, len(msgs))
	for _, msg := range msgs {
		responses = append(responses, ConvertMessage(msg, resultMap))
	}
	return responses
}

// ConvertMessage converts a single internal Message to the API response format.
// The resultMap parameter maps tool call IDs to their corresponding ToolResult,
// allowing ToolCall parts to be merged with results into unified "tool" APIParts.
// Pass nil if no result merging is needed.
func ConvertMessage(msg message.Message, resultMap map[string]message.ToolResult) APIMessageResponse {
	info := ConvertMessageInfo(msg)
	parts := convertParts(msg.ID, msg.SessionID, msg.Parts, resultMap)

	return APIMessageResponse{
		Info:  info,
		Parts: parts,
	}
}

// convertParts converts internal ContentPart slices to the flat APIPart format.
// ToolCall parts are merged with their results from resultMap. ToolResult parts
// that were already merged into a ToolCall are skipped. Standalone ToolResults
// (without a matching ToolCall in the same message) are emitted as tool parts.
func convertParts(messageID, sessionID string, parts []message.ContentPart, resultMap map[string]message.ToolResult) []APIPart {
	// Track which tool call IDs appear as ToolCall parts in this message
	// so we can skip their standalone ToolResult counterparts.
	toolCallIDs := make(map[string]struct{})
	for _, part := range parts {
		if tc, ok := part.(message.ToolCall); ok {
			toolCallIDs[tc.ID] = struct{}{}
		}
	}

	apiParts := make([]APIPart, 0, len(parts))
	partIndex := 0

	for _, part := range parts {
		switch p := part.(type) {
		case message.TextContent:
			if p.Text == "" {
				continue
			}
			apiParts = append(apiParts, APIPart{
				ID:   fmt.Sprintf("part-%d", partIndex),
				Type: "text",
				Text: p.Text,
			})
			partIndex++

		case message.ReasoningContent:
			if p.Thinking == "" {
				continue
			}
			apiParts = append(apiParts, APIPart{
				ID:   fmt.Sprintf("part-%d", partIndex),
				Type: "reasoning",
				Text: p.Thinking,
			})
			partIndex++

		case message.ToolCall:
			apiPart := convertToolCall(p, resultMap, partIndex)
			apiParts = append(apiParts, apiPart)
			partIndex++

		case message.ToolResult:
			// Skip ToolResults that were already merged into a ToolCall above.
			if _, merged := toolCallIDs[p.ToolCallID]; merged {
				continue
			}
			// Standalone ToolResult (e.g., on a user message without a matching ToolCall
			// in the same message). Emit as a tool part with the result info.
			apiPart := convertStandaloneToolResult(p, partIndex)
			apiParts = append(apiParts, apiPart)
			partIndex++

		case message.Finish:
			// Finish parts are internal-only metadata; not exposed via the API.
			continue

		case message.BinaryContent:
			apiParts = append(apiParts, APIPart{
				ID:   fmt.Sprintf("part-%d", partIndex),
				Type: "file",
			})
			partIndex++

		case message.ImageURLContent:
			apiParts = append(apiParts, APIPart{
				ID:   fmt.Sprintf("part-%d", partIndex),
				Type: "file",
				Text: p.URL,
			})
			partIndex++
		}
	}

	// Stamp messageID and sessionID on all parts (required by OpenWork schema).
	for i := range apiParts {
		apiParts[i].MessageID = messageID
		apiParts[i].SessionID = sessionID
	}

	return apiParts
}

// convertToolCall creates an APIPart for a ToolCall, merging in the ToolResult
// from resultMap if one exists for this call ID.
func convertToolCall(tc message.ToolCall, resultMap map[string]message.ToolResult, index int) APIPart {
	state := &APIToolState{
		Status: resolveToolStatus(tc, resultMap),
		Input:  parseToolInput(tc.Input),
	}

	// Merge the result if available.
	if result, ok := resultMap[tc.ID]; ok {
		if result.IsError {
			state.Error = result.Content
		} else {
			state.Output = result.Content
		}
		state.Metadata = parseMetadata(result.Metadata)
	}

	return APIPart{
		ID:     fmt.Sprintf("part-%d", index),
		Type:   "tool",
		Tool:   tc.Name,
		CallID: tc.ID,
		State:  state,
	}
}

// convertStandaloneToolResult creates an APIPart for a ToolResult that has no
// matching ToolCall in the same message.
func convertStandaloneToolResult(tr message.ToolResult, index int) APIPart {
	status := "completed"
	state := &APIToolState{
		Status:   status,
		Output:   tr.Content,
		Metadata: parseMetadata(tr.Metadata),
	}

	if tr.IsError {
		state.Status = "error"
		state.Error = tr.Content
		state.Output = ""
	}

	return APIPart{
		ID:     fmt.Sprintf("part-%d", index),
		Type:   "tool",
		Tool:   tr.Name,
		CallID: tr.ToolCallID,
		State:  state,
	}
}

// resolveToolStatus determines the tool execution status based on the ToolCall
// state and whether a result exists.
func resolveToolStatus(tc message.ToolCall, resultMap map[string]message.ToolResult) string {
	result, hasResult := resultMap[tc.ID]
	if !hasResult {
		if tc.Finished {
			return "running"
		}
		return "pending"
	}
	if result.IsError {
		return "error"
	}
	return "completed"
}

// parseToolInput attempts to parse the JSON string input of a ToolCall into a map.
// Returns nil if the input is empty or not valid JSON.
func parseToolInput(input string) map[string]any {
	if input == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		// If the input isn't valid JSON (e.g., partial streaming), wrap it as-is.
		return map[string]any{"raw": input}
	}
	return parsed
}

// parseMetadata attempts to parse a metadata JSON string into a map.
// Returns nil if the metadata is empty or not valid JSON.
func parseMetadata(metadata string) map[string]any {
	if metadata == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(metadata), &parsed); err != nil {
		return nil
	}
	return parsed
}

// ConvertMessageToResponse converts a single internal Message to the API
// response format without cross-message tool result merging. This is the
// convenience wrapper used by SSE event handlers where we only have the
// single message that changed.
func ConvertMessageToResponse(msg message.Message) APIMessageResponse {
	return ConvertMessage(msg, nil)
}

// ConvertPart converts a single ContentPart into an APIPart for use in
// `message.part.updated` SSE frames. Unlike convertParts, this has no
// cross-message context — ToolCall parts emit with status from Finished
// (pending/running) and ToolResult parts emit standalone with
// completed/error status. The SSE consumer reconciles by callID.
//
// Only ToolCall and ToolResult are emitted today (see PublishPart call
// sites in internal/llm/agent/agent.go). If text/reasoning/file delta
// streaming is added, extend this with position-derived part IDs — do not
// use static IDs, since the SSE consumer keys on `id` for dedup.
func ConvertPart(messageID, sessionID string, part message.ContentPart) APIPart {
	switch p := part.(type) {
	case message.ToolCall:
		status := "pending"
		if p.Finished {
			status = "running"
		}
		return APIPart{
			ID:        "tool-" + p.ID,
			Type:      "tool",
			MessageID: messageID,
			SessionID: sessionID,
			Tool:      p.Name,
			CallID:    p.ID,
			State: &APIToolState{
				Status: status,
				Input:  parseToolInput(p.Input),
			},
		}
	case message.ToolResult:
		state := &APIToolState{
			Status:   "completed",
			Output:   p.Content,
			Metadata: parseMetadata(p.Metadata),
		}
		if p.IsError {
			state.Status = "error"
			state.Error = p.Content
			state.Output = ""
		}
		return APIPart{
			ID:        "tool-" + p.ToolCallID,
			Type:      "tool",
			MessageID: messageID,
			SessionID: sessionID,
			Tool:      p.Name,
			CallID:    p.ToolCallID,
			State:     state,
		}
	}
	return APIPart{MessageID: messageID, SessionID: sessionID}
}

// buildToolResultMap builds a lookup from tool call ID to ToolResult across
// all messages. If multiple results exist for the same call ID, the last one wins.
func buildToolResultMap(msgs []message.Message) map[string]message.ToolResult {
	resultMap := make(map[string]message.ToolResult)
	for _, msg := range msgs {
		for _, part := range msg.Parts {
			if tr, ok := part.(message.ToolResult); ok {
				resultMap[tr.ToolCallID] = tr
			}
		}
	}
	return resultMap
}
