package config

import "time"

// DefaultToolCallTimeoutSeconds is the agent-loop bound on one tool call when
// neither the agent nor the top level sets toolCallTimeoutSeconds. Sized above
// the MCP per-call default (5 min) so a slow but answering server still
// finishes, and well below a daemon's human patience.
const DefaultToolCallTimeoutSeconds = 1200

// ToolCallTimeout resolves the per-tool-call bound for agentID: the agent's
// toolCallTimeoutSeconds, else the top-level one, else the default. Zero
// means no bound; a negative value is ignored.
func ToolCallTimeout(agentID AgentName) time.Duration {
	return ToolCallTimeoutFor(agentID, nil)
}

// ToolCallTimeoutFor is ToolCallTimeout with the value the agent registry
// merged for the agent (its markdown frontmatter, overlaid by its
// .opencode.json entry). The agent's own .opencode.json entry still wins over
// it, then the top-level value, then the default.
func ToolCallTimeoutFor(agentID AgentName, registryValue *int) time.Duration {
	seconds := DefaultToolCallTimeoutSeconds
	if cfg == nil {
		if registryValue != nil && *registryValue >= 0 {
			seconds = *registryValue
		}
		return time.Duration(seconds) * time.Second
	}
	if v := cfg.ToolCallTimeoutSeconds; v != nil && *v >= 0 {
		seconds = *v
	}
	if registryValue != nil && *registryValue >= 0 {
		seconds = *registryValue
	}
	if a, ok := cfg.Agents[agentID]; ok && a.ToolCallTimeoutSeconds != nil && *a.ToolCallTimeoutSeconds >= 0 {
		seconds = *a.ToolCallTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}
