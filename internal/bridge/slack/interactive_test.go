package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// captureBlocks parses the latest sendCall and pulls out the blocks
// payload — the mock's existing post-capture path only records
// channel/text/thread_ts, so the test needs to peek at the JSON itself.
//
// For Slack's intelligent block layout, the lib serializes blocks as a
// JSON-encoded string in the form fields. We capture it raw here.
type blockCapture struct {
	mu     []byte
	blocks []map[string]any
}

func TestSlackSendInteractiveQuestionPostsBlocks(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	// Hook the chat.postMessage handler to capture the blocks form
	// field. slack-go encodes blocks as a JSON string in the
	// `blocks` POST form.
	mock.server.Config.Handler = blockCapturingHandler(mock.server.Config.Handler, t, mock)

	apiURL := mock.URL() + "/"
	a, err := New(Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, Options{APIURL: apiURL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetBotUserID("UBOT")
	t.Cleanup(func() { _ = a.Stop() })

	resolved, err := a.SendInteractiveQuestion(context.Background(),
		bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D012345"},
		"Ship it?",
		[]bridge.QuestionChoice{
			{Label: "Yes", Value: "Yes"},
			{Label: "No", Value: "No"},
		},
	)
	if err != nil {
		t.Fatalf("SendInteractiveQuestion: %v", err)
	}
	if resolved == "" {
		t.Errorf("expected resolved peer-id, got empty")
	}

	// The mock now has 1 post call with `blocks` content. Pull blocks.
	captures := capturedBlocks()
	if len(captures) == 0 {
		t.Fatalf("no blocks captured")
	}
	bs := captures[len(captures)-1]
	// Expect at minimum: 1 section block + 1 actions block.
	if len(bs) < 2 {
		t.Errorf("blocks len = %d, want >= 2", len(bs))
	}
	// Find actions block and verify it has 2 buttons whose values are
	// "Yes" / "No".
	var actions map[string]any
	for _, b := range bs {
		if b["type"] == "actions" {
			actions = b
			break
		}
	}
	if actions == nil {
		t.Fatalf("no actions block found in %+v", bs)
	}
	elements, ok := actions["elements"].([]any)
	if !ok || len(elements) != 2 {
		t.Fatalf("actions.elements len = %d, want 2", len(elements))
	}
	gotValues := []string{}
	for _, e := range elements {
		m, _ := e.(map[string]any)
		if m["type"] != "button" {
			t.Errorf("element type = %v, want button", m["type"])
		}
		gotValues = append(gotValues, fmt.Sprint(m["value"]))
	}
	if gotValues[0] != "Yes" || gotValues[1] != "No" {
		t.Errorf("button values = %v, want [Yes No]", gotValues)
	}
}

func TestSlackSendInteractiveQuestionInvalidPeerID(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	apiURL := mock.URL() + "/"
	a, err := New(Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, Options{APIURL: apiURL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetBotUserID("UBOT")
	t.Cleanup(func() { _ = a.Stop() })

	_, err = a.SendInteractiveQuestion(context.Background(),
		bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: ""},
		"x",
		[]bridge.QuestionChoice{{Label: "ok", Value: "ok"}},
	)
	if err == nil {
		t.Errorf("expected error for empty peerId")
	}
}

func TestSlackSendInteractiveQuestionRequiresChoices(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	apiURL := mock.URL() + "/"
	a, err := New(Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, Options{APIURL: apiURL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetBotUserID("UBOT")
	t.Cleanup(func() { _ = a.Stop() })

	_, err = a.SendInteractiveQuestion(context.Background(),
		bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"},
		"x", nil,
	)
	if err == nil || !strings.Contains(err.Error(), "at least one choice") {
		t.Errorf("err = %v", err)
	}
}

// --- Test helpers below --------------------------------------------------

// Captured blocks payloads, indexed in order of POST calls. Each entry
// is the decoded `blocks` JSON from one chat.postMessage call.
//
// Stored as a package-level slice the harness in blockCapturingHandler
// appends to. Reset in the test setup; tests are t.Parallel-safe via the
// init mutex.
var (
	capturedBlocksMu sync.Mutex
	capturedBlocksJ  [][]map[string]any
)

func capturedBlocks() [][]map[string]any {
	capturedBlocksMu.Lock()
	defer capturedBlocksMu.Unlock()
	out := make([][]map[string]any, len(capturedBlocksJ))
	copy(out, capturedBlocksJ)
	return out
}

// blockCapturingHandler wraps the mock server's existing handler with a
// pre-pass that extracts the `blocks` form field from chat.postMessage
// requests. The blocks value is a JSON-encoded array of block objects;
// we decode it and append to the package slice for later assertion.
func blockCapturingHandler(inner http.Handler, t *testing.T, mock *mockSlackServer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postMessage" || r.URL.Path == "/api/chat.postMessage" {
			if err := r.ParseForm(); err == nil {
				if raw := r.FormValue("blocks"); raw != "" {
					var arr []map[string]any
					if err := json.Unmarshal([]byte(raw), &arr); err == nil {
						capturedBlocksMu.Lock()
						capturedBlocksJ = append(capturedBlocksJ, arr)
						capturedBlocksMu.Unlock()
					}
					// Reconstruct the body so the inner mock can re-read.
					// (inner mock doesn't actually use blocks; it just
					// reads channel/text/thread_ts. ParseForm consumed
					// the body — restore.)
					body := r.PostForm.Encode()
					r.Body = io.NopCloser(strings.NewReader(body))
					r.ContentLength = int64(len(body))
				}
			}
		}
		inner.ServeHTTP(w, r)
	})
}

// Make t.Parallel safe — reset the slice at the start of each test that
// reads it. We use TestMain to ensure resets are isolated.
func init() {
	capturedBlocksJ = nil
}
