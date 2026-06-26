package hooks

// MatcherGroup is one entry under `hooks.<EventName>` in `.opencode.json`.
// A group runs its Hooks list sequentially when its Matcher matches the
// triggering tool name (or always, for events that don't use matchers).
//
// This type is referenced from `internal/config/config.go` so a
// hooks-aware `.opencode.json` round-trips through viper into the Go
// config object without manual translation. The dependency direction
// is `config → hooks` (same pattern as `config.Router *bridge.Config`).
type MatcherGroup struct {
	Matcher string      `json:"matcher,omitempty"`
	Hooks   []HookEntry `json:"hooks"`
}

// HookEntry is one executable hook within a MatcherGroup.
//
// v1 supports `type: "command"` only. The runner ignores entries with any
// other type and logs a WARN — this is intentional so a `.opencode.json`
// that also targets Claude Code's other hook types (http, mcp_tool,
// prompt, agent) loads cleanly and silently skips the unsupported ones.
type HookEntry struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Timeout int      `json:"timeout,omitempty"` // seconds; 0 means use default
	Shell   string   `json:"shell,omitempty"`   // "sh" | "bash"; empty falls back to runner's default
}
