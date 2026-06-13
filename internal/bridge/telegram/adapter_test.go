package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// mockTelegramServer mimics the bot-API endpoints the adapter calls. It
// captures requests so tests can assert what was sent without going
// through the real Telegram service.
type mockTelegramServer struct {
	t                 *testing.T
	server            *httptest.Server
	mu                sync.Mutex
	sendMsg           []sendCall
	sendPh            []sendCall
	sendAud           []sendCall
	sendDoc           []sendCall
	getFile           []string
	editMessageText   []editTextCall
	editMessageMarkup []editMarkupCall
	// fileBody is what the adapter receives when downloading inbound
	// files (the /file/bot<token>/<path> endpoint).
	fileBody string
}

type editTextCall struct {
	ChatID    string
	MessageID string
	Text      string
}

type editMarkupCall struct {
	ChatID    string
	MessageID string
	// Markup is the raw reply_markup form value (empty when keyboard cleared).
	Markup string
}

type sendCall struct {
	ChatID  any
	Text    string
	Caption string
	// Multipart filename when the call is sendPhoto/sendAudio/sendDocument.
	Filename string
	// FileData contains the bytes sent in the multipart upload.
	FileData []byte
}

func newMockServer(t *testing.T) *mockTelegramServer {
	m := &mockTelegramServer{t: t, fileBody: "telegram-media"}
	mux := http.NewServeMux()

	// Bot API: every method comes in as POST /bot<token>/<method>. We
	// strip the prefix and dispatch on the trailing method name.
	mux.HandleFunc("/", m.handleBotMethod)
	m.server = httptest.NewServer(mux)
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockTelegramServer) URL() string { return m.server.URL }

func (m *mockTelegramServer) handleBotMethod(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// File download endpoint: /file/bot<token>/<filepath>.
	if strings.HasPrefix(path, "/file/bot") {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(m.fileBody))
		return
	}

	// Strip "/bot<token>/" prefix to find the method.
	idx := strings.LastIndex(path, "/")
	method := path[idx+1:]

	switch method {
	case "getMe":
		m.respond(w, models.User{ID: 999, Username: "routerbot", IsBot: true})
	case "getUpdates":
		// Return an empty update batch immediately so the long-poll
		// loop completes a cycle. Tests don't drive inbound through
		// here — they call dispatchUpdate directly.
		m.respond(w, []models.Update{})
	case "getFile":
		// The lib always sends multipart even for JSON-style methods,
		// so file_id arrives as a form field.
		fileID := ""
		if err := r.ParseMultipartForm(64 << 20); err == nil {
			if vals, ok := r.MultipartForm.Value["file_id"]; ok && len(vals) > 0 {
				fileID = vals[0]
			}
		}
		m.mu.Lock()
		m.getFile = append(m.getFile, fileID)
		m.mu.Unlock()
		m.respond(w, models.File{
			FileID:   fileID,
			FilePath: "downloads/" + fileID + ".bin",
		})
	case "sendMessage":
		call := captureSendMessage(r)
		m.mu.Lock()
		m.sendMsg = append(m.sendMsg, call)
		m.mu.Unlock()
		m.respond(w, models.Message{ID: 1})
	case "sendPhoto":
		call := captureMultipartCall(r, "photo")
		m.mu.Lock()
		m.sendPh = append(m.sendPh, call)
		m.mu.Unlock()
		m.respond(w, models.Message{ID: 2})
	case "sendAudio":
		call := captureMultipartCall(r, "audio")
		m.mu.Lock()
		m.sendAud = append(m.sendAud, call)
		m.mu.Unlock()
		m.respond(w, models.Message{ID: 3})
	case "sendDocument":
		call := captureMultipartCall(r, "document")
		m.mu.Lock()
		m.sendDoc = append(m.sendDoc, call)
		m.mu.Unlock()
		m.respond(w, models.Message{ID: 4})
	case "editMessageText":
		call := editTextCall{}
		if err := r.ParseMultipartForm(64 << 20); err == nil {
			if vals := r.MultipartForm.Value["chat_id"]; len(vals) > 0 {
				call.ChatID = vals[0]
			}
			if vals := r.MultipartForm.Value["message_id"]; len(vals) > 0 {
				call.MessageID = vals[0]
			}
			if vals := r.MultipartForm.Value["text"]; len(vals) > 0 {
				call.Text = vals[0]
			}
		}
		m.mu.Lock()
		m.editMessageText = append(m.editMessageText, call)
		m.mu.Unlock()
		m.respond(w, true)
	case "editMessageReplyMarkup":
		call := editMarkupCall{}
		if err := r.ParseMultipartForm(64 << 20); err == nil {
			if vals := r.MultipartForm.Value["chat_id"]; len(vals) > 0 {
				call.ChatID = vals[0]
			}
			if vals := r.MultipartForm.Value["message_id"]; len(vals) > 0 {
				call.MessageID = vals[0]
			}
			if vals := r.MultipartForm.Value["reply_markup"]; len(vals) > 0 {
				call.Markup = vals[0]
			}
		}
		m.mu.Lock()
		m.editMessageMarkup = append(m.editMessageMarkup, call)
		m.mu.Unlock()
		m.respond(w, true)
	case "answerCallbackQuery":
		m.respond(w, true)
	default:
		http.Error(w, "unknown method "+method, http.StatusNotFound)
	}
}

