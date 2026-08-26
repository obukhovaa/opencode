package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/flow"
)

// This file covers the pool-mode fixes that came out of the PR review of
// openspec change agent-pod-pool-runtime:
//
//   - a terminal run stays addressable by runID after the idle reset, and
//     the pool health block names it (review finding 3)
//   - POST /flow carries the per-run bridge job identity, and the MCP
//     discovery auth override reaches the registry (findings 4 and 2a)
//   - DELETE /flow does not answer until the run has actually settled
//   - POST /pool/bind and POST /flow/recycle latch atomically against a
//     racing POST /flow, and a conflicted recycle leaves no residue
//   - POST /flow/recycle clears the bind sentinel so the respawn is
//     unbound

// --- stubs -----------------------------------------------------------

// recordingJobScoper captures the bridge job identities the flow runner
// publishes, in order.
type recordingJobScoper struct {
	mu  sync.Mutex
	ids []string
}

func (r *recordingJobScoper) SetRemoteJobID(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
}

func (r *recordingJobScoper) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

// recordingDiscoveryAuth captures the discovery-auth override maps the
// flow runner publishes, in order. A nil entry is a clear.
type recordingDiscoveryAuth struct {
	mu   sync.Mutex
	sets []map[string]string
}

func (r *recordingDiscoveryAuth) SetDiscoveryAuth(overrides map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if overrides == nil {
		r.sets = append(r.sets, nil)
		return
	}
	cp := make(map[string]string, len(overrides))
	for k, v := range overrides {
		cp[k] = v
	}
	r.sets = append(r.sets, cp)
}

func (r *recordingDiscoveryAuth) snapshot() []map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]string(nil), r.sets...)
}

// startFlow POSTs /flow and returns the decoded body.
func startFlow(t *testing.T, client *http.Client, baseURL, body string) map[string]any {
	t.Helper()
	resp := postJSON(t, client, baseURL+"/flow", body)
	return decodeBody(t, resp)
}

// --- terminal snapshot survives the idle reset ------------------------

