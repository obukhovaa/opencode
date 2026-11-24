package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
)

type listingScopeKey struct{}

// WithListingScope returns ctx carrying a flow step's resolved listing scope.
// Like denyTools it rides the tool-call ctx, so subagents spawned by the
// step's tools are built with the same scope. The zero scope is stored too:
// it overrides whatever scope an enclosing agent's ctx carried.
func WithListingScope(ctx context.Context, scope agentregistry.ListingScope) context.Context {
	return context.WithValue(ctx, listingScopeKey{}, scope)
}

// ListingScopeFrom returns the scope WithListingScope put on ctx, or the zero
// (unscoped) value.
func ListingScopeFrom(ctx context.Context) agentregistry.ListingScope {
	if ctx == nil {
		return agentregistry.ListingScope{}
	}
	s, _ := ctx.Value(listingScopeKey{}).(agentregistry.ListingScope)
	return s
}

// ListingScopeKey renders a scope as a cache-key component. It tells a nil
// list (not scoped) apart from an empty one, and is "" for the zero scope.
func ListingScopeKey(scope agentregistry.ListingScope) string {
	if scope.Skills == nil && scope.Subagents == nil {
		return ""
	}
	part := func(list []string) string {
		if list == nil {
			return "-"
		}
		return "[" + strings.Join(list, "\x00") + "]"
	}
	return "skills=" + part(scope.Skills) + ";subagents=" + part(scope.Subagents)
}

var listingTokenPattern = regexp.MustCompile(`\$\{([^}]*)\}`)

// listingTokenWarned dedupes the misconfiguration warning per pattern.
var listingTokenWarned sync.Map

// ResolveListingScope expands the `${args.<path>}` tokens of every configured
// list with lookup. A list applies only when every token in every pattern
// resolves to a non-empty scalar; otherwise it comes back nil, leaving that
// listing unscoped. Resolved values are escaped so they match literally.
func ResolveListingScope(cfg *config.ListingScopeConfig, lookup func(path string) (any, bool)) agentregistry.ListingScope {
	if cfg == nil {
		return agentregistry.ListingScope{}
	}
	return agentregistry.ListingScope{
		Skills:    resolveListingPatterns(cfg.Skills, lookup),
		Subagents: resolveListingPatterns(cfg.Subagents, lookup),
	}
}

func resolveListingPatterns(patterns []string, lookup func(string) (any, bool)) []string {
	var out []string
	for _, raw := range patterns {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		resolved, ok := resolveListingPattern(raw, lookup)
		if !ok {
			return nil
		}
		out = append(out, resolved)
	}
	return out
}

func resolveListingPattern(pattern string, lookup func(string) (any, bool)) (string, bool) {
	ok := true
	resolved := listingTokenPattern.ReplaceAllStringFunc(pattern, func(token string) string {
		expr := strings.TrimSpace(listingTokenPattern.FindStringSubmatch(token)[1])
		path, isArg := strings.CutPrefix(expr, "args.")
		if !isArg || path == "" {
			if _, seen := listingTokenWarned.LoadOrStore(pattern, true); !seen {
				logging.Warn("listingScope pattern uses an unsupported token; only ${args.<path>} is resolved, so its list does not apply",
					"pattern", pattern)
			}
			ok = false
			return token
		}
		value, found := lookup(path)
		if !found {
			ok = false
			return token
		}
		s, scalar := listingScalar(value)
		if !scalar || s == "" {
			ok = false
			return token
		}
		return escapeGlob(s)
	})
	return resolved, ok
}

func listingScalar(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t), true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32), true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(t), true
	default:
		return "", false
	}
}

// escapeGlob makes a run value match literally inside a doublestar pattern.
func escapeGlob(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case '*', '?', '[', ']', '{', '}', '\\':
			sb.WriteRune('\\')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// InListingScope reports whether an item at location is listed in full under
// patterns. An item with no file location, or one outside the working
// directory, is installation-level rather than per-team and is always listed
// in full: patterns are workspace-relative and could never reach it.
func InListingScope(patterns []string, location string) bool {
	return inListingScope(config.WorkingDirectory(), patterns, location)
}

func inListingScope(workingDir string, patterns []string, location string) bool {
	if location == "" {
		return true
	}
	rel, ok := workspaceRelativePath(workingDir, location)
	if !ok {
		return true
	}
	for _, p := range patterns {
		p = strings.TrimPrefix(filepath.ToSlash(p), "./")
		if matched, err := doublestar.Match(p, rel); err == nil && matched {
			return true
		}
	}
	return false
}

// workspaceRelativePath returns location relative to workingDir with forward
// slashes, or false when it lies outside the working directory.
func workspaceRelativePath(workingDir, location string) (string, bool) {
	if !filepath.IsAbs(location) {
		location = filepath.Join(workingDir, location)
	}
	rel, err := filepath.Rel(workingDir, location)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}
