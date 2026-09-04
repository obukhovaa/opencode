package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/contextfile"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// ---- fixtures --------------------------------------------------------------

// recordingHook is a flow.InteractiveHook that records bind / unbind so a test
// can assert the bridge is STILL bound while a re-prompt runs.
type recordingHook struct {
	mu        sync.Mutex
	bound     []string
	unbound   []string
	boundNow  map[string]bool
	lastPeers []bridge.PeerRef
}

func newRecordingHook() *recordingHook {
	return &recordingHook{boundNow: map[string]bool{}}
}

func (h *recordingHook) OnInteractiveStepStart(_ context.Context, sessionID string, target []bridge.PeerRef) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.bound = append(h.bound, sessionID)
	h.boundNow[sessionID] = true
	h.lastPeers = target
	return nil
}

func (h *recordingHook) OnInteractiveStepComplete(_ context.Context, sessionID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unbound = append(h.unbound, sessionID)
	h.boundNow[sessionID] = false
	return nil
}

func (h *recordingHook) isBound(sessionID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.boundNow[sessionID]
}

func (h *recordingHook) unbindCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.unbound)
}

// interactivePermissions tracks the interactive-session flag the question tool
// consults to decide whether to route to the human or auto-approve past them.
type interactivePermissions struct {
	permission.Service
	mu          sync.Mutex
	interactive map[string]bool
	autoApprove map[string]bool
}

func newInteractivePermissions() *interactivePermissions {
	return &interactivePermissions{interactive: map[string]bool{}, autoApprove: map[string]bool{}}
}

func (p *interactivePermissions) AutoApproveSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.autoApprove[sessionID] = true
}

func (p *interactivePermissions) MarkInteractiveSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interactive[sessionID] = true
}

func (p *interactivePermissions) RemoveInteractiveSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interactive[sessionID] = false
}

func (p *interactivePermissions) IsInteractiveSession(sessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.interactive[sessionID]
}

func (p *interactivePermissions) IsAutoApproveSession(sessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.autoApprove[sessionID]
}

// stubMessages is the slice of message.Service the failure-diagnostics path
// uses: ListLatest, to recover the agent's last words for the step error.
type stubMessages struct {
	message.Service
	mu   sync.Mutex
	msgs []message.Message
}

func (m *stubMessages) append(role message.MessageRole, text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, message.Message{
		Role:  role,
		Parts: []message.ContentPart{message.TextContent{Text: text}},
	})
}

func (m *stubMessages) ListLatest(_ context.Context, _ string, limit int64) ([]message.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || int(limit) >= len(m.msgs) {
		out := make([]message.Message, len(m.msgs))
		copy(out, m.msgs)
		return out, nil
	}
	tail := m.msgs[len(m.msgs)-int(limit):]
	out := make([]message.Message, len(tail))
	copy(out, tail)
	return out, nil
}

// scriptedAgent is stubAgent plus a per-call hook, so a test can act at the
// exact moment the re-prompt turn is in flight (e.g. assert the bridge is still
// bound, or drive the question service the way the question tool would).
type scriptedAgent struct {
	*stubAgent
	onCall func(callIdx int, sessionID, prompt string)
	// startErr, when non-nil, decides the error RunWith returns instead of
	// starting a turn. Used to drive the "re-prompt never started" path.
	startErr func(callIdx int) error
}

func (a *scriptedAgent) Run(ctx context.Context, sessionID string, prompt string, maxTurns int, atts ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	return a.RunWith(ctx, sessionID, prompt, maxTurns, agentpkg.RunOptions{}, atts...)
}

func (a *scriptedAgent) RunWith(ctx context.Context, sessionID string, prompt string, maxTurns int, opts agentpkg.RunOptions, atts ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	if a.onCall != nil {
		a.onCall(a.stubAgent.callCount(), sessionID, prompt)
	}
	if a.startErr != nil {
		if err := a.startErr(a.stubAgent.callCount()); err != nil {
			return nil, err
		}
	}
	return a.stubAgent.RunWith(ctx, sessionID, prompt, maxTurns, opts, atts...)
}

// scriptedAgentFactory reuses stubAgentFactory for the rest of the
// AgentFactory surface and only overrides NewAgent to hand back the scripted
// agent (which wraps stubAgent with a per-call hook).
type scriptedAgentFactory struct {
	stubAgentFactory
	agent agentpkg.Service
}

