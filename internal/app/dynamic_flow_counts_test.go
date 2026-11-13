package app

import (
	"github.com/opencode-ai/opencode/internal/session"
	"testing"
)

func TestSynchronousFlowAgentsIncludeNestedAndExcludeOtherSteps(t *testing.T) {
	children := []session.Session{
		{ID: "sync", ParentSessionID: "step"},
		{ID: "nested", ParentSessionID: "sync"},
		{ID: "async", ParentSessionID: "step"},
		{ID: "other", ParentSessionID: "other-step"},
		{ID: "done", ParentSessionID: "step"},
	}
	got := countSynchronousFlowAgents("step", children, map[string]bool{"async": true}, func(id string) bool { return id != "done" })
	if got != 2 {
		t.Fatalf("count=%d want sync+nested only", got)
	}
}
