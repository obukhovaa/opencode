package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	slackgo "github.com/slack-go/slack"
)

// modalCapture wraps the existing newMockServer with a handler that
// records the most recent views.open call. The mock returns a
// minimal success response Slack-go can parse.
type modalCapture struct {
	mu   sync.Mutex
	body []byte
}

func newModalCapture(t *testing.T, m *mockSlackServer, cap *modalCapture) {
	t.Helper()
	// Inject /views.open into the mock by wrapping the existing
	// dispatch. We do this via the adapter's HTTPClient — see
	// TestOpenCustomAnswerModal_HappyPath below.
}

// TestOpenCustomAnswerModal_HappyPath verifies the helper POSTs a
// well-formed views.open payload with the metadata in
// private_metadata. We capture the request body via an http.Client
// RoundTripper.
func TestOpenCustomAnswerModal_HappyPath(t *testing.T) {
	t.Parallel()

	var capture modalCapture
	tripper := &captureTripper{
		mu:       &capture.mu,
		body:     &capture.body,
		response: `{"ok":true,"view":{"id":"V123"}}`,
	}
	client := &http.Client{Transport: tripper}

	a, err := New(Identity{
		ID:       "default",
		BotToken: "xoxb-test",
		AppToken: "xapp-test",
	}, Options{
		APIURL:     "http://localhost/",
		HTTPClient: client,
		MediaDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	meta := CustomAnswerMetadata{
		PeerID:    "C0123FAKEXX|1700000001.000001",
		RequestID: "req-XYZ",
	}
	if err := a.OpenCustomAnswerModal(context.Background(), "trig-xyz", meta); err != nil {
		t.Fatalf("OpenCustomAnswerModal: %v", err)
	}

	// Slack-go sends the views.open body as JSON (not
	// form-encoded). Inspect the raw JSON.
	body := string(capture.body)
	if !strings.Contains(body, `"trigger_id":"trig-xyz"`) {
		t.Errorf("body missing trigger_id: %s", body)
	}
	if !strings.Contains(body, `"action_id":"custom_answer"`) {
		t.Errorf("body missing input action_id: %s", body)
	}
	if !strings.Contains(body, `"private_metadata"`) {
		t.Errorf("body missing private_metadata field: %s", body)
	}
	if !strings.Contains(body, `\"peerId\":\"C0123FAKEXX|1700000001.000001\"`) {
		t.Errorf("body missing escaped peerId in private_metadata: %s", body)
	}
	if !strings.Contains(body, `\"requestId\":\"req-XYZ\"`) {
		t.Errorf("body missing escaped requestId in private_metadata: %s", body)
	}
	if !strings.Contains(body, `"callback_id":"custom_answer"`) {
		t.Errorf("body missing callback_id: %s", body)
	}
}

// captureTripper is a tiny http.RoundTripper that records the
// request body and returns a fixed response. Used to inspect what
// the slack-go OpenViewContext call put on the wire without
// standing up the full mock server's web-API surface.
type captureTripper struct {
	mu       *sync.Mutex
	body     *[]byte
	response string
}

func (c *captureTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if req.Body != nil {
		buf := make([]byte, 8192)
		n, _ := req.Body.Read(buf)
		*c.body = buf[:n]
	}
	return &http.Response{
		StatusCode: 200,
		Body:       &bodyReader{data: c.response},
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
	}, nil
}

// bodyReader is a tiny io.ReadCloser over a string. Sufficient for
// the mock response.
type bodyReader struct {
	data string
	off  int
}

func (b *bodyReader) Read(p []byte) (int, error) {
	if b.off >= len(b.data) {
		return 0, http.ErrBodyReadAfterClose
	}
	n := copy(p, b.data[b.off:])
	b.off += n
	return n, nil
}

func (b *bodyReader) Close() error { return nil }

// TestOpenCustomAnswerModal_RejectsEmptyTriggerID guards the
// pre-call validation: Slack will 400 a views.open without a
// trigger_id, so we surface that failure mode locally with a clear
// error.
func TestOpenCustomAnswerModal_RejectsEmptyTriggerID(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, err := New(Identity{
		ID:       "default",
		BotToken: "xoxb-test",
		AppToken: "xapp-test",
	}, Options{
		APIURL:     mock.URL() + "/",
		HTTPClient: &http.Client{},
		MediaDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = a.OpenCustomAnswerModal(context.Background(), "", CustomAnswerMetadata{PeerID: "D-1"})
	if err == nil || !strings.Contains(err.Error(), "trigger_id") {
		t.Errorf("err = %v", err)
	}
}

func TestOpenCustomAnswerModal_RejectsMissingPeerID(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, _ := New(Identity{
		ID:       "default",
		BotToken: "xoxb-test",
		AppToken: "xapp-test",
	}, Options{
		APIURL:     mock.URL() + "/",
		HTTPClient: &http.Client{},
		MediaDir:   t.TempDir(),
	})
	err := a.OpenCustomAnswerModal(context.Background(), "trig", CustomAnswerMetadata{})
	if err == nil || !strings.Contains(err.Error(), "PeerID") {
		t.Errorf("err = %v", err)
	}
}

// TestCustomAnswerMetadata_JSONShape locks the wire format so the
// orchestrator-side decoder stays compatible. The JSON tags MUST
// stay byte-identical to c2-agent/internal/orchestrator/bridge's
// CustomAnswerMetadata.
func TestCustomAnswerMetadata_JSONShape(t *testing.T) {
	t.Parallel()
	m := CustomAnswerMetadata{
		PeerID:    "C0123FAKEXX|1700000001.000001",
		RequestID: "req-XYZ",
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"peerId":"C0123FAKEXX|1700000001.000001","requestId":"req-XYZ"}`
	if string(body) != want {
		t.Errorf("wire shape drift\n got: %s\nwant: %s", body, want)
	}
}

// Suppress unused-import warnings if any helper falls out of use.
var _ = slackgo.VTModal
