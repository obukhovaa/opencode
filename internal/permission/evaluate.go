package permission

import (
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

func matchPatternsAny(input string, patterns map[string]any) Action {
	var lastMatch Action

	if v, ok := patterns["*"]; ok {
		if s, ok := v.(string); ok {
			lastMatch = toAction(s)
		}
	}

	for pattern, v := range patterns {
		if pattern == "*" {
			continue
		}
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

	for pattern, action := range patterns {
		if pattern == "*" {
			continue
		}
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