// TestFlowStatusByRunIDSurvivesIdleReset is the regression guard for the
// review's finding 3: a run that reached terminal while the orchestrator
// was restarting used to become unrecoverable, because the idle reset
// cleared the only record of it and {"status":"idle"} cannot be told
// apart from "nothing ever ran here".
func TestFlowStatusByRunIDSurvivesIdleReset(t *testing.T) {
	svc := newPoolStubFlowService(false, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusCompleted, Output: "the result",
	})
	s, srv := newPoolTestServer(t, poolTestOpts{
		svc:            svc,
		bound:          testWorkspace,
		allowlist:      testWorkspace,
		idleResetGrace: 40 * time.Millisecond,
	})
	client := srv.Client()

	started := startFlow(t, client, srv.URL, `{"flowID":"A"}`)
	runID, _ := started["runID"].(string)
	if runID == "" {
		t.Fatal("POST /flow returned no runID")
	}

	// Idle reset fires: the unqualified read forgets the run.
	waitFor(t, 2*time.Second, "idle reset", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == "idle"
	})

	// ...but the run is still addressable by id, with its output intact.
	resp, err := client.Get(srv.URL + "/flow/status?runID=" + runID)
	if err != nil {
		t.Fatalf("GET /flow/status?runID: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /flow/status?runID = %d, want 200", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["status"] != string(flowRunCompleted) {
		t.Errorf("retained snapshot status = %v, want completed", body["status"])
	}
	if body["runID"] != runID {
		t.Errorf("retained snapshot runID = %v, want %q", body["runID"], runID)
	}
	steps, _ := body["completedSteps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("retained snapshot completedSteps = %d, want 1", len(steps))
	}
	if step, _ := steps[0].(map[string]any); step["output"] != "the result" {
		t.Errorf("retained step output = %v, want %q", step["output"], "the result")
	}

	// The health block names the finished run so a reconnecting
	// orchestrator can find it without guessing.
	ph := s.buildPoolHealth()
	if ph.LastRunID == nil || *ph.LastRunID != runID {
		t.Errorf("pool.lastRunID = %v, want %q", ph.LastRunID, runID)
	}
	if ph.LastStatus == nil || *ph.LastStatus != string(flowRunCompleted) {
		t.Errorf("pool.lastStatus = %v, want completed", ph.LastStatus)
	}
	// Idle for claiming purposes — the retention must not make the pod
	// look busy.
	if ph.Mode != "available" || ph.CurrentRunID != nil {
		t.Errorf("pool block after idle reset = %+v, want available/no current run", ph)
	}
}

// TestFlowStatusUnknownRunIDIs404 pins the fail-fast half of the
// contract: a run this pod has no record of must NOT read as "idle",
// because the caller would then drive an event stream against a pod that
// will never emit anything.
func TestFlowStatusUnknownRunIDIs404(t *testing.T) {
	_, srv := newPoolTestServer(t, poolTestOpts{
		svc:       newPoolStubFlowService(false),
		bound:     testWorkspace,
		allowlist: testWorkspace,
	})
	resp, err := srv.Client().Get(srv.URL + "/flow/status?runID=never-existed")
	if err != nil {
		t.Fatalf("GET /flow/status?runID: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /flow/status?runID=<unknown> = %d, want 404", resp.StatusCode)
	}
	if body := decodeBody(t, resp); body["error"] != "unknown runID" {
		t.Errorf("error body = %v", body)
	}
}

// TestTerminalRingEvictsOldest pins the retention bound so a long-lived
// pool pod cannot accumulate step outputs without limit.
func TestTerminalRingEvictsOldest(t *testing.T) {
	svc := newPoolStubFlowService(false, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusCompleted,
	})
	_, srv := newPoolTestServer(t, poolTestOpts{
		svc:            svc,
		bound:          testWorkspace,
		allowlist:      testWorkspace,
		idleResetGrace: time.Millisecond,
	})
	client := srv.Client()

	var runIDs []string
	for range terminalRingSize + 2 {
		body := startFlow(t, client, srv.URL, `{"flowID":"A"}`)
		id, _ := body["runID"].(string)
		runIDs = append(runIDs, id)
		waitFor(t, 2*time.Second, "run "+id+" to reset to idle", func() bool {
			return flowStatus(t, client, srv.URL)["status"] == "idle"
		})
	}

	// The two oldest are gone, the last terminalRingSize are retained.
	for i, id := range runIDs {
		resp, err := client.Get(srv.URL + "/flow/status?runID=" + id)
		if err != nil {
			t.Fatalf("GET /flow/status?runID: %v", err)
		}
		resp.Body.Close()
		wantFound := i >= len(runIDs)-terminalRingSize
		gotFound := resp.StatusCode == http.StatusOK
		if gotFound != wantFound {
			t.Errorf("run %d (%s): found=%v, want %v (status %d)", i, id, gotFound, wantFound, resp.StatusCode)
		}
	}
}

// --- per-run process identity ----------------------------------------

// TestFlowStartPublishesAndClearsRunScopedIdentity covers finding 4 (the
// bridge job identity was never carried per run, so interactive steps on
// a pool pod registered nothing) together with the discovery half of
// finding 2 (the MCP token never reached tool discovery).
func TestFlowStartPublishesAndClearsRunScopedIdentity(t *testing.T) {
	svc := newPoolStubFlowService(false, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusCompleted,
	})
	s, srv := newPoolTestServer(t, poolTestOpts{
		svc:       svc,
		bound:     testWorkspace,
		allowlist: testWorkspace,
	})
	jobs := &recordingJobScoper{}
	auth := &recordingDiscoveryAuth{}
	s.flowRunner.bridgeJobs = jobs
	s.flowRunner.mcpDiscovery = auth

	startFlow(t, srv.Client(), srv.URL,
		`{"flowID":"A","bridgeJobID":"job-1","mcpAuth":"T1","mcpAuthServer":"orchestrator"}`)

	waitFor(t, 2*time.Second, "run to terminate", func() bool {
		// idleResetGrace is 0 here, so the terminal snapshot is cleared
		// synchronously in finish() — "idle" is the observable terminal.
		st := flowStatus(t, srv.Client(), srv.URL)["status"]
		return st == string(flowRunCompleted) || st == "idle"
	})
	// finish() clears identity in a defer that runs after fr.mu is
	// released, so it can trail the status flip by a scheduling quantum.
	waitFor(t, 2*time.Second, "identity to be cleared", func() bool {
		ids := jobs.snapshot()
		return len(ids) == 2
	})

	if got := jobs.snapshot(); got[0] != "job-1" || got[1] != "" {
		t.Errorf("bridge job ids = %q, want [job-1 \"\"]", got)
	}
	sets := auth.snapshot()
	if len(sets) != 2 {
		t.Fatalf("discovery auth writes = %d, want 2 (set + clear)", len(sets))
	}
	if sets[0]["orchestrator"] != "Bearer T1" {
		t.Errorf("discovery auth set = %v, want orchestrator=Bearer T1", sets[0])
	}
	if sets[1] != nil {
		t.Errorf("discovery auth clear = %v, want nil", sets[1])
	}
}

