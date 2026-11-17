package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

func TestFlowProgressMattermostRequests(t *testing.T) {
	calls := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"p1"}`)
	}))
	defer server.Close()
	a := &Adapter{client: NewClient(server.URL, "test", nil)}
	peer := bridge.PeerRef{PeerID: "channel|root"}
	p := bridge.FlowProgress{FlowID: "review", Status: "running", Full: true, Steps: []bridge.FlowProgressStep{{ID: "inspect", Status: "completed", Output: `{"answer":42}`, IsStructOutput: true}}}
	token, err := a.SendFlowProgress(context.Background(), peer, p)
	if err != nil {
		t.Fatal(err)
	}
	p.Full = false
	if err = a.EditFlowProgress(context.Background(), peer, token, p); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0]["root_id"] != "root" || calls[1]["id"] != "p1" {
		t.Fatal(calls)
	}
	raw, _ := json.Marshal(calls[0])
	if !strings.Contains(string(raw), `"collapsed":true`) || !strings.Contains(string(raw), "```json") {
		t.Fatal(string(raw))
	}
	raw, _ = json.Marshal(calls[1])
	if strings.Contains(string(raw), "answer") {
		t.Fatal("compact output leaked", string(raw))
	}
}

func TestFlowProgressMattermostMissingVersusTransient(t *testing.T) {
	for _, status := range []int{404, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); fmt.Fprint(w, `{"id":"error"}`) }))
			defer server.Close()
			a := &Adapter{client: NewClient(server.URL, "test", nil)}
			err := a.EditFlowProgress(context.Background(), bridge.PeerRef{PeerID: "c1"}, "p1", bridge.FlowProgress{})
			if err == nil || errors.Is(err, bridge.ErrFlowMessageNotFound) != (status == 404) {
				t.Fatal(err)
			}
		})
	}
}

func TestFlowProgressMattermostAggregateBudget(t *testing.T) {
	p := bridge.FlowProgress{FlowID: strings.Repeat("<", 1000), Status: "failed", Full: true, Error: strings.Repeat("<@all", 3000)}
	for i := 0; i < 100; i++ {
		p.Steps = append(p.Steps, bridge.FlowProgressStep{ID: strings.Repeat("<", 1000), Agent: strings.Repeat("<", 1000), Status: "completed", Output: `{"x":"` + strings.Repeat("<@all```😺", 3000) + `"}`, IsStructOutput: true})
	}
	props := flowProps(p)
	textChars, blocks := 0, 0
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if _, ok := x["type"]; ok {
				blocks++
			}
			for k, v := range x {
				if k == "text" {
					textChars += bridge.FlowTextLen(v.(string))
				} else {
					visit(v)
				}
			}
		case []any:
			for _, v := range x {
				visit(v)
			}
		}
	}
	visit(props)
	if blocks > 100 || textChars+3000 > 16000 {
		t.Fatalf("blocks %d chars %d", blocks, textChars)
	}
	raw, _ := json.Marshal(props)
	if strings.Contains(string(raw), "@all") || !strings.Contains(string(raw), "truncated") {
		t.Fatal("unsafe or unmarked output")
	}
}

func TestFlowProgressMattermostJSONAndPeerResolution(t *testing.T) {
	p := bridge.FlowProgress{Full: true, Steps: []bridge.FlowProgressStep{{ID: "json", Status: "completed", Output: "{\"value\":\"<a>&`\"}", IsStructOutput: true}}}
	blocks := flowProps(p)["mm_blocks"].([]any)
	output := blocks[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	body := strings.TrimSuffix(strings.TrimPrefix(output, "```json\n"), "\n```")
	var value map[string]string
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatal(body, err)
	}
	if value["value"] != "<a>&`" {
		t.Fatal(value)
	}
	a := &Adapter{}
	if got := a.ResolveFlowProgressPeer(bridge.PeerRef{PeerID: "channel"}, "post"); got != "channel|post" {
		t.Fatal(got)
	}
	if got := a.ResolveFlowProgressPeer(bridge.PeerRef{PeerID: "channel|root"}, "post"); got != "channel|root" {
		t.Fatal(got)
	}
}
