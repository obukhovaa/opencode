package agent

import (
	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// deniedToolsRegistry narrows an agent registry by a flow step's denyTools
// (tools.WithDeniedTools): a matching tool reads as disabled for every agent,
// so NewToolSet leaves it out of the built-in, MCP, CLI and deferred sets
// alike. Every other lookup passes through. NewAgent installs it when the
// ctx carries denied tools; the agent loop's dispatch guard backs it up.
type deniedToolsRegistry struct {
	agentregistry.Registry
	deny []string
}

func (r deniedToolsRegistry) IsToolEnabled(agentID, toolName string) bool {
	return !tools.IsToolDeniedBy(r.deny, toolName) && r.Registry.IsToolEnabled(agentID, toolName)
}

func (r deniedToolsRegistry) IsToolExplicitlyEnabled(agentID, toolName string) bool {
	return !tools.IsToolDeniedBy(r.deny, toolName) && r.Registry.IsToolExplicitlyEnabled(agentID, toolName)
}
