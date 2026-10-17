package flow

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

const secretToken = "s3cret-tenant-token"

// mapTenantToken configures an MCP server that maps the job arg
// `tenant_token` to a header, which is what makes the arg redacted.
func mapTenantToken(t *testing.T) {
	t.Helper()
	setIncludeWorkspace(t, t.TempDir())
	cfg := config.Get()
	cfg.MCPServers = map[string]config.MCPServer{"web": {
		Type: config.MCPHttp, URL: "http://example.test/mcp",
		FlowArgHeaders: map[string]string{"x-tenant": "tenant_token"},
	}}
}

func TestMarshalPersistedArgs(t *testing.T) {
	args := map[string]any{"ticket": "T-1", "tenant_token": secretToken, "nested": map[string]any{"k": "v"}}

	t.Run("redacted arg is not persisted", func(t *testing.T) {
		mapTenantToken(t)
		data, err := marshalPersistedArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), secretToken) || strings.Contains(string(data), "tenant_token") {
			t.Errorf("persisted args hold the mapped arg: %s", data)
		}
		var got map[string]any
		_ = json.Unmarshal(data, &got)
		if got["ticket"] != "T-1" || got["nested"] == nil {
			t.Errorf("other args were not kept: %s", data)
		}
		if _, ok := args["tenant_token"]; !ok {
			t.Error("the caller's args were modified")
		}
	})

	t.Run("without a mapping everything is persisted", func(t *testing.T) {
		setIncludeWorkspace(t, t.TempDir())
		data, _ := marshalPersistedArgs(args)
		if !strings.Contains(string(data), secretToken) {
			t.Errorf("unmapped arg was dropped: %s", data)
		}
	})
}

// ${args} and ${args.<mapped>} render the placeholder; routing keeps the real
// values.
func TestSubstitutePromptRedactsMappedArgs(t *testing.T) {
	mapTenantToken(t)
	args := map[string]any{"ticket": "T-1", "tenant_token": secretToken, "other": map[string]any{"tenant_token": "nested-keeps"}}

	tests := []struct {
		name, tmpl, want string
	}{
		{"named placeholder", "tok=${args.tenant_token}", "tok=[redacted]"},
		{"other args render", "t=${args.ticket}", "t=T-1"},
		{"nested same-named key is not top-level", "n=${args.other.tenant_token}", "n=nested-keeps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := substitutePrompt(tt.tmpl, args, nil); got != tt.want {
				t.Errorf("substitutePrompt(%q) = %q, want %q", tt.tmpl, got, tt.want)
			}
		})
	}

	t.Run("whole object", func(t *testing.T) {
		got := substitutePrompt("all: ${args}", args, nil)
		if strings.Contains(got, secretToken) {
			t.Fatalf("${args} leaks the mapped arg: %s", got)
		}
		if !strings.Contains(got, `"tenant_token": "[redacted]"`) || !strings.Contains(got, `"ticket": "T-1"`) {
			t.Errorf("${args} = %s, want the placeholder for tenant_token and the other args intact", got)
		}
	})

	t.Run("absent from a resumed step's args still renders the placeholder", func(t *testing.T) {
		got := substitutePrompt("tok=${args.tenant_token}", map[string]any{"ticket": "T-1"}, nil)
		if got != "tok=[redacted]" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("routing and naming keep the real value", func(t *testing.T) {
		if got := substituteScoped("${args.tenant_token}", args, nil); got != secretToken {
			t.Errorf("substituteScoped = %q, want the real value", got)
		}
	})
}

func TestWithFlowArgsDropsMappedArgsWhateverTheTelemetryConfig(t *testing.T) {
	mapTenantToken(t)
	config.Get().Telemetry = &config.TelemetryConfig{FlowArgs: []string{"*"}}

	ctx := withFlowArgs(context.Background(), map[string]any{"ticket": "T-1", "tenant_token": secretToken})
	got, _ := ctx.Value(tools.FlowArgsContextKey).(map[string]string)
	if got["ticket"] != "T-1" {
		t.Errorf("ticket not extracted: %v", got)
	}
	if _, ok := got["tenant_token"]; ok {
		t.Errorf("mapped arg extracted into trace metadata: %v", got)
	}
}

func requireNoSecret(t *testing.T, where, s string) {
	t.Helper()
	if strings.Contains(s, secretToken) {
		t.Errorf("%s holds the mapped arg: %s", where, s)
	}
}

// A full run: the mapped arg is on the run ctx for the step's agent (so its
// MCP calls stamp it), reaches neither flow_states.args nor the prompt — for
// an inline prompt and a Langfuse-hosted one alike.
func TestRunKeepsMappedArgsOutOfStateAndPrompts(t *testing.T) {
	tests := []struct {
		name   string
		step   Step
		hosted string
	}{
		{name: "repo prompt", step: Step{ID: "s", Prompt: "ticket ${args.ticket} tok ${args.tenant_token} all ${args}"}},
		{name: "langfuse-hosted prompt", step: Step{ID: "s", LangfusePromptPath: "flows/x/main"}, hosted: "ticket ${args.ticket} tok ${args.tenant_token} all ${args}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapTenantToken(t)
			f := singleStepFlow("test-redacted-"+strings.ReplaceAll(tt.name, " ", "-"), tt.step)
			registerTestFlow(t, f)
			agent := newStubAgent()
			q := &stubQuerier{}
			svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent}).(*service)
			if tt.hosted != "" {
				svc.promptResolver = &stubPromptResolver{text: tt.hosted}
			}

			args := map[string]any{"ticket": "T-1", "tenant_token": secretToken}
			agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", f.ID, args, true)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			drainFlow(t, agentEvents, flowStates)

			prompts := agent.snapshotPrompts()
			if len(prompts) != 1 {
				t.Fatalf("agent got %d prompts", len(prompts))
			}
			requireNoSecret(t, "prompt", prompts[0])
			if !strings.Contains(prompts[0], "tok [redacted]") || !strings.Contains(prompts[0], "ticket T-1") {
				t.Errorf("prompt = %q, want the placeholder for the mapped arg and the ticket kept", prompts[0])
			}

			for _, c := range q.createdFlowStates {
				requireNoSecret(t, "created flow_states.args", c.Args.String)
			}
			rows := q.snapshotFlowStates()
			if len(rows) == 0 {
				t.Fatal("no flow state persisted")
			}
			for _, r := range rows {
				requireNoSecret(t, "flow_states.args", r.Args.String)
				if !strings.Contains(r.Args.String, "T-1") {
					t.Errorf("flow_states.args lost the other args: %s", r.Args.String)
				}
			}

			jobArgs := agent.snapshotJobArgs()
			if len(jobArgs) != 1 || jobArgs[0]["tenant_token"] != secretToken {
				t.Errorf("the step's run ctx job args = %v, want the mapped arg for the MCP client", jobArgs)
			}
		})
	}
}

