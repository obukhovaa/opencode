package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
)

// resetServer is an HTTP/2 TLS server that aborts the first streaming request
// after the response headers (an RST_STREAM(INTERNAL_ERROR) on the wire) and
// answers every later one with ok. conns counts accepted TCP connections.
type resetServer struct {
	*httptest.Server
	conns    atomic.Int64
	requests atomic.Int64
}

func newResetServer(t *testing.T, ok func(w http.ResponseWriter)) *resetServer {
	t.Helper()
	rs := &resetServer{}
	rs.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("request proto = %s, want HTTP/2", r.Proto)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if rs.requests.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		ok(w)
	}))
	rs.EnableHTTP2 = true
	rs.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			rs.conns.Add(1)
		}
	}
	rs.StartTLS()
	t.Cleanup(rs.Close)
	return rs
}

func (rs *resetServer) transport(t *testing.T) *http.Transport {
	t.Helper()
	tr, ok := rs.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("test server transport is %T", rs.Client().Transport)
	}
	return tr
}

func drainStream(t *testing.T, client ProviderClient) *ProviderResponse {
	t.Helper()
	prompt := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
	var final *ProviderResponse
	for ev := range client.stream(context.Background(), prompt, nil) {
		switch ev.Type {
		case EventComplete:
			final = ev.Response
		case EventError:
			t.Fatalf("stream error: %v", ev.Error)
		}
	}
	if final == nil {
		t.Fatal("no EventComplete")
	}
	return final
}

func anthropicOK(w http.ResponseWriter) {
	events := []struct{ typ, data string }{
		{"message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
	for _, e := range events {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.typ, e.data)
	}
}

func openaiOK(w http.ResponseWriter) {
	fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"pong"},"finish_reason":null}]}`+"\n\n")
	fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// D5 (CD-5305): the RST_STREAM retry used to be multiplexed back onto the
// pooled HTTP/2 connection that had just reset. The retry must dial a new one.
func TestRSTStreamRetryUsesNewConnection(t *testing.T) {
	providers := []struct {
		name  string
		ok    func(http.ResponseWriter)
		build func(srv *resetServer) (ProviderClient, *connResetter)
	}{
		{"anthropic", anthropicOK, func(srv *resetServer) (ProviderClient, *connResetter) {
			c := newAnthropicClient(providerClientOptions{apiKey: "k", baseURL: srv.URL, baseTransport: srv.transport(t),
				model: models.AnthropicModels[models.Claude55Sonnet], maxTokens: 1024}).(*anthropicClient)
			return c, c.conns
		}},
		{"openai", openaiOK, func(srv *resetServer) (ProviderClient, *connResetter) {
			c := newOpenAIClient(providerClientOptions{apiKey: "k", baseURL: srv.URL, baseTransport: srv.transport(t),
				model: models.SupportedModels[models.GPT5], maxTokens: 1024}).(*openaiClient)
			return c, c.conns
		}},
	}
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			loadConfigIn(t, t.TempDir())
			srv := newResetServer(t, p.ok)
			client, conns := p.build(srv)

			resp := drainStream(t, client)

			if resp.Content != "pong" {
				t.Errorf("content = %q, want pong", resp.Content)
			}
			if got := srv.requests.Load(); got != 2 {
				t.Errorf("requests = %d, want 2 (reset, then retry)", got)
			}
			if got := srv.conns.Load(); got != 2 {
				t.Errorf("connections = %d, want 2: the retry must not reuse the reset connection", got)
			}
			if got := conns.Generation(); got != 2 {
				t.Errorf("connection generation = %d, want 2", got)
			}
		})
	}
}

// Control: without rotation the same server shows the defect (one connection
// carries both requests), so the test above can fail.
func TestRSTStreamRetryWithoutRotationReusesConnection(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	srv := newResetServer(t, anthropicOK)
	c := newAnthropicClient(providerClientOptions{apiKey: "k", baseURL: srv.URL, baseTransport: srv.transport(t),
		model: models.AnthropicModels[models.Claude55Sonnet], maxTokens: 1024}).(*anthropicClient)
	c.conns = nil

	drainStream(t, c)

	if got := srv.conns.Load(); got != 1 {
		t.Errorf("connections = %d, want 1 when the connection is not rotated", got)
	}
}

func TestConnResetterRotate(t *testing.T) {
	t.Run("rotates once per observed generation", func(t *testing.T) {
		c := newConnResetter(nil)
		if g := c.Generation(); g != 1 {
			t.Fatalf("initial generation = %d, want 1", g)
		}
		if got := c.Rotate(1); got != 2 {
			t.Errorf("Rotate(1) = %d, want 2", got)
		}
		// A second failure observed on generation 1 must not rotate again.
		if got := c.Rotate(1); got != 2 {
			t.Errorf("stale Rotate(1) = %d, want 2", got)
		}
		if got := c.Rotate(2); got != 3 {
			t.Errorf("Rotate(2) = %d, want 3", got)
		}
	})

	t.Run("owns its transport", func(t *testing.T) {
		c := newConnResetter(nil)
		if c.cur.Load().tr == http.DefaultTransport {
			t.Error("resetter must not run on the global default transport")
		}
	})

	t.Run("nil resetter is inert", func(t *testing.T) {
		var c *connResetter
		if c.Generation() != 0 || c.Rotate(0) != 0 {
			t.Error("nil resetter must report generation 0")
		}
	})
}

func TestStreamIDFromError(t *testing.T) {
	tests := []struct {
		err  error
		want uint32
	}{
		{errors.New("stream error: stream ID 17; INTERNAL_ERROR; received from peer"), 17},
		{fmt.Errorf("wrapped: %w", errors.New("stream error: stream ID 3; REFUSED_STREAM; received from peer")), 3},
		{errors.New("connection reset by peer"), 0},
		{nil, 0},
	}
	for _, tt := range tests {
		if got := streamIDFromError(tt.err); got != tt.want {
			t.Errorf("streamIDFromError(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}
