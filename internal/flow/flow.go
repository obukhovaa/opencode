package flow

import (
	"errors"
)

var (
	ErrFlowNotFound     = errors.New("flow not found")
	ErrFlowDisabled     = errors.New("flow is disabled")
	ErrInvalidFlowName  = errors.New("invalid flow name")
	ErrInvalidStepID    = errors.New("invalid step ID")
	ErrDuplicateStepID  = errors.New("duplicate step ID")
	ErrInvalidRule      = errors.New("rule references non-existent step")
	ErrInvalidFallback  = errors.New("fallback references non-existent step")
	ErrNoSteps          = errors.New("flow has no steps")
	ErrInvalidYAML      = errors.New("invalid flow YAML")
	ErrInvalidPredicate = errors.New("invalid predicate")
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