// respond writes a Telegram-format API envelope.
func (m *mockTelegramServer) respond(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":     true,
		"result": result,
	})
}

// captureSendMessage parses sendMessage's multipart form (the bot library
// uses multipart for every method, not JSON).
func captureSendMessage(r *http.Request) sendCall {
	call := sendCall{}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		return call
	}
	if vals, ok := r.MultipartForm.Value["chat_id"]; ok && len(vals) > 0 {
		call.ChatID = vals[0]
	}
	if vals, ok := r.MultipartForm.Value["text"]; ok && len(vals) > 0 {
		call.Text = vals[0]
	}
	if vals, ok := r.MultipartForm.Value["caption"]; ok && len(vals) > 0 {
		call.Caption = vals[0]
	}
	return call
}

func captureMultipartCall(r *http.Request, fieldName string) sendCall {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		return sendCall{}
	}
	call := sendCall{}
	if vals, ok := r.MultipartForm.Value["chat_id"]; ok && len(vals) > 0 {
		call.ChatID = vals[0]
	}
	if vals, ok := r.MultipartForm.Value["caption"]; ok && len(vals) > 0 {
		call.Caption = vals[0]
	}
	if files, ok := r.MultipartForm.File[fieldName]; ok && len(files) > 0 {
		call.Filename = files[0].Filename
		f, err := files[0].Open()
		if err == nil {
			data, _ := io.ReadAll(f)
			_ = f.Close()
			call.FileData = data
		}
	}
	return call
}

// --- adapter tests ---------------------------------------------------------

func TestNewMissingTokenErrors(t *testing.T) {
	t.Parallel()
	_, err := New(Identity{ID: "default", Token: "  "}, Options{})
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("err = %v", err)
	}
}

func TestNewPrivateRequiresPairingHashAndCallbacks(t *testing.T) {
	t.Parallel()
	_, err := New(Identity{ID: "x", Token: "t", Access: AccessPrivate}, Options{})
	if err == nil || !strings.Contains(err.Error(), "pairingCodeHash") {
		t.Errorf("err = %v; want pairingCodeHash error", err)
	}
	_, err = New(Identity{ID: "x", Token: "t", Access: AccessPrivate, PairingCodeHash: "h"}, Options{})
	if err == nil || !strings.Contains(err.Error(), "Allowlisted") {
		t.Errorf("err = %v; want callbacks error", err)
	}
}