// TestPoolRunWithoutIdentityClearsThePreviousRuns is the other half of
// the per-run identity contract. On a pool pod there is no boot-time
// value to preserve, so a run that carries no identity must positively
// CLEAR whatever the previous run left — inheriting it would run job B
// under job A's bearer token and job A's bridge identity. The terminal
// revert alone cannot guarantee this: it is suppressed once another run
// is current, which is exactly this case.
func TestPoolRunWithoutIdentityClearsThePreviousRuns(t *testing.T) {
	svc := newPoolStubFlowService(false, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusCompleted,
	})
	s, srv := newPoolTestServer(t, poolTestOpts{
		svc:       svc,
		bound:     testWorkspace,
		allowlist: testWorkspace,
	})
	jobs := &recordingJobScoper{}
	auth := &recordingDiscoveryAuth{}
	s.flowRunner.bridgeJobs = jobs
	s.flowRunner.mcpDiscovery = auth

	startFlow(t, srv.Client(), srv.URL, `{"flowID":"A"}`)
	waitFor(t, 2*time.Second, "run to terminate", func() bool {
		st := flowStatus(t, srv.Client(), srv.URL)["status"]
		return st == string(flowRunCompleted) || st == "idle"
	})

	if got := jobs.snapshot(); len(got) != 1 || got[0] != "" {
		t.Errorf("bridge identity writes = %q, want a single clear", got)
	}
	sets := auth.snapshot()
	if len(sets) != 1 || sets[0] != nil {
		t.Errorf("discovery auth writes = %v, want a single clear", sets)
	}
}

// TestNonPoolRunNeverTouchesTheBridgeIdentity is the per-Job
// no-regression guard. A per-Job or daemon pod's identity is the
// boot-time OPENCODE_BRIDGE_JOB_ID and the only one it will ever have;
// honouring a body field there would let any caller replace it and, at
// terminal, clear it for the rest of the process's life. The field is
// therefore ignored entirely outside pool mode.
func TestNonPoolRunNeverTouchesTheBridgeIdentity(t *testing.T) {
	svc := newPoolStubFlowService(false, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusCompleted,
	})
	s, srv := newPoolTestServer(t, poolTestOpts{svc: svc})
	// Drop back to the per-Job / daemon posture.
	s.poolMode = false
	s.flowRunner.poolMode = false
	s.flowRunner.draining = nil
	s.flowRunner.binding = nil
	jobs := &recordingJobScoper{}
	auth := &recordingDiscoveryAuth{}
	s.flowRunner.bridgeJobs = jobs
	s.flowRunner.mcpDiscovery = auth

	startFlow(t, srv.Client(), srv.URL,
		`{"flowID":"A","bridgeJobID":"job-1","mcpAuth":"T1","mcpAuthServer":"orchestrator"}`)
	waitFor(t, 2*time.Second, "run to terminate", func() bool {
		return flowStatus(t, srv.Client(), srv.URL)["status"] == string(flowRunCompleted)
	})
	waitFor(t, 2*time.Second, "discovery auth to be cleared", func() bool {
		return len(auth.snapshot()) == 2
	})
	time.Sleep(50 * time.Millisecond)

	if got := jobs.snapshot(); len(got) != 0 {
		t.Errorf("a non-pool pod's boot bridge identity was touched: %q", got)
	}
	// mcpAuth still works outside pool mode — only the bridge identity is gated.
	if sets := auth.snapshot(); len(sets) != 2 || sets[0]["orchestrator"] != "Bearer T1" || sets[1] != nil {
		t.Errorf("discovery auth writes = %v, want set then clear", sets)
	}
}

