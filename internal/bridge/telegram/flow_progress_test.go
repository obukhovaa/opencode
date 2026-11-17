package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"github.com/opencode-ai/opencode/internal/bridge"
)

func telegramFlowFixture() bridge.FlowProgress {
	return bridge.FlowProgress{FlowID: "review", Status: "running", Full: true, Steps: []bridge.FlowProgressStep{{ID: "inspect", Status: "completed", IsStructOutput: true, Output: `{"answer":"<@all> & 😺"}`}}}
}

func TestFlowProgressTelegramRequests(t *testing.T) {
	calls := []url.Values{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		calls = append(calls, r.Form)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "getMe") {
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test"}}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":7,"chat":{"id":123,"type":"private"},"date":1,"text":"ok"}}`)
	}))
	defer server.Close()
	a, err := New(Identity{ID: "test", Token: "test"}, Options{ServerURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	peer := bridge.PeerRef{PeerID: "123"}
	p := telegramFlowFixture()
	token, err := a.SendFlowProgress(context.Background(), peer, p)
	if err != nil {
		t.Fatal(err)
	}
	p.Full = false
	if err = a.EditFlowProgress(context.Background(), peer, token, p); err != nil {
		t.Fatal(err)
	}
	sends := []url.Values{}
	for _, c := range calls {
		if c.Get("text") != "" {
			sends = append(sends, c)
		}
	}
	if len(sends) != 2 || !strings.Contains(sends[0].Get("link_preview_options"), `"is_disabled":true`) || !strings.Contains(sends[1].Get("link_preview_options"), `"is_disabled":true`) || sends[0].Get("parse_mode") != string(models.ParseModeHTML) || !strings.Contains(sends[0].Get("text"), "<blockquote expandable>") || sends[1].Get("message_id") != "7" || strings.Contains(sends[1].Get("text"), "answer") {
		t.Fatal(sends)
	}
}

func TestFlowProgressTelegramBoundsAndEscaping(t *testing.T) {
	p := telegramFlowFixture()
	for i := 0; i < 100; i++ {
		p.Steps = append(p.Steps, bridge.FlowProgressStep{ID: fmt.Sprint(i), Status: "completed", Output: strings.Repeat("<tg-spoiler>😺&@all```", 500)})
	}
	text := flowHTML(p)
	visible := strings.ReplaceAll(strings.ReplaceAll(text, "<blockquote expandable>", ""), "</blockquote>", "")
	visible = html.UnescapeString(visible)
	if !utf8.ValidString(text) || bridge.FlowTextLen(visible) > 4096 || strings.Contains(text, "<tg-spoiler>") || strings.Contains(text, "@all") || strings.Count(text, "<blockquote expandable>") != strings.Count(text, "</blockquote>") {
		t.Fatal("invalid or oversized message", len(text), bridge.FlowTextLen(visible))
	}
}

func TestFlowProgressTelegramMissingVersusTransient(t *testing.T) {
	for _, message := range []string{"Bad Request: message to edit not found", "Bad Request: message is not modified", "Too Many Requests: retry later"} {
		t.Run(message, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "getMe") {
					fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test"}}`)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": message})
			}))
			defer server.Close()
			a, err := New(Identity{ID: "test", Token: "test"}, Options{ServerURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			err = a.EditFlowProgress(context.Background(), bridge.PeerRef{PeerID: "123"}, "7", telegramFlowFixture())
			if strings.Contains(message, "not found") && !errors.Is(err, bridge.ErrFlowMessageNotFound) {
				t.Fatal(err)
			}
			if strings.Contains(message, "not modified") && err != nil {
				t.Fatal(err)
			}
			if strings.Contains(message, "Too Many") && (err == nil || errors.Is(err, bridge.ErrFlowMessageNotFound)) {
				t.Fatal(err)
			}
		})
	}
}

func TestFlowProgressTelegramProseInsideExpandableQuote(t *testing.T) {
	p := telegramFlowFixture()
	p.Steps[0].IsStructOutput = false
	p.Steps[0].Output = "**Bold** with `code`\n\n> nested quote\n\n```go\nx := 1\n```\n\n[unsafe](tg://user?id=123)"
	text := flowHTML(p)
	if !strings.Contains(text, "<b>Bold</b>") || strings.Contains(text, "<code") || strings.Contains(text, "<pre") || strings.Contains(text, "<a ") || strings.Contains(text, "<blockquote>") {
		t.Fatal(text)
	}
	formatted, n := flowInlineProse("**"+strings.Repeat("😺", 1000)+"**", 100)
	if n > 100 || !strings.HasSuffix(formatted, "</b>") || !strings.Contains(formatted, "truncated") {
		t.Fatal(formatted, n)
	}
}

func TestFlowProgressTelegramHTMLFallback(t *testing.T) {
	a, mock, _ := newAdapter(t, Identity{ID: "test", Token: "test"})
	mock.sendMessageErrors = []string{"Bad Request: can't parse entities: Unsupported start tag"}
	token, err := a.SendFlowProgress(context.Background(), bridge.PeerRef{PeerID: "123"}, telegramFlowFixture())
	if err != nil || token == "" {
		t.Fatal(token, err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.sendMsg) != 2 || mock.sendMsg[0].ParseMode != "HTML" || mock.sendMsg[1].ParseMode != "" || strings.Contains(mock.sendMsg[1].Text, "<blockquote") {
		t.Fatal(mock.sendMsg)
	}
}

func TestFlowProgressTelegramKeepsProseLinkTargets(t *testing.T) {
	p := telegramFlowFixture()
	p.Steps[0].IsStructOutput = false
	p.Steps[0].Output = "Review [**MR**](https://gitlab.example/mr/42)"
	text := flowHTML(p)
	if !strings.Contains(text, "https://gitlab.example/mr/42") || strings.Contains(text, "<a ") {
		t.Fatal(text)
	}
}
