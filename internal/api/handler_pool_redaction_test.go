package api

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/llm/runidentity"
)

// syncBuffer is a bytes.Buffer safe for the logging goroutines to write
// while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestSecretsNeverReachTheLog is the redaction guard for the secrets
// that travel in a POST /flow body: the job-scoped MCP JWT, the
// orchestrator job identity, and the run's per-team LLM API key.
//
// A pool pod is long-lived and its logs interleave many jobs, so a token
// echoed once persists in the pod's log stream for the pod's whole
// lifetime — long past the JWT's own expiry window. The request-logging
// middleware deliberately records only method/path/status/duration, and
// no handler echoes the body; this test fails the moment either changes.
func TestSecretsNeverReachTheLog(t *testing.T) {
	const (
		secretToken  = "eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.SUPER-SECRET-JWT-BODY.sig"
		secretJobID  = "job-should-not-be-logged-1234"
		secretLLMKey = "sk-litellm-TEAM-KEY-MUST-NOT-BE-LOGGED"
	)
	t.Cleanup(func() { runidentity.Set(nil) })

	var captured syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&captured, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	svc := newPoolStubFlowService(false, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusCompleted,
	})
	s, srv := newPoolTestServer(t, poolTestOpts{
		svc:       svc,
		bound:     testWorkspace,
		allowlist: testWorkspace,
	})
	// Wire the run-scoped identity sinks so the full publish/clear path
	// runs — that is where a careless log line would most plausibly land.
	s.flowRunner.bridgeJobs = &recordingJobScoper{}
	s.flowRunner.mcpDiscovery = &recordingDiscoveryAuth{}

	client := srv.Client()
	// The middleware chain (logging included) only applies to the server's
	// own handler, so drive this through real HTTP.
	resp := postJSON(t, client, srv.URL+"/flow",
		`{"flowID":"A","mcpAuth":"`+secretToken+`","mcpAuthServer":"orchestrator","bridgeJobID":"`+secretJobID+`","llmApiKey":"`+secretLLMKey+`","telemetryUserId":"acme-dev","telemetryTeam":"acme"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /flow = %d, want 202", resp.StatusCode)
	}
	resp.Body.Close()

	waitFor(t, 2*time.Second, "run to terminate", func() bool {
		st := flowStatus(t, client, srv.URL)["status"]
		return st == string(flowRunCompleted) || st == "idle"
	})
	// Also exercise the error paths that echo a message back to the client.
	bad := postJSON(t, client, srv.URL+"/flow", `{"flowID":"B","mcpAuth":"`+secretToken+`"}`)
	bad.Body.Close()
	// ...and the health/status reads the orchestrator polls constantly.
	if r, err := client.Get(srv.URL + "/global/health"); err == nil {
		r.Body.Close()
	}

	logged := captured.String()
	if strings.Contains(logged, secretToken) {
		t.Errorf("the per-flow MCP token reached the log stream:\n%s", logged)
	}
	if strings.Contains(logged, "SUPER-SECRET-JWT-BODY") {
		t.Errorf("part of the per-flow MCP token reached the log stream:\n%s", logged)
	}
	if strings.Contains(logged, secretJobID) {
		t.Errorf("the bridge job identity reached the log stream:\n%s", logged)
	}
	// The LLM key is the highest-value secret in the body: unlike the MCP
	// JWT it is a long-lived LiteLLM credential with a team's budget
	// behind it, and a pool pod's log stream outlives any single run.
	if strings.Contains(logged, secretLLMKey) {
		t.Errorf("the per-run LLM API key reached the log stream:\n%s", logged)
	}
}

// TestSecretsNeverReachTheStatusSnapshot: /flow/status is polled by the
// orchestrator and, on a bridge-backed flow, its contents can be rendered
// into a chat card. Neither secret belongs in it.
func TestSecretsNeverReachTheStatusSnapshot(t *testing.T) {
	const (
		secretToken  = "eyJ-SECRET-TOKEN-IN-SNAPSHOT"
		secretLLMKey = "sk-litellm-KEY-IN-SNAPSHOT"
	)
	t.Cleanup(func() { runidentity.Set(nil) })

	svc := newPoolStubFlowService(true, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusRunning,
	})
	_, srv := newPoolTestServer(t, poolTestOpts{
		svc:       svc,
		bound:     testWorkspace,
		allowlist: testWorkspace,
	})
	client := srv.Client()
	startFlow(t, client, srv.URL,
		`{"flowID":"A","mcpAuth":"`+secretToken+`","mcpAuthServer":"orchestrator","bridgeJobID":"job-x","llmApiKey":"`+secretLLMKey+`"}`)
	waitFor(t, 2*time.Second, "run to be observed running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})

	for _, path := range []string{"/flow/status", "/global/health"} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(body), secretToken) {
			t.Errorf("GET %s echoes the per-flow MCP token: %s", path, body)
		}
		if strings.Contains(string(body), secretLLMKey) {
			t.Errorf("GET %s echoes the per-run LLM API key: %s", path, body)
		}
	}
}
