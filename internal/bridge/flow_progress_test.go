package bridge

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFlowProgressTextAndWindow(t *testing.T) {
	p := FlowProgress{FlowID: "review", Status: "running", StartedAt: 1000, UpdatedAt: 62000, TotalSteps: 101, Error: "failed ＠tool", Steps: make([]FlowProgressStep, 100)}
	for i := range p.Steps {
		p.Steps[i] = FlowProgressStep{ID: "step", Status: "completed"}
	}
	p.Steps[50].Status = "failed"
	p.Steps[70].Status = "running"
	visible, omitted := FlowVisibleSteps(p, 3)
	if omitted != 98 || len(visible) != 3 || visible[0].Status != "failed" || visible[1].Status != "running" {
		t.Fatalf("window=%+v omitted=%d", visible, omitted)
	}
	text := FlowProgressText(p)
	if !strings.Contains(text, "98/100 tracked steps · 101 total") || !strings.Contains(text, "1m1s") || !strings.Contains(text, "failed ＠tool") {
		t.Fatal(text)
	}
}

func TestFlowOutputJSONAndUnicodeBudget(t *testing.T) {
	text, structured := FlowStepOutput(FlowProgressStep{Output: `{"result":"@here <script>","count":2}`, IsStructOutput: true}, 200)
	if !structured || !strings.Contains(text, "\n  \"count\": 2") || strings.Contains(text, "@here") {
		t.Fatalf("%v %s", structured, text)
	}
	for limit := 0; limit < 100; limit++ {
		out := LimitFlowText(strings.Repeat("😺é", 100), limit)
		if !utf8.ValidString(out) || FlowTextLen(out) > limit {
			t.Fatalf("limit %d: %q", limit, out)
		}
	}
}

func TestFlowProgressWindowKeepsActiveStepAfterManyFailures(t *testing.T) {
	p := FlowProgress{}
	for i := 0; i < 30; i++ {
		p.Steps = append(p.Steps, FlowProgressStep{ID: "failed", Status: "failed"})
	}
	p.Steps = append(p.Steps, FlowProgressStep{ID: "active", Status: "running"})
	steps, _ := FlowVisibleSteps(p, 3)
	if steps[len(steps)-1].ID != "active" {
		t.Fatal(steps)
	}
}

func TestFlowProgressEndedPlansShowUnvisitedStepsAsSkipped(t *testing.T) {
	for _, status := range []string{"completed", "failed", "terminated"} {
		t.Run(status, func(t *testing.T) {
			p := FlowProgress{FlowID: "branching", Status: status, Steps: []FlowProgressStep{
				{ID: "done", Status: "completed"}, {ID: "unused", Status: "pending"}, {ID: "active", Status: "running"},
			}}
			steps, _ := FlowVisibleSteps(p, 12)
			if steps[1].Status != "skipped" || steps[2].Status != "interrupted" || p.Steps[1].Status != "pending" || p.Steps[2].Status != "running" {
				t.Fatalf("display=%+v, stored=%+v", steps, p.Steps)
			}
			if !strings.Contains(FlowProgressTitle(p), "1/3 steps") {
				t.Fatal("skipped steps counted as executed", FlowProgressTitle(p))
			}
		})
	}
}
