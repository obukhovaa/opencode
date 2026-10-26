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
		if reevaluatesShellCode(node) {
			// Bash can interpret variable contents again as arithmetic or
			// prompt syntax. Those commands have no child Stmt to inspect.
			action = ActionDeny
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

// reevaluatesShellCode refuses expansion contexts whose execution cannot be
// discovered from the source AST alone. In particular, escaped command text
// stored by ${x:=...} can execute later through $((x)), ${!x}, ${a[x]} or @P
// in the SAME allowed statement (CD-5483 review regression).
func reevaluatesShellCode(node syntax.Node) bool {
	switch n := node.(type) {
	case *syntax.ParamExp:
		if n.Excl || !literalArrayIndex(n.Index) {
			return true
		}
		if n.Slice != nil && (!literalArithmetic(n.Slice.Offset) || !literalArithmetic(n.Slice.Length)) {
			return true
		}
		if n.Exp != nil {
			switch n.Exp.Op {
			case syntax.AssignUnset, syntax.AssignUnsetOrNull:
				return true
			case syntax.OtherParamOps:
				if n.Exp.Word == nil {
					return true
				}
				// Known data-only transforms; @P and unknown transforms are
				// not evidence of a command-free expansion.
				switch n.Exp.Word.Lit() {
				case "Q", "E", "A", "K", "a", "k", "U", "u", "L":
				default:
					return true
				}
			}
		}
	case *syntax.ArithmExp:
		return !literalArithmetic(n.X)
	case *syntax.ArithmCmd:
		return !literalArithmetic(n.X)
	case *syntax.LetClause:
		for _, x := range n.Exprs {
			if !literalArithmetic(x) {
				return true
			}
		}
	case *syntax.CStyleLoop:
		return !literalArithmetic(n.Init) || !literalArithmetic(n.Cond) || !literalArithmetic(n.Post)
	case *syntax.Assign:
		return !literalArithmetic(n.Index)
	case *syntax.ArrayElem:
		return !literalArithmetic(n.Index)
	case *syntax.BinaryTest:
		switch n.Op {
		case syntax.TsEql, syntax.TsNeq, syntax.TsLeq, syntax.TsGeq, syntax.TsLss, syntax.TsGtr:
			return !literalArithmetic(n.X) || !literalArithmetic(n.Y)
		}
	case *syntax.UnaryTest:
		// -v/-R accept variable addresses, including subscript/nameref
		// forms that can cause another round of arithmetic evaluation.
		return n.Op == syntax.TsVarSet || n.Op == syntax.TsRefVar
	}
	return false
}

func literalArrayIndex(node syntax.ArithmExpr) bool {
	if w, ok := node.(*syntax.Word); ok && (w.Lit() == "@" || w.Lit() == "*") {
		return true // array enumeration, not an arithmetic variable
	}
	return literalArithmetic(node)
}

// Only decimal constants and operators over constants are inspectable without
// consulting mutable shell state. Reject names, expansions and quoted words.
func literalArithmetic(node syntax.Node) bool {
	switch n := node.(type) {
	case nil:
		return true
	case *syntax.Word:
		s := n.Lit()
		return s != "" && strings.Trim(s, "0123456789") == ""
	case *syntax.BinaryArithm:
		return literalArithmetic(n.X) && literalArithmetic(n.Y)
	case *syntax.UnaryArithm:
		return literalArithmetic(n.X)
	case *syntax.ParenArithm:
		return literalArithmetic(n.X)
	}
	return false
}
