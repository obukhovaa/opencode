package external

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// capturingRelay is a fake orchestrator relay endpoint. It records every
// decoded request body and responds with statusCode (202 by default).
type capturingRelay struct {
	mu         sync.Mutex
	bodies     []map[string]any
	rawBodies  [][]byte
	authUser   []string
	authPass   []string
	statusCode int
}

func newCapturingRelay(t *testing.T, statusCode int) (*capturingRelay, *httptest.Server) {
	t.Helper()
	if statusCode == 0 {
		statusCode = http.StatusAccepted
	}
	r := &capturingRelay{statusCode: statusCode}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/router/external/outbound" {
			t.Errorf("unexpected path: %s", req.URL.Path)
		}
		if req.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", req.Method)
		}
		user, pass, ok := req.BasicAuth()
		r.mu.Lock()
		if ok {
			r.authUser = append(r.authUser, user)
			r.authPass = append(r.authPass, pass)
		}
		r.mu.Unlock()

		var raw json.RawMessage
		_ = json.NewDecoder(req.Body).Decode(&raw)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		r.mu.Lock()
		r.bodies = append(r.bodies, m)
		r.rawBodies = append(r.rawBodies, []byte(raw))
		r.mu.Unlock()

		w.WriteHeader(r.statusCode)
	}))
	t.Cleanup(srv.Close)
	return r, srv
}

func (r *capturingRelay) last() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		return nil
	}
	return r.bodies[len(r.bodies)-1]
}

func (r *capturingRelay) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func newTestAdapter(t *testing.T, srv *httptest.Server) *Adapter {
	t.Helper()
	a, err := New(Identity{
		ID:              "c3",
		RelayBaseURL:    srv.URL,
		RelayCredential: "s3cret",
	}, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func testPeer() bridge.PeerRef {
	return bridge.PeerRef{Channel: "external", Identity: "c3", PeerID: "aid1:flow1:run1"}
}

// 1. See internal/bridge/config_test.go's
// TestConfigAnyChannelEnabled/external_enabled+consumer_enabled — an
// external-only config boots the bridge (AnyChannelEnabled() == true).
// Not duplicated here to keep the assertion next to the other channels'
// equivalent cases.

// 2. relay frame shape for "message".
func TestSendMessageFrameShape(t *testing.T) {
	t.Parallel()
	relay, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)

	ctx := bridge.ContextWithSessionID(context.Background(), "sess-1")
	res := a.Send(ctx, bridge.Outbound{Peer: testPeer(), Text: "hello there"})
	if !res.Delivered || res.Err != nil {
		t.Fatalf("Send result = %+v", res)
	}
	if res.ResolvedPeer != "" {
		t.Errorf("ResolvedPeer = %q, want empty", res.ResolvedPeer)
	}

	body := relay.last()
	if body == nil {
		t.Fatal("no relay call captured")
	}
	if body["kind"] != "message" {
		t.Errorf("kind = %v, want message", body["kind"])
	}
	if body["text"] != "hello there" {
		t.Errorf("text = %v", body["text"])
	}
	if body["sessionId"] != "sess-1" {
		t.Errorf("sessionId = %v, want sess-1", body["sessionId"])
	}
	peer, _ := body["peer"].(map[string]any)
	if peer["channel"] != "external" || peer["identity"] != "c3" || peer["peerId"] != "aid1:flow1:run1" {
		t.Errorf("peer = %+v", peer)
	}
	if _, has := body["attachments"]; has {
		t.Errorf("text-only outbound must not have an attachments key, got %v", body["attachments"])
	}
	if _, has := body["question"]; has {
		t.Errorf("message frame must not have a question key")
	}
}

// 3. relay frame shape for "ack".
func TestSendAckFrameShape(t *testing.T) {
	t.Parallel()
	relay, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)

	ctx := bridge.ContextWithSessionID(context.Background(), "sess-2")
	res := a.Send(ctx, bridge.Outbound{Peer: testPeer(), Text: "got it", IsAck: true})
	if !res.Delivered || res.Err != nil {
		t.Fatalf("Send result = %+v", res)
	}

	body := relay.last()
	if body["kind"] != "ack" {
		t.Errorf("kind = %v, want ack", body["kind"])
	}
	if body["text"] != "got it" {
		t.Errorf("text = %v", body["text"])
	}
}

