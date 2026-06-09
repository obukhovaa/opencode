package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
	"github.com/opencode-ai/opencode/internal/session"
)

// sseEventType maps internal pubsub event types to SDK-facing event type strings.
func sseEventType(resource string, evt pubsub.EventType) string {
	switch evt {
	case pubsub.CreatedEvent:
		return resource + ".created"
	case pubsub.UpdatedEvent:
		return resource + ".updated"
	case pubsub.DeletedEvent:
		return resource + ".deleted"
	default:
		return resource + "." + string(evt)
	}
}

// writeSSEEvent serialises a single event as an SSE data frame and flushes it.
// Returns an error if the write or flush fails (usually because the client disconnected).
func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, properties any) error {
	payload := APIEvent{
		Type:       eventType,
		Properties: properties,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		logging.Error("failed to marshal SSE event", "type", eventType, "error", err)
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// setSSEHeaders configures the response for Server-Sent Events streaming.
// It overrides the default JSON content type set by the middleware.
func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

// handleEventSubscribe streams real-time events for all resources
// (messages, sessions, permissions) via SSE. The frontend connects here to
// receive live updates while interacting with a session.
func (s *Server) handleEventSubscribe(w http.ResponseWriter, r *http.Request) {
	s.streamEvents(w, r)
}

// handleGlobalEvent streams cross-cutting events that are not scoped to a
// particular session. This includes session lifecycle events and all messages
// across every session. The frontend connects to GET /global/event to receive
// a global firehose of updates.
func (s *Server) handleGlobalEvent(w http.ResponseWriter, r *http.Request) {
	s.streamEvents(w, r)
}

// streamEvents is the shared implementation for SSE event streaming endpoints.
// It subscribes to the message, session, and permission brokers and fans in
// events from all of them into a single SSE stream.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	setSSEHeaders(w)

	ctx := r.Context()

	msgCh := s.app.Messages.Subscribe(ctx)
	partCh := s.app.Messages.SubscribeParts(ctx)
	sesCh := s.app.Sessions.Subscribe(ctx)
	permCh := s.app.Permissions.Subscribe(ctx)

	var questionCh <-chan pubsub.Event[question.Request]
	if s.app.Questions != nil {
		questionCh = s.app.Questions.Subscribe(ctx)
	}

	// Flow API SSE events: piggyback on the existing /event stream so
	// orchestrators see flow.step.started/completed/failed,
	// flow.waiting_for_input, flow.completed, flow.failed without
	// opening a second connection. nil when flowRunner is not wired.
	var flowCh <-chan pubsub.Event[FlowEvent]
	if s.flowRunner != nil {
		flowCh = s.flowRunner.subscribeFlowEvents(ctx)
	}

	streamLoop(ctx, w, flusher, msgCh, partCh, sesCh, permCh, questionCh, flowCh)
}

// streamLoop runs the fan-in select loop that reads from the broker channels
// and writes SSE frames until the client disconnects or a channel closes.
func streamLoop(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	msgCh <-chan pubsub.Event[message.Message],
	partCh <-chan pubsub.Event[message.PartEvent],
	sesCh <-chan pubsub.Event[session.Session],
	permCh <-chan pubsub.Event[permission.PermissionRequest],
	questionCh <-chan pubsub.Event[question.Request],
	flowCh <-chan pubsub.Event[FlowEvent],
) {
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	// nil channels are never selected, so if questionCh is nil the case is a no-op
	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-msgCh:
			if !ok {
				return
			}
			eventType := sseEventType("message", event.Type)
			props := ConvertMessageToResponse(event.Payload)
			if err := writeSSEEvent(w, flusher, eventType, props); err != nil {
				return
			}

		case event, ok := <-partCh:
			if !ok {
				return
			}
			apiPart := ConvertPart(event.Payload.MessageID, event.Payload.SessionID, event.Payload.Part)
			props := map[string]any{"part": apiPart}
			if err := writeSSEEvent(w, flusher, "message.part.updated", props); err != nil {
				return
			}

		case event, ok := <-sesCh:
			if !ok {
				return
			}
			eventType := sseEventType("session", event.Type)
			props := ConvertSession(event.Payload)
			if err := writeSSEEvent(w, flusher, eventType, props); err != nil {
				return
			}

		case event, ok := <-permCh:
			if !ok {
				return
			}
			props := ConvertPermissionRequest(event.Payload)
			if err := writeSSEEvent(w, flusher, "permission.asked", props); err != nil {
				return
			}

		case event, ok := <-questionCh:
			if !ok {
				return
			}
			props := ConvertQuestionRequest(event.Payload)
			if err := writeSSEEvent(w, flusher, "question.asked", props); err != nil {
				return
			}

		case event, ok := <-flowCh:
			if !ok {
				return
			}
			if err := writeSSEEvent(w, flusher, string(event.Payload.Type), event.Payload); err != nil {
				return
			}

		case <-heartbeat.C:
			if _, err := fmt.Fprintf(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
