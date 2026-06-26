package hooks

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Matcher reports whether a hook should fire for the given tool name.
// Always-fire matchers (empty / `*`) return true unconditionally.
type Matcher func(toolName string) bool

// simpleMatcherChars detects the "list of names" form. If the entire
// matcher consists only of these characters, we treat it as exact-match
// or pipe/comma-separated list — NOT a regex. Mirrors Claude Code's
// content-detection rule so authors can paste matchers across hosts.
var simpleMatcherChars = regexp.MustCompile(`^[A-Za-z0-9_, |]+$`)

// compiledMatchers caches compile-once results so the per-event hot path
// (matchEntries → CompileMatcher) doesn't re-compile the same regex
// pattern on every tool call. Cache key is the raw matcher string from
// settings.json; compiles are deterministic so no invalidation is needed.
//
// Memory bound: the cache holds at most one entry per distinct matcher
// string in the user's config — typically a single-digit number. The
// cache survives for the process lifetime, matching the rest of the
// configuration's "loaded at startup, restart to change" contract.
type matcherCacheEntry struct {
	m   Matcher
	err error
}

var compiledMatchers sync.Map // map[string]matcherCacheEntry

// CompileMatcher turns a raw matcher string from `.opencode.json` into a
// callable predicate. The detection rules (D5):
//   - "", "*", or whitespace-only → match all
//   - only [A-Za-z0-9_, |] → exact name or `|`/`,`-separated list,
//     compared CASE-INSENSITIVELY so that a Claude Code config using
//     PascalCase (`Bash`, `Edit`) matches opencode's lowercase tool
//     names (`bash`, `edit`) without modification
//   - anything else → Go regex (RE2 — no lookahead/lookbehind);
//     regex is case-sensitive by default, authors who want
//     case-insensitive regex add the `(?i)` flag explicitly
//
// Returns an error only for invalid regex; the "simple" path can't fail.
//
// Compiled matchers are cached by raw string so the per-tool-call hot
// path doesn't re-pay the regex compile cost. A power user with several
// regex matchers in their config previously paid N × regexp.Compile per
// tool call (~5µs each); now the second and subsequent calls hit the
// cache for ~30ns total.
func CompileMatcher(raw string) (Matcher, error) {
	if v, ok := compiledMatchers.Load(raw); ok {
		entry := v.(matcherCacheEntry)
		return entry.m, entry.err
	}
	m, err := compileMatcherUncached(raw)
	compiledMatchers.Store(raw, matcherCacheEntry{m: m, err: err})
	return m, err
}

// compileMatcherUncached is the original implementation, retained so
// the cache wrapper above stays trivial and the rules are still in one
// readable function.
func compileMatcherUncached(raw string) (Matcher, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "*" {
		return func(string) bool { return true }, nil
	}
	if simpleMatcherChars.MatchString(trimmed) {
		// Split on either | or , — trim each, drop empties, lowercase.
		// opencode tool names are lowercase by convention (bash, edit,
		// write, …); a Claude Code config that writes "Bash" needs to
		// match without manual translation. We lowercase both sides at
		// compare time and treat the simple-list as case-insensitive.
		names := splitListMatcher(trimmed)
		set := make(map[string]struct{}, len(names))
		for _, n := range names {
			set[strings.ToLower(n)] = struct{}{}
		}
		return func(toolName string) bool {
			_, ok := set[strings.ToLower(toolName)]
			return ok
		}, nil
	}
	re, err := regexp.Compile(trimmed)
	if err != nil {
		return nil, fmt.Errorf("matcher %q: %w", raw, err)
	}
	return func(toolName string) bool { return re.MatchString(toolName) }, nil
}

func splitListMatcher(s string) []string {
	// Normalize: split on `|` first, then split each token on `,`. This
	// handles `Edit|Write`, `Edit, Write`, and the mixed `Edit|Write, Notebook`
	// uniformly. Each terminal name is trimmed of surrounding whitespace.
	var out []string
	for group := range strings.SplitSeq(s, "|") {
		for name := range strings.SplitSeq(group, ",") {
			n := strings.TrimSpace(name)
			if n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}
