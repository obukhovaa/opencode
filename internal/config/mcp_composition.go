package config

import (
	"fmt"
	"strings"
	"unicode"
)

// MCPCompositionConfig lists operator-reviewed read endpoints, never patterns.
// An agent must separately grant the exact mcp_compose tool name.
type MCPCompositionConfig struct {
	ReadTools []string `json:"readTools,omitempty"`
}

func validateMCPComposition(c *MCPCompositionConfig) error {
	if c == nil {
		return nil
	}
	seen := make(map[string]bool, len(c.ReadTools))
	for _, name := range c.ReadTools {
		if name == "" || len(name) > 256 || strings.ContainsAny(name, "*?[]{}") || strings.ContainsFunc(name, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}) {
			return fmt.Errorf("mcpComposition.readTools must contain exact nonempty tool names of at most 256 bytes, without whitespace or patterns")
		}
		if seen[name] {
			return fmt.Errorf("mcpComposition.readTools contains duplicate tool %q", name)
		}
		seen[name] = true
	}
	return nil
}
