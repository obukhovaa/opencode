package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// The args a run was started with are the process's job args while it runs —
// what chat-bridge turns of the job read — and gone at terminal, so a pooled
// pod's next run, or a daemon turn, never inherits them.
func TestRunArgsArePublishedAsJobArgsForTheRun(t *testing.T) {
	tools.SetProcessJobArgs("", nil)
	t.Cleanup(func() { tools.SetProcessJobArgs("", nil) })
	svc := newPoolStubFlowService(true, flow.FlowState{StepID: "s1", Status: flow.FlowStatusRunning})
	_, srv := newPoolTestServer(t, poolTestOpts{svc: svc, bound: testWorkspace, allowlist: testWorkspace})
	client := srv.Client()

	startFlow(t, client, srv.URL, `{"flowID":"A","args":{"tenant":"acme","run":17}}`)
	waitFor(t, 2*time.Second, "run to be observed running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})
	if got := tools.ProcessJobArgs(); got["tenant"] != "acme" || got["run"] != float64(17) {
		t.Errorf("job args during the run = %v, want tenant=acme run=17", got)
	}

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/flow", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFor(t, 2*time.Second, "job args to be cleared", func() bool {
		return tools.ProcessJobArgs() == nil
	})
}

// The published job args carry the bridge job identity of the run, so a bridge
// turn is stamped with them only when it belongs to that job.
func TestRunArgsAreScopedToTheRunsBridgeJob(t *testing.T) {
	tools.SetProcessJobArgs("", nil)
	t.Cleanup(func() { tools.SetProcessJobArgs("", nil) })
	svc := newPoolStubFlowService(true, flow.FlowState{StepID: "s1", Status: flow.FlowStatusRunning})
	s, srv := newPoolTestServer(t, poolTestOpts{svc: svc, bound: testWorkspace, allowlist: testWorkspace})
	s.flowRunner.bridgeJobs = &recordingJobScoper{}
	client := srv.Client()

	startFlow(t, client, srv.URL, `{"flowID":"A","bridgeJobID":"job-9","args":{"tenant":"acme"}}`)
	waitFor(t, 2*time.Second, "run to be observed running", func() bool {
		return flowStatus(t, client, srv.URL)["status"] == string(flowRunRunning)
	})
	if got := tools.ProcessJobArgsFor("job-9"); got["tenant"] != "acme" {
		t.Errorf("job args for job-9 = %v, want tenant=acme", got)
	}
	if got := tools.ProcessJobArgsFor("job-8"); got != nil {
		t.Errorf("job args for another job = %v, want none", got)
	}
	if got := tools.ProcessJobArgsFor(""); got != nil {
		t.Errorf("job args for no job = %v, want none", got)
	}
}
