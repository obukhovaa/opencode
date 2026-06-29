package flow

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrFlowNotFound         = errors.New("flow not found")
	ErrFlowDisabled         = errors.New("flow is disabled")
	ErrInvalidFlowName      = errors.New("invalid flow name")
	ErrInvalidStepID        = errors.New("invalid step ID")
	ErrDuplicateStepID      = errors.New("duplicate step ID")
	ErrInvalidRule          = errors.New("rule references non-existent step")
	ErrInvalidFallback      = errors.New("fallback references non-existent step")
	ErrNoSteps              = errors.New("flow has no steps")
	ErrInvalidYAML          = errors.New("invalid flow YAML")
	ErrInvalidPredicate     = errors.New("invalid predicate")
	ErrInvalidMaxTurns      = errors.New("invalid maxTurns")
	ErrInvalidMaxIterations = errors.New("invalid maxIterations")
)

// Flow represents a discovered flow definition.
type Flow struct {
	ID          string
	Name        string
	Disabled    bool
	Description string
	Spec        FlowSpec
	Location    string
}

// FlowSession controls session behavior at the flow level.
//
// ResumeOnFailure governs how a re-trigger interprets prior `failed`
// flow-state rows. When false (default), `failed` is terminal — a
// re-trigger restarts the flow from step 0. When true, `failed` joins
// the in-progress set and a re-trigger resumes from the failed step.
// See openspec/specs/flow-runtime-resume for the full gating contract.
type FlowSession struct {
	Prefix          string `yaml:"prefix,omitempty"`
	ResumeOnFailure bool   `yaml:"resume_on_failure,omitempty"`
}

// FlowSpec contains the flow's args schema and step definitions.
type FlowSpec struct {
	Args    map[string]any `yaml:"args,omitempty"`
	Session FlowSession    `yaml:"session,omitempty"`
	Steps   []Step         `yaml:"steps"`
}

// Step defines a single step in the flow graph.
type Step struct {
	ID       string      `yaml:"id"`
	Agent    string      `yaml:"agent,omitempty"`
	Session  StepSession `yaml:"session,omitempty"`
	Prompt   string      `yaml:"prompt"`
	Output   *StepOutput `yaml:"output,omitempty"`
	Rules    []Rule      `yaml:"rules,omitempty"`
	Fallback *Fallback   `yaml:"fallback,omitempty"`
	// MaxTurns optionally overrides the agent's maxTurns for this step.
	// 0 (unset) inherits the agent's configured maxTurns (which in turn
	// falls back to the global default). When explicitly set in the YAML it
	// must be >= 1 — enforced by validateFlow.
	MaxTurns int `yaml:"maxTurns,omitempty"`
	// MaxIterations caps the number of in-process self-loop iterations of
	// this step. 0 (unset) means unbounded — capped only by flow timeout.
	// When the (N+1)th self-route would exceed this value, the step is
	// failed instead of re-scheduled, and the fallback (if any) runs.
	MaxIterations int `yaml:"maxIterations,omitempty"`
	// Timeout bounds the wall-clock time the flow runner gives this
	// step's agent.Run invocation. It cascades into agent.RunWith's ctx
	// so the non-interactive end-of-turn wait for pending background
	// tasks (bash run_in_background, task async, monitor) also honors
	// this deadline. Empty (unset) falls back to the
	// OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT env var; if neither is
	// set the wait is bounded only by the parent flow's surrounding ctx.
	// Format: any Go duration string (`5m`, `1h30m`, `30s`).
	Timeout string `yaml:"timeout,omitempty"`
	// Interactive marks this step as router-initiated. When true, the
	// flow engine MUST resolve Interaction.Target against flow-args, call
	// the configured InteractiveHook.OnInteractiveStepStart before
	// agent.Run, and call OnInteractiveStepComplete after the agent
	// produces a struct_output. See flow-api spec "interactive flow step
	// type with interaction block".
	Interactive bool `yaml:"interactive,omitempty"`
	// Interaction carries the binding targets for an Interactive step.
	// Ignored when Interactive is false.
	Interaction *StepInteraction `yaml:"interaction,omitempty"`
}

// StepInteraction captures the binding targets a router-initiated step
// expects to receive replies from. Target is a flow-arg expression
// (e.g. `${args.reviewer}` or `${args.reviewers}`) that resolves to a
// single PeerRef-shaped map or an array of such maps. Mention is an
// optional flow-arg expression for the first-message ping handle.
//
// The bridge identity is carried by the resolved PeerRef itself
// (args.reviewer.identity). The c2-agent orchestrator populates it
// from the trigger source (slash command's Slack app identity, webhook
// mapping's target_identity, REST caller's explicit value). There is
// intentionally no separate `interaction.identity` override in the flow
// YAML — the caller is the source of truth, and adding an override
// here would just duplicate that knowledge.
type StepInteraction struct {
	// Target is a flow-arg expression resolving to either a single
	// PeerRef map or an array of them. Format:
	//   target: ${args.reviewer}      # single
	//   target: ${args.reviewers}     # array
	Target string `yaml:"target"`
	// Mention is an optional first-message ping handle expression.
	Mention string `yaml:"mention,omitempty"`
}

// StepSession controls session behavior for a step.
type StepSession struct {
	Fork bool `yaml:"fork,omitempty"`
}

// StepOutput defines optional structured output for a step.
type StepOutput struct {
	Schema map[string]any `yaml:"schema"`
}

// Rule defines a conditional routing rule evaluated after step completion.
type Rule struct {
	If       string `yaml:"if"`
	Then     string `yaml:"then"`
	Postpone bool   `yaml:"postpone,omitempty"`
}

// Fallback defines retry and error-routing behavior for a step.
type Fallback struct {
	Retry int    `yaml:"retry"`
	Delay int    `yaml:"delay,omitempty"`
	To    string `yaml:"to,omitempty"`
}

// TimeoutDuration parses Step.Timeout as a Go duration string and returns
// the value. Empty / whitespace-only returns (0, nil) — caller should
// treat zero as "no per-step timeout set". A non-empty value that fails
// to parse OR is negative returns an error suitable for surfacing during
// flow validation. Successful parse of a positive duration is the only
// path that returns a non-zero value.
func (s Step) TimeoutDuration() (time.Duration, error) {
	if s.Timeout == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s.Timeout)
	if err != nil {
		return 0, fmt.Errorf("step %q: invalid timeout %q: %w", s.ID, s.Timeout, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("step %q: timeout must be non-negative, got %v", s.ID, d)
	}
	return d, nil
}