func (f *scriptedAgentFactory) NewAgent(context.Context, string, map[string]any, string, bool, []bridge.PeerRef, *contextfile.StepContext, contextfile.TemplateVars) (agentpkg.Service, error) {
	return f.agent, nil
}

// interactiveStep is the shape the failure came from: interactive, bound to a
// reviewer, and carrying an output schema.
func interactiveStep(id string, maxTurns int) Step {
	return Step{
		ID:          id,
		Prompt:      "present the products",
		Interactive: true,
		Interaction: &StepInteraction{Target: "${args.reviewer}"},
		MaxTurns:    maxTurns,
		Output: &StepOutput{Schema: map[string]any{
			"type":     "object",
			"required": []any{"products", "confirmed"},
			"properties": map[string]any{
				"products":  map[string]any{"type": "array"},
				"confirmed": map[string]any{"type": "boolean"},
			},
		}},
	}
}

var reviewerArgs = map[string]any{
	"reviewer": map[string]any{
		"channel": "slack", "identity": "default", "peerId": "D1", "mention": "<@U1>",
	},
}

type interactiveRun struct {
	agent    *stubAgent
	hook     *recordingHook
	perms    *interactivePermissions
	messages *stubMessages
	terminal *FlowState
	states   []*FlowState
	nextArgs map[string]any
}

// runInteractiveFlow drives a single interactive step (optionally followed by a
// second step, so a test can prove the output actually flows downstream).
func runInteractiveFlow(t *testing.T, flowID string, steps []Step, responses []agentpkg.AgentEvent, onCall func(int, string, string)) interactiveRun {
	t.Helper()
	registerTestFlow(t, Flow{ID: flowID, Name: "Interactive", Spec: FlowSpec{Steps: steps}})

	base := &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent](), responses: responses}
	agent := &scriptedAgent{stubAgent: base, onCall: onCall}
	hook := newRecordingHook()
	perms := newInteractivePermissions()
	msgs := &stubMessages{}

	svc := NewService(&stubSessions{}, msgs, &stubQuerier{}, perms, &scriptedAgentFactory{agent: agent})
	svc.(*service).SetInteractiveHook(hook)

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", flowID, copyArgs(reviewerArgs), true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	run := interactiveRun{agent: base, hook: hook, perms: perms, messages: msgs}
	for s := range flowStates {
		run.states = append(run.states, s)
		if s.Status == FlowStatusFailed || s.Status == FlowStatusCompleted {
			run.terminal = s
			run.nextArgs = s.Args
		}
	}
	for range agentEvents {
	}
	if run.terminal == nil {
		t.Fatal("expected a terminal flow state")
	}
	return run
}

func (r interactiveRun) statusesFor(stepID string) []FlowStatus {
	var out []FlowStatus
	for _, s := range r.states {
		if s.StepID == stepID {
			out = append(out, s.Status)
		}
	}
	return out
}

func (r interactiveRun) hasStatus(stepID string, want FlowStatus) bool {
	for _, s := range r.statusesFor(stepID) {
		if s == want {
			return true
		}
	}
	return false
}

// exhaustedEvent is an agent turn that ended because the turn budget ran out
// (no struct_output, no text) — the agent runtime sets TurnsExhausted on both
// of its budget gates.
func exhaustedEvent() agentpkg.AgentEvent {
	ev := emptyEvent()
	ev.TurnsExhausted = true
	return ev
}

// ---- Verification 1 --------------------------------------------------------

