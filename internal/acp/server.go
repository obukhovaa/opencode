package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/version"
)

// Server runs the ACP JSON-RPC 2.0 server over stdio.
type Server struct {
	app       *app.App
	transport *Transport
	handler   *Handler
}

// NewServer creates a new ACP server.
func NewServer(application *app.App, transport *Transport) *Server {
	handler := NewHandler(application, transport)
	return &Server{
		app:       application,
		transport: transport,
		handler:   handler,
	}
}

// Run starts the ACP server loop. It reads JSON-RPC messages from stdin and
// dispatches them to the handler. It blocks until EOF or the context is cancelled.
func (s *Server) Run(ctx context.Context) error {
	// Start the event subscription that forwards internal events to the client.
	s.handler.StartEventSubscription(ctx)
	defer s.handler.StopEventSubscription()

	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  ⌬ OpenCode ACP Server\n")
	fmt.Fprintf(os.Stderr, "  ─────────────────────\n")
	fmt.Fprintf(os.Stderr, "  Transport:  stdio (NDJSON)\n")
	fmt.Fprintf(os.Stderr, "  Version:    %s\n", version.Version)
	fmt.Fprintf(os.Stderr, "  Project:    %s\n", config.WorkingDirectory())
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  Waiting for JSON-RPC messages on stdin...\n")
	fmt.Fprintf(os.Stderr, "\n")

	logging.Info("ACP server started")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		raw, err := s.transport.ReadMessage()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) {
				logging.Info("ACP: input closed, shutting down")
				return nil
			}
			// If the context was cancelled, treat any read error as clean shutdown.
			if ctx.Err() != nil {
				logging.Info("ACP: shutting down")
				return ctx.Err()
			}
			logging.Error("ACP: failed to read message", "error", err)
			continue
		}

		if err := s.dispatch(raw); err != nil {
			logging.Error("ACP: dispatch error", "error", err)
		}
	}
}

// dispatch parses the raw JSON-RPC message and routes it to the appropriate handler.
func (s *Server) dispatch(raw json.RawMessage) error {
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return s.transport.SendError(nil, ErrCodeParse, "invalid JSON")
	}

	if req.JSONRPC != "2.0" {
		return s.transport.SendError(req.ID, ErrCodeInvalidRequest, "expected jsonrpc 2.0")
	}

	logging.Info("ACP: received method", "method", req.Method, "id", req.ID)

	switch req.Method {
	case "initialize":
		var params InitializeParams
		if err := unmarshalParams(req.Params, &params); err != nil {
			return s.transport.SendError(req.ID, ErrCodeInvalidParams, err.Error())
		}
		return s.handler.HandleInitialize(req.ID, params)

	case "session/new":
		var params NewSessionParams
		if err := unmarshalParams(req.Params, &params); err != nil {
			return s.transport.SendError(req.ID, ErrCodeInvalidParams, err.Error())
		}
		return s.handler.HandleNewSession(req.ID, params)

	case "session/load":
		var params LoadSessionParams
		if err := unmarshalParams(req.Params, &params); err != nil {
			return s.transport.SendError(req.ID, ErrCodeInvalidParams, err.Error())
		}
		return s.handler.HandleLoadSession(req.ID, params)

	case "session/list":
		var params ListSessionsParams
		if err := unmarshalParams(req.Params, &params); err != nil {
			return s.transport.SendError(req.ID, ErrCodeInvalidParams, err.Error())
		}
		return s.handler.HandleListSessions(req.ID, params)

	case "session/close":
		var params CloseSessionParams
		if err := unmarshalParams(req.Params, &params); err != nil {
			return s.transport.SendError(req.ID, ErrCodeInvalidParams, err.Error())
		}
		return s.handler.HandleCloseSession(req.ID, params)

	case "session/resume":
		var params ResumeSessionParams
		if err := unmarshalParams(req.Params, &params); err != nil {
			return s.transport.SendError(req.ID, ErrCodeInvalidParams, err.Error())
		}
		return s.handler.HandleResumeSession(req.ID, params)

	case "session/prompt":
		var params PromptParams
		if err := unmarshalParams(req.Params, &params); err != nil {
			return s.transport.SendError(req.ID, ErrCodeInvalidParams, err.Error())
		}
		return s.handler.HandlePrompt(req.ID, params)

	case "session/cancel":
		var params CancelParams
		if err := unmarshalParams(req.Params, &params); err != nil {
			return nil // cancel is a notification, no response needed
		}
		s.handler.HandleCancel(params)
		return nil

	default:
		if req.ID != nil {
			return s.transport.SendError(req.ID, ErrCodeMethodNotFound, fmt.Sprintf("unknown method: %s", req.Method))
		}
		// Unknown notifications are silently ignored per JSON-RPC spec.
		return nil
	}
}

// unmarshalParams extracts params from the raw request params field.
func unmarshalParams(params any, target any) error {
	if params == nil {
		return nil
	}

	// params can arrive as json.RawMessage or as a pre-decoded map.
	var data []byte
	switch v := params.(type) {
	case json.RawMessage:
		data = v
	default:
		var err error
		data, err = json.Marshal(v)
		if err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}
