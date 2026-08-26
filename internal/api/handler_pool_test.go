package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/flow"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/agent/mcpauthctx"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// poolStubFlowService is the pool-test flow.Service stub: emits the
// configured steps, optionally holds the run open until the run context
// is cancelled, and records every run context so tests can assert the
// per-run MCP auth override injection.
type poolStubFlowService struct {
	*pubsub.Broker[flow.FlowState]
	steps []flow.FlowState
	hold  bool // keep the run open until ctx cancel after emitting steps

	mu   sync.Mutex
	ctxs []context.Context
}

func newPoolStubFlowService(hold bool, steps ...flow.FlowState) *poolStubFlowService {
	return &poolStubFlowService{
		Broker: pubsub.NewBroker[flow.FlowState](),
		steps:  steps,
		hold:   hold,
	}
}

func (s *poolStubFlowService) Run(ctx context.Context, _ string, flowID string, _ map[string]any, _ bool) (<-chan agentpkg.AgentEvent, <-chan *flow.FlowState, error) {
	s.mu.Lock()
	s.ctxs = append(s.ctxs, ctx)
	s.mu.Unlock()
	ae := make(chan agentpkg.AgentEvent)
	fs := make(chan *flow.FlowState, len(s.steps)+1)
	go func() {
		defer close(ae)
		defer close(fs)
		for i := range s.steps {
			st := s.steps[i]
			st.FlowID = flowID
			fs <- &st
		}
		if s.hold {
			<-ctx.Done()
		}
	}()
	return ae, fs, nil
}

func (s *poolStubFlowService) capturedCtx(t *testing.T, i int) context.Context {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.ctxs) {
		t.Fatalf("captured ctx %d not available (have %d)", i, len(s.ctxs))
	}
	return s.ctxs[i]
}

// poolTestOpts configures newPoolTestServer.
type poolTestOpts struct {
	svc            flow.Service
	bound          string // raw bound-workspace URL ("" = unbound)
	allowlist      string // raw CSV
	sentinelPath   string
	bindExitGrace  time.Duration
	drainGrace     time.Duration
	idleResetGrace time.Duration
	exitFunc       func(int)
	shutdownFunc   func()
}

// newPoolTestServer builds a pool-mode Server the same way NewServer
// does (white-box: pool fields set directly so no *app.App is needed)
// and serves its full route table.
func newPoolTestServer(t *testing.T, o poolTestOpts) (*Server, *httptest.Server) {
	t.Helper()
	s := &Server{}
	s.poolMode = true
	s.poolBoundWorkspace = normalizeWorkspaceURL(o.bound)
	if s.poolBoundWorkspace != "" {
		s.poolBoundSince = time.Now().UnixMilli()
	}
	s.poolAllowlist = parseWorkspaceAllowlist(o.allowlist)
	s.poolSentinelPath = o.sentinelPath
	if s.poolSentinelPath == "" {
		s.poolSentinelPath = filepath.Join(t.TempDir(), ".pool-bind")
	}
	s.poolBindExitGrace = o.bindExitGrace
	s.poolDrainGrace = o.drainGrace
	s.poolExit = o.exitFunc
	if s.poolExit == nil {
		s.poolExit = func(int) {}
	}
	s.poolShutdown = o.shutdownFunc
	if o.svc != nil {
		fr := newFlowRunner(o.svc)
		fr.validateFlowID = nil
		fr.poolMode = true
		fr.idleResetGrace = o.idleResetGrace
		fr.draining = &s.poolDraining
		fr.binding = &s.poolBinding
		s.flowRunner = fr
	}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	// Serve through the SAME middleware chain NewServer builds. Serving
	// the bare mux would leave loggingMiddleware, corsMiddleware and
	// recoveryMiddleware unexercised for every pool route — so, for
	// instance, the secrets-redaction guard would pass even if the request
	// logger started dumping bodies.
	//
	// NOTE: s.password is empty here (only NewServer reads
	// OPENCODE_SERVER_PASSWORD), so authMiddleware is a passthrough and
	// these tests do NOT exercise authentication. That is deliberate —
	// every case below would otherwise need credentials — and
	// TestPoolRoutesRequireThePassword covers the auth contract directly.
	s.corsOrigin = "*"
	handler := chain(
		mux,
		recoveryMiddleware,
		loggingMiddleware,
		corsMiddleware(s.corsOrigin),
		authMiddleware(s.password),
		jsonContentTypeMiddleware,
	)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return s, server
}

func postJSON(t *testing.T, client *http.Client, url, body string) *http.Response {
	t.Helper()
	resp, err := client.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

// waitFor polls cond until true or the deadline, failing the test on
// timeout.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func flowStatus(t *testing.T, client *http.Client, baseURL string) map[string]any {
	t.Helper()
	resp, err := client.Get(baseURL + "/flow/status")
	if err != nil {
		t.Fatalf("GET /flow/status: %v", err)
	}
	return decodeBody(t, resp)
}