// 4. relay frame shape for "question".
func TestSendInteractiveQuestionFrameShape(t *testing.T) {
	t.Parallel()
	relay, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)

	ctx := bridge.ContextWithSessionID(context.Background(), "sess-3")
	ctx = bridge.ContextWithExternalQuestion(ctx, bridge.ExternalQuestionContext{
		RequestID: "req-9",
		Multiple:  true,
	})
	resolved, err := a.SendInteractiveQuestion(ctx, testPeer(), "ship it?", []bridge.QuestionChoice{
		{Label: "Yes", Value: "Yes", Custom: true},
		{Label: "No", Value: "No", Custom: true},
	})
	if err != nil {
		t.Fatalf("SendInteractiveQuestion: %v", err)
	}
	if resolved != "" {
		t.Errorf("resolvedPeer = %q, want empty", resolved)
	}

	body := relay.last()
	if body["kind"] != "question" {
		t.Errorf("kind = %v, want question", body["kind"])
	}
	if body["sessionId"] != "sess-3" {
		t.Errorf("sessionId = %v, want sess-3", body["sessionId"])
	}
	if _, has := body["text"]; has {
		t.Errorf("question frame must not have a text key, got %v", body["text"])
	}
	q, _ := body["question"].(map[string]any)
	if q == nil {
		t.Fatal("question sub-object missing")
	}
	if q["requestId"] != "req-9" {
		t.Errorf("requestId = %v, want req-9", q["requestId"])
	}
	if q["prompt"] != "ship it?" {
		t.Errorf("prompt = %v", q["prompt"])
	}
	if q["multiple"] != true {
		t.Errorf("multiple = %v, want true", q["multiple"])
	}
	if q["custom"] != true {
		t.Errorf("top-level custom = %v, want true", q["custom"])
	}
	choices, _ := q["choices"].([]any)
	if len(choices) != 2 {
		t.Fatalf("choices = %+v, want 2 entries", choices)
	}
	c0, _ := choices[0].(map[string]any)
	if c0["label"] != "Yes" || c0["value"] != "Yes" || c0["custom"] != true {
		t.Errorf("choices[0] = %+v", c0)
	}
}

// 5. rejected relay (500) -> Send reports a failed SendResult.
func TestSendRejectedRelayFails(t *testing.T) {
	t.Parallel()
	_, srv := newCapturingRelay(t, http.StatusInternalServerError)
	a := newTestAdapter(t, srv)

	res := a.Send(context.Background(), bridge.Outbound{Peer: testPeer(), Text: "hi"})
	if res.Delivered {
		t.Errorf("Delivered = true, want false on 500")
	}
	if res.Err == nil {
		t.Errorf("Err = nil, want non-nil on 500")
	}
	if res.ResolvedPeer != "" {
		t.Errorf("ResolvedPeer = %q, want empty", res.ResolvedPeer)
	}
	// Status/LastError should reflect the failure (diagnostic honesty),
	// while Status itself stays "running" — external has no persistent
	// connection to degrade.
	st := a.Status()
	if st.Status != "running" {
		t.Errorf("Status = %q, want running", st.Status)
	}
	if st.LastError == "" {
		t.Errorf("LastError = \"\", want the relay failure recorded")
	}
	if st.LastFailureAt == 0 {
		t.Errorf("LastFailureAt = 0, want non-zero after a failed send")
	}
}

