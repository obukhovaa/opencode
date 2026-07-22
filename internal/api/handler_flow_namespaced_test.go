package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/flow"
)

// TestFlowStartAcceptsNamespacedFlowID exercises the API surface's
// registry-backed flow-ID validation (the default validateFlowID wired
// by newFlowRunner, i.e. flow.Get) with a namespaced custom-path flow:
// POST /flow with `<namespace>/<basename>` must pass validation and
// return 202, while an unknown namespaced ID must still 404. Unlike the
// other handler tests this one keeps the default validator and loads a
// real config, because the namespaced-ID contract lives in the registry
// the validator consults.
//
// Not parallel: it mutates the process-global config and flow registry
// cache.
func TestFlowStartAcceptsNamespacedFlowID(t *testing.T) {
	workDir := t.TempDir()
	// Isolate global flow discovery from the developer's real home.
	t.Setenv("HOME", t.TempDir())

	// <work>/teams/id/flows/test-flow.yaml → flow ID "id/test-flow".
	flowsDir := filepath.Join(workDir, "teams", "id", "flows")
	if err := os.MkdirAll(flowsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "name: Namespaced Flow\ndescription: test flow\nflow:\n  steps:\n    - id: step-one\n      prompt: \"x\"\n"
	if err := os.WriteFile(filepath.Join(flowsDir, "test-flow.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	config.Reset()
	if _, err := config.Load(workDir, false); err != nil {
		t.Logf("config.Load warning: %v", err)
	}
	t.Cleanup(config.Reset)
	config.Get().FlowPaths = []string{flowsDir}

	flow.Invalidate()
	t.Cleanup(flow.Invalidate)

	svc := newStubFlowService([]flow.FlowState{
		{StepID: "step-one", Status: flow.FlowStatusRunning, UpdatedAt: 1700000000},
		{StepID: "step-one", Status: flow.FlowStatusCompleted, Output: "done"},
	})
	// Deliberately keep the default registry-backed validateFlowID.
	fr := newFlowRunner(svc)
	s := &Server{flowRunner: fr}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /flow", s.handleFlowStart)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// Unknown namespaced ID → 404 from the registry-backed validator.
	// Checked first so the accepted run below can't interfere.
	resp, err := server.Client().Post(server.URL+"/flow", "application/json",
		strings.NewReader(`{"flowID":"id/does-not-exist"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown namespaced flow: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	// Known namespaced ID → accepted.
	resp2, err := server.Client().Post(server.URL+"/flow", "application/json",
		strings.NewReader(`{"flowID":"id/test-flow"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusAccepted {
		t.Errorf("known namespaced flow: status = %d, want %d", resp2.StatusCode, http.StatusAccepted)
	}
}