// TestStructOutputRetry_EmptyTurnRescuedAndOutputFlowsDownstream: the reported
// failure. An interactive step's agent ends a turn with no tool call; the
// engine re-prompts once; the retry emits valid struct_output; the step
// succeeds and its fields land in the next step's args.
func TestStructOutputRetry_EmptyTurnRescuedAndOutputFlowsDownstream(t *testing.T) {
	steps := []Step{
		interactiveStep("products", 20),
		{ID: "summary", Prompt: "summarise ${args.products}"},
	}
	steps[0].Rules = []Rule{{Then: "summary"}}

	run := runInteractiveFlow(t, "retry-rescue", steps, []agentpkg.AgentEvent{
		emptyEvent(),
		structEvent(`{"products":["a","b"],"confirmed":true}`),
		proseEvent("summarised"),
	}, nil)

	if got := run.agent.callCount(); got != 3 {
		t.Fatalf("expected 3 agent calls (empty turn + re-prompt + next step), got %d", got)
	}
	if run.terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed; output=%q", run.terminal.Status, run.terminal.Output)
	}
	// The rescued struct_output must be the step's output, and must have been
	// merged into args so ${args.*} resolves for the next step.
	var productsStep *FlowState
	for _, s := range run.states {
		if s.StepID == "products" && s.Status == FlowStatusCompleted {
			productsStep = s
		}
	}
	if productsStep == nil {
		t.Fatal("step \"products\" never completed")
	}
	if !productsStep.IsStructOutput || !containsSubstring(productsStep.Output, `"confirmed":true`) {
		t.Fatalf("products output = %q (isStruct=%v), want the rescued struct_output",
			productsStep.Output, productsStep.IsStructOutput)
	}
	if run.nextArgs["confirmed"] != true {
		t.Errorf("struct_output fields did not flow into downstream args: %#v", run.nextArgs)
	}
	// The re-prompt must NOT force struct_output — that would make `question`
	// unreachable for a step whose whole point is asking the human.
	opts := run.agent.snapshotRunOpts()
	if len(opts) < 2 {
		t.Fatalf("expected at least 2 recorded RunOptions, got %d", len(opts))
	}
	if opts[1].ForceStructOutput {
		t.Error("interactive re-prompt must not set ForceStructOutput (it would block the question tool)")
	}
	// And the nudge must actually say what is missing.
	prompts := run.agent.snapshotPrompts()
	nudge := prompts[1]
	for _, want := range []string{"struct_output", "question", "products", "confirmed"} {
		if !strings.Contains(nudge, want) {
			t.Errorf("re-prompt missing %q; got:\n%s", want, nudge)
		}
	}
}

// TestStructOutputRetry_EmitsRetryingFlowState: the re-prompt must be visible
// on the /event stream (via the FlowStatusRetrying transition) rather than
// inferable only from timing — and it must not be persisted as a row.
func TestStructOutputRetry_EmitsRetryingFlowState(t *testing.T) {
	run := runInteractiveFlow(t, "retry-event", []Step{interactiveStep("products", 20)},
		[]agentpkg.AgentEvent{emptyEvent(), structEvent(`{"products":[],"confirmed":false}`)}, nil)

	if !run.hasStatus("products", FlowStatusRetrying) {
		t.Fatalf("expected a %q transition on the flow-state stream, got %v",
			FlowStatusRetrying, run.statusesFor("products"))
	}
	if run.terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed", run.terminal.Status)
	}
}

// ---- Verification 2 --------------------------------------------------------

// TestStructOutputRetry_FailsTwiceAndCarriesLastAssistantText: when the retry
// also comes back empty the step fails — but the error now carries the prose
// the agent wrote before it stopped, which is the one artefact that explains
// the failure.
func TestStructOutputRetry_FailsTwiceAndCarriesLastAssistantText(t *testing.T) {
	const stuck = "I have no tool that can list this client's products, so I cannot present them."

	var run interactiveRun
	msgs := &stubMessages{}
	// Seed the session history the way the real agent would: prose on an
	// earlier turn, then a turn that says nothing at all.
	msgs.append(message.Assistant, stuck)

	steps := []Step{interactiveStep("products", 20)}
	registerTestFlow(t, Flow{ID: "retry-fail", Name: "Interactive", Spec: FlowSpec{Steps: steps}})
	base := &stubAgent{
		Broker:    pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{emptyEvent(), emptyEvent()},
	}
	svc := NewService(&stubSessions{}, msgs, &stubQuerier{}, newInteractivePermissions(),
		&scriptedAgentFactory{agent: &scriptedAgent{stubAgent: base}})
	svc.(*service).SetInteractiveHook(newRecordingHook())

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", "retry-fail", copyArgs(reviewerArgs), true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	for s := range flowStates {
		run.states = append(run.states, s)
		if s.Status == FlowStatusFailed || s.Status == FlowStatusCompleted {
			run.terminal = s
		}
	}
	for range agentEvents {
	}

	if run.terminal == nil || run.terminal.Status != FlowStatusFailed {
		t.Fatalf("expected the step to fail after the re-prompt, got %+v", run.terminal)
	}
	if got := base.callCount(); got != 2 {
		t.Fatalf("expected exactly 2 agent calls (initial + one re-prompt), got %d", got)
	}
	if !containsSubstring(run.terminal.Output, "expects structured output") {
		t.Errorf("error lost its historical prefix: %q", run.terminal.Output)
	}
	if !containsSubstring(run.terminal.Output, stuck) {
		t.Errorf("step error does not carry the agent's last assistant text.\ngot: %q\nwant substring: %q",
			run.terminal.Output, stuck)
	}
	if !containsSubstring(run.terminal.Output, "re-prompted once") {
		t.Errorf("step error should record that the re-prompt was spent: %q", run.terminal.Output)
	}
}