// TestMcpAuthServerNameIsFolded: config loading lower-cases MCP server
// map keys, so an override keyed on the raw wire value would never be
// found for a server whose configured name has an uppercase letter —
// discovery would silently fall back to the boot header and 401.
func TestMcpAuthServerNameIsFolded(t *testing.T) {
	svc := newPoolStubFlowService(false, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusCompleted,
	})
	s, srv := newPoolTestServer(t, poolTestOpts{
		svc:       svc,
		bound:     testWorkspace,
		allowlist: testWorkspace,
	})
	auth := &recordingDiscoveryAuth{}
	s.flowRunner.mcpDiscovery = auth

	startFlow(t, srv.Client(), srv.URL,
		`{"flowID":"A","mcpAuth":"T1","mcpAuthServer":"  C2-Orchestrator "}`)
	waitFor(t, 2*time.Second, "discovery auth to be published", func() bool {
		return len(auth.snapshot()) >= 1
	})

	sets := auth.snapshot()
	if _, ok := sets[0]["c2-orchestrator"]; !ok {
		t.Errorf("discovery auth keyed %v — want the folded name config loading produces", sets[0])
	}
}

// --- DELETE /flow settles before answering ---------------------------

// TestAbortWaitsForTerminalInPoolMode: the orchestrator reclaims a pool
// pod the moment abort returns, so answering while the engine is still
// unwinding hands back a pod that still 409s POST /flow.
func TestAbortWaitsForTerminalInPoolMode(t *testing.T) {
	svc := newPoolStubFlowService(true, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusRunning,
	})
	_, srv := newPoolTestServer(t, poolTestOpts{
		svc:       svc,
		bound:     testWorkspace,
		allowlist: testWorkspace,
	})
	client := srv.Client()
	startFlow(t, client, srv.URL, `{"flowID":"A"}`)
	waitFor(t, 2*time.Second, "run to be observed running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/flow", nil)
	if err != nil {
		t.Fatalf("build DELETE: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("DELETE /flow: %v", err)
	}
	body := decodeBody(t, resp)
	if body["aborted"] != true {
		t.Fatalf("DELETE /flow body = %v", body)
	}
	if body["terminal"] != true {
		t.Fatalf("DELETE /flow returned before the run settled: %v", body)
	}
	// The whole point: the very next POST /flow succeeds, with no
	// intervening 409 for the orchestrator to misread as desync.
	next := postJSON(t, client, srv.URL+"/flow", `{"flowID":"B"}`)
	defer next.Body.Close()
	if next.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /flow right after abort = %d, want 202", next.StatusCode)
	}
}

// --- bind / recycle latching -----------------------------------------

