package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSEvent is a parsed Mattermost WebSocket frame. Only the fields the
// bridge cares about are decoded.
type WSEvent struct {
	Event  string         `json:"event,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
	Seq    int64          `json:"seq,omitempty"`
	Status string         `json:"status,omitempty"`
}

// wsConn wraps a *websocket.Conn with the small bits of state the adapter
// needs (write mutex, peer identity for logging). Mattermost's WebSocket
// protocol is JSON over a single bidirectional stream — we authenticate
// via an inline "authentication_challenge" action after Connect.
type wsConn struct {
	conn   *websocket.Conn
	wmu    sync.Mutex
	closed chan struct{}
}

// dialOpts controls Dial behavior; exposed for tests to override
// the WebSocket dialer (e.g. with httptest's URL scheme).
type dialOpts struct {
	dialer *websocket.Dialer
}

// DialOption mutates dial behavior. Internal use only.
type DialOption func(*dialOpts)

// WithDialer overrides the websocket.Dialer used by Connect. Tests pass a
// dialer that points at an httptest.Server URL.
func WithDialer(d *websocket.Dialer) DialOption {
	return func(o *dialOpts) { o.dialer = d }
}

// Connect dials wsURL, sends the authentication_challenge frame, and waits
// for the server's "OK" / "hello" response. On success the returned wsConn
// is ready to receive event frames.
//
// authResultPredicate: callers MAY override the auth-success check (default
// accepts status == "OK" or event == "hello"). Connect blocks until either
// auth succeeds, the server returns a FAIL status, or ctx is cancelled.
func Connect(ctx context.Context, wsURL, token string, opts ...DialOption) (*wsConn, error) {
	o := &dialOpts{dialer: websocket.DefaultDialer}
	for _, opt := range opts {
		opt(o)
	}
	conn, _, err := o.dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mattermost ws dial: %w", err)
	}

	// Send authentication challenge per the Mattermost WS protocol.
	authMsg := map[string]any{
		"seq":    1,
		"action": "authentication_challenge",
		"data":   map[string]any{"token": token},
	}
	if err := conn.WriteJSON(authMsg); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("mattermost ws auth send: %w", err)
	}

	// Wait for first OK / hello response. Mattermost can interleave the
	// auth response with prior buffered events; loop until a definitive
	// status emerges or the context cancels.
	authCh := make(chan error, 1)
	go func() {
		for {
			var ev WSEvent
			if err := conn.ReadJSON(&ev); err != nil {
				authCh <- fmt.Errorf("mattermost ws read auth: %w", err)
				return
			}
			if ev.Status == "OK" || ev.Event == "hello" {
				authCh <- nil
				return
			}
			if ev.Status == "FAIL" {
				authCh <- ErrAuthFailed
				return
			}
			// Other server-pushed events before auth confirmation are
			// ignored — Mattermost sometimes sends "hello" via the
			// event field (no Status) and other times via Status "OK".
		}
	}()

	select {
	case <-ctx.Done():
		_ = conn.Close()
		return nil, ctx.Err()
	case err := <-authCh:
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
	case <-time.After(15 * time.Second):
		_ = conn.Close()
		return nil, fmt.Errorf("mattermost ws auth timeout")
	}

	return &wsConn{conn: conn, closed: make(chan struct{})}, nil
}

// ErrAuthFailed indicates the Mattermost server rejected the access token.
// The adapter reports this in /router/health and refuses to reconnect.
var ErrAuthFailed = errors.New("mattermost websocket authentication failed")

// ReadEvent reads one event from the connection. Returns io.EOF-style
// errors on close. Safe to call from a single reader goroutine.
func (c *wsConn) ReadEvent() (WSEvent, error) {
	var ev WSEvent
	_, raw, err := c.conn.ReadMessage()
	if err != nil {
		return WSEvent{}, err
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		// Don't fail the loop on a malformed event — log via the
		// caller. Returning a zero event with a sentinel error lets
		// the caller decide whether to skip or escalate.
		return WSEvent{}, fmt.Errorf("mattermost ws decode: %w", err)
	}
	return ev, nil
}

// Close shuts down the websocket. Safe to call multiple times.
func (c *wsConn) Close() error {
	select {
	case <-c.closed:
		return nil
	default:
	}
	close(c.closed)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	return c.conn.Close()
}

// ParsePostFromEvent extracts a Post from a "posted" event's data.post
// JSON-encoded string. Returns nil + an error if the field is missing or
// malformed.
func ParsePostFromEvent(ev WSEvent) (*Post, error) {
	if ev.Event != "posted" {
		return nil, fmt.Errorf("mattermost: event is %q, not posted", ev.Event)
	}
	raw, ok := ev.Data["post"]
	if !ok {
		return nil, errors.New("mattermost: posted event missing data.post")
	}
	s, ok := raw.(string)
	if !ok {
		return nil, errors.New("mattermost: posted event data.post is not a string")
	}
	var p Post
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, fmt.Errorf("mattermost: unmarshal post payload: %w", err)
	}
	return &p, nil
}

// EventChannelType returns the channel_type field from a posted event, or "" if absent.
func EventChannelType(ev WSEvent) string {
	if v, ok := ev.Data["channel_type"].(string); ok {
		return v
	}
	return ""
}
