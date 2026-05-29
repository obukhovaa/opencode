package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/version"
)

// Handler implements ACP protocol methods using the internal app services.
type Handler struct {
	app       *app.App
	transport *Transport
	mu        sync.RWMutex           // protects sessions map
	sessions  map[string]*acpSession // sessionID -> ACP session state
	cancel    context.CancelFunc
}

// acpSession tracks per-session ACP state.
type acpSession struct {
	id  string
	cwd string
}

// NewHandler creates a new ACP handler.
func NewHandler(application *app.App, transport *Transport) *Handler {
	return &Handler{
		app:       application,
		transport: transport,
		sessions:  make(map[string]*acpSession),
	}
}

// HandleInitialize handles the "initialize" method.
func (h *Handler) HandleInitialize(id any, params InitializeParams) error {
	logging.Info("ACP initialize", "protocolVersion", params.ProtocolVersion)

	result := InitializeResult{
		ProtocolVersion: 1,
		AgentCapabilities: AgentCapabilities{
			LoadSession: true,
			PromptCapabilities: &PromptCapabilities{
				EmbeddedContext: true,
				Image:           true,
			},
			SessionCapabilities: &SessionCapabilities{
				Close:  &struct{}{},
				List:   &struct{}{},
				Resume: &struct{}{},
			},
		},
		AgentInfo: AgentInfo{
			Name:    "OpenCode",
			Version: version.Version,
		},
	}

	return h.transport.SendResponse(id, result)
}

// HandleNewSession handles the "session/new" method.
func (h *Handler) HandleNewSession(id any, params NewSessionParams) error {
	ctx := context.Background()

	sess, err := h.app.Sessions.Create(ctx, "")
	if err != nil {
		return h.transport.SendError(id, ErrCodeInternal, fmt.Sprintf("failed to create session: %v", err))
	}

	h.mu.Lock()
	h.sessions[sess.ID] = &acpSession{
		id:  sess.ID,
		cwd: params.CWD,
	}
	h.mu.Unlock()

	result := NewSessionResult{
		SessionID: sess.ID,
		Models:    h.buildModelsInfo(),
		Modes:     h.buildModesInfo(),
	}

	return h.transport.SendResponse(id, result)
}

// HandleLoadSession handles the "session/load" method.
func (h *Handler) HandleLoadSession(id any, params LoadSessionParams) error {
	ctx := context.Background()

	sess, err := h.app.Sessions.Get(ctx, params.SessionID)
	if err != nil {
		return h.transport.SendError(id, ErrCodeInvalidParams, fmt.Sprintf("session not found: %v", err))
	}

	h.mu.Lock()
	h.sessions[sess.ID] = &acpSession{
		id:  sess.ID,
		cwd: params.CWD,
	}
	h.mu.Unlock()

	// Replay existing messages.
	msgs, err := h.app.Messages.List(ctx, sess.ID)
	if err != nil {
		logging.Error("failed to list messages for session replay", "sessionID", sess.ID, "error", err)
	} else {
		h.replayMessages(sess.ID, msgs)
	}

	result := LoadSessionResult{
		SessionID: sess.ID,
		Models:    h.buildModelsInfo(),
		Modes:     h.buildModesInfo(),
	}

	return h.transport.SendResponse(id, result)
}

// HandleListSessions handles the "session/list" method.
func (h *Handler) HandleListSessions(id any, params ListSessionsParams) error {
	ctx := context.Background()

	sessions, err := h.app.Sessions.List(ctx)
	if err != nil {
		return h.transport.SendError(id, ErrCodeInternal, fmt.Sprintf("failed to list sessions: %v", err))
	}

	// Use the CWD from the request if provided, otherwise fall back to
	// the stored ACP session CWD or the process working directory.
	listCWD := params.CWD
	if listCWD == "" {
		listCWD = config.WorkingDirectory()
	}

	entries := make([]SessionInfo, 0, len(sessions))
	h.mu.RLock()
	for _, sess := range sessions {
		cwd := listCWD
		if tracked, ok := h.sessions[sess.ID]; ok {
			cwd = tracked.cwd
		}
		entries = append(entries, SessionInfo{
			SessionID: sess.ID,
			CWD:       cwd,
			Title:     sess.Title,
			UpdatedAt: time.UnixMilli(sess.UpdatedAt).UTC().Format(time.RFC3339),
		})
	}
	h.mu.RUnlock()

	return h.transport.SendResponse(id, ListSessionsResult{
		Sessions: entries,
	})
}

