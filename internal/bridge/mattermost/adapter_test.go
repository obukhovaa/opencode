package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// --- mock server harness ----------------------------------------------------

// wsConnState pairs a *websocket.Conn with a per-conn write mutex and an
// "auth complete" signal. gorilla/websocket's WriteJSON is NOT safe for
// concurrent use; without the mutex, the server-handler's auth-response
// write races against the test's pushPostedEvent write under -race.
type wsConnState struct {
	conn   *websocket.Conn
	wmu    sync.Mutex
	authOK chan struct{}
}

func (s *wsConnState) writeJSON(v any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.conn.WriteJSON(v)
}

// mockServer combines an httptest.Server (REST API) and the same server's
// websocket endpoint. Tests configure response shapes via setters and read
// the captured requests via the recorder.
type mockServer struct {
	t          *testing.T
	httpsrv    *httptest.Server
	wsUpgrader websocket.Upgrader
	dialer     *websocket.Dialer
	url        string

	mu      sync.Mutex
	wsConns []*wsConnState

	// Configurable responses.
	getMeResp       User
	files           map[string]string // fileID → body text
	uploadResp      []FileInfo
	createPostResp  func(in CreatePostInput) Post
	authChallengeCh chan bool

	// Captured request bodies for inspection by tests.
	createPostCalls []CreatePostInput
	uploadCalls     []FileUpload
	directCalls     []string
}

