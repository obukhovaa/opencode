package tools

import (
	"context"
	"strings"

	"github.com/opencode-ai/opencode/internal/permission"
)

type deniedToolsKey struct{}

// WithDeniedTools returns ctx carrying a flow step's denyTools globs. The
// agent factory drops matching tools from every agent built on such a ctx —
// the step's own and the subagents its tools spawn — and the agent loop
// refuses a call to one at dispatch (IsToolDenied). Entries are trimmed and
// blanks dropped; an empty list returns ctx unchanged.
func WithDeniedTools(ctx context.Context, globs []string) context.Context {
	var clean []string
	for _, g := range globs {
		if g = strings.TrimSpace(g); g != "" {
			clean = append(clean, g)
		}
	}
	if len(clean) == 0 {
		return ctx
	}
	return context.WithValue(ctx, deniedToolsKey{}, clean)
}

// DeniedTools returns the globs WithDeniedTools put on ctx, or nil.
func DeniedTools(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	globs, _ := ctx.Value(deniedToolsKey{}).([]string)
	return globs
}

// IsToolDeniedBy reports whether name matches one of globs, compared
// case-insensitively with the `tools` wildcard rules. struct_output is never
// denied: a schema step must always be able to finish.
func IsToolDeniedBy(globs []string, name string) bool {
	if len(globs) == 0 || name == StructOutputToolName {
		return false
	}
	lower := strings.ToLower(name)
	for _, g := range globs {
		if permission.MatchWildcard(strings.ToLower(g), lower) {
			return true
		}
	}
	return false
}

// IsToolDenied is IsToolDeniedBy with the globs carried on ctx.
func IsToolDenied(ctx context.Context, name string) bool {
	return IsToolDeniedBy(DeniedTools(ctx), name)
}
