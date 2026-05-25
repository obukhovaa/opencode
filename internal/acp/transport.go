package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/opencode-ai/opencode/internal/logging"
)

// Transport handles JSON-RPC 2.0 message framing over stdio using
// newline-delimited JSON (NDJSON) as specified by the ACP protocol.
// Each message is a single JSON object on one line, terminated by '\n'.
// Messages MUST NOT contain embedded newlines.
type Transport struct {
	writer  io.Writer
	mu      sync.Mutex // protects writes
	scanner *bufio.Scanner
}

// NewTransport creates a new Transport reading from r and writing to w.
func NewTransport(r io.Reader, w io.Writer) *Transport {
	scanner := bufio.NewScanner(r)
	// Allow up to 10MB per line for large JSON-RPC messages.
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	return &Transport{
		writer:  w,
		scanner: scanner,
	}
}

// ReadMessage reads a single JSON-RPC message using newline-delimited framing.
// Each message is a complete JSON object on one line, separated by '\n'.
func (t *Transport) ReadMessage() (json.RawMessage, error) {
	for t.scanner.Scan() {
		line := strings.TrimSpace(t.scanner.Text())
		if line == "" {
			continue
		}
		return json.RawMessage(line), nil
	}

	if err := t.scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading message: %w", err)
	}
	return nil, io.EOF
}

// WriteMessage writes a JSON-RPC message as newline-delimited JSON.
func (t *Transport) WriteMessage(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Write JSON followed by newline, per ACP spec.
	if _, err := t.writer.Write(data); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}
	if _, err := io.WriteString(t.writer, "\n"); err != nil {
		return fmt.Errorf("writing newline: %w", err)
	}

	return nil
}

// SendResponse sends a JSON-RPC 2.0 response.
func (t *Transport) SendResponse(id any, result any) error {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	return t.WriteMessage(resp)
}

// SendError sends a JSON-RPC 2.0 error response.
func (t *Transport) SendError(id any, code int, message string) error {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: message,
		},
	}
	return t.WriteMessage(resp)
}

// SendNotification sends a JSON-RPC 2.0 notification (no ID, no response expected).
func (t *Transport) SendNotification(method string, params any) error {
	notif := Notification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	if err := t.WriteMessage(notif); err != nil {
		logging.Error("failed to send ACP notification", "method", method, "error", err)
		return err
	}
	return nil
}
