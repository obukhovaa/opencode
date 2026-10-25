package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/external"
	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/question"
)

// TestMidTurnInbox_InboundToDeliveredFrame drives the bridge half of the
// mid-turn inbox through its real parts: POST /router/inbound with a batch,
// the inbound loop, the process-wide inbox the agent loop drains, and the
// external adapter's relay POST. The drain itself is the agent loop's
// (internal/llm/agent TestInbox_*); here it is performed the way
// agent.drainInbox does it, DrainAll then Delivered.
func TestMidTurnInbox_InboundToDeliveredFrame(t *testing.T) {
	var mu sync.Mutex
	var frames []map[string]any
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var f map[string]any
		_ = json.Unmarshal(body, &f)
		mu.Lock()
		frames = append(frames, f)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(relay.Close)
	delivered := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		var out []map[string]any
		for _, f := range frames {
			if f["kind"] == "delivered" {
				out = append(out, f)
			}
		}
		return out
	}

	svc, _ := newOrchestratorForTest(t)
	svc.app = &app.App{
		Permissions: permission.NewPermissionService(),
		Questions:   question.NewService(),
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The agent loop drains inbox.Default(); use it here instead of the
	// private box this package's tests get by default.
	svc.questionRouter.mu.Lock()
	svc.questionRouter.inbox = inbox.Default()
	svc.questionRouter.mu.Unlock()
	t.Cleanup(func() { inbox.Default().Clear("S1") })

	ad, err := external.New(external.Identity{ID: "c3", RelayBaseURL: relay.URL, RelayCredential: "s3cret"}, external.Options{})
	if err != nil {
		t.Fatalf("external.New: %v", err)
	}
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	svc.SetRemoteJobID("job-1")
	peer := bridge.PeerRef{Channel: "external", Identity: "c3", PeerID: "aid1:flow1:run1"}
	if _, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{peer}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	svc.app.Permissions.MarkInteractiveSession("S1")

	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)
	post := func(ids ...string) {
		t.Helper()
		in := bridge.Inbound{Peer: peer}
		for _, id := range ids {
			in.Messages = append(in.Messages, bridge.InboundMessage{ID: id, Text: "text " + id})
		}
		body, _ := json.Marshal(in)
		resp, err := http.Post(api.URL+"/router/inbound", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("status %d, want 202", resp.StatusCode)
		}
	}

	// Two messages arrive while the agent is busy; nothing is acknowledged
	// until the agent loop stores them.
	post("a", "b")
	waitFor(t, "batch in the inbox", func() bool { return inbox.Default().Len("S1") == 2 })
	if n := len(delivered()); n != 0 {
		t.Fatalf("%d delivered frame(s) before the drain", n)
	}

	box := inbox.Default()
	items := box.DrainAll("S1")
	if got := []string{items[0].Text, items[1].Text}; !reflect.DeepEqual(got, []string{"text a", "text b"}) {
		t.Fatalf("drained %v, want the batch in order", got)
	}
	box.Delivered("S1", items)

	waitFor(t, "one delivered frame", func() bool { return len(delivered()) == 1 })
	f := delivered()[0]
	if f["sessionId"] != "S1" || f["jobId"] != "job-1" {
		t.Fatalf("frame session/job = %v/%v", f["sessionId"], f["jobId"])
	}
	if _, ok := f["text"]; ok {
		t.Fatalf("delivered frame carries text: %v", f["text"])
	}
	d, _ := f["delivered"].(map[string]any)
	if ids, _ := d["messageIds"].([]any); len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("delivered = %v, want messageIds [a b]", f["delivered"])
	}

	// The sender lost that frame and re-sends a: skipped, acknowledged again.
	post("a")
	waitFor(t, "re-acknowledgement", func() bool { return len(delivered()) == 2 })
	if n := inbox.Default().Len("S1"); n != 0 {
		t.Fatalf("repeat queued again (%d items)", n)
	}
}