// 6. rejected question relay -> SendInteractiveQuestion returns an error.
// The QuestionRouter fallback-to-text half of this scenario is covered
// by internal/bridge/service's existing TestInteractiveFallsBackToTextOnError
// (a fake adapter returning an error), which already proves the fallback
// path is channel-agnostic — no external-specific fallback test needed.
func TestSendInteractiveQuestionRejectedRelayFails(t *testing.T) {
	t.Parallel()
	_, srv := newCapturingRelay(t, http.StatusInternalServerError)
	a := newTestAdapter(t, srv)

	ctx := bridge.ContextWithExternalQuestion(context.Background(), bridge.ExternalQuestionContext{RequestID: "req-1"})
	resolved, err := a.SendInteractiveQuestion(ctx, testPeer(), "ship?", []bridge.QuestionChoice{{Label: "yes", Value: "yes"}})
	if err == nil {
		t.Fatal("expected non-nil error on rejected relay")
	}
	if resolved != "" {
		t.Errorf("resolvedPeer = %q, want empty", resolved)
	}
}

// 7. empty RelayBaseURL -> adapter disabled, no HTTP call attempted.
func TestEmptyRelayBaseURLDisablesAdapter(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("relay endpoint must not be called by a disabled adapter")
	}))
	t.Cleanup(srv.Close)

	a, err := New(Identity{ID: "c3", RelayBaseURL: "", RelayCredential: "s3cret"}, Options{})
	if err != nil {
		t.Fatalf("New must not error on empty config: %v", err)
	}
	if a == nil {
		t.Fatal("New returned nil adapter")
	}
	if got := a.Status().Status; got != "disabled" {
		t.Errorf("Status = %q, want disabled", got)
	}

	res := a.Send(context.Background(), bridge.Outbound{Peer: testPeer(), Text: "hi"})
	if res.Delivered || res.Err == nil {
		t.Errorf("Send on disabled adapter = %+v, want Delivered:false with an error", res)
	}

	resolved, err := a.SendInteractiveQuestion(context.Background(), testPeer(), "q?", nil)
	if err == nil || resolved != "" {
		t.Errorf("SendInteractiveQuestion on disabled adapter = (%q, %v), want (\"\", error)", resolved, err)
	}
}

// 8. empty RelayCredential -> same as 7.
func TestEmptyRelayCredentialDisablesAdapter(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("relay endpoint must not be called by a disabled adapter")
	}))
	t.Cleanup(srv.Close)

	a, err := New(Identity{ID: "c3", RelayBaseURL: srv.URL, RelayCredential: ""}, Options{})
	if err != nil {
		t.Fatalf("New must not error on empty config: %v", err)
	}
	if got := a.Status().Status; got != "disabled" {
		t.Errorf("Status = %q, want disabled", got)
	}

	res := a.Send(context.Background(), bridge.Outbound{Peer: testPeer(), Text: "hi"})
	if res.Delivered || res.Err == nil {
		t.Errorf("Send on disabled adapter = %+v, want Delivered:false with an error", res)
	}
}

// 9. resolved peer is always empty, both on success and failure. Folded
// as assertions into tests 2-8 above (every Send/SendInteractiveQuestion
// assertion checks ResolvedPeer/resolvedPeer == "").

// 11. an attachment relays as metadata only — no "content" key at all,
// asserted via map[string]any (not a Go-side zero-value check, which
// would pass even for the exact bug the spec calls out: reusing
// bridge.Attachment, whose Content []byte marshals as "content":null).
func TestSendAttachmentIsMetadataOnly(t *testing.T) {
	t.Parallel()
	relay, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)

	res := a.Send(context.Background(), bridge.Outbound{
		Peer: testPeer(),
		Text: "see attached",
		Attachments: []bridge.Attachment{
			{FileName: "report.pdf", MimeType: "application/pdf", Content: []byte("not-empty-content")},
		},
	})
	// 12. a withheld attachment still reports the send delivered.
	if !res.Delivered || res.Err != nil {
		t.Fatalf("Send result = %+v, want Delivered:true", res)
	}

	body := relay.last()
	atts, ok := body["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments = %+v, want exactly one entry", body["attachments"])
	}
	att, _ := atts[0].(map[string]any)
	if att["fileName"] != "report.pdf" || att["mimeType"] != "application/pdf" {
		t.Errorf("attachment metadata = %+v", att)
	}
	if size, ok := att["size"].(float64); !ok || int(size) != len("not-empty-content") {
		t.Errorf("attachment size = %v, want %d", att["size"], len("not-empty-content"))
	}
	if _, has := att["content"]; has {
		t.Errorf("attachment must have NO content key at all, got %v", att["content"])
	}
}