// HandleCloseSession handles the "session/close" method.
func (h *Handler) HandleCloseSession(id any, params CloseSessionParams) error {
	activeAgent := h.app.ActiveAgent()
	if activeAgent != nil {
		activeAgent.Cancel(params.SessionID)
	}

	h.mu.Lock()
	delete(h.sessions, params.SessionID)
	h.mu.Unlock()

	return h.transport.SendResponse(id, struct{}{})
}

// HandleResumeSession handles the "session/resume" method.
func (h *Handler) HandleResumeSession(id any, params ResumeSessionParams) error {
	return h.HandleLoadSession(id, LoadSessionParams{
		SessionID: params.SessionID,
		CWD:       params.CWD,
	})
}

// HandlePrompt handles the "session/prompt" method.
func (h *Handler) HandlePrompt(id any, params PromptParams) error {
	h.mu.RLock()
	acpSess, ok := h.sessions[params.SessionID]
	h.mu.RUnlock()
	if !ok {
		return h.transport.SendError(id, ErrCodeInvalidParams, fmt.Sprintf("session not found: %s", params.SessionID))
	}

	// Extract text and attachments from prompt parts.
	var texts []string
	var attachments []message.Attachment
	for _, part := range params.Prompt {
		switch part.Type {
		case "text":
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		case "image":
			if part.Data != "" && part.MimeType != "" {
				decoded, err := base64.StdEncoding.DecodeString(part.Data)
				if err == nil {
					name := part.Name
					if name == "" {
						name = "image"
					}
					attachments = append(attachments, message.Attachment{
						FileName: name,
						MimeType: part.MimeType,
						Content:  decoded,
					})
				}
			}
		case "resource_link":
			if part.URI != "" && strings.HasPrefix(part.URI, "file://") {
				attachments = append(attachments, message.Attachment{
					FilePath: strings.TrimPrefix(part.URI, "file://"),
					FileName: part.Name,
					MimeType: part.MimeType,
				})
			}
		}
	}
	text := strings.Join(texts, "\n")
	if text == "" {
		return h.transport.SendError(id, ErrCodeInvalidParams, "prompt text is required")
	}

	activeAgent := h.app.ActiveAgent()
	if activeAgent == nil {
		return h.transport.SendError(id, ErrCodeInternal, "no active agent available")
	}

	_ = acpSess // cwd available for future use

	events, err := activeAgent.Run(context.Background(), params.SessionID, text, attachments...)
	if err != nil {
		if errors.Is(err, agent.ErrSessionBusy) {
			return h.transport.SendError(id, ErrCodeInternal, "session is busy")
		}
		return h.transport.SendError(id, ErrCodeInternal, fmt.Sprintf("failed to start agent: %v", err))
	}

	// Drain events — real-time updates are sent via the event subscription goroutine.
	var lastEvent agent.AgentEvent
	for evt := range events {
		lastEvent = evt
	}

	if lastEvent.Error != nil {
		return h.transport.SendError(id, ErrCodeInternal, lastEvent.Error.Error())
	}

	result := PromptResult{
		StopReason: "end_turn",
	}

	return h.transport.SendResponse(id, result)
}

// HandleCancel handles the "session/cancel" notification.
func (h *Handler) HandleCancel(params CancelParams) {
	activeAgent := h.app.ActiveAgent()
	if activeAgent != nil {
		activeAgent.Cancel(params.SessionID)
	}
}

// StartEventSubscription subscribes to internal brokers and forwards events
// as ACP session/update notifications to the client.
func (h *Handler) StartEventSubscription(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	h.cancel = cancel

	msgCh := h.app.Messages.Subscribe(ctx)
	sesCh := h.app.Sessions.Subscribe(ctx)
	permCh := h.app.Permissions.Subscribe(ctx)

	go h.eventLoop(ctx, msgCh, sesCh, permCh)
}

