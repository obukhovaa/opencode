package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

// TestTelegramSingleSelectCallbackUpdatesAnsweredWidget covers the
// single-select path: after the callback pushes the inbound, the adapter
// MUST clear the inline keyboard AND prefix the message text with the
// "✓ Answered: <label>" confirmation.
func TestTelegramSingleSelectCallbackUpdatesAnsweredWidget(t *testing.T) {
	t.Parallel()
	a, mock, inbound := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	a.handleCallbackQuery(context.Background(), &models.CallbackQuery{
		ID:   "cbq-1",
		Data: "Yes",
		From: models.User{ID: 42},
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{
				ID:   77,
				Chat: models.Chat{ID: 555, Type: "private"},
				Text: "Ship it?",
			},
		},
	})

	// Inbound must have been pushed first.
	in := receiveOne(t, inbound, time.Second)
	if in.Text != "Yes" {
		t.Fatalf("Inbound.Text = %q, want %q", in.Text, "Yes")
	}

	// Allow the goroutine the bot library spawns for outbound API calls
	// to drain its work before asserting on the mock.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mock.mu.Lock()
		gotMarkup := len(mock.editMessageMarkup) > 0
		gotText := len(mock.editMessageText) > 0
		mock.mu.Unlock()
		if gotMarkup && gotText {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.editMessageMarkup) == 0 {
		t.Fatal("expected editMessageReplyMarkup to clear keyboard")
	}
	if mc := mock.editMessageMarkup[0]; mc.Markup != "" {
		t.Errorf("expected empty reply_markup to clear keyboard, got %q", mc.Markup)
	}
	if len(mock.editMessageText) == 0 {
		t.Fatal("expected editMessageText to prefix confirmation")
	}
	got := mock.editMessageText[0].Text
	wantPrefix := "✓ Answered: Yes\n\n"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("text prefix mismatch: got %q, want prefix %q", got, wantPrefix)
	}
	if !strings.Contains(got, "Ship it?") {
		t.Errorf("original prompt preserved? got %q", got)
	}
}

// TestTelegramMultiSelectSubmitUpdatesAnsweredWidget covers the
// multi-select Submit branch end-to-end.
func TestTelegramMultiSelectSubmitUpdatesAnsweredWidget(t *testing.T) {
	t.Parallel()
	a, mock, inbound := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	// Pre-populate multi-select state — Submit reads from it.
	a.multiSelectStates().put(100, &multiSelectEntry{
		Order:    []string{"auth", "billing", "ui"},
		Labels:   map[string]string{"auth": "auth", "billing": "billing", "ui": "ui"},
		Selected: map[string]bool{"auth": true, "ui": true},
	})

	a.handleCallbackQuery(context.Background(), &models.CallbackQuery{
		ID:   "cbq-multi",
		Data: "ms:submit",
		From: models.User{ID: 42},
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{
				ID:   100,
				Chat: models.Chat{ID: 555, Type: "private"},
				Text: "Pick caps",
			},
		},
	})

	in := receiveOne(t, inbound, time.Second)
	if in.Text != "auth, ui" {
		t.Fatalf("Inbound.Text = %q, want %q", in.Text, "auth, ui")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mock.mu.Lock()
		gotText := len(mock.editMessageText) > 0
		mock.mu.Unlock()
		if gotText {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.editMessageText) == 0 {
		t.Fatal("expected editMessageText to prefix confirmation on Submit")
	}
	got := mock.editMessageText[0].Text
	if !strings.HasPrefix(got, "✓ Answered: auth, ui\n\n") {
		t.Errorf("prefix mismatch: got %q", got)
	}
}

// TestTelegramToggleClickDoesNotEmitConfirmation ensures intermediate
// toggle clicks (ms:t:<i>) don't fire the answered-widget update — the
// keyboard MUST redraw with the new tick state, no confirmation prefix.
func TestTelegramToggleClickDoesNotEmitConfirmation(t *testing.T) {
	t.Parallel()
	a, mock, inbound := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	a.multiSelectStates().put(200, &multiSelectEntry{
		Order:    []string{"auth", "billing"},
		Labels:   map[string]string{"auth": "auth", "billing": "billing"},
		Selected: map[string]bool{},
	})

	a.handleCallbackQuery(context.Background(), &models.CallbackQuery{
		ID:   "cbq-toggle",
		Data: "ms:t:0",
		From: models.User{ID: 42},
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{
				ID:   200,
				Chat: models.Chat{ID: 555, Type: "private"},
				Text: "Pick caps",
			},
		},
	})

	// Toggle must NOT emit an inbound.
	select {
	case got := <-inbound:
		t.Fatalf("toggle should not emit inbound, got %+v", got)
	case <-time.After(150 * time.Millisecond):
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.editMessageText) != 0 {
		t.Fatalf("toggle MUST NOT call editMessageText, got %d calls", len(mock.editMessageText))
	}
}