// TestBindLatchRefusesFlowStart: an accepted bind has already armed the
// process-exit timer, so a run started inside the exit grace would be
// killed mid-flight.
func TestBindLatchRefusesFlowStart(t *testing.T) {
	_, srv := newPoolTestServer(t, poolTestOpts{
		svc:       newPoolStubFlowService(false),
		allowlist: testWorkspace,
		// Long enough that the test drives the window deliberately.
		bindExitGrace: time.Hour,
	})
	client := srv.Client()

	bind := postJSON(t, client, srv.URL+"/pool/bind", `{"workspace":"`+testWorkspace+`"}`)
	defer bind.Body.Close()
	if bind.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /pool/bind = %d, want 202", bind.StatusCode)
	}

	resp := postJSON(t, client, srv.URL+"/flow", `{"flowID":"A","workspace":"`+testWorkspace+`"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("POST /flow during bind exit grace = %d, want 503", resp.StatusCode)
	}
	if body := decodeBody(t, resp); body["error"] != "pod binding; exiting for respawn" {
		t.Errorf("error body = %v", body)
	}

	// A second bind is likewise refused rather than writing a second
	// sentinel behind an already-armed exit.
	second := postJSON(t, client, srv.URL+"/pool/bind", `{"workspace":"`+testWorkspace+`"}`)
	defer second.Body.Close()
	if second.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second POST /pool/bind = %d, want 503", second.StatusCode)
	}
}

// TestRecycleConflictLeavesNoDrainingResidue is the regression guard for
// the latch-then-roll-back window: a recycle refused with 409 must leave
// the pod fully claimable, and a later recycle must still work.
func TestRecycleConflictLeavesNoDrainingResidue(t *testing.T) {
	svc := newPoolStubFlowService(true, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusRunning,
	})
	shutdown := make(chan struct{})
	var once sync.Once
	s, srv := newPoolTestServer(t, poolTestOpts{
		svc:          svc,
		bound:        testWorkspace,
		allowlist:    testWorkspace,
		drainGrace:   time.Millisecond,
		shutdownFunc: func() { once.Do(func() { close(shutdown) }) },
	})
	client := srv.Client()
	startFlow(t, client, srv.URL, `{"flowID":"A"}`)
	waitFor(t, 2*time.Second, "run to be observed running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})

	conflict := postJSON(t, client, srv.URL+"/flow/recycle", `{"reason":"test"}`)
	defer conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("POST /flow/recycle while in flight = %d, want 409", conflict.StatusCode)
	}
	if s.poolDraining.Load() {
		t.Fatal("a conflicted recycle left the pod latched as draining")
	}
	if ph := s.buildPoolHealth(); ph.Mode != "busy" || ph.Draining {
		t.Errorf("pool block after conflicted recycle = %+v, want busy/not draining", ph)
	}

	// Abort, then the recycle the orchestrator retries must take effect.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/flow", nil)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
	accepted := postJSON(t, client, srv.URL+"/flow/recycle", `{"reason":"retry"}`)
	defer accepted.Body.Close()
	if accepted.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /flow/recycle after abort = %d, want 202", accepted.StatusCode)
	}
	select {
	case <-shutdown:
	case <-time.After(2 * time.Second):
		t.Fatal("recycle accepted but no shutdown was scheduled")
	}
}

// TestRecycleClearsBindSentinel: the sentinel is the pod's durable
// binding record (agent.sh leaves it in place so a container restart
// re-clones), and recycle is exactly the transition that must break it —
// design D2's rebind protocol expects the respawned pod to come back
// empty.
func TestRecycleClearsBindSentinel(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), ".pool-bind")
	if err := os.WriteFile(sentinel, []byte(testWorkspace), 0o600); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}
	_, srv := newPoolTestServer(t, poolTestOpts{
		svc:          newPoolStubFlowService(false),
		bound:        testWorkspace,
		allowlist:    testWorkspace,
		sentinelPath: sentinel,
		drainGrace:   time.Hour, // never actually shut down during the test
		shutdownFunc: func() {},
	})

	resp := postJSON(t, srv.Client(), srv.URL+"/flow/recycle", `{"reason":"scale-down"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /flow/recycle = %d, want 202", resp.StatusCode)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("bind sentinel still present after recycle (err=%v) — the respawn would come back bound", err)
	}
}

// TestRecycleToleratesMissingSentinel: an unbound pod has no sentinel;
// clearing it must not turn a valid recycle into an error.
func TestRecycleToleratesMissingSentinel(t *testing.T) {
	drained := make(chan struct{})
	var once sync.Once
	_, srv := newPoolTestServer(t, poolTestOpts{
		svc:          newPoolStubFlowService(false),
		allowlist:    testWorkspace,
		sentinelPath: filepath.Join(t.TempDir(), "absent"),
		drainGrace:   time.Millisecond,
		shutdownFunc: func() { once.Do(func() { close(drained) }) },
	})
	resp := postJSON(t, srv.Client(), srv.URL+"/flow/recycle", ``)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /flow/recycle = %d, want 202", resp.StatusCode)
	}
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("recycle did not schedule a shutdown")
	}
}

// --- run-scoped identity is reverted only where it was applied --------

