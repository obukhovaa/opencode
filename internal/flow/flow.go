package flow

import (
	"errors"
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
type FlowSession struct {
	Prefix string `yaml:"prefix,omitempty"`
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
type StepInteraction struct {
	// Target is a flow-arg expression resolving to either a single
	// PeerRef map or an array of them. Format:
	//   target: ${args.reviewer}      # single
	//   target: ${args.reviewers}     # array
	Target string `yaml:"target"`
	// Mention is an optional first-message ping handle expression.
	Mention string `yaml:"mention,omitempty"`
	// Identity is the bridge identity ID this step's bind should scope
	// to (e.g. "default", "c2-agent-prod"). When non-empty, overrides
	// the Identity field on the resolved PeerRef so flow YAML controls
	// which bridge identity an interactive step uses — independent of
	// what args.reviewer.identity carries. Defaults to "default" when
	// omitted, preserving backwards compat with existing flow specs.
	// See c2-agent-flow-trigger-context capability
	// flow-interaction-identity-binding.
	Identity string `yaml:"identity,omitempty"`
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
