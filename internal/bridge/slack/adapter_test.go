package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// mockSlackServer mimics the Slack web API endpoints the adapter calls.
// It captures requests so tests can assert what was sent without going
// through the real Slack service.
type mockSlackServer struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	authTest int
	posts    []postCall
	uploads  []uploadCall
	opens    []string
	files    map[string]string // file ID → body
}

type postCall struct {
	Channel  string
	Text     string
	ThreadTS string
}

type uploadCall struct {
	ChannelID string
	Filename  string
	FileData  []byte
	ThreadTS  string
}

func newMockServer(t *testing.T) *mockSlackServer {
	m := &mockSlackServer{t: t, files: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handleAPI)
	m.server = httptest.NewServer(mux)
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockSlackServer) URL() string { return m.server.URL }

func (m *mockSlackServer) handleAPI(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// File download: /file/<id>
	if strings.HasPrefix(path, "/file/") {
		fileID := strings.TrimPrefix(path, "/file/")
		body, ok := m.files[fileID]
		if !ok {
			body = "downloaded-slack-file"
		}
		_, _ = w.Write([]byte(body))
		return
	}

	// Step 2 of UploadFile flow: the lib POSTs multipart data to the
	// upload_url returned by files.getUploadURLExternal. Our step-1
	// handler points back here.
	if path == "/file-upload-target" {
		call := captureUpload(r)
		m.mu.Lock()
		m.uploads = append(m.uploads, call)
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
		return
	}

	method := strings.TrimPrefix(path, "/")
	switch method {
	case "auth.test":
		m.mu.Lock()
		m.authTest++
		m.mu.Unlock()
		m.respond(w, map[string]any{
			"ok":      true,
			"user_id": "UBOT",
			"team_id": "T1",
		})
	case "chat.postMessage":
		_ = r.ParseForm()
		call := postCall{
			Channel:  r.FormValue("channel"),
			Text:     r.FormValue("text"),
			ThreadTS: r.FormValue("thread_ts"),
		}
		m.mu.Lock()
		m.posts = append(m.posts, call)
		m.mu.Unlock()
		m.respond(w, map[string]any{
			"ok":      true,
			"channel": call.Channel,
			"ts":      "1700000123.000200",
		})
	case "files.getUploadURLExternal":
		// Step 1 of slack-go's UploadFile flow: hand back an upload URL
		// pointing at our own mock so step 2 lands here too.
		m.respond(w, map[string]any{
			"ok":         true,
			"upload_url": m.server.URL + "/file-upload-target",
			"file_id":    "F-new",
		})
	case "files.completeUploadExternal":
		// Step 3: parses the form, captures channel/thread.
		_ = r.ParseForm()
		call := uploadCall{
			ChannelID: r.FormValue("channel_id"),
			ThreadTS:  r.FormValue("thread_ts"),
		}
		m.mu.Lock()
		// The most recent multipart capture from step 2 is appended;
		// we merge by overwriting the last entry's channel/thread.
		if len(m.uploads) > 0 {
			last := &m.uploads[len(m.uploads)-1]
			last.ChannelID = call.ChannelID
			last.ThreadTS = call.ThreadTS
		} else {
			m.uploads = append(m.uploads, call)
		}
		m.mu.Unlock()
		m.respond(w, map[string]any{
			"ok": true,
			"files": []map[string]any{
				{"id": "F-new", "title": ""},
			},
		})
	case "files.upload":
		// Legacy single-call upload endpoint — not used by slack-go's
		// modern UploadFile, but accept it for completeness.
		call := captureUpload(r)
		m.mu.Lock()
		m.uploads = append(m.uploads, call)
		m.mu.Unlock()
		m.respond(w, map[string]any{
			"ok":   true,
			"file": map[string]any{"id": "F1"},
		})
	case "conversations.open":
		_ = r.ParseForm()
		users := r.FormValue("users")
		m.mu.Lock()
		m.opens = append(m.opens, users)
		m.mu.Unlock()
		m.respond(w, map[string]any{
			"ok":      true,
			"channel": map[string]any{"id": "D012345"},
		})
	default:
		// apps.connections.open is what Socket Mode calls to get a
		// WebSocket URL. Tests don't drive Socket Mode; just respond
		// with a benign error so the Run goroutine exits quickly.
		m.respond(w, map[string]any{
			"ok":    false,
			"error": "test_no_socket",
		})
	}
}