// TestPartialIdentityRunsClearTheOtherSingleton: the two POST /flow
// fields are independent on the wire, and on a pool pod a run that
// carries only one of them must set that one and CLEAR the other —
// never inherit the previous run's. Inheriting is the cross-job leak;
// clearing the wrong one is the identity wipe.
func TestPartialIdentityRunsClearTheOtherSingleton(t *testing.T) {
	t.Run("mcpAuth only", func(t *testing.T) {
		svc := newPoolStubFlowService(false, flow.FlowState{
			StepID: "s1", Status: flow.FlowStatusCompleted,
		})
		s, srv := newPoolTestServer(t, poolTestOpts{
			svc: svc, bound: testWorkspace, allowlist: testWorkspace,
		})
		jobs := &recordingJobScoper{}
		auth := &recordingDiscoveryAuth{}
		s.flowRunner.bridgeJobs = jobs
		s.flowRunner.mcpDiscovery = auth

		startFlow(t, srv.Client(), srv.URL,
			`{"flowID":"A","mcpAuth":"T1","mcpAuthServer":"orchestrator"}`)
		waitFor(t, 2*time.Second, "discovery auth set then cleared", func() bool {
			return len(auth.snapshot()) == 2
		})
		time.Sleep(50 * time.Millisecond)

		if got := jobs.snapshot(); len(got) != 1 || got[0] != "" {
			t.Errorf("bridge identity writes = %q, want exactly one clear (never a stale carry-over)", got)
		}
		if sets := auth.snapshot(); sets[0]["orchestrator"] != "Bearer T1" || sets[1] != nil {
			t.Errorf("discovery auth writes = %v, want set then clear", sets)
		}
	})

	t.Run("bridgeJobID only", func(t *testing.T) {
		svc := newPoolStubFlowService(false, flow.FlowState{
			StepID: "s1", Status: flow.FlowStatusCompleted,
		})
		s, srv := newPoolTestServer(t, poolTestOpts{
			svc: svc, bound: testWorkspace, allowlist: testWorkspace,
		})
		jobs := &recordingJobScoper{}
		auth := &recordingDiscoveryAuth{}
		s.flowRunner.bridgeJobs = jobs
		s.flowRunner.mcpDiscovery = auth

		startFlow(t, srv.Client(), srv.URL, `{"flowID":"A","bridgeJobID":"job-1"}`)
		waitFor(t, 2*time.Second, "bridge identity set then cleared", func() bool {
			return len(jobs.snapshot()) == 2
		})
		time.Sleep(50 * time.Millisecond)

		if got := jobs.snapshot(); got[0] != "job-1" || got[1] != "" {
			t.Errorf("bridge identity writes = %q, want [job-1 \"\"]", got)
		}
		if sets := auth.snapshot(); len(sets) != 1 || sets[0] != nil {
			t.Errorf("discovery auth writes = %v, want exactly one clear", sets)
		}
	})
}

// TestStaleClearDoesNotWipeTheNextRunsIdentity: the publish and the
// revert both happen outside fr.mu, so run A's terminal defer can be
// descheduled past run B's start. Without the pointer-identity guard,
// A's clear wipes B's identity for B's entire life — exactly the failure
// the per-run identity was introduced to prevent.
func TestStaleClearDoesNotWipeTheNextRunsIdentity(t *testing.T) {
	// hold=true so run B stays in flight while we replay A's clear.
	svc := newPoolStubFlowService(true, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusRunning,
	})
	s, srv := newPoolTestServer(t, poolTestOpts{
		svc:       svc,
		bound:     testWorkspace,
		allowlist: testWorkspace,
		// Retain A's terminal state so we can hold a pointer to it, the
		// way a descheduled goroutine would.
		idleResetGrace: 5 * time.Second,
	})
	jobs := &recordingJobScoper{}
	s.flowRunner.bridgeJobs = jobs
	client := srv.Client()

	// Run A: start, then abort so it reaches terminal and does its own
	// (legitimate) clear.
	startFlow(t, client, srv.URL, `{"flowID":"A","bridgeJobID":"job-A"}`)
	waitFor(t, 2*time.Second, "run A running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/flow", nil)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
	waitFor(t, 2*time.Second, "run A's own clear", func() bool {
		return len(jobs.snapshot()) == 2
	})
	s.flowRunner.mu.Lock()
	runA := s.flowRunner.currentRun
	s.flowRunner.mu.Unlock()
	if runA == nil {
		t.Fatal("run A's state was not retained")
	}

	// Run B takes over and stays in flight.
	startFlow(t, client, srv.URL, `{"flowID":"B","bridgeJobID":"job-B"}`)
	waitFor(t, 2*time.Second, "run B running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})

	// Replay run A's terminal clear now that B is current — the exact
	// interleaving a goroutine preemption between two defers produces.
	s.flowRunner.clearRunScopedIdentity(runA)

	ids := jobs.snapshot()
	want := []string{"job-A", "", "job-B"}
	if len(ids) != len(want) {
		t.Fatalf("bridge identity writes = %q, want %q (a stale clear from run A leaked through)", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("bridge identity writes = %q, want %q", ids, want)
		}
	}
}