// 13. a text-only outbound emits no "attachments" key at all — asserted
// via map[string]any. Covered inline in TestSendMessageFrameShape's
// "attachments" key-presence check above; this test isolates it as its
// own case per the spec's numbered scenario list.
func TestSendTextOnlyOmitsAttachmentsKey(t *testing.T) {
	t.Parallel()
	relay, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)

	if res := a.Send(context.Background(), bridge.Outbound{Peer: testPeer(), Text: "no files here"}); !res.Delivered {
		t.Fatalf("Send result = %+v", res)
	}
	body := relay.last()
	if _, has := body["attachments"]; has {
		t.Errorf("text-only outbound must omit the attachments key entirely, got %v", body["attachments"])
	}
}

// Basic-auth credential sanity: the shared relay credential is used as
// the Basic-auth password (username is an arbitrary breadcrumb, mirrors
// internal/bridge/registrar.go's HTTPRegistrar.applyAuth).
func TestSendUsesBasicAuthWithRelayCredential(t *testing.T) {
	t.Parallel()
	relay, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)

	if res := a.Send(context.Background(), bridge.Outbound{Peer: testPeer(), Text: "hi"}); !res.Delivered {
		t.Fatalf("Send result = %+v", res)
	}
	if relay.callCount() != 1 {
		t.Fatalf("callCount = %d, want 1", relay.callCount())
	}
	if relay.authPass[0] != "s3cret" {
		t.Errorf("basic auth password = %q, want s3cret", relay.authPass[0])
	}
}

// Channel()/Identity()/InboundActive()/Start()/ResolveUserToDM() are the
// remaining bridge.Adapter contract surface — pin their trivial
// behaviors so a future refactor can't silently break them.
func TestAdapterTrivialContract(t *testing.T) {
	t.Parallel()
	_, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)

	if a.Channel() != "external" {
		t.Errorf("Channel() = %q, want external", a.Channel())
	}
	if a.Identity() != "c3" {
		t.Errorf("Identity() = %q, want c3", a.Identity())
	}
	if a.InboundActive() {
		t.Errorf("InboundActive() = true, want false")
	}
	if err := a.Start(context.Background(), make(chan bridge.Inbound)); err != nil {
		t.Errorf("Start() = %v, want nil", err)
	}
	if got, err := a.ResolveUserToDM(context.Background(), "some-peer-id"); err != nil || got != "some-peer-id" {
		t.Errorf("ResolveUserToDM() = (%q, %v), want (\"some-peer-id\", nil)", got, err)
	}
}

// relay frame shape for "progress": every progress key is present even
// at its zero value, and the frame names the session and job.
func TestSendProgressFrameShape(t *testing.T) {
	t.Parallel()
	relay, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)
	a.SetJobID("job-1")

	ctx := bridge.ContextWithSessionID(context.Background(), "sess-1")
	if err := a.SendProgress(ctx, testPeer(), bridge.RunProgress{
		Status:      bridge.RunProgressRunning,
		ToolsDone:   2,
		Running:     1,
		CurrentTool: "grep",
		Elapsed:     72 * time.Second,
	}); err != nil {
		t.Fatalf("SendProgress: %v", err)
	}

	body := relay.last()
	if body == nil {
		t.Fatal("no relay call captured")
	}
	if body["kind"] != "progress" || body["sessionId"] != "sess-1" || body["jobId"] != "job-1" {
		t.Errorf("kind/sessionId/jobId = %v/%v/%v; want progress/sess-1/job-1", body["kind"], body["sessionId"], body["jobId"])
	}
	if _, ok := body["text"]; ok {
		t.Errorf("progress frame carries text: %v", body["text"])
	}
	got, ok := body["progress"].(map[string]any)
	if !ok {
		t.Fatalf("progress = %#v; want an object", body["progress"])
	}
	want := map[string]any{
		"status":            "running",
		"toolsDone":         float64(2),
		"failed":            float64(0),
		"running":           float64(1),
		"currentTool":       "grep",
		"lastFailedTool":    "",
		"lastFailureReason": "",
		"elapsedMs":         float64(72000),
	}
	if len(got) != len(want) {
		t.Errorf("progress keys = %v; want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("progress[%q] = %#v; want %#v", k, got[k], v)
		}
	}
}