func (m *mockSlackServer) respond(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func captureUpload(r *http.Request) uploadCall {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		// Some Slack uploads use application/x-www-form-urlencoded
		// with file content base64'd in a form field — but slack-go's
		// UploadFile uses multipart.
		return uploadCall{}
	}
	call := uploadCall{}
	if vals, ok := r.MultipartForm.Value["channels"]; ok && len(vals) > 0 {
		call.ChannelID = vals[0]
	}
	if vals, ok := r.MultipartForm.Value["thread_ts"]; ok && len(vals) > 0 {
		call.ThreadTS = vals[0]
	}
	if vals, ok := r.MultipartForm.Value["filename"]; ok && len(vals) > 0 {
		call.Filename = vals[0]
	}
	if files, ok := r.MultipartForm.File["file"]; ok && len(files) > 0 {
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

func (m *mockSlackServer) Posts() []postCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]postCall, len(m.posts))
	copy(out, m.posts)
	return out
}

func (m *mockSlackServer) Uploads() []uploadCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]uploadCall, len(m.uploads))
	copy(out, m.uploads)
	return out
}

// --- adapter tests ---------------------------------------------------------

func TestNewMissingBotTokenErrors(t *testing.T) {
	t.Parallel()
	_, err := New(Identity{ID: "x", BotToken: "", AppToken: "xapp"}, Options{})
	if err == nil || !strings.Contains(err.Error(), "bot token") {
		t.Errorf("err = %v", err)
	}
}

func TestNewMissingAppTokenErrors(t *testing.T) {
	t.Parallel()
	_, err := New(Identity{ID: "x", BotToken: "xoxb", AppToken: ""}, Options{})
	if err == nil || !strings.Contains(err.Error(), "app token") {
		t.Errorf("err = %v", err)
	}
}

func newAdapter(t *testing.T, id Identity) (*Adapter, *mockSlackServer, <-chan bridge.Inbound) {
	t.Helper()
	mock := newMockServer(t)
	// Slack web API URLs end with a trailing slash per the slack-go lib's
	// path-building convention.
	apiURL := mock.URL() + "/"
	a, err := New(id, Options{
		APIURL:     apiURL,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		MediaDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetBotUserID("UBOT")
	a.SetFileBaseURL(mock.URL())

	inbound := make(chan bridge.Inbound, 4)
	// We don't call Start because Socket Mode requires a real WebSocket
	// connection that's awkward to mock. Tests drive HandleMessageEvent /
	// HandleAppMention directly via SetInbound.
	a.SetInbound(inbound)
	t.Cleanup(func() { _ = a.Stop() })
	return a, mock, inbound
}

func TestRoutesDMAndAppMention(t *testing.T) {
	t.Parallel()
	a, _, inbound := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	// DM message.
	a.HandleMessageEvent(context.Background(), &slackevents.MessageEvent{
		Type:      "message",
		Channel:   "D123",
		User:      "U1",
		Text:      "hi",
		TimeStamp: "1700000000.000001",
	})
	in1 := receiveOne(t, inbound, time.Second)
	if in1.Peer.PeerID != "D123" || in1.Text != "hi" {
		t.Errorf("DM in = %+v", in1)
	}

	// app_mention in a channel.
	a.HandleAppMention(context.Background(), &slackevents.AppMentionEvent{
		Type:      "app_mention",
		Channel:   "C123",
		User:      "U2",
		Text:      "<@UBOT> run tests",
		TimeStamp: "1700000000.000100",
	})
	in2 := receiveOne(t, inbound, time.Second)
	if in2.Peer.PeerID != "C123|1700000000.000100" {
		t.Errorf("app_mention peerId = %q", in2.Peer.PeerID)
	}
	if in2.Text != "run tests" {
		t.Errorf("app_mention text = %q (mention not stripped?)", in2.Text)
	}
}

func TestSendTextToDMAndThread(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	// DM: no thread_ts.
	r1 := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Text: "ok",
	})
	if !r1.Delivered {
		t.Fatalf("DM send: %v", r1.Err)
	}
	if r1.ResolvedPeer != "" {
		t.Errorf("DM ResolvedPeer = %q; want empty", r1.ResolvedPeer)
	}

	// Thread: include thread_ts.
	r2 := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "C123|1700000000.000100"},
		Text: "ok-thread",
	})
	if !r2.Delivered {
		t.Fatalf("thread send: %v", r2.Err)
	}

	posts := mock.Posts()
	if len(posts) != 2 {
		t.Fatalf("posts count = %d, want 2", len(posts))
	}
	if posts[0].Channel != "D123" || posts[0].Text != "ok" || posts[0].ThreadTS != "" {
		t.Errorf("post 0 = %+v", posts[0])
	}
	if posts[1].Channel != "C123" || posts[1].Text != "ok-thread" || posts[1].ThreadTS != "1700000000.000100" {
		t.Errorf("post 1 = %+v", posts[1])
	}
}

func TestSendChannelOnlyPeerSurfacesResolvedThread(t *testing.T) {
	t.Parallel()
	a, _, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	// Channel ID without thread_ts → first outbound returns the channel|ts form.
	r := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "C0DEF456"},
		Text: "first turn",
	})
	if !r.Delivered {
		t.Fatalf("send: %v", r.Err)
	}
	if r.ResolvedPeer != "C0DEF456|1700000123.000200" {
		t.Errorf("ResolvedPeer = %q", r.ResolvedPeer)
	}
}