// StopEventSubscription stops the event subscription goroutine.
func (h *Handler) StopEventSubscription() {
	if h.cancel != nil {
		h.cancel()
	}
}

func (h *Handler) eventLoop(
	ctx context.Context,
	msgCh <-chan pubsub.Event[message.Message],
	sesCh <-chan pubsub.Event[session.Session],
	permCh <-chan pubsub.Event[permission.PermissionRequest],
) {
	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-msgCh:
			if !ok {
				return
			}
			h.handleMessageEvent(event)

		case event, ok := <-sesCh:
			if !ok {
				return
			}
			_ = event // Session lifecycle events could be forwarded in the future.

		case event, ok := <-permCh:
			if !ok {
				return
			}
			h.handlePermissionEvent(event)
		}
	}
}

func (h *Handler) handleMessageEvent(event pubsub.Event[message.Message]) {
	msg := event.Payload

	// Only forward events for sessions we're tracking.
	h.mu.RLock()
	_, tracked := h.sessions[msg.SessionID]
	h.mu.RUnlock()
	if !tracked {
		return
	}

	if event.Type != pubsub.UpdatedEvent && event.Type != pubsub.CreatedEvent {
		return
	}

	// Forward message content as ACP session updates.
	if msg.Role == message.Assistant {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case message.TextContent:
				if p.Text == "" {
					continue
				}
				h.sendSessionUpdate(msg.SessionID, AgentMessageChunk{
					SessionUpdate: "agent_message_chunk",
					MessageID:     msg.ID,
					Content: ContentBlock{
						Type: "text",
						Text: p.Text,
					},
				})

			case message.ReasoningContent:
				if p.Thinking == "" {
					continue
				}
				h.sendSessionUpdate(msg.SessionID, AgentThoughtChunk{
					SessionUpdate: "agent_thought_chunk",
					MessageID:     msg.ID,
					Content: ContentBlock{
						Type: "text",
						Text: p.Thinking,
					},
				})

			case message.ToolCall:
				status := "pending"
				if p.Finished {
					status = "in_progress"
				}
				h.sendSessionUpdate(msg.SessionID, ToolCallUpdate{
					SessionUpdate: "tool_call_update",
					ToolCallID:    p.ID,
					Status:        status,
					Kind:          toolKind(p.Name),
					Title:         p.Name,
					Locations:     toolLocations(p.Name, p.Input),
					RawInput:      parseInput(p.Input),
				})

			case message.ToolResult:
				status := "completed"
				if p.IsError {
					status = "failed"
				}

				var content []ToolContent
				if p.Content != "" {
					content = []ToolContent{{
						Type: "content",
						Content: &ContentBlock{
							Type: "text",
							Text: p.Content,
						},
					}}
				}

				h.sendSessionUpdate(msg.SessionID, ToolCallUpdate{
					SessionUpdate: "tool_call_update",
					ToolCallID:    p.ToolCallID,
					Status:        status,
					Kind:          toolKind(p.Name),
					Title:         p.Name,
					Content:       content,
				})

				// Emit plan update for todowrite results so ACP clients
				// (AionUI, OpenWork) can render the todo panel.
				if p.Name == "todowrite" && p.Metadata != "" {
					var meta struct {
						Todos []PlanEntry `json:"todos"`
					}
					if json.Unmarshal([]byte(p.Metadata), &meta) == nil && len(meta.Todos) > 0 {
						h.sendSessionUpdate(msg.SessionID, PlanUpdate{
							SessionUpdate: "plan",
							Entries:       meta.Todos,
						})
					}
				}
			}
		}
	}
}

func (h *Handler) handlePermissionEvent(event pubsub.Event[permission.PermissionRequest]) {
	perm := event.Payload

	// Only forward for sessions we're tracking.
	h.mu.RLock()
	_, tracked := h.sessions[perm.SessionID]
	h.mu.RUnlock()
	if !tracked {
		return
	}

	// Send permission request as a notification to the client.
	// The ACP client can then approve/deny it.
	h.sendSessionUpdate(perm.SessionID, map[string]any{
		"sessionUpdate": "permission_request",
		"id":            perm.ID,
		"toolName":      perm.ToolName,
		"description":   perm.Description,
		"action":        perm.Action,
	})
}

