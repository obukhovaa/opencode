package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	tgmodels "github.com/go-telegram/bot/models"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// captureTextHandler hooks sendMessage to capture the text form-field
// so render-tests can verify MarkdownV2 / Markdown content.
var (
	capturedTextMu sync.Mutex
	capturedTextJ  []string
)

func capturedTexts() []string {
	capturedTextMu.Lock()
	defer capturedTextMu.Unlock()
	out := make([]string, len(capturedTextJ))
	copy(out, capturedTextJ)
	return out
}

func textCapturingHandler(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			if err := r.ParseMultipartForm(64 << 20); err == nil {
				if vals := r.MultipartForm.Value["text"]; len(vals) > 0 {
					capturedTextMu.Lock()
					capturedTextJ = append(capturedTextJ, vals[0])
					capturedTextMu.Unlock()
				}
			}
		}
		inner.ServeHTTP(w, r)
	})
}

func resetCapturedTexts() {
	capturedTextMu.Lock()
	capturedTextJ = nil
	capturedTextMu.Unlock()
}

func TestTelegramRenderToolCall(t *testing.T) {
	resetCapturedTexts()
	mock := newMockServer(t)
	mock.server.Config.Handler = textCapturingHandler(mock.server.Config.Handler)

	a, err := New(Identity{ID: "default", Token: "t"}, Options{ServerURL: mock.URL()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetMe(&tgmodels.User{ID: 999, Username: "routerbot", IsBot: true})
	t.Cleanup(func() { _ = a.Stop() })

	res := a.Render(context.Background(),
		bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		bridge.NewToolCallHint("bash", "abc", map[string]string{"command": "ls"}),
	)
	if res.Err != nil {
		t.Fatalf("Render: %v", res.Err)
	}
	captures := capturedTexts()
	if len(captures) == 0 {
		t.Fatalf("no text captured")
	}
	last := captures[len(captures)-1]
	if !strings.Contains(last, "bash") || !strings.Contains(last, "abc") || !strings.Contains(last, "⏳") {
		t.Errorf("tool-call text missing markers: %q", last)
	}
	if !strings.Contains(last, "command: ls") {
		t.Errorf("params not embedded: %q", last)
	}
}

func TestTelegramRenderToolResultPostsFreshWhenNoCache(t *testing.T) {
	resetCapturedTexts()
	mock := newMockServer(t)
	mock.server.Config.Handler = textCapturingHandler(mock.server.Config.Handler)

	a, err := New(Identity{ID: "default", Token: "t"}, Options{ServerURL: mock.URL()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetMe(&tgmodels.User{ID: 999, Username: "routerbot", IsBot: true})
	t.Cleanup(func() { _ = a.Stop() })

	res := a.Render(context.Background(),
		bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		bridge.NewToolResultHint("bash", "noseen", "error", "permission denied", 0),
	)
	if res.Err != nil {
		t.Fatalf("Render: %v", res.Err)
	}
	captures := capturedTexts()
	if len(captures) == 0 {
		t.Fatalf("no text captured")
	}
	last := captures[len(captures)-1]
	if !strings.Contains(last, "✗") || !strings.Contains(last, "bash") {
		t.Errorf("error-result text missing markers: %q", last)
	}
	if !strings.Contains(last, "permission denied") {
		t.Errorf("preview not in body: %q", last)
	}
}

func TestTelegramSendInteractiveMultiSelectKeyboardLayout(t *testing.T) {
	resetCapturedTexts()
	capturedMarkupMu.Lock()
	capturedMarkupJ = nil
	capturedMarkupMu.Unlock()

	mock := newMockServer(t)
	mock.server.Config.Handler = markupCapturingHandler(mock.server.Config.Handler)

	a, err := New(Identity{ID: "default", Token: "t"}, Options{ServerURL: mock.URL()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetMe(&tgmodels.User{ID: 999, Username: "routerbot", IsBot: true})
	t.Cleanup(func() { _ = a.Stop() })

	_, err = a.SendInteractiveMultiSelect(context.Background(),
		bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		"Pick capabilities",
		[]bridge.QuestionChoice{
			{Label: "auth", Value: "auth"},
			{Label: "billing", Value: "billing"},
			{Label: "ui", Value: "ui"},
		},
	)
	if err != nil {
		t.Fatalf("SendInteractiveMultiSelect: %v", err)
	}
	caps := capturedMarkups()
	if len(caps) == 0 {
		t.Fatalf("no reply_markup captured")
	}
	var mk struct {
		InlineKeyboard [][]struct {
			Text         string `json:"text"`
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal(caps[len(caps)-1], &mk); err != nil {
		t.Fatalf("decode markup: %v", err)
	}
	if len(mk.InlineKeyboard) != 4 { // 3 options + Submit row
		t.Fatalf("rows = %d; want 4 (3 toggles + Submit)", len(mk.InlineKeyboard))
	}
	// First toggle row.
	if mk.InlineKeyboard[0][0].CallbackData != "ms:t:0" {
		t.Errorf("toggle 0 callback_data = %q; want ms:t:0", mk.InlineKeyboard[0][0].CallbackData)
	}
	// Submit row.
	submit := mk.InlineKeyboard[3][0]
	if submit.Text != "Submit" || submit.CallbackData != "ms:submit" {
		t.Errorf("submit row = %+v; want {Submit, ms:submit}", submit)
	}
}

func TestMultiSelectStateToggleAndExpire(t *testing.T) {
	s := newMultiSelectState()
	s.ttl = 10 * time.Millisecond
	entry := &multiSelectEntry{
		Selected: map[string]bool{},
		Labels:   map[string]string{"a": "Option A"},
		Order:    []string{"a"},
	}
	s.put(42, entry)
	got, ok := s.get(42)
	if !ok {
		t.Fatal("expected state to be retrievable")
	}
	got.Selected["a"] = true
	s.put(42, got) // refresh TTL

	time.Sleep(15 * time.Millisecond)
	_, ok = s.get(42)
	if ok {
		t.Error("expected TTL eviction after 15ms with 10ms ttl")
	}
}
