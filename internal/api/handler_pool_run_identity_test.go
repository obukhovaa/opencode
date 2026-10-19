package api

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/llm/runidentity"
)

// resetRunIdentity keeps the process-global holder from leaking between
// tests in this package.
func resetRunIdentity(t *testing.T) {
	t.Helper()
	runidentity.Set(nil)
	t.Cleanup(func() { runidentity.Set(nil) })
}

// TestRunIdentityIsPublishedForTheRunAndClearedAtTerminal is the D10
// counterpart of the bridge/discovery identity contract: the LLM key and
// telemetry identity a run carries must be visible while it runs and gone
// once it finishes, so a pooled run bills and attributes exactly as the
// per-Job pod it replaced would have.
func TestRunIdentityIsPublishedForTheRunAndClearedAtTerminal(t *testing.T) {
	resetRunIdentity(t)
	// hold: true parks the run so the identity can be observed WHILE it is
	// in flight — the only window in which it is supposed to exist.
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
		`{"flowID":"A","llmApiKey":"team-key","telemetryUserId":"acme-dev","telemetryTeam":"acme"}`)
	waitFor(t, 2*time.Second, "run to be observed running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})

	if got := runidentity.APIKey(); got != "team-key" {
		t.Errorf("APIKey during the run = %q, want team-key", got)
	}
	if got := runidentity.UserID(); got != "acme-dev" {
		t.Errorf("UserID during the run = %q, want acme-dev", got)
	}
	wantTags := []string{"identity:acme-dev", "team:acme", "flow:A"}
	if got := runidentity.Tags(); !reflect.DeepEqual(got, wantTags) {
		t.Errorf("Tags during the run = %v, want %v", got, wantTags)
	}

	// Abort releases the held run and drives the terminal revert.
	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/flow", nil)
	if err != nil {
		t.Fatalf("build DELETE: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("DELETE /flow: %v", err)
	}
	resp.Body.Close()

	waitFor(t, 2*time.Second, "run identity to be cleared", func() bool {
		return runidentity.Get() == nil
	})
}

// A run that carries no identity must positively CLEAR the previous
// run's rather than inherit it. Inheriting is the dangerous default here:
// the tokens would bill to the wrong team's LiteLLM key and the traces
// would file under the wrong user, both silently and both looking like
// ordinary usage.
func TestRunWithoutLLMIdentityClearsThePreviousRuns(t *testing.T) {
	resetRunIdentity(t)
	svc := newPoolStubFlowService(false, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusCompleted,
	})
	_, srv := newPoolTestServer(t, poolTestOpts{
		svc:       svc,
		bound:     testWorkspace,
		allowlist: testWorkspace,
	})
	client := srv.Client()

	// Run A leaves an identity behind.
	runidentity.Set(&runidentity.Identity{APIKey: "run-a-key", UserID: "run-a-user"})

	// B still publishes its `flow:` tag, so its own identity is set while it
	// runs — but carries none of A's values. Start publishes synchronously,
	// and both B's identity and the cleared one have an empty key and user,
	// so this holds whether or not B has already finished.
	startFlow(t, client, srv.URL, `{"flowID":"B"}`)
	if got := runidentity.APIKey(); got != "" {
		t.Errorf("APIKey after an identity-less run started = %q, want empty (inherited run A's)", got)
	}
	if got := runidentity.UserID(); got != "" {
		t.Errorf("UserID after an identity-less run started = %q, want empty (inherited run A's)", got)
	}
	waitFor(t, 2*time.Second, "run identity to be cleared at terminal", func() bool {
		return runidentity.Get() == nil
	})
}