// TestMissingStructOutputError_Message pins the assembled error text, including
// the historical prefix orchestrators match on.
func TestMissingStructOutputError_Message(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  *missingStructOutputError
		want []string
		deny []string
	}{
		{
			name: "bare",
			err:  &missingStructOutputError{StepID: "products"},
			want: []string{`step "products" expects structured output but agent produced empty response`},
			deny: []string{"last assistant message", "turn budget"},
		},
		{
			name: "with last assistant text",
			err:  &missingStructOutputError{StepID: "products", Retried: true, LastAssistant: "I am stuck"},
			want: []string{"re-prompted once", `last assistant message: "I am stuck"`},
		},
		{
			name: "turn exhaustion wins over retried",
			err:  &missingStructOutputError{StepID: "products", TurnsExhausted: true, Retried: true},
			want: []string{"turn budget exhausted", "no re-prompt attempted"},
			deny: []string{"re-prompted once"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in %q", w, got)
				}
			}
			for _, d := range tc.deny {
				if strings.Contains(got, d) {
					t.Errorf("unexpected %q in %q", d, got)
				}
			}
		})
	}
}

// TestLastAssistantText_ScansBackPastTheEmptyTurn: the failing turn itself says
// nothing, so the lookup must walk backwards past it — and must skip user and
// tool messages.
func TestLastAssistantText_ScansBackPastTheEmptyTurn(t *testing.T) {
	t.Parallel()
	msgs := &stubMessages{}
	msgs.append(message.Assistant, "early thought")
	msgs.append(message.User, "a user turn")
	msgs.append(message.Assistant, "the real  explanation\nspanning lines")
	msgs.append(message.Assistant, "")

	s := &service{messages: msgs}
	got := s.lastAssistantText(context.Background(), "S1")
	if got != "the real explanation spanning lines" {
		t.Errorf("lastAssistantText = %q", got)
	}

	// No message service (unit-test wiring, or a session with nothing to say)
	// must degrade to the bare error, not panic.
	if got := (&service{}).lastAssistantText(context.Background(), "S1"); got != "" {
		t.Errorf("nil message service should yield empty, got %q", got)
	}
	if got := (&service{messages: &stubMessages{}}).lastAssistantText(context.Background(), "S1"); got != "" {
		t.Errorf("empty history should yield empty, got %q", got)
	}
}

