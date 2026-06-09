package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// captureReplyMarkup hooks the mock's sendMessage handler to capture
// the reply_markup form field for inline-keyboard assertions.
var (
	capturedMarkupMu sync.Mutex
	capturedMarkupJ  [][]byte
)

func capturedMarkups() [][]byte {
	capturedMarkupMu.Lock()
	defer capturedMarkupMu.Unlock()
	out := make([][]byte, len(capturedMarkupJ))
	copy(out, capturedMarkupJ)
	return out
}

func markupCapturingHandler(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			if err := r.ParseMultipartForm(64 << 20); err == nil {
				if vals := r.MultipartForm.Value["reply_markup"]; len(vals) > 0 {
					capturedMarkupMu.Lock()
					capturedMarkupJ = append(capturedMarkupJ, []byte(vals[0]))
					capturedMarkupMu.Unlock()
				}
			}
		}
		inner.ServeHTTP(w, r)
	})
}

func TestTelegramSendInteractiveQuestionEmitsInlineKeyboard(t *testing.T) {
	t.Parallel()
	capturedMarkupMu.Lock()
	capturedMarkupJ = nil
	capturedMarkupMu.Unlock()

	mock := newMockServer(t)
	mock.server.Config.Handler = markupCapturingHandler(mock.server.Config.Handler)

	a, err := New(Identity{ID: "default", Token: "tg-token"}, Options{
		ServerURL: mock.URL(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetMe(&models.User{ID: 999, Username: "routerbot", IsBot: true})
	t.Cleanup(func() { _ = a.Stop() })

	err = a.SendInteractiveQuestion(context.Background(),
		bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		"Ship it?",
		[]bridge.QuestionChoice{
			{Label: "Yes", Value: "Yes"},
			{Label: "No", Value: "No"},
		},
	)
	if err != nil {
		t.Fatalf("SendInteractiveQuestion: %v", err)
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
		t.Fatalf("decode reply_markup: %v (%s)", err, caps[len(caps)-1])
	}
	if len(mk.InlineKeyboard) != 2 {
		t.Fatalf("rows = %d, want 2", len(mk.InlineKeyboard))
	}
	if mk.InlineKeyboard[0][0].Text != "Yes" || mk.InlineKeyboard[0][0].CallbackData != "Yes" {
		t.Errorf("row 0 = %+v", mk.InlineKeyboard[0][0])
	}
	if mk.InlineKeyboard[1][0].Text != "No" {
		t.Errorf("row 1 = %+v", mk.InlineKeyboard[1][0])
	}
}

func TestTelegramSendInteractiveQuestionInvalidPeerID(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, err := New(Identity{ID: "default", Token: "t"}, Options{ServerURL: mock.URL()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetMe(&models.User{ID: 999, Username: "routerbot", IsBot: true})
	t.Cleanup(func() { _ = a.Stop() })

	err = a.SendInteractiveQuestion(context.Background(),
		bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "@username"},
		"x", []bridge.QuestionChoice{{Label: "ok", Value: "ok"}},
	)
	if err == nil {
		t.Errorf("expected error for non-numeric peerId")
	}
}

func TestTelegramCallbackQueryRoutesAsInbound(t *testing.T) {
	t.Parallel()
	a, mock, inbound := newAdapter(t, Identity{ID: "default", Token: "tg-token"})
	_ = mock

	a.handleCallbackQuery(context.Background(), &models.CallbackQuery{
		ID:   "cbq-1",
		Data: "Yes",
		From: models.User{ID: 12345},
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{
				ID:   42,
				Chat: models.Chat{ID: 777, Type: "private"},
			},
		},
	})

	in := receiveOne(t, inbound, time.Second)
	if in.Text != "Yes" {
		t.Errorf("Text = %q, want %q (callback data)", in.Text, "Yes")
	}
	if in.Peer.PeerID != "777" {
		t.Errorf("PeerID = %q, want 777", in.Peer.PeerID)
	}
	if in.AuthorID != "12345" {
		t.Errorf("AuthorID = %q", in.AuthorID)
	}
}

// io import marker
var _ = io.EOF
