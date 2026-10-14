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
	seconds := DefaultToolCallTimeoutSeconds
	if cfg == nil {
		return time.Duration(seconds) * time.Second
	}
	if v := cfg.ToolCallTimeoutSeconds; v != nil && *v >= 0 {
		seconds = *v
	}
	if a, ok := cfg.Agents[agentID]; ok && a.ToolCallTimeoutSeconds != nil && *a.ToolCallTimeoutSeconds >= 0 {
		seconds = *a.ToolCallTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}