func TestTruncateForError_CapsLength(t *testing.T) {
	t.Parallel()
	got := truncateForError(strings.Repeat("x", lastAssistantTextMaxLen+50))
	if len([]rune(got)) != lastAssistantTextMaxLen+1 { // +1 for the ellipsis
		t.Errorf("truncated length = %d, want %d", len([]rune(got)), lastAssistantTextMaxLen+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected an ellipsis suffix, got %q", got[len(got)-8:])
	}
}

// ---- Verification 3 --------------------------------------------------------

// TestStructOutputRetry_TurnExhaustionSkipsRetry: an empty response caused by
// the turn budget running out must NOT be re-prompted — the retry has no budget
// to spend and the agent runtime already forced its own struct_output wrap-up.
func TestStructOutputRetry_TurnExhaustionSkipsRetry(t *testing.T) {
	run := runInteractiveFlow(t, "retry-exhausted", []Step{interactiveStep("products", 3)},
		[]agentpkg.AgentEvent{exhaustedEvent(), structEvent(`{"products":[],"confirmed":true}`)}, nil)

	if got := run.agent.callCount(); got != 1 {
		t.Fatalf("turn exhaustion must not trigger a re-prompt; got %d agent calls", got)
	}
	if run.terminal.Status != FlowStatusFailed {
		t.Fatalf("terminal status = %q, want failed", run.terminal.Status)
	}
	if run.hasStatus("products", FlowStatusRetrying) {
		t.Error("no flow.step.retrying transition should be emitted on turn exhaustion")
	}
	if !containsSubstring(run.terminal.Output, "turn budget exhausted") {
		t.Errorf("error should say the budget ran out: %q", run.terminal.Output)
	}
}

// TestStructOutputRetry_TurnExhaustionSkipsNonInteractiveForcedTurn: the same
// rule applies to the non-interactive last-ditch forced turn.
func TestStructOutputRetry_TurnExhaustionSkipsNonInteractiveForcedTurn(t *testing.T) {
	agent, terminal := runForceFlow(t, "force-exhausted", &StepOutput{Schema: map[string]any{"type": "object"}},
		[]agentpkg.AgentEvent{exhaustedEvent(), structEvent(`{"ok":true}`)})

	if got := agent.callCount(); got != 1 {
		t.Fatalf("turn exhaustion must not trigger the forced wrap-up turn; got %d agent calls", got)
	}
	if terminal.Status != FlowStatusFailed {
		t.Fatalf("terminal status = %q, want failed", terminal.Status)
	}
}

// ---- Verification 4 --------------------------------------------------------

// TestStructOutputRetry_BridgeStillBoundAndQuestionReachesPeer: the crux. The
// interactive unbind is deferred to runStep's return, so a re-prompt issued
// from inside the attempt loop still has its bridge binding and its interactive
// session flag — which is exactly what makes `question` route to the human
// instead of being auto-approved past them.
func TestStructOutputRetry_BridgeStillBoundAndQuestionReachesPeer(t *testing.T) {
	hook := newRecordingHook()
	perms := newInteractivePermissions()
	// peerSaw records what the bridge would have delivered to the reviewer.
	var (
		mu      sync.Mutex
		peerSaw []string
		snap    struct {
			bound       bool
			interactive bool
			unbinds     int
		}
	)

	steps := []Step{interactiveStep("products", 20)}
	registerTestFlow(t, Flow{ID: "retry-bound", Name: "Interactive", Spec: FlowSpec{Steps: steps}})

	base := &stubAgent{
		Broker: pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{
			emptyEvent(),
			structEvent(`{"products":["a"],"confirmed":true}`),
		},
	}
	agent := &scriptedAgent{stubAgent: base}
	// The session id comes from RunWith itself — reading it off the flow-state
	// stream would race with the agent call we are inspecting.
	agent.onCall = func(callIdx int, sessionID, _ string) {
		if callIdx != 1 { // callIdx 1 == the re-prompt turn
			return
		}
		mu.Lock()
		defer mu.Unlock()
		snap.bound = hook.isBound(sessionID)
		snap.interactive = perms.IsInteractiveSession(sessionID)
		snap.unbinds = hook.unbindCount()
		// Simulate what the question tool does on this turn: it only reaches
		// the peer when the session is flagged interactive (otherwise
		// auto-approve silently answers for the human) AND still bound.
		if snap.interactive && snap.bound {
			peerSaw = append(peerSaw, "Which products should I present?")
		}
	}

	svc := NewService(&stubSessions{}, &stubMessages{}, &stubQuerier{}, perms,
		&scriptedAgentFactory{agent: agent})
	svc.(*service).SetInteractiveHook(hook)

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", "retry-bound", copyArgs(reviewerArgs), true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	var terminal *FlowState
	for s := range flowStates {
		if s.Status == FlowStatusFailed || s.Status == FlowStatusCompleted {
			terminal = s
		}
	}
	for range agentEvents {
	}

	mu.Lock()
	defer mu.Unlock()
	if !snap.bound {
		t.Error("bridge was already unbound when the re-prompt ran — a re-prompt with no bridge cannot ask the reviewer anything")
	}
	if !snap.interactive {
		t.Error("session lost its interactive flag before the re-prompt — question would auto-approve past the human")
	}
	if snap.unbinds != 0 {
		t.Errorf("OnInteractiveStepComplete ran %d time(s) before the re-prompt; unbind must stay deferred to runStep's return", snap.unbinds)
	}
	if len(peerSaw) != 1 {
		t.Errorf("the re-prompt's question did not reach the peer: %v", peerSaw)
	}
	if terminal == nil || terminal.Status != FlowStatusCompleted {
		t.Fatalf("expected the step to complete, got %+v", terminal)
	}
	// And the binding must still be torn down once the step really ends.
	if hook.unbindCount() != 1 {
		t.Errorf("expected exactly one unbind after the step completed, got %d", hook.unbindCount())
	}
}

// ---- Verification 5 --------------------------------------------------------

// TestStructOutputRetry_FirstTurnStructOutputCostsNoExtraCall: the happy path
// must be untouched — struct_output on the first turn means exactly one model
// call and no retrying transition.
func TestStructOutputRetry_FirstTurnStructOutputCostsNoExtraCall(t *testing.T) {
	run := runInteractiveFlow(t, "retry-happy", []Step{interactiveStep("products", 20)},
		[]agentpkg.AgentEvent{structEvent(`{"products":["a"],"confirmed":true}`)}, nil)

	if got := run.agent.callCount(); got != 1 {
		t.Fatalf("expected exactly 1 agent call on the happy path, got %d", got)
	}
	if run.hasStatus("products", FlowStatusRetrying) {
		t.Error("happy path must not emit a retrying transition")
	}
	if run.terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed", run.terminal.Status)
	}
}