// A step re-entered from a persisted row (the pod was killed mid-step) takes
// the mapped arg from the args the orchestrator supplied again, never from
// the row, and does not write it back.
func TestResumedStepStampsFromSuppliedArgsNotFromState(t *testing.T) {
	mapTenantToken(t)
	f := singleStepFlow("test-redacted-resume", Step{ID: "step-one", Prompt: "tok ${args.tenant_token} k ${args.k}"})
	registerTestFlow(t, f)

	rootSessionID := "prefix-test-redacted-resume-step-one"
	q := &stubQuerier{flowStates: []db.FlowState{{
		SessionID: rootSessionID, RootSessionID: rootSessionID, FlowID: f.ID, StepID: "step-one",
		Status:    string(FlowStatusRunning),
		Args:      sql.NullString{String: `{"k":"persisted"}`, Valid: true},
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}}}
	agent := newStubAgent()
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.RunWithOptions(context.Background(), "prefix", f.ID,
		map[string]any{"k": "supplied", "tenant_token": secretToken}, RunOptions{RecoverRunning: true})
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	drainFlow(t, agentEvents, flowStates)

	jobArgs := agent.snapshotJobArgs()
	if len(jobArgs) != 1 || jobArgs[0]["tenant_token"] != secretToken {
		t.Fatalf("resumed step's job args = %v, want the supplied mapped arg", jobArgs)
	}
	prompts := agent.snapshotPrompts()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "tok [redacted]") || !strings.Contains(prompts[0], "k persisted") {
		t.Errorf("prompts = %q, want the placeholder and the persisted k", prompts)
	}
	for _, r := range q.snapshotFlowStates() {
		requireNoSecret(t, "flow_states.args after resume", r.Args.String)
	}
}

// A step re-entered from a row that lacks the mapped arg gets it back from the
// current run's args, so routing predicates on it keep working.
func TestWithRedactedArgsOverlaysTheRunsArgs(t *testing.T) {
	mapTenantToken(t)
	got := withRedactedArgs(map[string]any{"k": "persisted"}, map[string]any{"k": "supplied", "tenant_token": secretToken})
	if got["tenant_token"] != secretToken || got["k"] != "persisted" {
		t.Errorf("withRedactedArgs = %v", got)
	}
	if got := withRedactedArgs(nil, map[string]any{"tenant_token": secretToken}); got["tenant_token"] != secretToken {
		t.Errorf("nil destination: %v", got)
	}
}
