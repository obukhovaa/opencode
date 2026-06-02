package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
)

// handleMessageList returns all messages for a session.
func (s *Server) handleMessageList(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	messages, err := s.app.Messages.List(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list messages")
		return
	}
	writeJSON(w, http.StatusOK, ConvertMessages(messages))
}

// handleMessageGet returns a single message by ID.
func (s *Server) handleMessageGet(w http.ResponseWriter, r *http.Request) {
	messageID := r.PathValue("messageID")
	msg, err := s.app.Messages.Get(r.Context(), messageID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "message not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get message")
		return
	}
	writeJSON(w, http.StatusOK, ConvertMessageToResponse(msg))
}

// extractPromptContent extracts text and attachments from prompt parts.
// Text parts are concatenated; file parts are converted to message.Attachment.
func extractPromptContent(parts []APIPromptPart) (string, []message.Attachment) {
	var texts []string
	var attachments []message.Attachment
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		case "file":
			if p.URL == "" {
				continue
			}
			att := message.Attachment{
				FileName: p.FileName,
				MimeType: p.Mime,
			}
			// Data URLs: "data:<mime>;base64,<data>"
			if strings.HasPrefix(p.URL, "data:") {
				if idx := strings.Index(p.URL, ","); idx >= 0 {
					decoded, err := base64.StdEncoding.DecodeString(p.URL[idx+1:])
					if err != nil {
						logging.Warn("failed to decode file attachment base64", "filename", p.FileName, "error", err)
						continue
					}
					att.Content = decoded
				} else {
					continue
				}
			} else if strings.HasPrefix(p.URL, "file://") {
				att.FilePath = strings.TrimPrefix(p.URL, "file://")
			} else {
				continue
			}
			attachments = append(attachments, att)
		}
	}
	return strings.Join(texts, "\n"), attachments
}

// handleSessionPrompt sends a prompt and waits for the agent to complete.
func (s *Server) handleSessionPrompt(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")

	var req APIPromptRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	text, attachments := extractPromptContent(req.Parts)
	if text == "" {
		writeError(w, http.StatusBadRequest, "prompt text is required")
		return
	}

	activeAgent := s.app.ActiveAgent()
	if activeAgent == nil {
		writeError(w, http.StatusInternalServerError, "no active agent available")
		return
	}

	events, err := activeAgent.Run(r.Context(), sessionID, text, 0, attachments...)
	if err != nil {
		if errors.Is(err, agent.ErrSessionBusy) {
			writeError(w, http.StatusConflict, "session is busy")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to start agent run")
		return
	}

	// Drain the event channel and capture the final result.
	var lastEvent agent.AgentEvent
	for evt := range events {
		lastEvent = evt
	}

	if lastEvent.Error != nil {
		writeError(w, http.StatusInternalServerError, lastEvent.Error.Error())
		return
	}

	writeJSON(w, http.StatusOK, ConvertMessageToResponse(lastEvent.Message))
}

// handleSessionPromptAsync sends a prompt and returns immediately.
func (s *Server) handleSessionPromptAsync(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")

	var req APIPromptRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	text, attachments := extractPromptContent(req.Parts)
	if text == "" {
		writeError(w, http.StatusBadRequest, "prompt text is required")
		return
	}

	activeAgent := s.app.ActiveAgent()
	if activeAgent == nil {
		writeError(w, http.StatusInternalServerError, "no active agent available")
		return
	}

	events, err := activeAgent.Run(context.Background(), sessionID, text, 0, attachments...)
	if err != nil {
		if errors.Is(err, agent.ErrSessionBusy) {
			writeError(w, http.StatusConflict, "session is busy")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to start agent run")
		return
	}

	// Drain events in the background so the agent run completes.
	go func() {
		for evt := range events {
			if evt.Error != nil {
				logging.Error("async prompt error", "sessionID", sessionID, "error", evt.Error)
			}
		}
	}()

	w.WriteHeader(http.StatusNoContent)
}

// handleSessionSummarize triggers summarization of a session.
func (s *Server) handleSessionSummarize(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")

	activeAgent := s.app.ActiveAgent()
	if activeAgent == nil {
		writeError(w, http.StatusInternalServerError, "no active agent available")
		return
	}

	err := activeAgent.Summarize(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, agent.ErrSessionBusy) {
			writeError(w, http.StatusConflict, "session is busy")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to start summarization")
		return
	}

	writeJSON(w, http.StatusOK, true)
}