// TestStructOutputRetry_ProseFirstTurnIsNotReprompted: an interactive step whose
// agent DID say something keeps the pre-existing text-fallback behaviour — the
// re-prompt is reserved for the "produced nothing at all" case.
func TestStructOutputRetry_ProseFirstTurnIsNotReprompted(t *testing.T) {
	run := runInteractiveFlow(t, "retry-prose", []Step{interactiveStep("products", 20)},
		[]agentpkg.AgentEvent{proseEvent("here are the products, in prose")}, nil)

	if got := run.agent.callCount(); got != 1 {
		t.Fatalf("expected exactly 1 agent call (prose is accepted as-is), got %d", got)
	}
	if run.terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed", run.terminal.Status)
	}
}

// TestStructOutputRetry_CappedAtOnePerStepAcrossFallbackRetries: fallback.retry
// re-runs the whole step; the struct_output re-prompt must still be spent at
// most once, not once per attempt.
func TestStructOutputRetry_CappedAtOnePerStepAcrossFallbackRetries(t *testing.T) {
	step := interactiveStep("products", 20)
	step.Fallback = &Fallback{Retry: 2}

	run := runInteractiveFlow(t, "retry-capped", []Step{step},
		[]agentpkg.AgentEvent{emptyEvent()}, nil)

	// 3 attempts (1 + fallback.retry 2) + exactly 1 re-prompt.
	if got := run.agent.callCount(); got != 4 {
		t.Fatalf("expected 3 attempts + 1 re-prompt = 4 agent calls, got %d", got)
	}
	if run.terminal.Status != FlowStatusFailed {
		t.Fatalf("terminal status = %q, want failed", run.terminal.Status)
	}
}

// ---- prompt construction ---------------------------------------------------

func TestStructOutputRetryPrompt_ShapesPerStepKind(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"required":   []any{"decision", "rationale"},
		"properties": map[string]any{"decision": map[string]any{}, "rationale": map[string]any{}, "extra": map[string]any{}},
	}

	interactive := structOutputRetryPrompt(Step{
		ID: "s", Interactive: true, Output: &StepOutput{Schema: schema},
	})
	for _, want := range []string{"struct_output", "decision, rationale", "`question` is the only tool"} {
		if !strings.Contains(interactive, want) {
			t.Errorf("interactive nudge missing %q:\n%s", want, interactive)
		}
	}

	plain := structOutputRetryPrompt(Step{ID: "s", Output: &StepOutput{Schema: schema}})
	if strings.Contains(plain, "question") {
		t.Errorf("non-interactive nudge must not mention the question tool:\n%s", plain)
	}
	if !strings.Contains(plain, "Do not reply with prose") {
		t.Errorf("non-interactive nudge should demand struct_output:\n%s", plain)
	}
}

func TestSchemaFieldNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		schema map[string]any
		want   []string
	}{
		{"nil", nil, nil},
		{"required any-slice", map[string]any{"required": []any{"b", "a"}}, []string{"b", "a"}},
		{"required string-slice", map[string]any{"required": []string{"x"}}, []string{"x"}},
		{
			"falls back to sorted properties",
			map[string]any{"properties": map[string]any{"z": nil, "a": nil}},
			[]string{"a", "z"},
		},
		{"empty required falls through", map[string]any{"required": []any{}, "properties": map[string]any{"q": nil}}, []string{"q"}},
		{"no fields", map[string]any{"type": "object"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := schemaFieldNames(tc.schema)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("schemaFieldNames = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStructOutputTurnsExhausted(t *testing.T) {
	t.Parallel()
	if structOutputTurnsExhausted(nil) {
		t.Error("nil must not report exhaustion")
	}
	if structOutputTurnsExhausted(errors.New("boom")) {
		t.Error("an unrelated error must not report exhaustion")
	}
	if structOutputTurnsExhausted(&missingStructOutputError{StepID: "s"}) {
		t.Error("a non-exhausted miss must not report exhaustion")
	}
	if !structOutputTurnsExhausted(&missingStructOutputError{StepID: "s", TurnsExhausted: true}) {
		t.Error("an exhausted miss must report exhaustion")
	}
	wrapped := errors.Join(errors.New("other"), &missingStructOutputError{StepID: "s", TurnsExhausted: true})
	if !structOutputTurnsExhausted(wrapped) {
		t.Error("errors.As must see through wrapping")
	}
}

// ---- schema-rejected struct_output ----------------------------------------

// rejectedStructEvent is an agent turn whose struct_output call was refused by
// the schema: message.Message.StructOutput surfaces the ERRORED tool result
// (non-empty Content, IsError) when the turn produced no accepted one.
func rejectedStructEvent(reason string) agentpkg.AgentEvent {
	return agentpkg.AgentEvent{
		Type: agentpkg.AgentEventTypeResponse,
		Done: true,
		StructOutput: &message.ToolResult{
			Name:    tools.StructOutputToolName,
			Content: reason,
			IsError: true,
		},
		Message: message.Message{
			Role:  message.Assistant,
			Parts: []message.ContentPart{message.TextContent{Text: ""}},
		},
	}
}

// TestStructOutputRetry_SchemaRejectedIsRepromptedNotAccepted: a rejection is
// not a document. Before the fix the gate only asked `Content == ""`, so the
// rejection TEXT satisfied it and the step completed with
// `Output: "Output does not match schema: …"` and IsStructOutput=true — no
// re-prompt, no failure, and nothing merged into args for downstream steps.
func TestStructOutputRetry_SchemaRejectedIsRepromptedNotAccepted(t *testing.T) {
	run := runInteractiveFlow(t, "retry-rejected", []Step{interactiveStep("products", 20)},
		[]agentpkg.AgentEvent{
			rejectedStructEvent(`Output does not match schema: missing required field "confirmed"`),
			structEvent(`{"products":["a"],"confirmed":true}`),
		}, nil)

	if got := run.agent.callCount(); got != 2 {
		t.Fatalf("expected the rejection to be re-prompted (2 agent calls), got %d", got)
	}
	if !run.hasStatus("products", FlowStatusRetrying) {
		t.Errorf("expected a %q transition, got %v", FlowStatusRetrying, run.statusesFor("products"))
	}
	if run.terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed; output=%q", run.terminal.Status, run.terminal.Output)
	}
	if !containsSubstring(run.terminal.Output, `"confirmed":true`) {
		t.Errorf("step output = %q, want the rescued struct_output", run.terminal.Output)
	}
	if run.nextArgs["confirmed"] != true {
		t.Errorf("rescued struct_output did not merge into args: %#v", run.nextArgs)
	}
}

// TestStructOutputRetry_SchemaRejectedNeverBecomesStepOutput: when the
// re-prompt is rejected too, the step must FAIL rather than publish the
// rejection text as its structured output.
func TestStructOutputRetry_SchemaRejectedNeverBecomesStepOutput(t *testing.T) {
	const reason = `Output does not match schema: missing required field "confirmed"`
	run := runInteractiveFlow(t, "retry-rejected-twice", []Step{interactiveStep("products", 20)},
		[]agentpkg.AgentEvent{rejectedStructEvent(reason)}, nil)

	if run.terminal.Status != FlowStatusFailed {
		t.Fatalf("terminal status = %q, want failed; output=%q", run.terminal.Status, run.terminal.Output)
	}
	if run.terminal.IsStructOutput {
		t.Error("a schema-rejected result must never be published as IsStructOutput")
	}
	if !containsSubstring(run.terminal.Output, "rejected by the schema") {
		t.Errorf("error should say the call was rejected rather than absent: %q", run.terminal.Output)
	}
}

// TestForceStructOutput_SchemaRejectedWithProseIsForced: the same gate on the
// non-interactive prose path — a rejected struct_output alongside prose must
// still earn the forcing wrap-up turn instead of being accepted as the result.
func TestForceStructOutput_SchemaRejectedWithProseIsForced(t *testing.T) {
	rejectedWithProse := rejectedStructEvent("Invalid JSON: unexpected end of input")
	rejectedWithProse.Message = message.Message{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "here is my plan, in prose"}},
	}

	agent, terminal := runForceFlow(t, "force-rejected", &StepOutput{Schema: map[string]any{"type": "object"}},
		[]agentpkg.AgentEvent{rejectedWithProse, structEvent(`{"ok":true}`)})

	if got := agent.callCount(); got != 2 {
		t.Fatalf("expected the forcing turn to fire on a rejected struct_output, got %d calls", got)
	}
	if terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed", terminal.Status)
	}
	if !containsSubstring(terminal.Output, `"ok":true`) {
		t.Errorf("step output = %q, want the forced struct_output", terminal.Output)
	}
}