const testWorkspace = "https://git.example.com/acme/agents/developer"

// --- URL normalisation (B.3) ---

func TestNormalizeWorkspaceURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "https://git.example.com/acme/dev", "https://git.example.com/acme/dev"},
		{"trailing slash", "https://git.example.com/acme/dev/", "https://git.example.com/acme/dev"},
		{"trailing .git", "https://git.example.com/acme/dev.git", "https://git.example.com/acme/dev"},
		{"trailing .git slash", "https://git.example.com/acme/dev.git/", "https://git.example.com/acme/dev"},
		{"host lowercased", "https://Git.EXAMPLE.com/acme/dev", "https://git.example.com/acme/dev"},
		{"scheme lowercased", "HTTPS://git.example.com/acme/dev", "https://git.example.com/acme/dev"},
		{"path case preserved", "https://git.example.com/Acme/Dev", "https://git.example.com/Acme/Dev"},
		{"whitespace trimmed", "  https://git.example.com/acme/dev \n", "https://git.example.com/acme/dev"},
		{"host only", "https://GITLAB.com", "https://gitlab.com"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeWorkspaceURL(tt.in); got != tt.want {
				t.Errorf("normalizeWorkspaceURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseWorkspaceAllowlist(t *testing.T) {
	t.Parallel()
	got := parseWorkspaceAllowlist("https://gitlab.com/a.git, https://GitLab.com/b/ ,,")
	want := []string{"https://gitlab.com/a", "https://gitlab.com/b"}
	if len(got) != len(want) {
		t.Fatalf("allowlist = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("allowlist[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- POST /pool/bind (B.1, B.4, B.5, B.9) ---

func TestPoolBindFreshBind(t *testing.T) {
	t.Parallel()
	sentinel := filepath.Join(t.TempDir(), ".pool-bind")
	exitCh := make(chan int, 1)
	_, server := newPoolTestServer(t, poolTestOpts{
		svc:           newPoolStubFlowService(false),
		allowlist:     testWorkspace,
		sentinelPath:  sentinel,
		bindExitGrace: 10 * time.Millisecond,
		exitFunc:      func(code int) { exitCh <- code },
	})

	resp := postJSON(t, server.Client(), server.URL+"/pool/bind",
		`{"workspace":"`+testWorkspace+`.git/"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["binding"] != testWorkspace {
		t.Errorf("binding = %v, want %s", body["binding"], testWorkspace)
	}
	if body["exitGraceMs"] != float64(10) {
		t.Errorf("exitGraceMs = %v, want 10", body["exitGraceMs"])
	}

	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel not written: %v", err)
	}
	if string(data) != testWorkspace {
		t.Errorf("sentinel content = %q, want %q", string(data), testWorkspace)
	}

	select {
	case code := <-exitCh:
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("os.Exit(0) not scheduled after bind exit grace")
	}
}

func TestPoolBindIdempotentSameURL(t *testing.T) {
	t.Parallel()
	sentinel := filepath.Join(t.TempDir(), ".pool-bind")
	exitCh := make(chan int, 1)
	_, server := newPoolTestServer(t, poolTestOpts{
		svc:           newPoolStubFlowService(false),
		bound:         testWorkspace,
		allowlist:     testWorkspace,
		sentinelPath:  sentinel,
		bindExitGrace: 5 * time.Millisecond,
		exitFunc:      func(code int) { exitCh <- code },
	})

	// A normalisation-equivalent variant of the bound URL must be
	// recognised as the same workspace.
	resp := postJSON(t, server.Client(), server.URL+"/pool/bind",
		`{"workspace":"`+testWorkspace+`.git"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["alreadyBound"] != true {
		t.Errorf("alreadyBound = %v, want true", body["alreadyBound"])
	}
	if body["binding"] != testWorkspace {
		t.Errorf("binding = %v, want %s", body["binding"], testWorkspace)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("sentinel written on idempotent bind (stat err = %v)", err)
	}
	select {
	case <-exitCh:
		t.Error("exit scheduled on idempotent bind")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPoolBindConflictWhenBoundElsewhere(t *testing.T) {
	t.Parallel()
	sentinel := filepath.Join(t.TempDir(), ".pool-bind")
	_, server := newPoolTestServer(t, poolTestOpts{
		svc:          newPoolStubFlowService(false),
		bound:        testWorkspace,
		allowlist:    testWorkspace + ",https://gitlab.com/other/repo",
		sentinelPath: sentinel,
	})

	resp := postJSON(t, server.Client(), server.URL+"/pool/bind",
		`{"workspace":"https://gitlab.com/other/repo"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["boundWorkspace"] != testWorkspace {
		t.Errorf("boundWorkspace = %v, want %s", body["boundWorkspace"], testWorkspace)
	}
	if !strings.Contains(body["error"].(string), "recycle to rebind") {
		t.Errorf("error = %v, want mention of recycle", body["error"])
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("sentinel written on conflicting bind")
	}
}

func TestPoolBindForbiddenWhenNotAllowlisted(t *testing.T) {
	t.Parallel()
	sentinel := filepath.Join(t.TempDir(), ".pool-bind")
	_, server := newPoolTestServer(t, poolTestOpts{
		svc:          newPoolStubFlowService(false),
		allowlist:    testWorkspace,
		sentinelPath: sentinel,
	})

	resp := postJSON(t, server.Client(), server.URL+"/pool/bind",
		`{"workspace":"https://gitlab.com/evil/repo"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["error"] != "workspace not in allowlist" {
		t.Errorf("error = %v", body["error"])
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("sentinel written for non-allowlisted workspace")
	}
}

func TestPoolBindRejectedWhileFlowInFlight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		steps []flow.FlowState
	}{
		{"running", []flow.FlowState{{StepID: "s1", Status: flow.FlowStatusRunning}}},
		{"waiting_for_input", []flow.FlowState{
			{StepID: "s1", Status: flow.FlowStatusRunning},
			{StepID: "s1", Status: flow.FlowStatusWaitingForInput},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := newPoolStubFlowService(true, tt.steps...)
			s, server := newPoolTestServer(t, poolTestOpts{
				svc:       svc,
				bound:     testWorkspace,
				allowlist: testWorkspace,
			})

			resp := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"x"}`)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("POST /flow = %d, want 202", resp.StatusCode)
			}
			resp.Body.Close()
			wantStatus := string(flowRunRunning)
			if tt.name == "waiting_for_input" {
				wantStatus = string(flowRunWaitingForInput)
			}
			waitFor(t, 2*time.Second, "run to reach "+wantStatus, func() bool {
				snap := s.flowRunner.Snapshot()
				return snap != nil && string(snap.Status) == wantStatus
			})

			bindResp := postJSON(t, server.Client(), server.URL+"/pool/bind",
				`{"workspace":"`+testWorkspace+`"}`)
			if bindResp.StatusCode != http.StatusBadRequest {
				t.Fatalf("bind status = %d, want 400", bindResp.StatusCode)
			}
			body := decodeBody(t, bindResp)
			if body["error"] != "flow in progress; cannot rebind" {
				t.Errorf("error = %v", body["error"])
			}
		})
	}
}

func TestPoolBindGet(t *testing.T) {
	t.Parallel()
	t.Run("unbound", func(t *testing.T) {
		t.Parallel()
		_, server := newPoolTestServer(t, poolTestOpts{svc: newPoolStubFlowService(false)})
		resp, err := server.Client().Get(server.URL + "/pool/bind")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		body := decodeBody(t, resp)
		if body["boundWorkspace"] != nil {
			t.Errorf("boundWorkspace = %v, want null", body["boundWorkspace"])
		}
		if body["since"] != nil {
			t.Errorf("since = %v, want null", body["since"])
		}
	})
	t.Run("bound", func(t *testing.T) {
		t.Parallel()
		_, server := newPoolTestServer(t, poolTestOpts{
			svc:   newPoolStubFlowService(false),
			bound: testWorkspace + ".git",
		})
		resp, err := server.Client().Get(server.URL + "/pool/bind")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		body := decodeBody(t, resp)
		if body["boundWorkspace"] != testWorkspace {
			t.Errorf("boundWorkspace = %v, want %s", body["boundWorkspace"], testWorkspace)
		}
		if since, ok := body["since"].(float64); !ok || since <= 0 {
			t.Errorf("since = %v, want positive unix-ms", body["since"])
		}
	})
}

// --- POST /flow pool gates (B.7, A.3) ---

func TestFlowStartRejectedWhenUnbound(t *testing.T) {
	t.Parallel()
	_, server := newPoolTestServer(t, poolTestOpts{svc: newPoolStubFlowService(false)})

	resp := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"x"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["error"] != "pod not bound; call POST /pool/bind first" {
		t.Errorf("error = %v", body["error"])
	}
}

func TestFlowStartWorkspaceMismatch(t *testing.T) {
	t.Parallel()
	_, server := newPoolTestServer(t, poolTestOpts{
		svc:   newPoolStubFlowService(false),
		bound: testWorkspace,
	})

	resp := postJSON(t, server.Client(), server.URL+"/flow",
		`{"flowID":"x","workspace":"https://gitlab.com/other/repo"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["boundWorkspace"] != testWorkspace {
		t.Errorf("boundWorkspace = %v, want %s", body["boundWorkspace"], testWorkspace)
	}
}

func TestFlowStartWorkspaceMatchesAfterNormalisation(t *testing.T) {
	t.Parallel()
	_, server := newPoolTestServer(t, poolTestOpts{
		svc:   newPoolStubFlowService(false, flow.FlowState{StepID: "s1", Status: flow.FlowStatusCompleted}),
		bound: testWorkspace,
	})

	resp := postJSON(t, server.Client(), server.URL+"/flow",
		`{"flowID":"x","workspace":"`+testWorkspace+`.git/"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestFlowStartMCPAuthRequiresServer(t *testing.T) {
	t.Parallel()
	_, server := newPoolTestServer(t, poolTestOpts{
		svc:   newPoolStubFlowService(false),
		bound: testWorkspace,
	})

	resp := postJSON(t, server.Client(), server.URL+"/flow",
		`{"flowID":"x","mcpAuth":"T1"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["error"] != "mcpAuthServer required when mcpAuth is set" {
		t.Errorf("error = %v", body["error"])
	}
	// No run was started.
	status := flowStatus(t, server.Client(), server.URL)
	if status["status"] != "idle" {
		t.Errorf("flow status = %v, want idle", status["status"])
	}
}

// TestFlowStartInjectsMCPAuthOverride is the Phase A contract test: the
// run context carries the override for the named server during the run
// that supplied mcpAuth, and the next run without mcpAuth carries none.
func TestFlowStartInjectsMCPAuthOverride(t *testing.T) {
	t.Parallel()
	svc := newPoolStubFlowService(false, flow.FlowState{StepID: "s1", Status: flow.FlowStatusCompleted})
	s, server := newPoolTestServer(t, poolTestOpts{
		svc:   svc,
		bound: testWorkspace,
		// grace 0 → snapshot resets synchronously on terminal, so run 2
		// can start immediately.
		idleResetGrace: 0,
	})

	resp := postJSON(t, server.Client(), server.URL+"/flow",
		`{"flowID":"a","mcpAuth":"T1","mcpAuthServer":"orchestrator"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run A status = %d, want 202", resp.StatusCode)
	}
	resp.Body.Close()
	waitFor(t, 2*time.Second, "run A to terminate", func() bool {
		return s.flowRunner.Snapshot() == nil
	})

	runACtx := svc.capturedCtx(t, 0)
	if v, ok := mcpauthctx.AuthOverrideFromContext(runACtx, "orchestrator"); !ok || v != "Bearer T1" {
		t.Errorf("run A override = %q ok=%v, want Bearer T1 true", v, ok)
	}
	// The override is keyed to the named server only.
	if _, ok := mcpauthctx.AuthOverrideFromContext(runACtx, "gitlab"); ok {
		t.Error("run A override leaked to a different server name")
	}
	// runCtx is cancelled on terminal, bounding the override's reach.
	select {
	case <-runACtx.Done():
	default:
		t.Error("run A ctx not cancelled after terminal")
	}

	resp = postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"b"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run B status = %d, want 202", resp.StatusCode)
	}
	resp.Body.Close()
	waitFor(t, 2*time.Second, "run B to terminate", func() bool {
		return s.flowRunner.Snapshot() == nil
	})

	runBCtx := svc.capturedCtx(t, 1)
	if _, ok := mcpauthctx.AuthOverrideFromContext(runBCtx, "orchestrator"); ok {
		t.Error("run B (no mcpAuth) inherited run A's override")
	}
}

// TestFlowStartAbortCancelsRunCtx covers the D7 revert-on-abort
// scenario: DELETE /flow cancels the run context carrying the override.
func TestFlowStartAbortCancelsRunCtx(t *testing.T) {
	t.Parallel()
	svc := newPoolStubFlowService(true, flow.FlowState{StepID: "s1", Status: flow.FlowStatusRunning})
	s, server := newPoolTestServer(t, poolTestOpts{
		svc:   svc,
		bound: testWorkspace,
	})

	resp := postJSON(t, server.Client(), server.URL+"/flow",
		`{"flowID":"a","mcpAuth":"T1","mcpAuthServer":"orchestrator"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	resp.Body.Close()
	waitFor(t, 2*time.Second, "run to be running", func() bool {
		snap := s.flowRunner.Snapshot()
		return snap != nil && snap.Status == flowRunRunning
	})

	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/flow", nil)
	delResp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE = %d, want 200", delResp.StatusCode)
	}

	runCtx := svc.capturedCtx(t, 0)
	select {
	case <-runCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("run ctx not cancelled after DELETE /flow")
	}
}

// --- Pool in-flight guard (B''.3 / design D8) ---

func TestFlowStartPoolGuardCoversWaitingForInput(t *testing.T) {
	t.Parallel()
	svc := newPoolStubFlowService(true,
		flow.FlowState{StepID: "s1", Status: flow.FlowStatusRunning},
		flow.FlowState{StepID: "s1", Status: flow.FlowStatusWaitingForInput},
	)
	s, server := newPoolTestServer(t, poolTestOpts{
		svc:   svc,
		bound: testWorkspace,
	})

	resp := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"a"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first POST = %d, want 202", resp.StatusCode)
	}
	first := decodeBody(t, resp)
	waitFor(t, 2*time.Second, "run to reach waiting_for_input", func() bool {
		snap := s.flowRunner.Snapshot()
		return snap != nil && snap.Status == flowRunWaitingForInput
	})

	resp2 := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"b"}`)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second POST = %d, want 409", resp2.StatusCode)
	}
	resp2.Body.Close()

	// The waiting run is untouched — same runID, still waiting.
	snap := s.flowRunner.Snapshot()
	if snap == nil || snap.RunID != first["runID"] {
		t.Fatalf("currentRun replaced: snap = %+v, want runID %v", snap, first["runID"])
	}
	if snap.Status != flowRunWaitingForInput {
		t.Errorf("status = %q, want waiting_for_input", snap.Status)
	}
}

// TestFlowStartNonPoolReplacesWaitingRun pins today's non-pool
// behaviour exactly: without --pool-mode a run in waiting_for_input is
// NOT protected and the next POST /flow replaces it.
func TestFlowStartNonPoolReplacesWaitingRun(t *testing.T) {
	t.Parallel()
	svc := newPoolStubFlowService(true,
		flow.FlowState{StepID: "s1", Status: flow.FlowStatusRunning},
		flow.FlowState{StepID: "s1", Status: flow.FlowStatusWaitingForInput},
	)
	fr := newFlowRunner(svc)
	fr.validateFlowID = nil
	s := &Server{flowRunner: fr}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	resp := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"a"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first POST = %d, want 202", resp.StatusCode)
	}
	first := decodeBody(t, resp)
	waitFor(t, 2*time.Second, "run to reach waiting_for_input", func() bool {
		snap := fr.Snapshot()
		return snap != nil && snap.Status == flowRunWaitingForInput
	})

	resp2 := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"b"}`)
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("second POST = %d, want 202 (non-pool waiting run is replaceable)", resp2.StatusCode)
	}
	second := decodeBody(t, resp2)
	if second["runID"] == first["runID"] {
		t.Error("second run did not get a fresh runID")
	}
}

// TestPoolAbortCoversWaitingRun: pool mode extends DELETE /flow to any
// non-terminal run so the orchestrator's recycle remedy works for a run
// parked on a reviewer question.
func TestPoolAbortCoversWaitingRun(t *testing.T) {
	t.Parallel()
	svc := newPoolStubFlowService(true,
		flow.FlowState{StepID: "s1", Status: flow.FlowStatusRunning},
		flow.FlowState{StepID: "s1", Status: flow.FlowStatusWaitingForInput},
	)
	s, server := newPoolTestServer(t, poolTestOpts{
		svc:   svc,
		bound: testWorkspace,
	})

	resp := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"a"}`)
	resp.Body.Close()
	waitFor(t, 2*time.Second, "run to reach waiting_for_input", func() bool {
		snap := s.flowRunner.Snapshot()
		return snap != nil && snap.Status == flowRunWaitingForInput
	})

	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/flow", nil)
	delResp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Errorf("DELETE = %d, want 200 (pool mode aborts waiting runs)", delResp.StatusCode)
	}
}

// --- Idle reset (Phase C) ---

func TestPoolIdleResetAfterGrace(t *testing.T) {
	t.Parallel()
	svc := newPoolStubFlowService(false,
		flow.FlowState{StepID: "s1", Status: flow.FlowStatusRunning},
		flow.FlowState{StepID: "s1", Status: flow.FlowStatusCompleted, Output: "done"},
	)
	s, server := newPoolTestServer(t, poolTestOpts{
		svc:            svc,
		bound:          testWorkspace,
		idleResetGrace: 150 * time.Millisecond,
	})

	resp := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"a"}`)
	resp.Body.Close()
	waitFor(t, 2*time.Second, "run to terminate", func() bool {
		snap := s.flowRunner.Snapshot()
		return snap != nil && snap.Status == flowRunCompleted
	})

	// Within the grace window the terminal snapshot is retained.
	status := flowStatus(t, server.Client(), server.URL)
	if status["status"] != string(flowRunCompleted) {
		t.Fatalf("status inside grace = %v, want completed", status["status"])
	}

	// After the grace window the runner resets to idle.
	waitFor(t, 2*time.Second, "idle reset", func() bool {
		return flowStatus(t, server.Client(), server.URL)["status"] == "idle"
	})
}

func TestPoolIdleResetGraceZeroIsImmediate(t *testing.T) {
	t.Parallel()
	svc := newPoolStubFlowService(false,
		flow.FlowState{StepID: "s1", Status: flow.FlowStatusCompleted},
	)
	s, server := newPoolTestServer(t, poolTestOpts{
		svc:            svc,
		bound:          testWorkspace,
		idleResetGrace: 0,
	})

	resp := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"a"}`)
	resp.Body.Close()
	waitFor(t, 2*time.Second, "immediate idle reset", func() bool {
		return s.flowRunner.Snapshot() == nil
	})
	status := flowStatus(t, server.Client(), server.URL)
	if status["status"] != "idle" {
		t.Errorf("status = %v, want idle", status["status"])
	}
}

func TestPoolIdleResetCancelledByNewStart(t *testing.T) {
	t.Parallel()
	// Run A completes instantly; run B holds open. Both served by one
	// stub: first call gets A's steps... a single stub emits the same
	// steps every run, so use completed steps + hold=false for A, then
	// swap? Simpler: one stub emitting a completed step per run with
	// hold=false — run B must outlive A's grace window, so give B a
	// hold-open stub via a second runner start through the same service
	// is impossible. Instead: A completes; B starts inside A's grace and
	// holds via a running step + hold.
	svc := &switchingFlowService{
		Broker: pubsub.NewBroker[flow.FlowState](),
		runs: [][]flow.FlowState{
			{{StepID: "a1", Status: flow.FlowStatusCompleted}},
			{{StepID: "b1", Status: flow.FlowStatusRunning}},
		},
		holdFrom: 1, // second run holds open
	}
	s, server := newPoolTestServer(t, poolTestOpts{
		svc:            svc,
		bound:          testWorkspace,
		idleResetGrace: 200 * time.Millisecond,
	})

	resp := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"a"}`)
	resp.Body.Close()
	waitFor(t, 2*time.Second, "run A to terminate", func() bool {
		snap := s.flowRunner.Snapshot()
		return snap != nil && snap.Status == flowRunCompleted
	})

	// Start run B inside A's grace window.
	resp2 := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"b"}`)
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("run B = %d, want 202", resp2.StatusCode)
	}
	body := decodeBody(t, resp2)

	// Wait past A's grace: the timer was cancelled, so the status shows
	// run B, never idle.
	time.Sleep(300 * time.Millisecond)
	status := flowStatus(t, server.Client(), server.URL)
	if status["status"] == "idle" {
		t.Fatal("idle reset fired despite run B replacing the snapshot")
	}
	if status["runID"] != body["runID"] {
		t.Errorf("status runID = %v, want run B %v", status["runID"], body["runID"])
	}
}

// TestNonPoolRetainsTerminalSnapshot pins the per-Job / daemon-mode
// behaviour: without poolMode the terminal snapshot is retained
// indefinitely (until process exit), even when an idle-reset grace is
// configured on the runner.
func TestNonPoolRetainsTerminalSnapshot(t *testing.T) {
	t.Parallel()
	svc := newPoolStubFlowService(false,
		flow.FlowState{StepID: "s1", Status: flow.FlowStatusCompleted},
	)
	fr := newFlowRunner(svc)
	fr.validateFlowID = nil
	// Deliberately set a grace: it must be ignored without poolMode.
	fr.idleResetGrace = 50 * time.Millisecond

	if _, err := fr.Start(context.Background(), "x", nil, false); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, 2*time.Second, "run to terminate", func() bool {
		snap := fr.Snapshot()
		return snap != nil && snap.Status == flowRunCompleted
	})

	time.Sleep(200 * time.Millisecond) // well past the would-be grace
	snap := fr.Snapshot()
	if snap == nil {
		t.Fatal("terminal snapshot reset to idle in non-pool mode")
	}
	if snap.Status != flowRunCompleted {
		t.Errorf("status = %q, want completed", snap.Status)
	}
}

// switchingFlowService emits a different step script per run and can
// hold runs open from a given run index onward.
type switchingFlowService struct {
	*pubsub.Broker[flow.FlowState]
	runs     [][]flow.FlowState
	holdFrom int

	mu sync.Mutex
	n  int
}

func (s *switchingFlowService) Run(ctx context.Context, _ string, flowID string, _ map[string]any, _ bool) (<-chan agentpkg.AgentEvent, <-chan *flow.FlowState, error) {
	s.mu.Lock()
	idx := s.n
	s.n++
	s.mu.Unlock()
	var steps []flow.FlowState
	if idx < len(s.runs) {
		steps = s.runs[idx]
	}
	hold := idx >= s.holdFrom
	ae := make(chan agentpkg.AgentEvent)
	fs := make(chan *flow.FlowState, len(steps)+1)
	go func() {
		defer close(ae)
		defer close(fs)
		for i := range steps {
			st := steps[i]
			st.FlowID = flowID
			fs <- &st
		}
		if hold {
			<-ctx.Done()
		}
	}()
	return ae, fs, nil
}

// --- POST /flow/recycle (Phase D) ---

func TestFlowRecycleWhenIdle(t *testing.T) {
	t.Parallel()
	shutdownCh := make(chan struct{}, 1)
	_, server := newPoolTestServer(t, poolTestOpts{
		svc:          newPoolStubFlowService(false),
		bound:        testWorkspace,
		allowlist:    testWorkspace,
		drainGrace:   20 * time.Millisecond,
		shutdownFunc: func() { shutdownCh <- struct{}{} },
	})

	resp := postJSON(t, server.Client(), server.URL+"/flow/recycle", `{"reason":"run-count-reached"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("recycle = %d, want 202", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["draining"] != true {
		t.Errorf("draining = %v, want true", body["draining"])
	}

	// Health flips to draining.
	hResp, err := server.Client().Get(server.URL + "/global/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	health := decodeBody(t, hResp)
	pool, ok := health["pool"].(map[string]any)
	if !ok {
		t.Fatalf("health pool block missing: %v", health)
	}
	if pool["mode"] != "draining" || pool["draining"] != true {
		t.Errorf("pool = %v, want mode draining", pool)
	}

	// New work is refused with 503...
	fResp := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"x"}`)
	if fResp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("POST /flow while draining = %d, want 503", fResp.StatusCode)
	}
	fBody := decodeBody(t, fResp)
	if fBody["error"] != "pod draining" {
		t.Errorf("POST /flow error = %v", fBody["error"])
	}
	bResp := postJSON(t, server.Client(), server.URL+"/pool/bind", `{"workspace":"`+testWorkspace+`"}`)
	if bResp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("POST /pool/bind while draining = %d, want 503", bResp.StatusCode)
	}
	bResp.Body.Close()

	// ...while reads stay available for observability.
	sResp, err := server.Client().Get(server.URL + "/flow/status")
	if err != nil || sResp.StatusCode != http.StatusOK {
		t.Errorf("GET /flow/status while draining: err=%v status=%v", err, sResp.StatusCode)
	}
	if sResp != nil {
		sResp.Body.Close()
	}

	// The serve-context shutdown fires after the drain grace.
	select {
	case <-shutdownCh:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown not triggered after drain grace")
	}
}

func TestFlowRecycleConflictWhenInFlight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		steps []flow.FlowState
		want  flowRunStatus
	}{
		{
			name:  "running",
			steps: []flow.FlowState{{StepID: "s1", Status: flow.FlowStatusRunning}},
			want:  flowRunRunning,
		},
		{
			name: "waiting_for_input",
			steps: []flow.FlowState{
				{StepID: "s1", Status: flow.FlowStatusRunning},
				{StepID: "s1", Status: flow.FlowStatusWaitingForInput},
			},
			want: flowRunWaitingForInput,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			shutdownCh := make(chan struct{}, 1)
			svc := newPoolStubFlowService(true, tt.steps...)
			s, server := newPoolTestServer(t, poolTestOpts{
				svc:          svc,
				bound:        testWorkspace,
				drainGrace:   10 * time.Millisecond,
				shutdownFunc: func() { shutdownCh <- struct{}{} },
			})

			resp := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"a"}`)
			resp.Body.Close()
			waitFor(t, 2*time.Second, "run to reach "+string(tt.want), func() bool {
				snap := s.flowRunner.Snapshot()
				return snap != nil && snap.Status == tt.want
			})

			rResp := postJSON(t, server.Client(), server.URL+"/flow/recycle", `{}`)
			if rResp.StatusCode != http.StatusConflict {
				t.Fatalf("recycle = %d, want 409", rResp.StatusCode)
			}
			rBody := decodeBody(t, rResp)
			if rBody["error"] != "flow in progress, abort first" {
				t.Errorf("error = %v", rBody["error"])
			}

			// The run is undisturbed and health still reports busy (not
			// draining — the CAS was rolled back).
			snap := s.flowRunner.Snapshot()
			if snap == nil || snap.Status != tt.want {
				t.Errorf("run disturbed by rejected recycle: %+v", snap)
			}
			hResp, err := server.Client().Get(server.URL + "/global/health")
			if err != nil {
				t.Fatalf("GET health: %v", err)
			}
			health := decodeBody(t, hResp)
			pool := health["pool"].(map[string]any)
			if pool["mode"] != "busy" || pool["draining"] != false {
				t.Errorf("pool = %v, want mode busy / draining false", pool)
			}

			select {
			case <-shutdownCh:
				t.Error("shutdown scheduled despite 409")
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func TestFlowRecycleIdempotentWhileDraining(t *testing.T) {
	t.Parallel()
	shutdownCount := make(chan struct{}, 4)
	_, server := newPoolTestServer(t, poolTestOpts{
		svc:          newPoolStubFlowService(false),
		bound:        testWorkspace,
		drainGrace:   50 * time.Millisecond,
		shutdownFunc: func() { shutdownCount <- struct{}{} },
	})

	for i := 0; i < 2; i++ {
		resp := postJSON(t, server.Client(), server.URL+"/flow/recycle", `{}`)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("recycle #%d = %d, want 202", i+1, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Only ONE drain is scheduled — the second 202 was idempotent.
	<-shutdownCount
	select {
	case <-shutdownCount:
		t.Error("second recycle scheduled a second drain")
	case <-time.After(150 * time.Millisecond):
	}
}

// --- /global/health pool block (Phase E) ---

func TestHealthPoolBlockLifecycle(t *testing.T) {
	t.Parallel()
	svc := newPoolStubFlowService(true,
		flow.FlowState{StepID: "s1", Status: flow.FlowStatusRunning},
	)
	s, server := newPoolTestServer(t, poolTestOpts{
		svc:            svc,
		bound:          testWorkspace,
		idleResetGrace: 10 * time.Second, // keep the terminal snapshot around
	})

	getPool := func() map[string]any {
		t.Helper()
		resp, err := server.Client().Get(server.URL + "/global/health")
		if err != nil {
			t.Fatalf("GET health: %v", err)
		}
		health := decodeBody(t, resp)
		if health["healthy"] != true {
			t.Fatalf("healthy = %v, want true (top-level field preserved)", health["healthy"])
		}
		pool, ok := health["pool"].(map[string]any)
		if !ok {
			t.Fatalf("pool block missing: %v", health)
		}
		return pool
	}

	// Fresh pod: available, no runs.
	pool := getPool()
	if pool["mode"] != "available" {
		t.Errorf("mode = %v, want available", pool["mode"])
	}
	if pool["boundWorkspace"] != testWorkspace {
		t.Errorf("boundWorkspace = %v, want %s", pool["boundWorkspace"], testWorkspace)
	}
	if pool["runCount"] != float64(0) {
		t.Errorf("runCount = %v, want 0", pool["runCount"])
	}
	if pool["currentRunID"] != nil {
		t.Errorf("currentRunID = %v, want null", pool["currentRunID"])
	}
	if pool["lastTerminalAt"] != nil {
		t.Errorf("lastTerminalAt = %v, want null", pool["lastTerminalAt"])
	}
	if pool["draining"] != false {
		t.Errorf("draining = %v, want false", pool["draining"])
	}

	// Busy: run in flight surfaces mode + currentRunID + runCount.
	resp := postJSON(t, server.Client(), server.URL+"/flow", `{"flowID":"a"}`)
	body := decodeBody(t, resp)
	waitFor(t, 2*time.Second, "run to be running", func() bool {
		snap := s.flowRunner.Snapshot()
		return snap != nil && snap.Status == flowRunRunning
	})
	pool = getPool()
	if pool["mode"] != "busy" {
		t.Errorf("mode = %v, want busy", pool["mode"])
	}
	if pool["currentRunID"] != body["runID"] {
		t.Errorf("currentRunID = %v, want %v", pool["currentRunID"], body["runID"])
	}
	if pool["runCount"] != float64(1) {
		t.Errorf("runCount = %v, want 1", pool["runCount"])
	}

	// Terminal (snapshot retained): back to available, terminal stamp set.
	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/flow", nil)
	delResp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	delResp.Body.Close()
	waitFor(t, 2*time.Second, "run to terminate", func() bool {
		snap := s.flowRunner.Snapshot()
		return snap != nil && isTerminalStatus(snap.Status)
	})
	pool = getPool()
	if pool["mode"] != "available" {
		t.Errorf("post-terminal mode = %v, want available", pool["mode"])
	}
	if pool["currentRunID"] != nil {
		t.Errorf("post-terminal currentRunID = %v, want null", pool["currentRunID"])
	}
	if lta, ok := pool["lastTerminalAt"].(float64); !ok || lta <= 0 {
		t.Errorf("lastTerminalAt = %v, want positive unix-ms", pool["lastTerminalAt"])
	}
	if pool["runCount"] != float64(1) {
		t.Errorf("post-terminal runCount = %v, want 1", pool["runCount"])
	}
}

func TestHealthOmitsPoolBlockWithoutPoolMode(t *testing.T) {
	t.Parallel()
	// Covers both per-Job (--flow) and daemon-mode pods at the API
	// layer: neither sets PoolMode, so the block must be absent.
	s := &Server{}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	resp, err := server.Client().Get(server.URL + "/global/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	health := decodeBody(t, resp)
	if _, present := health["pool"]; present {
		t.Errorf("pool block present without --pool-mode: %v", health)
	}
	if health["healthy"] != true {
		t.Errorf("healthy = %v, want true", health["healthy"])
	}
}

// --- Route registration gating ---

func TestPoolRoutesNotRegisteredWithoutPoolMode(t *testing.T) {
	t.Parallel()
	s := &Server{}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/pool/bind"},
		{http.MethodGet, "/pool/bind"},
		{http.MethodPost, "/flow/recycle"},
	} {
		req, _ := http.NewRequest(tc.method, server.URL+tc.path, strings.NewReader(`{}`))
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 (pool routes must not exist without --pool-mode)", tc.method, tc.path, resp.StatusCode)
		}
	}
}
