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
	if action == ActionAllow && BashHidesExecution(file) {
		// The tree shows every command, but not code bash runs while
		// expanding a word (see BashHidesExecution). A rule that allowed a
		// part by its text vouches for what it shows, so it cannot vouch
		// here: fall back to the decision for a command no specific rule
		// matches. "*": allow agents keep allow; "*": deny agents deny.
		action = stricter(action, evaluate(catchAllProbe))
	}
	return action
}

// catchAllProbe is a command no realistic rule other than "*" matches, so
// evaluating it yields the agent's catch-all decision.
const catchAllProbe = "\x00"

func stricter(a, b Action) Action {
	switch {
	case a == ActionDeny || b == ActionDeny:
		return ActionDeny
	case a == ActionAllow && b == ActionAllow:
		return ActionAllow
	}
	return ActionAsk
}

// BashHidesExecution reports whether node contains an expansion through
// which bash can run a command that never appears as a node in the syntax
// tree. A command string stored in a variable runs when bash evaluates it
// arithmetically (`$((x))`, `((x))`, `let`, `[[ x -eq y ]]`, an array
// subscript `${a[x]}` or `a[x]=`, a slice `${v:x:y}`, an indirection
// `${!x}`) or as a prompt (`${x@P}`); an assigning expansion (`${x:=…}`)
// stores one within the same statement. Arithmetic over numeric literals,
// plain `$x` expansions and the other `${x@…}` transforms run nothing.
func BashHidesExecution(node syntax.Node) bool {
	hides := false
	syntax.Walk(node, func(n syntax.Node) bool {
		if hides {
			return false
		}
		switch n := n.(type) {
		case *syntax.ParamExp:
			hides = (n.Excl && n.Names == 0) ||
				(n.Index != nil && arithmReadsVariable(n.Index)) ||
				(n.Slice != nil && (arithmReadsVariable(n.Slice.Offset) || arithmReadsVariable(n.Slice.Length))) ||
				(n.Exp != nil && hidingParamOp(n.Exp))
		case *syntax.ArithmExp:
			hides = arithmReadsVariable(n.X)
		case *syntax.ArithmCmd:
			hides = arithmReadsVariable(n.X)
		case *syntax.LetClause:
			for _, expr := range n.Exprs {
				hides = hides || arithmReadsVariable(expr)
			}
		case *syntax.Assign:
			hides = n.Index != nil && arithmReadsVariable(n.Index)
		case *syntax.ArrayElem:
			hides = n.Index != nil && arithmReadsVariable(n.Index)
		case *syntax.CStyleLoop:
			hides = arithmReadsVariable(n.Init) || arithmReadsVariable(n.Cond) || arithmReadsVariable(n.Post)
		case *syntax.BinaryTest:
			if n.Op >= syntax.TsEql && n.Op <= syntax.TsGtr {
				hides = !numericTestOperand(n.X) || !numericTestOperand(n.Y)
			}
		}
		return !hides
	})
	return hides
}

func hidingParamOp(exp *syntax.Expansion) bool {
	switch exp.Op {
	case syntax.AssignUnset, syntax.AssignUnsetOrNull:
		return true
	case syntax.OtherParamOps:
		// ${x@P} expands x as a prompt string, which runs $(…) in it.
		lit, ok := literalWord(exp.Word)
		return !ok || lit == "P"
	}
	return false
}

// arithmReadsVariable reports whether an arithmetic expression refers to
// anything but numeric literals: bash evaluates a variable's value as an
// arithmetic expression in turn, and a subscript inside it runs $(…).
func arithmReadsVariable(expr syntax.ArithmExpr) bool {
	switch e := expr.(type) {
	case nil:
		return false
	case *syntax.Word:
		lit, ok := literalWord(e)
		return !ok || !(lit == "@" || lit == "*" || (lit != "" && lit[0] >= '0' && lit[0] <= '9'))
	case *syntax.BinaryArithm:
		return arithmReadsVariable(e.X) || arithmReadsVariable(e.Y)
	case *syntax.UnaryArithm:
		return arithmReadsVariable(e.X)
	case *syntax.ParenArithm:
		return arithmReadsVariable(e.X)
	}
	return true
}

func numericTestOperand(expr syntax.TestExpr) bool {
	word, ok := expr.(*syntax.Word)
	if !ok {
		return false
	}
	lit, ok := literalWord(word)
	lit = strings.TrimPrefix(lit, "-")
	return ok && lit != "" && lit[0] >= '0' && lit[0] <= '9'
}

// literalWord returns the text of a word made of a single unquoted literal.
func literalWord(word *syntax.Word) (string, bool) {
	if word == nil || len(word.Parts) != 1 {
		return "", false
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	if !ok {
		return "", false
	}
	return lit.Value, true
}
