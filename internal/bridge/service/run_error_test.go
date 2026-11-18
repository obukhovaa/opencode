package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
)

// gateway502 is the error a pre-stream 502 from an nginx ingress in front
// of the model gateway surfaces as, once the agent loop has wrapped it:
// the SDK appends the proxy's HTML page to the status line verbatim.
// It is the failure that ended a daemon run on 2026-11-18 at 14:59:33Z
// with nothing in Slack but "✗ Run failed · 7 tool calls · …".
var gateway502 = fmt.Errorf("failed to process events: %w", errors.New(
	`POST "https://litellm.ai.pianocore.net/bedrock/v1/messages": 502 Bad Gateway <html>`+"\r\n"+
		`<head><title>502 Bad Gateway</title></head>`+"\r\n"+
		`<body>`+"\r\n"+`<center><h1>502 Bad Gateway</h1></center>`+"\r\n"+
		`<hr><center>nginx</center>`+"\r\n"+`</body>`+"\r\n"+`</html>`+"\r\n"))

const gateway502Reason = `failed to process events: POST "https://litellm.ai.pianocore.net/bedrock/v1/messages": 502 Bad Gateway`

func TestSanitizeRunError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "plain error kept", err: errors.New("boom"), want: "boom"},
		{name: "HTML gateway page dropped", err: gateway502, want: gateway502Reason},
		{
			name: "doctype page, any case",
			err:  errors.New("POST \"https://gw/v1/messages\": 504 Gateway Timeout <!DOCTYPE html>\n<html><body>upstream timed out</body></html>"),
			want: `POST "https://gw/v1/messages": 504 Gateway Timeout`,
		},
		{
			name: "error that is only a page",
			err:  errors.New("<html><body><h1>502 Bad Gateway</h1></body></html>"),
			want: "the model endpoint answered with an HTML error page",
		},
		{
			name: "query string and fragment stripped",
			err:  errors.New(`GET "https://example.com/v1/models?api_key=sk-secret&x=1#frag": 401 Unauthorized`),
			want: `GET "https://example.com/v1/models": 401 Unauthorized`,
		},
		{
			name: "JSON error body kept on one line",
			err:  errors.New("POST \"https://api.anthropic.com/v1/messages\": 500 Internal Server Error\n{\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"Internal server error\"}}"),
			want: `POST "https://api.anthropic.com/v1/messages": 500 Internal Server Error {"type":"error","error":{"type":"api_error","message":"Internal server error"}}`,
		},
		{
			name: "long reason capped",
			err:  errors.New(strings.Repeat("x", toolErrorPreviewRunes+50)),
			want: strings.Repeat("x", toolErrorPreviewRunes) + "…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeRunError(tt.err)
			if got != tt.want {
				t.Errorf("sanitizeRunError() = %q; want %q", got, tt.want)
			}
			if strings.ContainsAny(got, "\r\n") || strings.Contains(strings.ToLower(got), "<html") {
				t.Errorf("sanitizeRunError() = %q; want one line without HTML", got)
			}
		})
	}
}

func TestIsCancelledRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"request cancelled", agentpkg.ErrRequestCancelled, true},
		{"wrapped context cancel", fmt.Errorf("failed to process events: %w", context.Canceled), true},
		{"gateway 502", gateway502, false},
		{"deadline is a failure", context.DeadlineExceeded, false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isCancelledRun(tt.err); got != tt.want {
				t.Errorf("isCancelledRun(%v) = %v; want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestRunProgress_RunFailedHeaderReason: the terminal "✗ Run failed" line
// carries the run's reason; an OK run never shows one, and only the first
// finish decides.
func TestRunProgress_RunFailedHeaderReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status string
		reason string
		want   string
	}{
		{
			name:   "error with reason",
			status: progressStatusError,
			reason: "LLM gateway: 502 Bad Gateway",
			want:   "✗ Run failed · 7 tool calls · 1m02s · LLM gateway: 502 Bad Gateway",
		},
		{
			name:   "error without reason",
			status: progressStatusError,
			want:   "✗ Run failed · 7 tool calls · 1m02s",
		},
		{
			name:   "ok ignores a reason",
			status: progressStatusOK,
			reason: "should not show",
			want:   "✓ Done · 7 tool calls · 1m02s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := fixedClock(62 * time.Second)
			for i := 0; i < 7; i++ {
				p.toolDone(fmt.Sprintf("toolu_%d", i), "read", "", false, "")
			}
			p.finishWithReason(tt.status, tt.reason)
			p.finishWithReason(progressStatusError, "a later finish must not win")
			f := p.snapshot()
			if f.text != tt.want {
				t.Errorf("card = %q; want %q", f.text, tt.want)
			}
			if !f.final {
				t.Error("snapshot after finish is not final")
			}
		})
	}
}

