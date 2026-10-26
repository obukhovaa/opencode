package permission

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// EvaluateBashPermission checks the complete invocation and every executable
// statement in its Bash syntax tree (CD-5483, bash-command-permissions:
// "Every executable part is checked before execution"). A prefix glob must
// never authorize another statement swallowed by its trailing wildcard.
// Parsing does not expand variables or execute shell code.
//
// Existing whole-command denies and prompts remain authoritative. Among all
// checks deny wins over ask, and ask wins over allow. Even unreachable branches
// and function bodies are checked: deciding which branch runs is the shell's
// job, after permission has been settled for the entire call.
func EvaluateBashPermission(command string, evaluate func(string) Action) Action {
	action := evaluate(command)
	if action == ActionDeny {
		return action
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		// An allow glob is not evidence that malformed/unsupported syntax
		// contains no hidden command. Never fall back to the prefix match.
		return ActionDeny
	}
	syntax.Walk(file, func(node syntax.Node) bool {
		if action == ActionDeny {
			return false
		}
		stmt, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}
		switch stmt.Cmd.(type) {
		case *syntax.Block, *syntax.Subshell, *syntax.BinaryCmd,
			*syntax.IfClause, *syntax.WhileClause, *syntax.ForClause, *syntax.CaseClause:
			// Structural containers: Walk visits their statements and every
			// command/process substitution, including those inside words.
			return true
		}
		start, end := stmt.Pos().Offset(), stmt.End().Offset()
		if stmt.Cmd != nil {
			// Exclude ! and the separator/background operator; neither is
			// part of the command whose permission must be checked.
			start, end = stmt.Cmd.Pos().Offset(), stmt.Cmd.End().Offset()
		}
		for _, redir := range stmt.Redirs {
			if pos := redir.End().Offset(); pos > end {
				end = pos
			}
		}
		part := strings.TrimSpace(command[start:end])
		if part == "" {
			return true
		}
		switch evaluate(part) {
		case ActionDeny:
			action = ActionDeny
		case ActionAsk, "":
			if action != ActionDeny {
				action = ActionAsk
			}
		}
		return action != ActionDeny
	})
	return action
}
