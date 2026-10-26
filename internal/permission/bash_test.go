package permission

import "testing"

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
		{"here-string substitution", allowed + ` <<< "$(forbidden)"`, ActionDeny},
		{"env-prefix substitution", "X=$(forbidden) " + allowed, ActionDeny},
		{"eval suffix", allowed + "; eval forbidden", ActionDeny},
		{"shell suffix", allowed + "; bash -c 'forbidden'", ActionDeny},
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

func TestBashPermissionRejectsReevaluatedExpansions(t *testing.T) {
	rules := map[string]any{"bash": map[string]any{"*": "deny", "printf allowed*": "allow"}}
	for _, command := range []string{
		`printf allowed ${x:=a[\$\(touch MK\)]} $((x))`,
		`printf allowed ${x:=a[\$\(touch MK\)]} ${!x}`,
		`printf allowed ${x:=a[\$\(touch MK\)]} ${y[x]}`,
		`printf allowed ${x:=\$\(touch MK\)} ${x@P}`,
		`printf allowed ${x=1}`,
		`printf allowed $((x))`,
		`printf allowed $[x]`,
		`printf allowed ${!x}`,
		`printf allowed ${y[x]}`,
		`printf allowed ${x@P}`,
		`printf allowed ${x:offset:len}`,
		`printf allowed; ((x))`,
		`printf allowed; let x`,
		`printf allowed; [[ x -eq 1 ]]`,
		`printf allowed; [[ -v 'a[x]' ]]`,
		`printf allowed; a[x]=value`,
		`printf allowed; a=([x]=value)`,
		`printf allowed; for ((i=0; i<1; i++)); do printf allowed; done`,
	} {
		t.Run(command, func(t *testing.T) {
			// Policy-only fixture: no shell or touch command runs, so this
			// detects the Bash 5.x bypass even on macOS's Bash 3.2.
			for name, policy := range map[string]map[string]any{
				"prefix allow": rules,
				"allow all":    {"bash": "allow"},
			} {
				got := EvaluateBashPermission(command, func(part string) Action {
					return EvaluateToolPermission("bash", part, policy, nil)
				})
				if got != ActionDeny {
					t.Fatalf("%s: uninspectable expansion was %s", name, got)
				}
			}
		})
	}
}

func TestBashPermissionAllowsInspectablyLiteralExpansions(t *testing.T) {
	for _, command := range []string{
		`printf allowed $((1 + 2 * (3 - 1)))`,
		`printf allowed $[1+2]`,
		`printf allowed ${x[0]}`,
		`printf allowed ${x[@]}`,
		`printf allowed ${x[*]}`,
		`printf allowed ${x:1:2}`,
		`printf allowed ${x:-fallback}`,
		`printf allowed ${x@Q}`,
		`printf allowed '${x:=literal} ${x@P} $((x))'`,
	} {
		t.Run(command, func(t *testing.T) {
			got := EvaluateBashPermission(command, func(string) Action { return ActionAllow })
			if got != ActionAllow {
				t.Fatalf("literal/data-only expansion was %s", got)
			}
		})
	}
}