func TestNewReturnsAdapterShape(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, err := New(Identity{ID: "default", Token: "tg-token"}, Options{ServerURL: mock.URL()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Channel() != "telegram" {
		t.Errorf("Channel() = %q", a.Channel())
	}
	if a.Identity() != "default" {
		t.Errorf("Identity() = %q", a.Identity())
	}
}

func newAdapter(t *testing.T, id Identity) (*Adapter, *mockTelegramServer, <-chan bridge.Inbound) {
	t.Helper()
	mock := newMockServer(t)
	a, err := New(id, Options{
		ServerURL:    mock.URL(),
		MediaDir:     t.TempDir(),
		HTTPClient:   &http.Client{Timeout: 5 * time.Second},
		PollTimeout:  100 * time.Millisecond,
		Allowlisted:  id.allowlistChecker(),
		AddAllowlist: id.addAllowlist(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetMe(&models.User{ID: 999, Username: "routerbot", IsBot: true})
	a.SetFileBaseURL(mock.URL())

	inbound := make(chan bridge.Inbound, 4)
	if err := a.Start(context.Background(), inbound); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	return a, mock, inbound
}

// allowlistChecker / addAllowlist are field-tag helpers Identity uses in
// tests to inject closures. Production-quality wiring lives at the bridge
// service boundary; tests provide trivial in-memory fakes when needed.
func (i Identity) allowlistChecker() AllowlistChecker {
	if i.Access != AccessPrivate {
		return nil
	}
	return func(_ context.Context, _ string) (bool, error) { return false, nil }
}
func (i Identity) addAllowlist() AllowlistAdder {
	if i.Access != AccessPrivate {
		return nil
	}
	return func(_ context.Context, _ string) error { return nil }
}

// TestSendsTextImagesAudioFiles mirrors the TS test
// "createTelegramAdapter sends text/images/audio/files".
func TestSendsTextImagesAudioFiles(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Text: "hello",
		Attachments: []bridge.Attachment{
			{FileName: "sample.jpg", MimeType: "image/jpeg", Content: []byte("img")},
			{FileName: "sample.ogg", MimeType: "audio/ogg", Content: []byte("aud")},
			{FileName: "sample.txt", MimeType: "text/plain", Content: []byte("doc")},
		},
	})
	if !res.Delivered {
		t.Fatalf("Send err: %v", res.Err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.sendMsg) != 1 || mock.sendMsg[0].Text != "hello" {
		t.Errorf("sendMessage calls = %+v", mock.sendMsg)
	}
	if len(mock.sendPh) != 1 || mock.sendPh[0].Filename != "sample.jpg" {
		t.Errorf("sendPhoto calls = %+v", mock.sendPh)
	}
	if len(mock.sendAud) != 1 {
		t.Errorf("sendAudio calls = %+v", mock.sendAud)
	}
	if len(mock.sendDoc) != 1 {
		t.Errorf("sendDocument calls = %+v", mock.sendDoc)
	}
}

// TestDownloadsInboundMediaToStore mirrors the TS test
// "createTelegramAdapter downloads inbound media to store".
func TestDownloadsInboundMediaToStore(t *testing.T) {
	t.Parallel()
	a, mock, inbound := newAdapter(t, Identity{ID: "default", Token: "tg-token"})
	mock.fileBody = "telegram-media"

	msg := &models.Message{
		Chat:    models.Chat{ID: 777, Type: "private"},
		Caption: "here is photo",
		Photo: []models.PhotoSize{
			{FileID: "FILE123", FileUniqueID: "UNIQ123", FileSize: 13},
		},
	}
	a.handleMessage(context.Background(), msg)

	in := receiveOne(t, inbound, 2*time.Second)
	if in.Text != "here is photo" {
		t.Errorf("Text = %q", in.Text)
	}
	if len(in.Attachments) != 1 {
		t.Fatalf("Attachments len = %d", len(in.Attachments))
	}
	att := in.Attachments[0]
	if string(att.Content) != "telegram-media" {
		t.Errorf("Content = %q", string(att.Content))
	}
	if att.FilePath == "" {
		t.Errorf("FilePath empty; want persisted")
	}
}

// TestIgnoresBotOriginatedMessages mirrors the TS test
// "createTelegramAdapter ignores bot-originated inbound messages".
func TestIgnoresBotOriginatedMessages(t *testing.T) {
	t.Parallel()
	a, _, inbound := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	// Self (bot's own user_id).
	a.handleMessage(context.Background(), &models.Message{
		Chat: models.Chat{ID: 777, Type: "private"},
		From: &models.User{ID: 999, IsBot: true},
		Text: "bot says hi",
	})
	// Another bot.
	a.handleMessage(context.Background(), &models.Message{
		Chat: models.Chat{ID: 777, Type: "private"},
		From: &models.User{ID: 123, IsBot: true},
		Text: "another bot says hi",
	})

	assertNoInbound(t, inbound, 200*time.Millisecond)
}

// --- additional tests for spec coverage ------------------------------------

func TestGroupMessagesRequireGroupsEnabledAndMention(t *testing.T) {
	t.Parallel()
	a, _, inbound := newAdapter(t, Identity{ID: "default", Token: "t", GroupsEnabled: false})

	a.handleMessage(context.Background(), &models.Message{
		Chat: models.Chat{ID: -100123, Type: "supergroup"},
		From: &models.User{ID: 1},
		Text: "@routerbot run tests",
	})
	assertNoInbound(t, inbound, 200*time.Millisecond)
}

func TestGroupMessagesWithGroupsEnabledRequireMention(t *testing.T) {
	t.Parallel()
	a, _, inbound := newAdapter(t, Identity{ID: "default", Token: "t", GroupsEnabled: true})

	// Without @mention → ignored.
	a.handleMessage(context.Background(), &models.Message{
		Chat: models.Chat{ID: -100123, Type: "supergroup"},
		From: &models.User{ID: 1},
		Text: "hello team",
	})
	assertNoInbound(t, inbound, 200*time.Millisecond)

	// With @mention → forwarded; mention stripped.
	a.handleMessage(context.Background(), &models.Message{
		Chat: models.Chat{ID: -100123, Type: "supergroup"},
		From: &models.User{ID: 1},
		Text: "@routerbot please review",
	})
	in := receiveOne(t, inbound, 1*time.Second)
	if in.Text != "please review" {
		t.Errorf("group inbound Text = %q, want %q", in.Text, "please review")
	}
}

func TestPrivateModeRejectsNonAllowlistedPeer(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	// Identity with private access; pairing code "secret123".
	hash := hashPairingCode("secret123")
	var allowlistAdds []string
	var mu sync.Mutex
	allowed := map[string]bool{}

	a, err := New(Identity{
		ID:              "default",
		Token:           "tg-token",
		Access:          AccessPrivate,
		PairingCodeHash: hash,
	}, Options{
		ServerURL:  mock.URL(),
		MediaDir:   t.TempDir(),
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
		Allowlisted: func(_ context.Context, peerID string) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			return allowed[peerID], nil
		},
		AddAllowlist: func(_ context.Context, peerID string) error {
			mu.Lock()
			defer mu.Unlock()
			allowed[peerID] = true
			allowlistAdds = append(allowlistAdds, peerID)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetMe(&models.User{ID: 999, Username: "routerbot", IsBot: true})
	a.SetFileBaseURL(mock.URL())

	inbound := make(chan bridge.Inbound, 4)
	if err := a.Start(context.Background(), inbound); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	// Inbound with bad code → rejected, no allowlist add.
	a.handleMessage(context.Background(), &models.Message{
		Chat: models.Chat{ID: 777, Type: "private"},
		From: &models.User{ID: 1},
		Text: "/pair wrong",
	})
	mu.Lock()
	wrongAdds := len(allowlistAdds)
	mu.Unlock()
	if wrongAdds != 0 {
		t.Errorf("allowlist added for wrong code: %d", wrongAdds)
	}
	assertNoInbound(t, inbound, 200*time.Millisecond)

	// Inbound with correct code → allowlist add, peer can subsequently
	// message normally.
	a.handleMessage(context.Background(), &models.Message{
		Chat: models.Chat{ID: 777, Type: "private"},
		From: &models.User{ID: 1},
		Text: "/pair secret123",
	})
	mu.Lock()
	addedNow := len(allowlistAdds) == 1 && allowlistAdds[0] == "777"
	mu.Unlock()
	if !addedNow {
		t.Errorf("allowlist not updated after correct pair: %v", allowlistAdds)
	}

	// Subsequent message → forwarded.
	a.handleMessage(context.Background(), &models.Message{
		Chat: models.Chat{ID: 777, Type: "private"},
		From: &models.User{ID: 1},
		Text: "hi after pairing",
	})
	in := receiveOne(t, inbound, 1*time.Second)
	if in.Text != "hi after pairing" {
		t.Errorf("post-pair inbound Text = %q", in.Text)
	}
}

func TestSendChunksLongText(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	// Build text just over the 4096 limit so we get exactly 2 chunks.
	big := strings.Repeat("a", MaxTextLength+1)
	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Text: big,
	})
	if !res.Delivered {
		t.Fatalf("Send err: %v", res.Err)
	}
	mock.mu.Lock()
	got := len(mock.sendMsg)
	mock.mu.Unlock()
	if got != 2 {
		t.Errorf("chunk count = %d, want 2", got)
	}
}

func TestSendInvalidPeerIDErrors(t *testing.T) {
	t.Parallel()
	a, _, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "@notnumeric"},
		Text: "hi",
	})
	if res.Delivered {
		t.Errorf("delivered=true, want false")
	}
	if res.Err == nil || res.Err.Error() == "" {
		t.Errorf("err = %v", res.Err)
	}
}

func TestSendOversizeAttachmentRejected(t *testing.T) {
	t.Parallel()
	a, _, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Attachments: []bridge.Attachment{
			{FileName: "big.bin", Content: make([]byte, MaxFileSize+1)},
		},
	})
	if res.Delivered {
		t.Errorf("delivered=true; want false")
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "byte limit") {
		t.Errorf("err = %v", res.Err)
	}
}

func TestResolveUserToDMIsPassthrough(t *testing.T) {
	t.Parallel()
	a, _, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})
	got, err := a.ResolveUserToDM(context.Background(), "12345")
	if err != nil || got != "12345" {
		t.Errorf("ResolveUserToDM = %q/%v", got, err)
	}
}

// --- helpers ---------------------------------------------------------------

func receiveOne(t *testing.T, ch <-chan bridge.Inbound, d time.Duration) bridge.Inbound {
	t.Helper()
	select {
	case in := <-ch:
		return in
	case <-time.After(d):
		t.Fatalf("no inbound within %v", d)
	}
	return bridge.Inbound{}
}

func assertNoInbound(t *testing.T, ch <-chan bridge.Inbound, d time.Duration) {
	t.Helper()
	select {
	case in := <-ch:
		t.Errorf("unexpected inbound: %+v", in)
	case <-time.After(d):
	}
}
