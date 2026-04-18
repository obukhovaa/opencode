package permission

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Action string

const (
	ActionAllow Action = "allow"
	ActionDeny  Action = "deny"
	ActionAsk   Action = "ask"
)

func EvaluateToolPermission(toolName, input string, agentPerms, globalPerms map[string]any) Action {
	if agentPerms != nil {
		if v, ok := agentPerms[toolName]; ok {
			if act := resolvePermissionValue(input, v); act != "" {
				return act
			}
		}
	}

	if globalPerms != nil {
		if v, ok := globalPerms[toolName]; ok {
			if act := resolvePermissionValue(input, v); act != "" {
				return act
			}
		}
	}

	if agentPerms != nil {
		if v, ok := agentPerms["*"]; ok {
			if act := resolvePermissionValue(input, v); act != "" {
				return act
			}
		}
	}

	if globalPerms != nil {
		if v, ok := globalPerms["*"]; ok {
			if act := resolvePermissionValue(input, v); act != "" {
				return act
			}
		}
	}

	return ActionAsk
}

// EvaluateReadToolPermission evaluates permission for read-category tools
// (read, grep, glob, ls). The lookup chain is:
//  1. Specific tool name (e.g., "grep") in agent perms, then global perms
//  2. "read" fallback (if toolName != "read") in agent perms, then global perms
//  3. "*" wildcard in agent perms, then global perms
//  4. Default: ActionAllow (read tools are safe by default)
//
// This allows users to set a blanket "read" permission that applies to all
// read-category tools, while still being able to override for specific tools.
func EvaluateReadToolPermission(toolName, input string, agentPerms, globalPerms map[string]any) Action {
	// 1. Try specific tool name (e.g., "grep") — overrides "read"
	if toolName != "read" {
		if act := lookupToolAction(toolName, input, agentPerms, globalPerms); act != "" {
			return act
		}
	}

	// 2. Fall back to "read" category
	if act := lookupToolAction("read", input, agentPerms, globalPerms); act != "" {
		return act
	}

	// 3. Fall back to "*" wildcard
	if act := lookupToolAction("*", input, agentPerms, globalPerms); act != "" {
		return act
	}

	// 4. Default: allow (read tools are safe by default)
	return ActionAllow
}

// lookupToolAction checks a single tool name against agent and global perms.
func lookupToolAction(toolName, input string, agentPerms, globalPerms map[string]any) Action {
	if agentPerms != nil {
		if v, ok := agentPerms[toolName]; ok {
			if act := resolvePermissionValue(input, v); act != "" {
				return act
			}
		}
	}
	if globalPerms != nil {
		if v, ok := globalPerms[toolName]; ok {
			if act := resolvePermissionValue(input, v); act != "" {
				return act
			}
		}
	}
	return ""
}

func IsToolEnabled(toolName string, toolsConfig map[string]bool) bool {
	if toolsConfig == nil {
		return true
	}
	if enabled, ok := toolsConfig[toolName]; ok {
		return enabled
	}
	for pattern, enabled := range toolsConfig {
		if MatchWildcard(pattern, toolName) {
			return enabled
		}
	}
	return true
}

func resolvePermissionValue(input string, value any) Action {
	switch v := value.(type) {
	case string:
		return toAction(v)
	case map[string]any:
		return matchPatternsAny(input, v)
	case map[string]string:
		return matchPatternsString(input, v)
	}
	return ""
}

// sortedPatternKeys returns map keys sorted for deterministic matching.
// The "*" wildcard is excluded (handled separately as the default).
// Keys are sorted by length ascending (least specific first), then
// alphabetically as a tiebreaker. Combined with "last match wins" semantics,
// this ensures the most specific (longest) matching pattern takes priority.
func sortedPatternKeys[V any](patterns map[string]V) []string {
	keys := make([]string, 0, len(patterns))
	for k := range patterns {
		if k == "*" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) < len(keys[j])
		}
		return keys[i] < keys[j]
	})
	return keys
}

func matchPatternsAny(input string, patterns map[string]any) Action {
	var lastMatch Action

	if v, ok := patterns["*"]; ok {
		if s, ok := v.(string); ok {
			lastMatch = toAction(s)
		}
	}

	for _, pattern := range sortedPatternKeys(patterns) {
		v := patterns[pattern]
		s, ok := v.(string)
		if !ok {
			continue
		}
		if MatchWildcard(pattern, input) {
			lastMatch = toAction(s)
		}
	}

	return lastMatch
}

func matchPatternsString(input string, patterns map[string]string) Action {
	var lastMatch Action

	if v, ok := patterns["*"]; ok {
		lastMatch = toAction(v)
	}

	for _, pattern := range sortedPatternKeys(patterns) {
		action := patterns[pattern]
		if MatchWildcard(pattern, input) {
			lastMatch = toAction(action)
		}
	}

	return lastMatch
}

func toAction(s string) Action {
	switch Action(strings.ToLower(s)) {
	case ActionAllow:
		return ActionAllow
	case ActionDeny:
		return ActionDeny
	case ActionAsk:
		return ActionAsk
	}
	return ""
}

func MatchWildcard(pattern, str string) bool {
	if pattern == "*" {
		return true
	}
	pattern = expandHome(pattern)
	// A pattern like "/foo/*" should also match "/foo" itself —
	// denying everything inside a directory implies denying access to the directory.
	if strings.HasSuffix(pattern, "/*") {
		dir := pattern[:len(pattern)-2]
		if str == dir {
			return true
		}
	}
	if !strings.Contains(pattern, "*") {
		return pattern == str
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 2 {
		prefix := parts[0]
		suffix := parts[1]
		if prefix != "" && !strings.HasPrefix(str, prefix) {
			return false
		}
		if suffix != "" && !strings.HasSuffix(str, suffix) {
			return false
		}
		if prefix != "" && suffix != "" {
			return len(str) >= len(prefix)+len(suffix)
		}
		return true
	}
	// Multi-wildcard: simple recursive check
	return deepMatch(pattern, str)
}

func deepMatch(pattern, str string) bool {
	for len(pattern) > 0 {
		if pattern[0] == '*' {
			pattern = pattern[1:]
			if len(pattern) == 0 {
				return true
			}
			for i := 0; i <= len(str); i++ {
				if deepMatch(pattern, str[i:]) {
					return true
				}
			}
			return false
		}
		if len(str) == 0 || pattern[0] != str[0] {
			return false
		}
		pattern = pattern[1:]
		str = str[1:]
	}
	return len(str) == 0
}

// expandHome replaces a leading "~/" or "~" in a pattern with the user's
// home directory so that permission patterns like "~/.openai/*" match
// absolute paths like "/Users/foo/.openai/file".
func expandHome(pattern string) string {
	if !strings.HasPrefix(pattern, "~") {
		return pattern
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return pattern
	}
	if pattern == "~" {
		return home
	}
	if strings.HasPrefix(pattern, "~/") {
		return filepath.Join(home, pattern[2:])
	}
	return pattern
}