// A rejected progress frame is a failed send, recorded like any other.
func TestSendProgressNon202IsError(t *testing.T) {
	t.Parallel()
	_, srv := newCapturingRelay(t, http.StatusBadRequest)
	a := newTestAdapter(t, srv)

	if err := a.SendProgress(context.Background(), testPeer(), bridge.RunProgress{Status: bridge.RunProgressOK}); err == nil {
		t.Fatal("SendProgress on 400 returned nil error")
	}
	if a.Status().LastFailureAt == 0 {
		t.Error("LastFailureAt not recorded for a rejected progress frame")
	}
}

// The bridge offers RunProgress only to an adapter that cannot edit: a
// MessageEditor gets the rendered card instead, so the relay would stop
// receiving progress frames the moment it grew an edit method.
func TestAdapterIsNotMessageEditor(t *testing.T) {
	t.Parallel()
	if _, ok := any((*Adapter)(nil)).(bridge.MessageEditor); ok {
		t.Fatal("external adapter implements bridge.MessageEditor; deliverProgress would send it the card, not RunProgress")
	}
}

// A disabled adapter refuses progress without calling the relay.
func TestSendProgressDisabled(t *testing.T) {
	t.Parallel()
	a, err := New(Identity{ID: "c3"}, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.SendProgress(context.Background(), testPeer(), bridge.RunProgress{}); err == nil {
		t.Fatal("disabled adapter accepted SendProgress")
	}
}

// TestSendDeliveredFrameShape pins the acknowledgement frame: kind
// "delivered", the session and job ids, the ids in order, and no text.
func TestSendDeliveredFrameShape(t *testing.T) {
	t.Parallel()
	relay, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)
	a.SetJobID("job-1")

	ctx := bridge.ContextWithSessionID(context.Background(), "sess-1")
	if err := a.SendDelivered(ctx, testPeer(), []string{"a", "b"}); err != nil {
		t.Fatalf("SendDelivered: %v", err)
	}

	body := relay.last()
	if body == nil {
		t.Fatal("no relay call captured")
	}
	if body["kind"] != "delivered" || body["sessionId"] != "sess-1" || body["jobId"] != "job-1" {
		t.Errorf("kind/sessionId/jobId = %v/%v/%v; want delivered/sess-1/job-1", body["kind"], body["sessionId"], body["jobId"])
	}
	if _, ok := body["text"]; ok {
		t.Errorf("delivered frame carries text: %v", body["text"])
	}
	got, ok := body["delivered"].(map[string]any)
	if !ok {
		t.Fatalf("delivered = %#v; want an object", body["delivered"])
	}
	ids, ok := got["messageIds"].([]any)
	if !ok || len(got) != 1 || len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("delivered = %#v; want {messageIds: [a b]}", got)
	}
}

// A rejected acknowledgement is a failed send, recorded like any other.
func TestSendDeliveredNon202IsError(t *testing.T) {
	t.Parallel()
	_, srv := newCapturingRelay(t, http.StatusBadRequest)
	a := newTestAdapter(t, srv)

	if err := a.SendDelivered(context.Background(), testPeer(), []string{"a"}); err == nil {
		t.Fatal("SendDelivered on 400 returned nil error")
	}
	if a.Status().LastFailureAt == 0 {
		t.Error("LastFailureAt not recorded for a rejected delivered frame")
	}
}

// An empty acknowledgement is refused without calling the relay.
func TestSendDeliveredRequiresIDs(t *testing.T) {
	t.Parallel()
	relay, srv := newCapturingRelay(t, 0)
	a := newTestAdapter(t, srv)
	if err := a.SendDelivered(context.Background(), testPeer(), nil); err == nil {
		t.Fatal("SendDelivered with no ids returned nil error")
	}
	if relay.last() != nil {
		t.Fatal("empty acknowledgement reached the relay")
	}
}
