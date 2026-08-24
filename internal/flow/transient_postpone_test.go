package flow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

func TestIsTransientProviderError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// The two errors observed on real MICRO-1014 runs:
		{"429 retry exhausted", errors.New("maximum retry attempts reached for HTTP 429: 8 retries"), true},
		{"http2 RST INTERNAL_ERROR", errors.New("stream error: stream ID 147; INTERNAL_ERROR; received from peer"), true},
		// Other transient shapes.
		{"anthropic overloaded", errors.New("Overloaded"), true},
		{"bedrock throttling", errors.New("received exception ThrottlingException: rate exceeded"), true},
		{"bedrock 503", errors.New("ServiceUnavailableException: Bedrock is unable to process"), true},
		{"wrapped 429", fmt.Errorf("step %q failed: %w", "implement", errors.New("maximum retry attempts reached for HTTP 429: 8 retries")), true},
		// Must NOT match: a local stream error not from the peer, or unrelated failures.
		{"local stream error, not peer", errors.New("stream error: stream ID 5; CANCEL; sent by client"), false},
		{"plain build failure", errors.New("go build failed: undefined: Foo"), false},
		{"generic error", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientProviderError(tt.err); got != tt.want {
				t.Fatalf("isTransientProviderError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestStepResumeAfterDuration(t *testing.T) {
	dur := func(s string) *string { return &s }
	tests := []struct {
		name    string
		step    Step
		wantErr bool
	}{
		{"unset", Step{ID: "a"}, false},
		{"blank (bare opt-in)", Step{ID: "a", ResumeAfter: dur("  ")}, false},
		{"valid 15m", Step{ID: "a", ResumeAfter: dur("15m")}, false},
		{"valid 1h30m", Step{ID: "a", ResumeAfter: dur("1h30m")}, false},
		{"typo 15minutes", Step{ID: "a", ResumeAfter: dur("15minutes")}, true},
		{"zero", Step{ID: "a", ResumeAfter: dur("0s")}, true},
		{"negative", Step{ID: "a", ResumeAfter: dur("-5m")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.step.ResumeAfterDuration()
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResumeAfterDuration() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStepPostponesOnProviderError(t *testing.T) {
	dur := func(s string) *string { return &s }
	tests := []struct {
		name string
		step Step
		want bool
	}{
		{"no resume_after", Step{ID: "resolve-team"}, false},
		{"resume_after set", Step{ID: "implement", ResumeAfter: dur("30m")}, true},
		{"resume_after blank", Step{ID: "x", ResumeAfter: dur("   ")}, false},
		{"resume_after empty string", Step{ID: "x", ResumeAfter: dur("")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stepPostponesOnProviderError(tt.step); got != tt.want {
				t.Fatalf("stepPostponesOnProviderError(%+v) = %v, want %v", tt.step, got, tt.want)
			}
		})
	}
}

// TestPostponeStepForTransientError_PreservesPriorStructOutput is the GENAI-230
// regression, driven end-to-end through the resume path.
//
// A step parked on a TeamCity build carries `awaiting_build_id` in its struct
// output. On resume, runStep's entry-time write blanks that output (the resume
// stepWork carries postpone=false, so the row goes to `running`), and if the
// attempt then dies on a transient provider error the park used to persist and
// emit an EMPTY output. Downstream, the orchestrator read that silence as "no
// build left to await", dropped the build match_key and completed the job — the
// build had long finished and nothing ever resumed the work.
//
// Both halves are asserted because either alone leaves the bug alive: the
// persisted row is what a later resume reads, and the emitted state is what the
// orchestrator actually sees (it never re-queries).
func TestPostponeStepForTransientError_PreservesPriorStructOutput(t *testing.T) {
	const buildAwait = `{"awaiting_build_id":"155566","teamcity_instance":"c2","summary":"MR open, build triggered"}`

	tests := []struct {
		name         string
		seedOutput   sql.NullString
		seedIsStruct bool
		wantOutput   string
		wantIsStruct bool
	}{
		{
			name:         "struct output survives the park",
			seedOutput:   sql.NullString{String: buildAwait, Valid: true},
			seedIsStruct: true,
			wantOutput:   buildAwait,
			wantIsStruct: true,
		},
		{
			// A step that never produced output must still park cleanly — the
			// carry is a preservation, not a requirement.
			name:         "no prior output still parks",
			seedOutput:   sql.NullString{},
			seedIsStruct: false,
			wantOutput:   "",
			wantIsStruct: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resumeAfter := "30m"
			testFlow := Flow{
				ID:   "test-genai-230-park",
				Name: "Transient park preserves output",
				Spec: FlowSpec{
					Steps: []Step{{
						ID:          "implement",
						Prompt:      "do the work",
						Output:      &StepOutput{Schema: map[string]any{"type": "object"}},
						ResumeAfter: &resumeAfter,
					}},
				},
			}
			registerTestFlow(t, testFlow)

			sessionID := "prefix-" + testFlow.ID + "-implement"
			q := &stubQuerier{flowStates: []db.FlowState{{
				SessionID:      sessionID,
				RootSessionID:  sessionID,
				FlowID:         testFlow.ID,
				StepID:         "implement",
				Status:         string(FlowStatusPostponed),
				Args:           sql.NullString{String: "{}", Valid: true},
				Output:         tt.seedOutput,
				IsStructOutput: tt.seedIsStruct,
				Iteration:      1,
				// Recent: an old row trips maxTransientPostponeAge and the park
				// is declined in favour of terminal failure.
				CreatedAt: time.Now().Unix(),
				UpdatedAt: time.Now().Unix(),
			}}}

			agent := &stubAgent{
				Broker: pubsub.NewBroker[agentpkg.AgentEvent](),
				responses: []agentpkg.AgentEvent{{
					Type:    agentpkg.AgentEventTypeError,
					Message: message.Message{Role: message.Assistant},
					Error:   errors.New("stream error: stream ID 7; INTERNAL_ERROR; received from peer"),
				}},
			}
			svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

			agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, false)
			if err != nil {
				t.Fatalf("Run() error: %v", err)
			}
			states := drainFlow(t, agentEvents, flowStates)

			if agent.callCount() == 0 {
				t.Fatal("agent was never called — the run did not reach the step, so nothing was exercised")
			}

			final := findLatestByStepID(states, "implement")
			if final == nil {
				t.Fatalf("no state emitted for the step; got %+v", states)
			}
			if final.Status != FlowStatusPostponed {
				t.Fatalf("emitted status = %q, want %q (a transient provider error must park, not fail)", final.Status, FlowStatusPostponed)
			}
			if final.Output != tt.wantOutput {
				t.Errorf("emitted output = %q, want %q — this is what the orchestrator reads to find the awaited build", final.Output, tt.wantOutput)
			}
			if final.IsStructOutput != tt.wantIsStruct {
				t.Errorf("emitted is_struct_output = %v, want %v — without the flag the JSON is read as a plain string downstream", final.IsStructOutput, tt.wantIsStruct)
			}

			var row *db.FlowState
			for _, fs := range q.snapshotFlowStates() {
				if fs.StepID == "implement" {
					got := fs
					row = &got
				}
			}
			if row == nil {
				t.Fatal("no persisted row for the step")
			}
			if row.Status != string(FlowStatusPostponed) {
				t.Errorf("persisted status = %q, want %q", row.Status, FlowStatusPostponed)
			}
			if row.Output.String != tt.wantOutput {
				t.Errorf("persisted output = %q, want %q", row.Output.String, tt.wantOutput)
			}
			if row.IsStructOutput != tt.wantIsStruct {
				t.Errorf("persisted is_struct_output = %v, want %v", row.IsStructOutput, tt.wantIsStruct)
			}
		})
	}
}

// TestPostponeStepForTransientError_CreatesRowWithCarriedOutput covers the
// create branch, which svc.Run cannot reach: the entry-time write always leaves
// a row behind, so only a caller that parks before any row exists takes it.
// Called directly to keep the real function under test.
func TestPostponeStepForTransientError_CreatesRowWithCarriedOutput(t *testing.T) {
	const prior = `{"awaiting_build_id":"155566","teamcity_instance":"c2"}`
	resumeAfter := "30m"
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{}).(*service)

	ch := make(chan *FlowState, 1)
	ok := svc.postponeStepForTransientError(
		context.Background(),
		Step{ID: "implement", ResumeAfter: &resumeAfter},
		"sid", "root", "flow",
		map[string]any{},
		1,
		errors.New("Overloaded"),
		&db.FlowState{Output: sql.NullString{String: prior, Valid: true}, IsStructOutput: true},
		ch,
	)
	if !ok {
		t.Fatal("postponeStepForTransientError returned false, want a park")
	}

	created := q.createdFlowStates
	if len(created) != 1 {
		t.Fatalf("created rows = %d, want 1", len(created))
	}
	if created[0].Output.String != prior || !created[0].IsStructOutput {
		t.Errorf("created row output = %q (struct=%v), want %q (struct=true)", created[0].Output.String, created[0].IsStructOutput, prior)
	}

	select {
	case state := <-ch:
		if state.Output != prior || !state.IsStructOutput {
			t.Errorf("emitted output = %q (struct=%v), want %q (struct=true)", state.Output, state.IsStructOutput, prior)
		}
	default:
		t.Fatal("no state emitted on the channel")
	}
}

// TestPostponeStepForTransientError_NilPriorRowFallsBackToTheRow covers the
// priorRow == nil branch, which is what makes handleStepError correct: its
// transient-error paths all run BEFORE the entry-time write, so the row this
// function reads for itself still holds the output and must be used. Without the
// fallback those parks would silently drop the await — the same bug, one caller
// over.
func TestPostponeStepForTransientError_NilPriorRowFallsBackToTheRow(t *testing.T) {
	const onRow = `{"awaiting_build_id":"149953","teamcity_instance":"vx"}`
	resumeAfter := "30m"
	q := &stubQuerier{flowStates: []db.FlowState{{
		SessionID:      "sid",
		RootSessionID:  "root",
		FlowID:         "flow",
		StepID:         "implement",
		Status:         string(FlowStatusRunning),
		Output:         sql.NullString{String: onRow, Valid: true},
		IsStructOutput: true,
		Iteration:      1,
		CreatedAt:      time.Now().Unix(),
	}}}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{}).(*service)

	ch := make(chan *FlowState, 1)
	ok := svc.postponeStepForTransientError(
		context.Background(),
		Step{ID: "implement", ResumeAfter: &resumeAfter},
		"sid", "root", "flow",
		map[string]any{},
		1,
		errors.New("Overloaded"),
		nil, // no caller snapshot
		ch,
	)
	if !ok {
		t.Fatal("postponeStepForTransientError returned false, want a park")
	}

	var row *db.FlowState
	for _, fs := range q.snapshotFlowStates() {
		if fs.SessionID == "sid" {
			got := fs
			row = &got
		}
	}
	if row == nil {
		t.Fatal("no persisted row")
	}
	if row.Status != string(FlowStatusPostponed) {
		t.Errorf("persisted status = %q, want %q", row.Status, FlowStatusPostponed)
	}
	if row.Output.String != onRow || !row.IsStructOutput {
		t.Errorf("persisted output = %q (struct=%v), want %q (struct=true) carried from the row", row.Output.String, row.IsStructOutput, onRow)
	}

	select {
	case state := <-ch:
		if state.Output != onRow || !state.IsStructOutput {
			t.Errorf("emitted output = %q (struct=%v), want %q (struct=true)", state.Output, state.IsStructOutput, onRow)
		}
	default:
		t.Fatal("no state emitted on the channel")
	}
}

// TestPostponeStepForTransientError_DoesNotCarryAnotherEntryPointsOutput pins the
// other side of the priorRow gate. A step re-entered from an UPSTREAM step still
// has its own row from an earlier pass, and that row's output belongs to a
// different lifecycle. Carrying it would make the park assert an output this
// attempt never produced — and worse, an output that declares no build reads
// downstream as "the await is over" rather than "no signal", completing the very
// job the GENAI-230 fix keeps alive. Carrying nothing is the safe degradation.
func TestPostponeStepForTransientError_DoesNotCarryAnotherEntryPointsOutput(t *testing.T) {
	const stale = `{"correction_complete":true}`
	resumeAfter := "30m"
	testFlow := Flow{
		ID:   "test-genai-230-stale",
		Name: "Park must not re-assert a stale output",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:     "first",
					Prompt: "go",
					Output: &StepOutput{Schema: map[string]any{"type": "object"}},
					Rules:  []Rule{{Then: "second"}},
				},
				{
					ID:          "second",
					Prompt:      "go on",
					Output:      &StepOutput{Schema: map[string]any{"type": "object"}},
					ResumeAfter: &resumeAfter,
				},
			},
		},
	}
	registerTestFlow(t, testFlow)

	rootSessionID := "prefix-" + testFlow.ID + "-first"
	secondSessionID := "prefix-" + testFlow.ID + "-second"
	now := time.Now().Unix()
	q := &stubQuerier{flowStates: []db.FlowState{
		{
			SessionID: rootSessionID, RootSessionID: rootSessionID, FlowID: testFlow.ID,
			StepID: "first", Status: string(FlowStatusCompleted),
			Args: sql.NullString{String: "{}", Valid: true}, Iteration: 1,
			CreatedAt: now, UpdatedAt: now,
		},
		{
			// `second` already ran once and completed; this is its stale row.
			SessionID: secondSessionID, RootSessionID: rootSessionID, FlowID: testFlow.ID,
			StepID: "second", Status: string(FlowStatusCompleted),
			Args:   sql.NullString{String: "{}", Valid: true},
			Output: sql.NullString{String: stale, Valid: true}, IsStructOutput: true,
			Iteration: 1, CreatedAt: now, UpdatedAt: now,
		},
	}}

	agent := &stubAgent{
		Broker: pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{
			// `first` succeeds and routes on to `second`...
			loopRespond(`{"ok":true}`),
			// ...whose attempt then dies on a transient provider error.
			{
				Type:    agentpkg.AgentEventTypeError,
				Message: message.Message{Role: message.Assistant},
				Error:   errors.New("stream error: stream ID 9; INTERNAL_ERROR; received from peer"),
			},
		},
	}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	states := drainFlow(t, agentEvents, flowStates)

	final := findLatestByStepID(states, "second")
	if final == nil {
		t.Fatalf("no state emitted for `second`; got %+v", states)
	}
	if final.Status != FlowStatusPostponed {
		t.Fatalf("emitted status = %q, want %q", final.Status, FlowStatusPostponed)
	}
	if final.Output != "" || final.IsStructOutput {
		t.Errorf("emitted output = %q (struct=%v), want empty: the stale row belongs to an earlier pass, and re-asserting it would read downstream as an authoritative \"no build to await\"", final.Output, final.IsStructOutput)
	}

	for _, fs := range q.snapshotFlowStates() {
		if fs.SessionID == secondSessionID && fs.Output.String != "" {
			t.Errorf("persisted output = %q, want empty", fs.Output.String)
		}
	}
}
