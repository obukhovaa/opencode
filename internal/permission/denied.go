package permission

import (
	"errors"
	"fmt"
)

// RuleDeniedError reports that a configured permission rule — an agent's
// `permission` block, the global `permission.rules`, or a workspace CLI
// tool's manifest default — evaluated to deny for a tool call. It is a
// policy decision made before anyone was asked, unlike ErrorPermissionDenied
// on its own, which the tools return when a human rejected the prompt.
//
// The agent loop turns it into an error tool result and keeps the run going
// (a human rejection still ends the run), so a workspace can deny a command
// without the first attempt at it terminating the whole agent. errors.Is
// against ErrorPermissionDenied still holds, so callers that only ask "was
// this denied?" need no change.
type RuleDeniedError struct {
	// ToolName is the tool whose rule denied the call.
	ToolName string
	// Input is the string the rule was evaluated against: the command, file
	// path, URL, query, task ID or argument string, depending on the tool.
	Input string
}

// DeniedByRule returns the error a tool reports when a configured permission
// rule denies the call.
func DeniedByRule(toolName, input string) error {
	return &RuleDeniedError{ToolName: toolName, Input: input}
}

func (e *RuleDeniedError) Error() string {
	if e.Input == "" {
		return fmt.Sprintf("permission denied by a configured rule for tool %q", e.ToolName)
	}
	return fmt.Sprintf("permission denied by a configured rule for tool %q on %q", e.ToolName, e.Input)
}

// Is makes errors.Is(err, ErrorPermissionDenied) true for a rule denial.
func (e *RuleDeniedError) Is(target error) bool {
	return target == ErrorPermissionDenied
}

// AsRuleDenied returns the RuleDeniedError in err's chain, if any.
func AsRuleDenied(err error) (*RuleDeniedError, bool) {
	var rd *RuleDeniedError
	if errors.As(err, &rd) {
		return rd, true
	}
	return nil, false
}