// terminalAgent is an agent.Service whose Run reports `tools` successful
// tool calls through the dispatcher (once the run's card is open) and
// then ends with ev.
type terminalAgent struct {
	agentpkg.Service
	d     *sessionDispatch
	tools int
	ev    agentpkg.AgentEvent
}

func (a *terminalAgent) Run(
	_ context.Context, _, _ string, _ int, _ ...message.Attachment,
) (<-chan agentpkg.AgentEvent, error) {
	ch := make(chan agentpkg.AgentEvent)
	go func() {
		defer close(ch)
		if a.tools > 0 {
			// progressStart runs after Run returns; wait for the card.
			deadline := time.Now().Add(3 * time.Second)
			for a.d.progress.Load() == nil && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			for i := 0; i < a.tools; i++ {
				id := fmt.Sprintf("toolu_%02d", i)
				a.d.handlePartEvent(partEvent("S1", message.ToolCall{ID: id, Name: "read", Input: "{}", Finished: true}), nil)
				a.d.handlePartEvent(partEvent("S1", message.ToolResult{ToolCallID: id, Name: "read", Content: "ok"}), nil)
			}
		}
		ch <- a.ev
	}()
	return ch, nil
}

func sentTexts(outs []bridge.Outbound) []string {
	texts := make([]string, 0, len(outs))
	for _, o := range outs {
		texts = append(texts, o.Text)
	}
	return texts
}

func countFailureLines(texts []string) int {
	n := 0
	for _, s := range texts {
		if strings.HasPrefix(s, "⚠️ The run failed") {
			n++
		}
	}
	return n
}

