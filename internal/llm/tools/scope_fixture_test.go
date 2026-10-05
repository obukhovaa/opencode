package tools

import (
	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/permission"
)

// allowAllAgentRegistry answers every permission evaluation with allow, so
// tool tests can exercise a tool's own gates without loading a config.
type allowAllAgentRegistry struct{ agentregistry.Registry }

func (allowAllAgentRegistry) EvaluatePermission(string, string, string) permission.Action {
	return permission.ActionAllow
}
