package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	slackgo "github.com/slack-go/slack"

	"github.com/opencode-ai/opencode/internal/bridge"
)

func flowFixture() bridge.FlowProgress {
	return bridge.FlowProgress{FlowID: "review", Status: "running", Full: true, Steps: []bridge.FlowProgressStep{{ID: "inspect", Agent: "reviewer", Status: "completed", Output: `{"answer":42}`, IsStructOutput: true, StartedAt: 1000, FinishedAt: 2000}}}
}

func TestFlowProgressSlackRequestsAndFallback(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			requests := []url.Values{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				requests = append(requests, r.Form)
				w.Header().Set("Content-Type", "application/json")
				if reject && len(requests) == 1 {
					fmt.Fprint(w, `{"ok":false,"error":"invalid_blocks"}`)
					return
				}
				fmt.Fprint(w, `{"ok":true,"channel":"C1","ts":"1.2","message":{"text":"ok"}}`)
			}))
			defer server.Close()
			a := &Adapter{api: slackgo.New("test", slackgo.OptionAPIURL(server.URL+"/"))}
			peer := bridge.PeerRef{PeerID: "C1|1.0"}
			token, err := a.SendFlowProgress(context.Background(), peer, flowFixture())
			if err != nil {
				t.Fatal(err)
			}
			if err = a.EditFlowProgress(context.Background(), peer, token, flowFixture()); err != nil {
				t.Fatal(err)
			}
			if requests[0].Get("thread_ts") != "1.0" || !strings.Contains(requests[0].Get("blocks"), `"type":"plan"`) {
				t.Fatal(requests[0])
			}
			last := requests[len(requests)-1]
			if last.Get("ts") != "1.2" {
				t.Fatal(last)
			}
			if reject && (!a.flowBlocksUnsupported.Load() || a.markdownBlocksUnsupported.Load() || last.Get("blocks") != "[]") {
				t.Fatal("independent fallback failed", last)
			}
		})
	}
}

func TestFlowProgressSlackMissingMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false,"error":"message_not_found"}`)
	}))
	defer server.Close()
	a := &Adapter{api: slackgo.New("test", slackgo.OptionAPIURL(server.URL+"/"))}
	if err := a.EditFlowProgress(context.Background(), bridge.PeerRef{PeerID: "C1"}, "C1\x001.2", flowFixture()); !errors.Is(err, bridge.ErrFlowMessageNotFound) {
		t.Fatal(err)
	}
}

func TestFlowProgressSlackBoundsAndFormatting(t *testing.T) {
	p := flowFixture()
	p.Steps[0].Output = `{"answer":"` + strings.Repeat("😺<@U123>```", 10000) + `"}`
	for i := 0; i < 100; i++ {
		p.Steps = append(p.Steps, p.Steps[0])
	}
	raw, err := json.Marshal(flowBlocks(p))
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(raw) || len(raw) > 100000 || !strings.Contains(string(raw), "truncated") || !strings.Contains(string(raw), "omitted") {
		t.Fatalf("size %d: %s", len(raw), raw[:100])
	}
	p = flowFixture()
	p.Full = false
	raw, _ = json.Marshal(flowBlocks(p))
	if strings.Contains(string(raw), "answer") {
		t.Fatal("compact output leaked", string(raw))
	}
	p.Full = true
	p.Steps[0].IsStructOutput = false
	p.Steps[0].Output = "**Bold** and `code`"
	raw, _ = json.Marshal(flowBlocks(p))
	if !strings.Contains(string(raw), `"bold":true`) || !strings.Contains(string(raw), `"code":true`) {
		t.Fatal(string(raw))
	}
}

func TestFlowProgressSlackLinksAndPeerResolution(t *testing.T) {
	p := flowFixture()
	p.Steps[0].IsStructOutput = false
	p.Steps[0].Output = "Review [**MR**](https://gitlab.example/mr/42)"
	raw, _ := json.Marshal(flowBlocks(p))
	if !strings.Contains(string(raw), `"url":"https://gitlab.example/mr/42"`) {
		t.Fatal(string(raw))
	}
	a := &Adapter{}
	for _, tc := range []struct{ peer, token, want string }{{"C1", "C1\x001.2", "C1|1.2"}, {"C1|root", "C1\x001.2", "C1|root"}, {"D1", "D1\x001.2", "D1"}, {"C1", "C2\x001.2", ""}} {
		if got := a.ResolveFlowProgressPeer(bridge.PeerRef{PeerID: tc.peer}, tc.token); got != tc.want {
			t.Fatalf("%+v got %q", tc, got)
		}
	}
	p.Steps[0].Output = " \n \t"
	raw, _ = json.Marshal(flowBlocks(p))
	if strings.Contains(string(raw), `"output":`) {
		t.Fatal("empty output block", string(raw))
	}
}

func TestFlowProgressSlackEndedPlanHasNoPendingOrRunningTasks(t *testing.T) {
	for _, status := range []string{"completed", "failed", "terminated"} {
		t.Run(status, func(t *testing.T) {
			p := flowFixture()
			p.Status, p.Summary = status, "Final result"
			p.Steps = append(p.Steps, bridge.FlowProgressStep{ID: "unvisited", Status: "pending"})
			blocks := flowBlocks(p)
			plan, ok := blocks[0].(*slackgo.PlanBlock)
			if !ok {
				t.Fatalf("plan must precede the outcome: %T", blocks[0])
			}
			for _, task := range plan.Tasks {
				if task.Status == slackgo.TaskCardStatusPending || task.Status == slackgo.TaskCardStatusInProgress {
					t.Fatalf("ended plan still spins: %+v", task)
				}
			}
			if !strings.Contains(plan.Tasks[1].Title, "skipped") {
				t.Fatalf("unvisited branch not labelled skipped: %+v", plan.Tasks[1])
			}
			if status != "completed" {
				failed := false
				for _, task := range plan.Tasks {
					failed = failed || task.Status == slackgo.TaskCardStatusError
				}
				if !failed {
					t.Fatal("unsuccessful run renders a successful plan")
				}
			}
		})
	}
}