func newMockServer(t *testing.T, bot User) *mockServer {
	t.Helper()
	m := &mockServer{
		t:          t,
		getMeResp:  bot,
		files:      map[string]string{},
		uploadResp: []FileInfo{{ID: "f1", Name: "file.txt", Size: 5, MimeType: "text/plain"}},
		createPostResp: func(in CreatePostInput) Post {
			return Post{ID: "new_post", ChannelID: in.ChannelID, Message: in.Message}
		},
		authChallengeCh: make(chan bool, 4),
		wsUpgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/users/me", m.handleGetMe)
	mux.HandleFunc("/api/v4/users/", m.handleUsersTyping)
	mux.HandleFunc("/api/v4/posts", m.handlePosts)
	mux.HandleFunc("/api/v4/files", m.handleFilesUpload)
	mux.HandleFunc("/api/v4/files/", m.handleFileDownload)
	mux.HandleFunc("/api/v4/channels/direct", m.handleChannelsDirect)
	mux.HandleFunc("/api/v4/websocket", m.handleWebSocket)

	m.httpsrv = httptest.NewServer(mux)
	m.url = m.httpsrv.URL

	// gorilla/websocket dials wss:// or ws://; httptest serves http://, so
	// we hand the adapter a base URL of http://… and the Client will
	// rewrite it to ws://… in WebSocketURL().
	m.dialer = websocket.DefaultDialer
	t.Cleanup(m.close)
	return m
}

func (m *mockServer) close() {
	m.mu.Lock()
	conns := m.wsConns
	m.wsConns = nil
	m.mu.Unlock()
	for _, c := range conns {
		_ = c.conn.Close()
	}
	m.httpsrv.Close()
}

func (m *mockServer) URL() string { return m.url }

func (m *mockServer) handleGetMe(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(m.getMeResp)
}

func (m *mockServer) handlePosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var in struct {
		ChannelID string   `json:"channel_id"`
		Message   string   `json:"message"`
		RootID    string   `json:"root_id"`
		FileIDs   []string `json:"file_ids"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	captured := CreatePostInput{ChannelID: in.ChannelID, Message: in.Message, RootID: in.RootID, FileIDs: in.FileIDs}
	m.mu.Lock()
	m.createPostCalls = append(m.createPostCalls, captured)
	m.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(m.createPostResp(captured))
}

func (m *mockServer) handleFilesUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, hs := range r.MultipartForm.File["files"] {
		f, err := hs.Open()
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(f)
		_ = f.Close()
		m.mu.Lock()
		m.uploadCalls = append(m.uploadCalls, FileUpload{Filename: hs.Filename, Data: data})
		m.mu.Unlock()
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(fileUploadResponse{FileInfos: m.uploadResp})
}

func (m *mockServer) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v4/files/")
	if body, ok := m.files[id]; ok {
		_, _ = w.Write([]byte(body))
		return
	}
	// Default body for tests that don't configure specific file content.
	_, _ = w.Write([]byte("downloaded-mm-file"))
}

func (m *mockServer) handleUsersTyping(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (m *mockServer) handleChannelsDirect(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var ids []string
	_ = json.Unmarshal(body, &ids)
	m.mu.Lock()
	m.directCalls = append(m.directCalls, ids...)
	m.mu.Unlock()
	_ = json.NewEncoder(w).Encode(directChannelResponse{ID: "dm_resolved_" + ids[len(ids)-1]})
}

func (m *mockServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	c, err := m.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	state := &wsConnState{conn: c, authOK: make(chan struct{})}
	m.mu.Lock()
	m.wsConns = append(m.wsConns, state)
	m.mu.Unlock()

	// Wait for the auth challenge, then send "hello" so Connect resolves.
	_, body, err := c.ReadMessage()
	if err != nil {
		return
	}
	var msg map[string]any
	if err := json.Unmarshal(body, &msg); err == nil {
		select {
		case m.authChallengeCh <- true:
		default:
		}
		if msg["action"] == "authentication_challenge" {
			_ = state.writeJSON(map[string]any{"event": "hello"})
		}
	}
	close(state.authOK)

	// Block; tests push messages onto the conn explicitly via pushPostedEvent.
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
	}
}

// pushPostedEvent injects a posted event onto the most recently connected
// WebSocket. The adapter's reader picks it up via ReadEvent and dispatches.
// Blocks until the server-side auth handshake for this conn completes so
// the inline "hello" write doesn't race against this write (gorilla
// WriteJSON is not safe for concurrent use).
func (m *mockServer) pushPostedEvent(channelType string, post Post) {
	m.mu.Lock()
	if len(m.wsConns) == 0 {
		m.mu.Unlock()
		m.t.Fatalf("no WS connection to push event onto")
	}
	state := m.wsConns[len(m.wsConns)-1]
	m.mu.Unlock()

	<-state.authOK

	postJSON, _ := json.Marshal(post)
	frame := map[string]any{
		"event": "posted",
		"data": map[string]any{
			"channel_type": channelType,
			"post":         string(postJSON),
		},
	}
	if err := state.writeJSON(frame); err != nil {
		m.t.Fatalf("writeJSON: %v", err)
	}
}

// --- adapter factory + lifecycle tests --------------------------------------

func TestNewMissingServerURLErrors(t *testing.T) {
	t.Parallel()
	_, err := New(Identity{ID: "test", ServerURL: "", AccessToken: "tok"}, Options{})
	if err == nil || !strings.Contains(err.Error(), "server URL") {
		t.Errorf("err = %v; want server URL required", err)
	}
}

func TestNewMissingAccessTokenErrors(t *testing.T) {
	t.Parallel()
	_, err := New(Identity{ID: "test", ServerURL: "https://mm.example.com", AccessToken: "  "}, Options{})
	if err == nil || !strings.Contains(err.Error(), "access token") {
		t.Errorf("err = %v; want access token required", err)
	}
}

func TestNewReturnsAdapterWithCorrectShape(t *testing.T) {
	t.Parallel()
	a, err := New(Identity{ID: "test", ServerURL: "https://mm.example.com", AccessToken: "tok123"}, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Channel() != "mattermost" {
		t.Errorf("Channel() = %q", a.Channel())
	}
	if a.Identity() != "test" {
		t.Errorf("Identity() = %q", a.Identity())
	}
	if MaxTextLength != 16383 {
		t.Errorf("MaxTextLength = %d", MaxTextLength)
	}
}

// --- inbound dispatch tests -------------------------------------------------

func startAdapter(t *testing.T, id Identity, mock *mockServer, inboundCap int) (*Adapter, <-chan bridge.Inbound, func()) {
	t.Helper()
	if id.ServerURL == "" {
		id.ServerURL = mock.URL()
	}
	adapter, err := New(id, Options{
		MediaDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch := make(chan bridge.Inbound, inboundCap)
	ctx, cancel := context.WithCancel(context.Background())
	if err := adapter.Start(ctx, ch); err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = adapter.Stop()
		cancel()
	})
	return adapter, ch, func() {
		cancel()
		_ = adapter.Stop()
	}
}

func TestHandlesDMPostedEvents(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})

	_, inbound, _ := startAdapter(t, Identity{
		ID:          "default",
		AccessToken: "tok-test",
	}, mock, 4)

	mock.pushPostedEvent("D", Post{
		ID: "post1", ChannelID: "dm_channel_1", UserID: "user1", Message: "hello bot",
	})

	in := receiveOne(t, inbound, 2*time.Second)
	if in.Peer.Channel != "mattermost" || in.Peer.Identity != "default" {
		t.Errorf("peer = %+v", in.Peer)
	}
	if in.Text != "hello bot" {
		t.Errorf("Text = %q", in.Text)
	}
	// Top-level post: rootPostId = post.id since root_id was empty.
	if in.Peer.PeerID != "dm_channel_1|post1" {
		t.Errorf("PeerID = %q, want dm_channel_1|post1", in.Peer.PeerID)
	}
}

func TestFiltersOwnMessages(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	_, inbound, _ := startAdapter(t, Identity{ID: "default", AccessToken: "tok-test"}, mock, 4)

	mock.pushPostedEvent("D", Post{
		ID: "post2", ChannelID: "dm1", UserID: "bot123", Message: "my own response",
	})
	assertNoInbound(t, inbound, 200*time.Millisecond)
}

func TestFiltersWebhookPosts(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	_, inbound, _ := startAdapter(t, Identity{ID: "default", AccessToken: "tok-test"}, mock, 4)

	mock.pushPostedEvent("D", Post{
		ID: "p1", ChannelID: "dm1", UserID: "webhook_user", Message: "ci passed",
		Props: map[string]any{"from_webhook": "true"},
	})
	mock.pushPostedEvent("D", Post{
		ID: "p2", ChannelID: "dm1", UserID: "another_bot", Message: "auto",
		Props: map[string]any{"from_bot": "true"},
	})
	assertNoInbound(t, inbound, 200*time.Millisecond)
}

func TestChannelMessagesRequireGroupsEnabledAndMention(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})

	// groupsEnabled = false → channel posts ignored even WITH @mention.
	_, inbound, _ := startAdapter(t, Identity{ID: "default", AccessToken: "tok"}, mock, 4)

	mock.pushPostedEvent("O", Post{
		ID: "p1", ChannelID: "ch1", UserID: "u1", Message: "@testbot run tests",
	})
	assertNoInbound(t, inbound, 200*time.Millisecond)
}

func TestChannelMessagesWithGroupsEnabledAndMentionWork(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	_, inbound, _ := startAdapter(t, Identity{
		ID: "default", AccessToken: "tok", GroupsEnabled: true,
	}, mock, 4)

	mock.pushPostedEvent("O", Post{
		ID: "p1", ChannelID: "ch1", UserID: "u1", Message: "@testbot run tests",
	})
	in := receiveOne(t, inbound, 2*time.Second)
	if in.Text != "run tests" {
		t.Errorf("Text = %q, want %q (mention stripped)", in.Text, "run tests")
	}
}

func TestIdentityGroupsEnabledOverridesGlobal(t *testing.T) {
	t.Parallel()
	// Our config model has groupsEnabled per-identity only (no global)
	// so the override is implicit. Verify both directions still work:
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})

	// groupsEnabled: true wins.
	_, inboundOn, _ := startAdapter(t, Identity{
		ID: "on", AccessToken: "tok", GroupsEnabled: true,
	}, mock, 4)
	mock.pushPostedEvent("O", Post{
		ID: "p1", ChannelID: "ch1", UserID: "u1", Message: "@testbot run",
	})
	if in := receiveOne(t, inboundOn, 2*time.Second); in.Text != "run" {
		t.Errorf("on: text = %q", in.Text)
	}

	// Separate adapter with groupsEnabled: false → ignored.
	mock2 := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	_, inboundOff, _ := startAdapter(t, Identity{
		ID: "off", AccessToken: "tok", GroupsEnabled: false,
	}, mock2, 4)
	mock2.pushPostedEvent("O", Post{
		ID: "p1", ChannelID: "ch1", UserID: "u1", Message: "@testbot run",
	})
	assertNoInbound(t, inboundOff, 200*time.Millisecond)
}

func TestGroupDMsHonorGroupsEnabled(t *testing.T) {
	t.Parallel()
	// channel_type "G" = group DM. Per the chat-bridge-adapters spec
	// scenario "Group DM honors per-identity groupsEnabled", G channels
	// gate on groupsEnabled + bot @mention just like O/P channels —
	// they are NOT treated as direct DMs.
	t.Run("ignored when groupsEnabled=false", func(t *testing.T) {
		t.Parallel()
		mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
		_, inbound, _ := startAdapter(t, Identity{
			ID: "default", AccessToken: "tok", GroupsEnabled: false,
		}, mock, 4)

		mock.pushPostedEvent("G", Post{
			ID: "p1", ChannelID: "gdm1", UserID: "u1", Message: "hello everyone",
		})
		select {
		case in := <-inbound:
			t.Errorf("group DM should be ignored when groupsEnabled=false, got Text=%q", in.Text)
		case <-time.After(500 * time.Millisecond):
			// Expected: no inbound delivered.
		}
	})

	t.Run("delivered when groupsEnabled=true + bot @mention", func(t *testing.T) {
		t.Parallel()
		mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
		_, inbound, _ := startAdapter(t, Identity{
			ID: "default", AccessToken: "tok", GroupsEnabled: true,
		}, mock, 4)

		mock.pushPostedEvent("G", Post{
			ID: "p1", ChannelID: "gdm1", UserID: "u1",
			Message: "@testbot hello everyone",
		})
		in := receiveOne(t, inbound, 2*time.Second)
		if in.Text != "hello everyone" {
			t.Errorf("Text = %q, want %q (mention stripped)", in.Text, "hello everyone")
		}
	})
}

func TestIgnoresUnknownChannelTypes(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	_, inbound, _ := startAdapter(t, Identity{
		ID: "default", AccessToken: "tok", GroupsEnabled: true,
	}, mock, 4)

	mock.pushPostedEvent("X", Post{
		ID: "p1", ChannelID: "?", UserID: "u1", Message: "@testbot hi",
	})
	assertNoInbound(t, inbound, 200*time.Millisecond)
}

func TestIgnoresChannelWithoutMentionEvenIfGroupsEnabled(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	_, inbound, _ := startAdapter(t, Identity{
		ID: "default", AccessToken: "tok", GroupsEnabled: true,
	}, mock, 4)

	mock.pushPostedEvent("O", Post{
		ID: "p1", ChannelID: "ch1", UserID: "u1", Message: "no mention here",
	})
	assertNoInbound(t, inbound, 200*time.Millisecond)
}

func TestThreadedMessagesUseRootID(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	_, inbound, _ := startAdapter(t, Identity{ID: "default", AccessToken: "tok"}, mock, 4)

	mock.pushPostedEvent("D", Post{
		ID: "reply_post_1", ChannelID: "dm1", UserID: "u1",
		RootID: "root_post_1", Message: "reply in thread",
	})
	in := receiveOne(t, inbound, 2*time.Second)
	if in.Peer.PeerID != "dm1|root_post_1" {
		t.Errorf("PeerID = %q, want dm1|root_post_1", in.Peer.PeerID)
	}
}

func TestDownloadsInboundFiles(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	a, inbound, _ := startAdapter(t, Identity{ID: "default", AccessToken: "tok"}, mock, 4)
	_ = a

	mock.files["file_abc"] = "downloaded-mm-file"
	mock.pushPostedEvent("D", Post{
		ID: "post_with_file", ChannelID: "dm1", UserID: "u1",
		Message: "", FileIDs: []string{"file_abc"},
	})

	in := receiveOne(t, inbound, 2*time.Second)
	if len(in.Attachments) != 1 {
		t.Fatalf("Attachments len = %d, want 1", len(in.Attachments))
	}
	att := in.Attachments[0]
	if att.FilePath == "" {
		t.Errorf("expected FilePath set; got %+v", att)
	}
	if string(att.Content) != "downloaded-mm-file" {
		t.Errorf("Content = %q", string(att.Content))
	}
}

// --- outbound tests ---------------------------------------------------------

func TestOutboundSendText(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	a, _, _ := startAdapter(t, Identity{ID: "default", AccessToken: "tok"}, mock, 4)

	// DM destination.
	if r := a.Send(ctxBg(t), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "mattermost", Identity: "default", PeerID: "dm_ch1"},
		Text: "hello there",
	}); !r.Delivered {
		t.Errorf("delivered=false err=%v", r.Err)
	}
	// Thread destination.
	if r := a.Send(ctxBg(t), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "mattermost", Identity: "default", PeerID: "ch1|root_post_123"},
		Text: "thread reply",
	}); !r.Delivered {
		t.Errorf("thread delivered=false err=%v", r.Err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.createPostCalls) != 2 {
		t.Fatalf("createPostCalls = %d, want 2", len(mock.createPostCalls))
	}
	if mock.createPostCalls[0].ChannelID != "dm_ch1" || mock.createPostCalls[0].Message != "hello there" || mock.createPostCalls[0].RootID != "" {
		t.Errorf("call 0 = %+v", mock.createPostCalls[0])
	}
	if mock.createPostCalls[1].ChannelID != "ch1" || mock.createPostCalls[1].Message != "thread reply" || mock.createPostCalls[1].RootID != "root_post_123" {
		t.Errorf("call 1 = %+v", mock.createPostCalls[1])
	}
}

func TestOutboundFileUpload(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	mock.uploadResp = []FileInfo{{ID: "file1", Name: "test.txt", Size: 5, MimeType: "text/plain"}}

	a, _, _ := startAdapter(t, Identity{ID: "default", AccessToken: "tok"}, mock, 4)

	r := a.Send(ctxBg(t), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "mattermost", Identity: "default", PeerID: "dm_ch1"},
		Text: "with attachment",
		Attachments: []bridge.Attachment{
			{FileName: "test.txt", Content: []byte("hello")},
		},
	})
	if !r.Delivered {
		t.Fatalf("delivered=false err=%v", r.Err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.uploadCalls) != 1 {
		t.Errorf("uploadCalls = %d", len(mock.uploadCalls))
	}
	if len(mock.createPostCalls) != 1 || len(mock.createPostCalls[0].FileIDs) != 1 || mock.createPostCalls[0].FileIDs[0] != "file1" {
		t.Errorf("createPost FileIDs not propagated: %+v", mock.createPostCalls)
	}
}

func TestSendTextToInvalidPeerIDErrors(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	a, _, _ := startAdapter(t, Identity{ID: "default", AccessToken: "tok"}, mock, 4)

	r := a.Send(ctxBg(t), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "mattermost", Identity: "default", PeerID: ""},
		Text: "hi",
	})
	if r.Delivered {
		t.Errorf("delivered=true; want false (invalid peerId)")
	}
	if r.Err == nil || !strings.Contains(r.Err.Error(), "invalid peerId") {
		t.Errorf("err = %v", r.Err)
	}
}

func TestSendChannelOnlyPeerSurfacesResolvedThread(t *testing.T) {
	t.Parallel()
	// First outbound to a channel-only peer must surface ResolvedPeer so
	// the orchestrator can mutate the binding to channelID|postID.
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	mock.createPostResp = func(in CreatePostInput) Post {
		return Post{ID: "thread_root_1", ChannelID: in.ChannelID}
	}
	a, _, _ := startAdapter(t, Identity{ID: "default", AccessToken: "tok"}, mock, 4)

	r := a.Send(ctxBg(t), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "mattermost", Identity: "default", PeerID: "ch_only"},
		Text: "first turn",
	})
	if !r.Delivered {
		t.Fatalf("err=%v", r.Err)
	}
	if r.ResolvedPeer != "ch_only|thread_root_1" {
		t.Errorf("ResolvedPeer = %q", r.ResolvedPeer)
	}
}

// --- ResolveUserToDM tests --------------------------------------------------

func TestResolveUserToDMOnly26CharIDs(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})
	a, _, _ := startAdapter(t, Identity{ID: "default", AccessToken: "tok"}, mock, 4)

	// Channel-shaped peerID: returned unchanged.
	got, err := a.ResolveUserToDM(ctxBg(t), "C0DEF456")
	if err != nil {
		t.Fatalf("ResolveUserToDM: %v", err)
	}
	if got != "C0DEF456" {
		t.Errorf("got %q, want C0DEF456 (non-user-id passthrough)", got)
	}

	// 26-char user ID: resolved via /channels/direct.
	uid := "abcdefghijklmnopqrstuvwxyz"
	got, err = a.ResolveUserToDM(ctxBg(t), uid)
	if err != nil {
		t.Fatalf("ResolveUserToDM: %v", err)
	}
	if !strings.HasPrefix(got, "dm_resolved_") {
		t.Errorf("got %q, want resolved DM ID prefix", got)
	}
}

// --- helpers ----------------------------------------------------------------

func receiveOne(t *testing.T, ch <-chan bridge.Inbound, timeout time.Duration) bridge.Inbound {
	t.Helper()
	select {
	case in := <-ch:
		return in
	case <-time.After(timeout):
		t.Fatalf("no inbound received within %v", timeout)
	}
	return bridge.Inbound{}
}

func assertNoInbound(t *testing.T, ch <-chan bridge.Inbound, window time.Duration) {
	t.Helper()
	select {
	case in := <-ch:
		t.Errorf("unexpected inbound: %+v", in)
	case <-time.After(window):
	}
}

func ctxBg(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// Sanity: confirm http→ws URL conversion works against an httptest.Server
// (whose URL starts with http://). The Client's WebSocketURL() does the
// conversion, and the dialer must accept the resulting ws:// URL.
func TestWebSocketURLConvertsHTTPToWS(t *testing.T) {
	t.Parallel()
	c := NewClient("http://example.com:8065", "tok", nil)
	if got := c.WebSocketURL(); got != "ws://example.com:8065/api/v4/websocket" {
		t.Errorf("http→ws conversion: %q", got)
	}
	c = NewClient("https://mm.example.com", "tok", nil)
	if got := c.WebSocketURL(); got != "wss://mm.example.com/api/v4/websocket" {
		t.Errorf("https→wss conversion: %q", got)
	}
}

// Ensure HTTPError plumbing works (the spec mentions returning errors
// from REST calls; a quick test against the mock confirms non-2xx paths).
func TestHTTPErrorOnNon2xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("bad token"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", nil)
	_, err := c.GetMe(ctxBg(t))
	var herr *HTTPError
	if !errors.As(err, &herr) {
		t.Fatalf("err = %v; want HTTPError", err)
	}
	if herr.Status != http.StatusUnauthorized {
		t.Errorf("status = %d", herr.Status)
	}
}

// Verifies that FileURL produces a parseable URL. We don't escape `/` in
// the file ID because real Mattermost file IDs are 26-char alphanumeric
// strings — the test exists to catch accidental regressions in the
// base-URL concatenation.
func TestFileURLProducesParseableURL(t *testing.T) {
	t.Parallel()
	c := NewClient("https://mm.example.com", "tok", nil)
	got := c.FileURL("abcdef")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Path != "/api/v4/files/abcdef" {
		t.Errorf("Path = %q", parsed.Path)
	}
}