// ---- the retrying signal must describe a turn that really ran --------------

// TestStructOutputRetry_NotClaimedWhenRePromptNeverStarted: retryStructOutputTurn
// can give up without issuing a model call (ErrSessionBusy outliving its short
// retry budget). Neither the flow.step.retrying transition nor the
// "(re-prompted once…)" clause in the step error may claim a turn nobody paid
// for — and the one-per-step budget must remain unspent.
func TestStructOutputRetry_NotClaimedWhenRePromptNeverStarted(t *testing.T) {
	steps := []Step{interactiveStep("products", 20)}
	registerTestFlow(t, Flow{ID: "retry-never-started", Name: "Interactive", Spec: FlowSpec{Steps: steps}})

	base := &stubAgent{
		Broker:    pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{emptyEvent()},
	}
	agent := &scriptedAgent{
		stubAgent: base,
		// Every call after the step's own turn is permanently session-busy, so
		// the re-prompt exhausts its retry budget without ever starting.
		startErr: func(callIdx int) error {
			if callIdx == 0 {
				return nil
			}
			return agentpkg.ErrSessionBusy
		},
	}
	svc := NewService(&stubSessions{}, &stubMessages{}, &stubQuerier{}, newInteractivePermissions(),
		&scriptedAgentFactory{agent: agent})
	svc.(*service).SetInteractiveHook(newRecordingHook())

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", "retry-never-started", copyArgs(reviewerArgs), true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	var (
		terminal *FlowState
		statuses []FlowStatus
	)
	for s := range flowStates {
		statuses = append(statuses, s.Status)
		if s.Status == FlowStatusFailed || s.Status == FlowStatusCompleted {
			terminal = s
		}
	}
	for range agentEvents {
	}

	if terminal == nil || terminal.Status != FlowStatusFailed {
		t.Fatalf("expected the step to fail, got %+v", terminal)
	}
	for _, st := range statuses {
		if st == FlowStatusRetrying {
			t.Error("flow.step.retrying was emitted for a re-prompt that never started")
		}
	}
	if containsSubstring(terminal.Output, "re-prompted once") {
		t.Errorf("error claims a re-prompt that never ran: %q", terminal.Output)
	}
}

// ---- truncation ------------------------------------------------------------

// TestTruncateForError_KeepsValidUTF8: the truncated text is written to
// flow_states.output (LONGTEXT / utf8mb4 on MySQL, which rejects invalid
// utf8mb4 with error 1366), so the cut must land on a rune boundary while still
// honouring the byte budget. 1000 % 3 == 1, so a bare s[:1000] splits the 334th
// 3-byte rune.
func TestTruncateForError_KeepsValidUTF8(t *testing.T) {
	t.Parallel()
	// Every rune width, at every alignment relative to the cut — the `pad`
	// prefix shifts the multi-byte run so each width really does get a case
	// where byte lastAssistantTextMaxLen lands mid-sequence.
	for _, r := range []string{"é", "日", "😀"} { // 2, 3, 4 bytes
		for pad := 0; pad < utf8.UTFMax; pad++ {
			in := strings.Repeat("a", pad) + strings.Repeat(r, lastAssistantTextMaxLen)
			got := truncateForError(in)
			label := fmt.Sprintf("rune %q pad %d", r, pad)
			if !utf8.ValidString(got) {
				t.Errorf("%s: truncated text is not valid UTF-8: % x", label, got)
			}
			// The ellipsis is the only thing allowed past the byte budget.
			body := strings.TrimSuffix(got, "…")
			if len(body) > lastAssistantTextMaxLen {
				t.Errorf("%s: body = %d bytes, want <= %d", label, len(body), lastAssistantTextMaxLen)
			}
			// Backing off to a rune boundary costs at most UTFMax-1 bytes.
			if len(body) < lastAssistantTextMaxLen-(utf8.UTFMax-1) {
				t.Errorf("%s: backed off too far: %d bytes", label, len(body))
			}
			if !strings.HasSuffix(got, "…") {
				t.Errorf("%s: expected an ellipsis suffix", label)
			}
		}
	}
	// A string already inside the cap must come back byte-identical.
	short := "короткий ответ агента"
	if out := truncateForError(short); out != short {
		t.Errorf("short multibyte text was altered: %q", out)
	}
}
