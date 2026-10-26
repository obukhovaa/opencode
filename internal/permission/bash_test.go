package permission

import (
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func TestBashPermissionChecksEveryExecutablePart(t *testing.T) {
	const allowed = "python3 /workspace/scripts/report.py"
	rules := map[string]any{"bash": map[string]any{
		"*": "deny", "python3 /workspace/scripts/*": "allow",
		"approved-ask *": "ask",
	}}
	cases := []struct {
		name, command string
		want          Action
	}{
		{"simple", allowed, ActionAllow},
		{"semicolon", allowed + "; forbidden", ActionDeny},
		{"and", allowed + " && forbidden", ActionDeny},
		{"or", allowed + " || forbidden", ActionDeny},
		{"pipe", allowed + " | forbidden", ActionDeny},
		{"pipe stderr", allowed + " |& forbidden", ActionDeny},
		{"background", allowed + " & forbidden", ActionDeny},
		{"newline", allowed + "\nforbidden", ActionDeny},
		{"subshell", allowed + "; (forbidden)", ActionDeny},
		{"block", allowed + "; { forbidden; }", ActionDeny},
		{"dollar substitution", allowed + " $(forbidden)", ActionDeny},
		{"double quoted substitution", allowed + ` "$(forbidden)"`, ActionDeny},
		{"backticks", allowed + " `forbidden`", ActionDeny},
		{"nested substitution", allowed + " $(python3 /workspace/scripts/other.py $(forbidden))", ActionDeny},
		{"input process substitution", allowed + " <(forbidden)", ActionDeny},
		{"output process substitution", allowed + " >(forbidden)", ActionDeny},
		{"redirect substitution", allowed + " >$(forbidden)", ActionDeny},
		{"heredoc substitution", allowed + " <<EOF\n$(forbidden)\nEOF\n", ActionDeny},
		{"unreached branch", allowed + "; if false; then forbidden; fi", ActionDeny},
		{"function body", allowed + "; f() { forbidden; }; f", ActionDeny},
		{"declaration", allowed + "; export X=1", ActionDeny},
		{"assignment substitution", allowed + "; X=$(forbidden)", ActionDeny},
		{"ask component", allowed + "; approved-ask arg", ActionAsk},
		{"deny beats ask", allowed + "; approved-ask arg; forbidden", ActionDeny},
		{"all allowed", allowed + "; python3 /workspace/scripts/other.py", ActionAllow},
		{"all allowed pipeline", allowed + " | python3 /workspace/scripts/other.py", ActionAllow},
		{"allowed substitution", allowed + " $(python3 /workspace/scripts/other.py)", ActionAllow},
		{"quoted operators", allowed + ` '; && || | $(forbidden) ` + "`forbidden`" + `'`, ActionAllow},
		{"double quoted operators", allowed + ` "; && || |"`, ActionAllow},
		{"escaped substitution", allowed + ` \$(forbidden)`, ActionDeny}, // invalid Bash, never prefix-allow
		{"literal dollar", allowed + ` '\$(forbidden)'`, ActionAllow},
		{"quoted heredoc", allowed + " <<'EOF'\n$(forbidden)\nEOF\n", ActionAllow},
		{"comment", allowed + " # ; forbidden", ActionAllow},
		{"malformed", allowed + ` "unterminated`, ActionDeny},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateBashPermission(tt.command, func(part string) Action {
				return EvaluateToolPermission("bash", part, rules, nil)
			})
			if got != tt.want {
				t.Fatalf("EvaluateBashPermission(%q) = %s, want %s", tt.command, got, tt.want)
			}
		})
	}
}

func TestBashPermissionRetainsWholeCommandDecision(t *testing.T) {
	for _, action := range []Action{ActionDeny, ActionAsk} {
		got := EvaluateBashPermission("first; second", func(part string) Action {
			if part == "first; second" {
				return action
			}
			return ActionAllow
		})
		if got != action {
			t.Fatalf("whole-command %s became %s", action, got)
		}
	}
}