// TestHandleInbound_RunErrorSurfacesReason drives handleInbound end to end
// through a run's terminal error and checks what reaches chat: the card's
// terminal line, and the failure reply sent to every bound peer.
func TestHandleInbound_RunErrorSurfacesReason(t *testing.T) {
	old := progressMinInterval
	progressMinInterval = 10 * time.Millisecond
	t.Cleanup(func() { progressMinInterval = old })

	wantLine := runFailureLine(gateway502Reason)
	tests := []struct {
		name         string
		toolUpdates  bool
		tools        int
		ev           agentpkg.AgentEvent
		wantCard     string // terminal card text; "" when no card is expected
		wantReplies  []string
		wantFailures int
	}{
		{
			// The 2026-11-18 incident: seven good tool calls, then the
			// model call died on a gateway 502 before any text.
			name:         "gateway 502 after seven tool calls",
			toolUpdates:  true,
			tools:        7,
			ev:           agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeError, Error: gateway502},
			wantCard:     "✗ Run failed · 7 tool calls · <t> · " + gateway502Reason,
			wantReplies:  []string{wantLine},
			wantFailures: 1,
		},
		{
			name:        "error carrying partial text delivers the text only",
			toolUpdates: true,
			ev: agentpkg.AgentEvent{
				Type:  agentpkg.AgentEventTypeError,
				Error: gateway502,
				Message: message.Message{Parts: []message.ContentPart{
					message.TextContent{Text: "Here is what I found so far."},
				}},
			},
			wantCard:     "✗ Run failed · 0 tool calls · <t> · " + gateway502Reason,
			wantReplies:  []string{"Here is what I found so far."},
			wantFailures: 0,
		},
		{
			name:         "cancelled run posts no failure line and no reason",
			toolUpdates:  true,
			tools:        2,
			ev:           agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeError, Error: agentpkg.ErrRequestCancelled},
			wantCard:     "✗ Run failed · 2 tool calls · <t>",
			wantReplies:  []string{},
			wantFailures: 0,
		},
		{
			// No card at all: the failure line is the only signal.
			name:         "tool updates off still gets the failure line",
			toolUpdates:  false,
			ev:           agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeError, Error: gateway502},
			wantReplies:  []string{wantLine},
			wantFailures: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, ed, relay := newProgressTestSvc(t, &bridge.Config{ToolUpdatesEnabled: tt.toolUpdates})
			d := newBareDispatch(svc, "S1")
			ag := &terminalAgent{d: d, tools: tt.tools, ev: tt.ev}
			svc.app.PrimaryAgents = map[config.AgentName]agentpkg.Service{config.AgentCoder: ag}
			svc.app.PrimaryAgentKeys = []config.AgentName{config.AgentCoder}

			d.handleInbound(context.Background(), testInbound("do the thing"))

			if tt.wantCard == "" {
				if posts := ed.Posts(); len(posts) != 0 {
					t.Errorf("card posts = %q; want none with tool updates off", posts)
				}
			} else {
				edits := ed.Edits()
				if len(edits) == 0 {
					t.Fatal("card was never edited to a terminal state")
				}
				last := elideElapsed(edits[len(edits)-1])
				if last != tt.wantCard {
					t.Errorf("terminal card = %q; want %q (elapsed elided)", last, tt.wantCard)
				}
			}

			// Both bound peers get the same replies: the Slack-like peer
			// next to its card, and the relay peer, which has no card.
			for peer, sends := range map[string][]bridge.Outbound{"editor": ed.Sends(), "relay": relay.Sends()} {
				texts := sentTexts(sends)
				if got := countFailureLines(texts); got != tt.wantFailures {
					t.Errorf("%s: failure lines = %d; want %d (sends %q)", peer, got, tt.wantFailures, texts)
				}
				if strings.Join(texts, "\n") != strings.Join(tt.wantReplies, "\n") {
					t.Errorf("%s: sends = %q; want %q", peer, texts, tt.wantReplies)
				}
				for _, s := range texts {
					if strings.Contains(strings.ToLower(s), "<html") {
						t.Errorf("%s: HTML reached chat: %q", peer, s)
					}
				}
			}
		})
	}
}

// elideElapsed blanks the elapsed-time field of a terminal card line
// ("… · 12s · …" → "… · <t> · …") so a wall-clock run compares exactly.
func elideElapsed(card string) string {
	parts := strings.Split(card, " · ")
	for i, p := range parts {
		if _, err := time.ParseDuration(p); err == nil && i > 0 {
			parts[i] = "<t>"
			break
		}
	}
	return strings.Join(parts, " · ")
}

// TestHandleHeartbeatTerminal_GatewayErrorIsSanitised: a beat's failure
// line still comes from the heartbeat path, once, with the same
// sanitised reason and no HTML.
func TestHandleHeartbeatTerminal_GatewayErrorIsSanitised(t *testing.T) {
	svc, ed, relay := newProgressTestSvc(t, &bridge.Config{ToolUpdatesEnabled: true})
	d := newBareDispatch(svc, "S1")
	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}
	hb := &bridge.HeartbeatTurn{At: time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)}

	d.handleHeartbeatTerminal(context.Background(), peer, hb,
		agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeError, Error: gateway502})

	texts := sentTexts(ed.Sends())
	want := "Heartbeat started at 14:00Z and failed: " + gateway502Reason
	if len(texts) != 1 || texts[0] != want {
		t.Errorf("heartbeat sends = %q; want exactly [%q]", texts, want)
	}
	if n := countFailureLines(texts); n != 0 {
		t.Errorf("heartbeat posted %d chat failure lines; want 0 (it has its own)", n)
	}
	if got := relay.Sends(); len(got) != 0 {
		t.Errorf("relay sends = %q; want none (a beat replies to its own peer)", sentTexts(got))
	}
}