// The three fields are independent on the wire, so a run may send only
// one. It must publish that one and leave the others as "no override"
// rather than as an empty override that blanks the process value.
func TestPartialRunIdentityPublishesOnlyWhatWasSent(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantKey  string
		wantUser string
		wantTags []string
	}{
		{
			name:     "key only",
			body:     `{"flowID":"A","llmApiKey":"k"}`,
			wantKey:  "k",
			wantTags: []string{"flow:A"},
		},
		{
			// Every POST /flow names its flow, so the tag is published
			// even with no identity field at all.
			name:     "flow only",
			body:     `{"flowID":"A"}`,
			wantTags: []string{"flow:A"},
		},
		{
			name:     "telemetry only",
			body:     `{"flowID":"A","telemetryUserId":"u","telemetryTeam":"acme"}`,
			wantUser: "u",
			wantTags: []string{"identity:u", "team:acme", "flow:A"},
		},
		{
			name:     "team only",
			body:     `{"flowID":"A","telemetryTeam":"acme"}`,
			wantTags: []string{"team:acme", "flow:A"},
		},
		{
			// The identity tag must not wait on a team: a run billed to a
			// per-team key whose team did not resolve still has to correct
			// the pod's boot-time `identity:` tag.
			name:     "user only",
			body:     `{"flowID":"A","telemetryUserId":"u"}`,
			wantUser: "u",
			wantTags: []string{"identity:u", "flow:A"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetRunIdentity(t)
			svc := newPoolStubFlowService(true, flow.FlowState{
				StepID: "s1", Status: flow.FlowStatusRunning,
			})
			_, srv := newPoolTestServer(t, poolTestOpts{
				svc:       svc,
				bound:     testWorkspace,
				allowlist: testWorkspace,
			})
			client := srv.Client()
			startFlow(t, client, srv.URL, tt.body)
			waitFor(t, 2*time.Second, "run to be observed running", func() bool {
				return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
			})
			if got := runidentity.APIKey(); got != tt.wantKey {
				t.Errorf("APIKey = %q, want %q", got, tt.wantKey)
			}
			if got := runidentity.UserID(); got != tt.wantUser {
				t.Errorf("UserID = %q, want %q", got, tt.wantUser)
			}
			if got := runidentity.Tags(); !reflect.DeepEqual(got, tt.wantTags) {
				t.Errorf("Tags = %v, want %v", got, tt.wantTags)
			}
		})
	}
}

// A blank or whitespace-only field must not render its tag — an empty
// one would shadow the pod's real boot-time tag and leave the trace with
// no value at all for that namespace, which is worse than not
// overriding. Each field is independent: a blank team must not suppress
// the identity tag, or vice versa.
func TestBlankIdentityFieldsRenderNoTag(t *testing.T) {
	blank := []string{"", "   "}
	for _, user := range blank {
		for _, team := range blank {
			if got := identityTags(user, team); got != nil {
				t.Errorf("identityTags(%q, %q) = %v, want nil", user, team, got)
			}
		}
	}
	for _, blankField := range blank {
		if got := identityTags("u", blankField); !reflect.DeepEqual(got, []string{"identity:u"}) {
			t.Errorf("identityTags(\"u\", %q) = %v, want only the identity tag", blankField, got)
		}
		if got := identityTags(blankField, "acme"); !reflect.DeepEqual(got, []string{"team:acme"}) {
			t.Errorf("identityTags(%q, \"acme\") = %v, want only the team tag", blankField, got)
		}
	}
	if got := identityTags("  cos-c2-agent ", "  acme "); !reflect.DeepEqual(got, []string{"identity:cos-c2-agent", "team:acme"}) {
		t.Errorf("identityTags trims: got %v", got)
	}
}

// A blank flow ID renders no tag, for the same reason as a blank identity
// field: it would shadow the configured `flow:` tag with nothing.
func TestFlowTags(t *testing.T) {
	for _, blank := range []string{"", "   "} {
		if got := flowTags(blank); got != nil {
			t.Errorf("flowTags(%q) = %v, want nil", blank, got)
		}
	}
	if got := flowTags(" pr-review "); !reflect.DeepEqual(got, []string{"flow:pr-review"}) {
		t.Errorf("flowTags trims: got %v", got)
	}
}