// recordingStepCacheResetter counts ResetStepCache calls.
type recordingStepCacheResetter struct {
	mu     sync.Mutex
	resets int
}

func (r *recordingStepCacheResetter) ResetStepCache() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resets++
}

func (r *recordingStepCacheResetter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resets
}

// TestEveryRunDropsThePerStepAgentCache: the factory memoises agents by
// the flow YAML's step ID, which recurs across runs, and each cached
// agent resolves its toolset exactly once. On a process that serves many
// runs, reusing run N-1's agents also reuses their frozen toolsets — so
// a single failed MCP discovery would strip the tools from every later
// job on the pod, and the per-run auth token would have nothing to apply
// to. Every run must start from fresh agents.
func TestEveryRunDropsThePerStepAgentCache(t *testing.T) {
	svc := newPoolStubFlowService(false, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusCompleted,
	})
	s, srv := newPoolTestServer(t, poolTestOpts{
		svc:       svc,
		bound:     testWorkspace,
		allowlist: testWorkspace,
	})
	resetter := &recordingStepCacheResetter{}
	s.flowRunner.stepAgents = resetter
	client := srv.Client()

	for i := 1; i <= 3; i++ {
		startFlow(t, client, srv.URL, `{"flowID":"A"}`)
		waitFor(t, 2*time.Second, "run to terminate", func() bool {
			st := flowStatus(t, client, srv.URL)["status"]
			return st == string(flowRunCompleted) || st == "idle"
		})
		if got := resetter.count(); got != i {
			t.Fatalf("after run %d the step-agent cache was reset %d times, want %d", i, got, i)
		}
	}
}

// TestPoolRoutesRequireThePassword pins the auth contract for the pool
// surface directly, since the shared harness runs with an empty password
// (authMiddleware is a passthrough there). /pool/bind exits the process
// and /flow/recycle drains it — neither may be reachable unauthenticated
// when the pod is configured with a server password.
func TestPoolRoutesRequireThePassword(t *testing.T) {
	s, _ := newPoolTestServer(t, poolTestOpts{
		svc:       newPoolStubFlowService(false),
		bound:     testWorkspace,
		allowlist: testWorkspace,
	})
	s.password = "s3cret"

	mux := http.NewServeMux()
	s.registerRoutes(mux)
	srv := httptest.NewServer(chain(
		mux,
		recoveryMiddleware,
		loggingMiddleware,
		corsMiddleware("*"),
		authMiddleware(s.password),
		jsonContentTypeMiddleware,
	))
	t.Cleanup(srv.Close)
	client := srv.Client()

	cases := []struct {
		name, method, path, body string
	}{
		{"POST /pool/bind", http.MethodPost, "/pool/bind", `{"workspace":"` + testWorkspace + `"}`},
		{"GET /pool/bind", http.MethodGet, "/pool/bind", ""},
		{"POST /flow/recycle", http.MethodPost, "/flow/recycle", `{}`},
		{"POST /flow", http.MethodPost, "/flow", `{"flowID":"A"}`},
		{"GET /flow/status", http.MethodGet, "/flow/status", ""},
		{"GET /global/health", http.MethodGet, "/global/health", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name+" without credentials", func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, body)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s = %d, want 401", tc.name, resp.StatusCode)
			}
		})
	}

	// With credentials the pool routes answer normally.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/pool/bind", nil)
	req.SetBasicAuth("c2-agent-orchestrator", "s3cret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("authenticated GET /pool/bind: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("authenticated GET /pool/bind = %d, want 200", resp.StatusCode)
	}
}
