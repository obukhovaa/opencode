package hooks

import (
	"strings"
	"testing"
)

func TestCompileMatcher(t *testing.T) {
	cases := []struct {
		name       string
		matcher    string
		shouldFire []string // tool names that MUST match
		shouldSkip []string // tool names that MUST NOT match
		wantErr    string   // substring of error msg; empty = expect success
	}{
		{
			// Empty matcher is documented as "match everything" — Claude
			// Code parity. Tools the plugin doesn't care about still get
			// the event delivered; it's the plugin's job to filter.
			name:       "empty_matches_all",
			matcher:    "",
			shouldFire: []string{"Bash", "Edit", "mcp__memory__create_entities"},
		},
		{
			name:       "wildcard_star_matches_all",
			matcher:    "*",
			shouldFire: []string{"Bash", "Edit", "Notebook"},
		},
		{
			// Lowercase canonical form (matches opencode's tool-name convention).
			name:       "exact_lowercase",
			matcher:    "bash",
			shouldFire: []string{"bash"},
			shouldSkip: []string{"edit", "bashcommand"},
		},
		{
			// PascalCase from a Claude Code config still matches —
			// case-insensitive comparison aligns opencode (`bash`)
			// with Claude Code (`Bash`) so copy-paste works.
			name:       "exact_pascalcase_matches_lowercase_tool",
			matcher:    "Bash",
			shouldFire: []string{"bash"},
			shouldSkip: []string{"edit"},
		},
		{
			name:       "pipe_list_lowercase",
			matcher:    "edit|write",
			shouldFire: []string{"edit", "write"},
			shouldSkip: []string{"bash", "read"},
		},
		{
			name:       "pipe_list_pascalcase_matches_lowercase_tools",
			matcher:    "Edit|Write",
			shouldFire: []string{"edit", "write"},
			shouldSkip: []string{"bash", "read"},
		},
		{
			name:       "comma_list_with_whitespace",
			matcher:    "edit, write , multiedit",
			shouldFire: []string{"edit", "write", "multiedit"},
			shouldSkip: []string{"editwrite"},
		},
		{
			// Regex branch — anything with chars outside [A-Za-z0-9_, |]
			// switches to RE2. Anchoring is the author's responsibility.
			// In opencode, MCP tool names are `<server>_<tool>` (single
			// underscore), NOT Claude Code's `mcp__<server>__<tool>`
			// double-underscore convention. Authors matching all MCP
			// tools should use a pattern keyed off the configured
			// server names (e.g. `^(memory|filesystem)_`) since opencode
			// doesn't add a universal `mcp` prefix.
			name:       "regex_matches_opencode_mcp_form",
			matcher:    "^memory_",
			shouldFire: []string{"memory_create_entities", "memory_read_graph"},
			shouldSkip: []string{"bash", "edit", "filesystem_read"},
		},
		{
			name:       "regex_alternation_via_parens",
			matcher:    "^(bash|edit)$",
			shouldFire: []string{"bash", "edit"},
			shouldSkip: []string{"bashlike", "editor"},
		},
		{
			// Regex stays CASE-SENSITIVE by default. If a Claude Code
			// regex matcher uses PascalCase and the operator wants it
			// to match opencode's lowercase tool names, they add the
			// `(?i)` flag inline. Without (?i), uppercase regex does
			// NOT match lowercase tool names.
			name:       "regex_is_case_sensitive_without_flag",
			matcher:    "^Bash$",
			shouldFire: []string{"Bash"}, // hypothetical PascalCase tool, won't exist in practice
			shouldSkip: []string{"bash"}, // canonical opencode name; regex doesn't fold case
		},
		{
			// Operators can opt into case-insensitive regex with the
			// standard Go RE2 inline flag — preserves the escape hatch
			// for Claude Code configs that author regex in PascalCase.
			name:       "regex_caseinsensitive_flag_matches_lowercase",
			matcher:    "(?i)^Bash$",
			shouldFire: []string{"bash", "Bash", "BASH"},
			shouldSkip: []string{"edit"},
		},
		{
			// RE2 doesn't support lookahead. Lookbehind syntax `(?<...)`
			// fails to compile in Go's regexp package; the matcher
			// constructor surfaces the error rather than silently
			// degrading to a permissive match.
			name:    "regex_lookbehind_fails",
			matcher: "(?<=foo)bar",
			wantErr: "invalid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := CompileMatcher(tc.matcher)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CompileMatcher(%q) returned unexpected error: %v", tc.matcher, err)
			}
			for _, name := range tc.shouldFire {
				if !m(name) {
					t.Errorf("matcher %q: expected to match %q but did not", tc.matcher, name)
				}
			}
			for _, name := range tc.shouldSkip {
				if m(name) {
					t.Errorf("matcher %q: expected NOT to match %q but did", tc.matcher, name)
				}
			}
		})
	}
}