// Pool pod reuse: run B, which names only its flow, must carry exactly its
// own `flow:` tag while in flight, and none of run A's key, user, `identity:`,
// `team:` or `flow:` values.
func TestFlowOnlyRunCarriesNoneOfThePreviousRunsIdentity(t *testing.T) {
	resetRunIdentity(t)
	svc := newPoolStubFlowService(true, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusRunning,
	})
	_, srv := newPoolTestServer(t, poolTestOpts{
		svc:       svc,
		bound:     testWorkspace,
		allowlist: testWorkspace,
	})
	client := srv.Client()
	abort := func() {
		t.Helper()
		req, err := http.NewRequest(http.MethodDelete, srv.URL+"/flow", nil)
		if err != nil {
			t.Fatalf("build DELETE: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("DELETE /flow: %v", err)
		}
		resp.Body.Close()
	}

	startFlow(t, client, srv.URL,
		`{"flowID":"A","llmApiKey":"run-a-key","telemetryUserId":"run-a-user","telemetryTeam":"acme"}`)
	waitFor(t, 2*time.Second, "run A running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})
	abort()
	waitFor(t, 2*time.Second, "run A to settle", func() bool {
		return flowStatus(t, client, srv.URL)["status"] != string(flowRunRunning)
	})

	startFlow(t, client, srv.URL, `{"flowID":"B"}`)
	waitFor(t, 2*time.Second, "run B running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})
	want := &runidentity.Identity{Tags: []string{"flow:B"}}
	if got := runidentity.Get(); !reflect.DeepEqual(got, want) {
		t.Errorf("run identity while B runs = %+v, want %+v", got, want)
	}

	abort()
	waitFor(t, 2*time.Second, "run B's identity to be cleared", func() bool {
		return runidentity.Get() == nil
	})
}

// The per-Job pod's --flow auto-start keeps the `flow:` tag its config was
// rendered with: only POST /flow overrides it.
func TestAutoStartedFlowPublishesNoFlowTag(t *testing.T) {
	resetRunIdentity(t)
	svc := newPoolStubFlowService(true, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusRunning,
	})
	s, srv := newPoolTestServer(t, poolTestOpts{
		svc:       svc,
		bound:     testWorkspace,
		allowlist: testWorkspace,
	})
	client := srv.Client()

	if _, err := s.StartFlow("A", nil, false, false); err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	waitFor(t, 2*time.Second, "run to be observed running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})
	if got := runidentity.Get(); got != nil {
		t.Errorf("run identity of an auto-started flow = %+v, want nil", got)
	}
}

// The stale-clear guard, for run identity. Run A's terminal revert fires
// after fr.mu is released, so it can land AFTER run B has published its
// own identity — and clearing there would run B on the pod's shared key
// for B's whole life. Run A published nothing to revert here beyond its
// own state, so the pointer-identity check must make its clear a no-op.
func TestStaleClearDoesNotWipeTheNextRunsLLMIdentity(t *testing.T) {
	resetRunIdentity(t)
	svc := newPoolStubFlowService(true, flow.FlowState{
		StepID: "s1", Status: flow.FlowStatusRunning,
	})
	s, srv := newPoolTestServer(t, poolTestOpts{
		svc:       svc,
		bound:     testWorkspace,
		allowlist: testWorkspace,
		// Retain run A's terminal snapshot so B's Start is the thing that
		// replaces it, rather than an idle reset in between.
		idleResetGrace: 5 * time.Second,
	})
	client := srv.Client()

	startFlow(t, client, srv.URL, `{"flowID":"A","llmApiKey":"run-a-key"}`)
	waitFor(t, 2*time.Second, "run A running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})

	// Park run A's state, then hand-drive the interleaving: capture A,
	// start B, and only then let A's stale clear run.
	s.flowRunner.mu.Lock()
	runA := s.flowRunner.currentRun
	s.flowRunner.mu.Unlock()

	// Abort A and wait for it to settle so it is genuinely finished.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/flow", nil)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
	waitFor(t, 2*time.Second, "run A to settle", func() bool {
		st := flowStatus(t, client, srv.URL)["status"]
		return st != string(flowRunRunning)
	})

	startFlow(t, client, srv.URL, `{"flowID":"B","llmApiKey":"run-b-key"}`)
	waitFor(t, 2*time.Second, "run B running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})
	if got := runidentity.APIKey(); got != "run-b-key" {
		t.Fatalf("APIKey after B started = %q, want run-b-key", got)
	}

	// Now replay A's terminal revert, late. It must be a no-op.
	s.flowRunner.clearRunScopedIdentity(runA)
	if got := runidentity.APIKey(); got != "run-b-key" {
		t.Errorf("a stale clear wiped run B's key: APIKey = %q, want run-b-key", got)
	}
}