func TestSendUploadsFile(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	r := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Text: "see file",
		Attachments: []bridge.Attachment{
			{FileName: "hello.txt", MimeType: "text/plain", Content: []byte("hello from file")},
		},
	})
	if !r.Delivered {
		t.Fatalf("send: %v", r.Err)
	}
	uploads := mock.Uploads()
	if len(uploads) != 1 {
		t.Fatalf("uploads = %d", len(uploads))
	}
	if uploads[0].ChannelID != "D123" || uploads[0].Filename != "hello.txt" {
		t.Errorf("upload = %+v", uploads[0])
	}
	if string(uploads[0].FileData) != "hello from file" {
		t.Errorf("upload data = %q", string(uploads[0].FileData))
	}
}

func TestInboundFileDownloadIntoMediaStore(t *testing.T) {
	t.Parallel()
	a, mock, inbound := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})
	mock.files["F1"] = "downloaded-slack-file"

	a.HandleMessageEvent(context.Background(), &slackevents.MessageEvent{
		Type:    "message",
		SubType: "file_share",
		Channel: "D123",
		User:    "U1",
		Message: &slackgo.Msg{
			Files: []slackgo.File{
				{
					ID:                 "F1",
					Name:               "report.txt",
					Mimetype:           "text/plain",
					Size:               22,
					URLPrivateDownload: "https://files.slack.com/download/F1",
				},
			},
		},
	})

	in := receiveOne(t, inbound, 2*time.Second)
	if len(in.Attachments) != 1 {
		t.Fatalf("Attachments = %d", len(in.Attachments))
	}
	att := in.Attachments[0]
	if string(att.Content) != "downloaded-slack-file" {
		t.Errorf("content = %q", string(att.Content))
	}
	if att.FilePath == "" {
		t.Errorf("FilePath empty; want persisted")
	}
}

func TestFiltersBotAndSubtypeMessages(t *testing.T) {
	t.Parallel()
	a, _, inbound := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	// bot_id set → ignored.
	a.HandleMessageEvent(context.Background(), &slackevents.MessageEvent{
		Type: "message", Channel: "D123", BotID: "B123", Text: "auto",
	})
	// subtype != "" && != "file_share" → ignored (message_changed etc).
	a.HandleMessageEvent(context.Background(), &slackevents.MessageEvent{
		Type: "message", Channel: "D123", User: "U1", SubType: "message_changed", Text: "edit",
	})
	// User matches bot → ignored.
	a.HandleMessageEvent(context.Background(), &slackevents.MessageEvent{
		Type: "message", Channel: "D123", User: "UBOT", Text: "echo",
	})
	// Non-DM channel without app_mention → ignored.
	a.HandleMessageEvent(context.Background(), &slackevents.MessageEvent{
		Type: "message", Channel: "C123", User: "U1", Text: "hi",
	})

	assertNoInbound(t, inbound, 200*time.Millisecond)
}

func TestSendInvalidPeerIDErrors(t *testing.T) {
	t.Parallel()
	a, _, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})
	r := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: ""},
		Text: "hi",
	})
	if r.Delivered {
		t.Errorf("delivered=true; want false")
	}
	if r.Err == nil {
		t.Errorf("err = nil")
	}
}

func TestSendOversizeFileRejected(t *testing.T) {
	t.Parallel()
	a, _, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})
	r := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Attachments: []bridge.Attachment{
			{FileName: "big.bin", Content: make([]byte, MaxFileSize+1)},
		},
	})
	if r.Delivered {
		t.Errorf("delivered=true; want false (oversize)")
	}
	if r.Err == nil || !strings.Contains(r.Err.Error(), "byte limit") {
		t.Errorf("err = %v", r.Err)
	}
}

func TestResolveUserToDMOpensConversation(t *testing.T) {
	t.Parallel()
	a, _, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	// User-id form → resolved via conversations.open.
	got, err := a.ResolveUserToDM(context.Background(), "U01ABC")
	if err != nil {
		t.Fatalf("ResolveUserToDM: %v", err)
	}
	if got != "D012345" {
		t.Errorf("got %q, want D012345", got)
	}

	// D-form → passthrough.
	got, err = a.ResolveUserToDM(context.Background(), "D012345")
	if err != nil || got != "D012345" {
		t.Errorf("got %q/%v", got, err)
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

// Sanity: confirm http→ws URL conversion makes sense via the mock.
func TestMockServerURLIsValid(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	u, err := url.Parse(mock.URL())
	if err != nil {
		t.Fatalf("invalid mock URL: %v", err)
	}
	if u.Scheme != "http" {
		t.Errorf("scheme = %q", u.Scheme)
	}
}
