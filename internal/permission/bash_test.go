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