func (h *Handler) sendSessionUpdate(sessionID string, update any) {
	if err := h.transport.SendNotification("session/update", SessionUpdateParams{
		SessionID: sessionID,
		Update:    update,
	}); err != nil {
		logging.Error("failed to send ACP session update", "sessionID", sessionID, "error", err)
	}
}

// replayMessages sends existing messages as ACP session updates so the client
// can reconstruct the conversation history.
func (h *Handler) replayMessages(sessionID string, msgs []message.Message) {
	for _, msg := range msgs {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case message.TextContent:
				if p.Text == "" {
					continue
				}
				updateType := "agent_message_chunk"
				if msg.Role == message.User {
					updateType = "user_message_chunk"
				}
				h.sendSessionUpdate(sessionID, AgentMessageChunk{
					SessionUpdate: updateType,
					MessageID:     msg.ID,
					Content: ContentBlock{
						Type: "text",
						Text: p.Text,
					},
				})

			case message.ReasoningContent:
				if p.Thinking == "" {
					continue
				}
				h.sendSessionUpdate(sessionID, AgentThoughtChunk{
					SessionUpdate: "agent_thought_chunk",
					MessageID:     msg.ID,
					Content: ContentBlock{
						Type: "text",
						Text: p.Thinking,
					},
				})

			case message.ToolCall:
				h.sendSessionUpdate(sessionID, ToolCallNotification{
					SessionUpdate: "tool_call",
					ToolCallID:    p.ID,
					Title:         p.Name,
					Kind:          toolKind(p.Name),
					Status:        "completed",
					Locations:     toolLocations(p.Name, p.Input),
					RawInput:      parseInput(p.Input),
				})
			}
		}
	}
}

// buildModelsInfo returns available models info for ACP responses.
func (h *Handler) buildModelsInfo() *ModelsInfo {
	activeAgent := h.app.ActiveAgent()
	if activeAgent == nil {
		return nil
	}

	currentModel := activeAgent.Model()
	available := make([]ModelOption, 0, len(models.SupportedModels))

	for _, m := range models.SupportedModels {
		available = append(available, ModelOption{
			ModelID: string(m.ID),
			Name:    m.Name,
		})
	}

	sort.Slice(available, func(i, j int) bool {
		return available[i].Name < available[j].Name
	})

	return &ModelsInfo{
		CurrentModelID:  string(currentModel.ID),
		AvailableModels: available,
	}
}

// buildModesInfo returns available agent modes for ACP responses.
func (h *Handler) buildModesInfo() *ModesInfo {
	agents := h.app.Registry.List()

	modes := make([]ModeOption, 0)
	for _, a := range agents {
		if a.Mode == config.AgentModeSubagent || a.Hidden {
			continue
		}
		modes = append(modes, ModeOption{
			ID:          a.ID,
			Name:        a.Name,
			Description: a.Description,
		})
	}

	if len(modes) == 0 {
		return nil
	}

	currentMode := string(h.app.ActiveAgentName())

	return &ModesInfo{
		CurrentModeID:  currentMode,
		AvailableModes: modes,
	}
}

// toolKind maps a tool name to an ACP tool kind.
func toolKind(name string) string {
	switch strings.ToLower(name) {
	case "bash", "shell":
		return "execute"
	case "webfetch":
		return "fetch"
	case "edit", "patch", "write":
		return "edit"
	case "grep", "glob":
		return "search"
	case "read":
		return "read"
	default:
		return "other"
	}
}

// toolLocations extracts file locations from tool input.
func toolLocations(name string, input string) []Location {
	parsed := parseInput(input)
	if parsed == nil {
		return nil
	}

	switch strings.ToLower(name) {
	case "read", "edit", "write":
		if path, ok := parsed["filePath"].(string); ok {
			return []Location{{Path: path}}
		}
	case "glob", "grep":
		if path, ok := parsed["path"].(string); ok {
			return []Location{{Path: path}}
		}
	}

	return nil
}

// parseInput parses a JSON string input into a map.
func parseInput(input string) map[string]any {
	if input == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return nil
	}
	return parsed
}