// Use an allow-by-default policy here so a denied wrapper/condition cannot
// accidentally make a traversal test pass before reaching the nested command.
func TestBashPermissionFindsDeniedCommandsInContainers(t *testing.T) {
	rules := map[string]any{"bash": map[string]any{"*": "allow", "forbidden*": "deny"}}
	for _, command := range []string{
		"approved; (forbidden)",
		"approved; { forbidden; }",
		"approved; if true; then forbidden; fi",
		"approved; while false; do forbidden; done",
		"approved; for x in a; do forbidden; done",
		"approved; case x in x) forbidden;; esac",
		"approved; f() { forbidden; }; f",
		"approved; ! forbidden",
		"approved $(forbidden)",
		"approved `forbidden`",
		"approved <(forbidden)",
		"approved > >(forbidden)",
		"approved; export X=$(forbidden)",
		"approved; ((x = $(forbidden)))",
	} {
		t.Run(command, func(t *testing.T) {
			got := EvaluateBashPermission(command, func(part string) Action {
				return EvaluateToolPermission("bash", part, rules, nil)
			})
			if got != ActionDeny {
				t.Fatalf("nested forbidden command was %s", got)
			}
		})
	}
}

// Bash runs code while expanding some words: arithmetic and subscripts
// evaluate a variable's value, ${x@P} expands it as a prompt. A command
// stored by ${x:=…} in the same allowed statement then runs although no
// node of the tree shows it, so an allow rule must not grant such a call.
func TestBashPermissionRefusesHiddenExecution(t *testing.T) {
	denyAll := map[string]any{"bash": map[string]any{"*": "deny", "printf allowed*": "allow"}}
	allowAll := map[string]any{"bash": map[string]any{"*": "allow", "forbidden*": "deny"}}
	noCatchAll := map[string]any{"bash": map[string]any{"printf allowed*": "allow"}}
	hidden := []string{
		`printf allowed ${x:=a[\$\(touch MK\)]} $((x))`,
		`printf allowed ${x:=a[\$\(touch MK\)]} ${!x}`,
		`printf allowed ${x:=a[\$\(touch MK\)]} ${y[x]}`,
		`printf allowed ${x:=\$\(touch MK\)} ${x@P}`,
		`printf allowed ${x=\$\(touch MK\)}`,
		`printf allowed $(( $x + 1 ))`,
		`printf allowed ${v:x:1}`,
		`printf allowed ${a["k"]}`,
		`printf allowed; ((x))`,
		`printf allowed; let x`,
		`printf allowed; [[ x -eq 1 ]]`,
		`printf allowed; for ((i = x; i < 1; i++)); do printf allowed; done`,
		`printf allowed; a[x]=1`,
	}
	for _, command := range hidden {
		t.Run(command, func(t *testing.T) {
			for _, tc := range []struct {
				name  string
				rules map[string]any
				want  Action
			}{
				{"deny all", denyAll, ActionDeny},
				{"no catch-all", noCatchAll, ActionAsk},
				{"allow all", allowAll, ActionAllow},
			} {
				got := EvaluateBashPermission(command, func(part string) Action {
					return EvaluateToolPermission("bash", part, tc.rules, nil)
				})
				if got != tc.want {
					t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
				}
			}
		})
	}
	// Expansions that run nothing keep the allow rule's grant.
	safe := `printf allowed $((1 + 2)) ${a[0]} ${a[@]} ${#x} ${x:-d} ${x/a/b} ${x@Q} ${v:1:-1} "$x" ${!prefix*}`
	if got := EvaluateBashPermission(safe, func(part string) Action {
		return EvaluateToolPermission("bash", part, denyAll, nil)
	}); got != ActionAllow {
		t.Errorf("%q: got %s, want allow", safe, got)
	}
	for command, want := range map[string]bool{
		`[[ 3 -gt -1 ]]`: false,
		`[[ $x == y ]]`:  false,
		`[[ $x -gt 1 ]]`: true,
		// Reads i arithmetically; telling that i was just set to a literal
		// would need symbolic evaluation, so the loop is refused too.
		`for ((i = 0; i < 3; i++)); do :; done`: true,
	} {
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
		if err != nil {
			t.Fatal(err)
		}
		if got := BashHidesExecution(file); got != want {
			t.Errorf("BashHidesExecution(%q) = %t, want %t", command, got, want)
		}
	}
}

// Forms the CD-5483 review exercised by hand; pinned as regression guards.
func TestBashPermissionReviewedForms(t *testing.T) {
	rules := map[string]any{"bash": map[string]any{"*": "deny", "allowed*": "allow"}}
	for _, command := range []string{
		`allowed <<< "$(forbidden)"`,
		`X=$(forbidden) allowed`,
		`allowed; eval forbidden`,
		`allowed; bash -c 'forbidden'`,
	} {
		got := EvaluateBashPermission(command, func(part string) Action {
			return EvaluateToolPermission("bash", part, rules, nil)
		})
		if got != ActionDeny {
			t.Errorf("%q: got %s, want deny", command, got)
		}
	}
}
