package vim

import "strconv"

// TransitionContext extends OperatorContext with undo and dot-repeat callbacks.
type TransitionContext struct {
	OperatorContext
	OnUndo      func()
	OnDotRepeat func()
}

// TransitionResult is the result of a state transition.
type TransitionResult struct {
	Next    CommandState // nil means no state change
	Execute func()       // nil means no action
}

// Transition dispatches based on current state type.
func Transition(state CommandState, input string, ctx *TransitionContext) TransitionResult {
	switch s := state.(type) {
	case CommandIdle:
		return fromIdle(input, ctx)
	case CommandCount:
		return fromCount(s, input, ctx)
	case CommandOperator:
		return fromOperator(s, input, ctx)
	case CommandOperatorCount:
		return fromOperatorCount(s, input, ctx)
	case CommandOperatorFind:
		return fromOperatorFind(s, input, ctx)
	case CommandOperatorTextObj:
		return fromOperatorTextObj(s, input, ctx)
	case CommandFind:
		return fromFind(s, input, ctx)
	case CommandG:
		return fromG(s, input, ctx)
	case CommandOperatorG:
		return fromOperatorG(s, input, ctx)
	case CommandReplace:
		return fromReplace(s, input, ctx)
	case CommandIndent:
		return fromIndent(s, input, ctx)
	default:
		return TransitionResult{}
	}
}

// handleNormalInput handles input valid in both idle and count states.
func handleNormalInput(input string, count int, ctx *TransitionContext) *TransitionResult {
	if op, ok := IsOperatorKey(input); ok {
		return &TransitionResult{Next: CommandOperator{Op: op, Count: count}}
	}

	if IsSimpleMotion(input) {
		return &TransitionResult{Execute: func() {
			target := ResolveMotion(input, ctx.Text, ctx.Offset, count)
			ctx.SetOffset(target)
		}}
	}

	if ft, ok := IsFindKey(input); ok {
		return &TransitionResult{Next: CommandFind{Find: ft, Count: count}}
	}

	if input == "g" {
		return &TransitionResult{Next: CommandG{Count: count}}
	}
	if input == "r" {
		return &TransitionResult{Next: CommandReplace{Count: count}}
	}
	if input == ">" || input == "<" {
		return &TransitionResult{Next: CommandIndent{Dir: rune(input[0]), Count: count}}
	}
	if input == "~" {
		return &TransitionResult{Execute: func() { ExecuteToggleCase(count, &ctx.OperatorContext) }}
	}
	if input == "x" {
		return &TransitionResult{Execute: func() { ExecuteX(count, &ctx.OperatorContext) }}
	}
	if input == "J" {
		return &TransitionResult{Execute: func() { ExecuteJoin(count, &ctx.OperatorContext) }}
	}
	if input == "p" || input == "P" {
		after := input == "p"
		return &TransitionResult{Execute: func() { ExecutePaste(after, count, &ctx.OperatorContext) }}
	}
	if input == "D" {
		return &TransitionResult{Execute: func() { ExecuteOperatorMotion(OpDelete, "$", 1, &ctx.OperatorContext) }}
	}
	if input == "C" {
		return &TransitionResult{Execute: func() { ExecuteOperatorMotion(OpChange, "$", 1, &ctx.OperatorContext) }}
	}
	if input == "Y" {
		return &TransitionResult{Execute: func() { ExecuteLineOp(OpYank, count, &ctx.OperatorContext) }}
	}
	if input == "G" {
		return &TransitionResult{Execute: func() {
			if count == 1 {
				ctx.SetOffset(startOfLastLine(ctx.Text))
			} else {
				ctx.SetOffset(goToLine(ctx.Text, count))
			}
		}}
	}
	if input == "." {
		return &TransitionResult{Execute: func() {
			if ctx.OnDotRepeat != nil {
				ctx.OnDotRepeat()
			}
		}}
	}
	if input == ";" || input == "," {
		reverse := input == ","
		return &TransitionResult{Execute: func() { executeRepeatFind(reverse, count, ctx) }}
	}
	if input == "u" {
		return &TransitionResult{Execute: func() {
			if ctx.OnUndo != nil {
				ctx.OnUndo()
			}
		}}
	}
	if input == "i" {
		return &TransitionResult{Execute: func() { ctx.EnterInsert(ctx.Offset) }}
	}
	if input == "I" {
		return &TransitionResult{Execute: func() {
			ctx.EnterInsert(firstNonBlank(ctx.Text, ctx.Offset))
		}}
	}
	if input == "a" {
		return &TransitionResult{Execute: func() {
			newOffset := ctx.Offset
			if ctx.Offset < len(ctx.Text) {
				newOffset = ctx.Offset + 1
			}
			ctx.EnterInsert(newOffset)
		}}
	}
	if input == "A" {
		return &TransitionResult{Execute: func() {
			ctx.EnterInsert(endOfLine(ctx.Text, ctx.Offset) + 1)
		}}
	}
	if input == "o" {
		return &TransitionResult{Execute: func() { ExecuteOpenLine("below", &ctx.OperatorContext) }}
	}
	if input == "O" {
		return &TransitionResult{Execute: func() { ExecuteOpenLine("above", &ctx.OperatorContext) }}
	}

	return nil
}

// handleOperatorInput handles operator input (motion, find, text object scope).
func handleOperatorInput(op Operator, count int, input string, ctx *TransitionContext) *TransitionResult {
	if scope, ok := IsTextObjScopeKey(input); ok {
		return &TransitionResult{Next: CommandOperatorTextObj{Op: op, Count: count, Scope: scope}}
	}

	if ft, ok := IsFindKey(input); ok {
		return &TransitionResult{Next: CommandOperatorFind{Op: op, Count: count, Find: ft}}
	}

	if IsSimpleMotion(input) {
		return &TransitionResult{Execute: func() { ExecuteOperatorMotion(op, input, count, &ctx.OperatorContext) }}
	}

	if input == "G" {
		return &TransitionResult{Execute: func() { executeOperatorG(op, count, ctx) }}
	}

	if input == "g" {
		return &TransitionResult{Next: CommandOperatorG{Op: op, Count: count}}
	}

	return nil
}

// Per-state transition functions

func fromIdle(input string, ctx *TransitionContext) TransitionResult {
	if len(input) == 1 && input[0] >= '1' && input[0] <= '9' {
		return TransitionResult{Next: CommandCount{Digits: input}}
	}
	if input == "0" {
		return TransitionResult{Execute: func() {
			ctx.SetOffset(startOfLine(ctx.Text, ctx.Offset))
		}}
	}

	if r := handleNormalInput(input, 1, ctx); r != nil {
		return *r
	}

	return TransitionResult{}
}

func fromCount(state CommandCount, input string, ctx *TransitionContext) TransitionResult {
	if len(input) == 1 && input[0] >= '0' && input[0] <= '9' {
		newDigits := state.Digits + input
		count, _ := strconv.Atoi(newDigits)
		if count > MaxVimCount {
			count = MaxVimCount
		}
		return TransitionResult{Next: CommandCount{Digits: strconv.Itoa(count)}}
	}

	count, _ := strconv.Atoi(state.Digits)
	if r := handleNormalInput(input, count, ctx); r != nil {
		return *r
	}

	return TransitionResult{Next: CommandIdle{}}
}

func fromOperator(state CommandOperator, input string, ctx *TransitionContext) TransitionResult {
	// dd, cc, yy = line operation
	if len(input) == 1 && rune(input[0]) == rune(state.Op[0]) {
		return TransitionResult{Execute: func() { ExecuteLineOp(state.Op, state.Count, &ctx.OperatorContext) }}
	}

	if len(input) == 1 && input[0] >= '0' && input[0] <= '9' {
		return TransitionResult{Next: CommandOperatorCount{Op: state.Op, Count: state.Count, Digits: input}}
	}

	if r := handleOperatorInput(state.Op, state.Count, input, ctx); r != nil {
		return *r
	}

	return TransitionResult{Next: CommandIdle{}}
}

func fromOperatorCount(state CommandOperatorCount, input string, ctx *TransitionContext) TransitionResult {
	if len(input) == 1 && input[0] >= '0' && input[0] <= '9' {
		newDigits := state.Digits + input
		parsed, _ := strconv.Atoi(newDigits)
		if parsed > MaxVimCount {
			parsed = MaxVimCount
		}
		return TransitionResult{Next: CommandOperatorCount{Op: state.Op, Count: state.Count, Digits: strconv.Itoa(parsed)}}
	}

	motionCount, _ := strconv.Atoi(state.Digits)
	effectiveCount := state.Count * motionCount
	if r := handleOperatorInput(state.Op, effectiveCount, input, ctx); r != nil {
		return *r
	}

	return TransitionResult{Next: CommandIdle{}}
}

func fromOperatorFind(state CommandOperatorFind, input string, ctx *TransitionContext) TransitionResult {
	return TransitionResult{Execute: func() {
		ExecuteOperatorFind(state.Op, state.Find, input, state.Count, &ctx.OperatorContext)
	}}
}

func fromOperatorTextObj(state CommandOperatorTextObj, input string, ctx *TransitionContext) TransitionResult {
	if IsTextObjType(input) {
		return TransitionResult{Execute: func() {
			ExecuteOperatorTextObj(state.Op, state.Scope, input, state.Count, &ctx.OperatorContext)
		}}
	}
	return TransitionResult{Next: CommandIdle{}}
}

func fromFind(state CommandFind, input string, ctx *TransitionContext) TransitionResult {
	return TransitionResult{Execute: func() {
		result := findCharacter(ctx.Text, ctx.Offset, input, state.Find, state.Count)
		if result != -1 {
			ctx.SetOffset(result)
			ctx.SetLastFind(state.Find, input)
		}
	}}
}

func fromG(state CommandG, input string, ctx *TransitionContext) TransitionResult {
	if input == "j" || input == "k" {
		motion := "g" + input
		return TransitionResult{Execute: func() {
			target := ResolveMotion(motion, ctx.Text, ctx.Offset, state.Count)
			ctx.SetOffset(target)
		}}
	}
	if input == "g" {
		if state.Count > 1 {
			return TransitionResult{Execute: func() {
				ctx.SetOffset(goToLine(ctx.Text, state.Count))
			}}
		}
		return TransitionResult{Execute: func() {
			ctx.SetOffset(startOfFirstLine())
		}}
	}
	return TransitionResult{Next: CommandIdle{}}
}

func fromOperatorG(state CommandOperatorG, input string, ctx *TransitionContext) TransitionResult {
	if input == "j" || input == "k" {
		motion := "g" + input
		return TransitionResult{Execute: func() {
			ExecuteOperatorMotion(state.Op, motion, state.Count, &ctx.OperatorContext)
		}}
	}
	if input == "g" {
		return TransitionResult{Execute: func() {
			executeOperatorGg(state.Op, state.Count, ctx)
		}}
	}
	return TransitionResult{Next: CommandIdle{}}
}

func fromReplace(state CommandReplace, input string, ctx *TransitionContext) TransitionResult {
	if input == "" {
		return TransitionResult{Next: CommandIdle{}}
	}
	return TransitionResult{Execute: func() {
		ExecuteReplace(input, state.Count, &ctx.OperatorContext)
	}}
}

func fromIndent(state CommandIndent, input string, ctx *TransitionContext) TransitionResult {
	if len(input) == 1 && rune(input[0]) == state.Dir {
		return TransitionResult{Execute: func() {
			ExecuteIndent(state.Dir, state.Count, &ctx.OperatorContext)
		}}
	}
	return TransitionResult{Next: CommandIdle{}}
}

// executeRepeatFind repeats the last find command.
func executeRepeatFind(reverse bool, count int, ctx *TransitionContext) {
	lastFind := ctx.GetLastFind()
	if lastFind == nil {
		return
	}

	findType := lastFind.Type
	if reverse {
		switch findType {
		case FindF:
			findType = FindB
		case FindB:
			findType = FindF
		case FindT:
			findType = FindR
		case FindR:
			findType = FindT
		}
	}

	result := findCharacter(ctx.Text, ctx.Offset, lastFind.Char, findType, count)
	if result != -1 {
		ctx.SetOffset(result)
	}
}

// executeOperatorG executes an operator with G motion.
func executeOperatorG(op Operator, count int, ctx *TransitionContext) {
	var targetOffset int
	if count == 1 {
		targetOffset = startOfLastLine(ctx.Text)
	} else {
		targetOffset = goToLine(ctx.Text, count)
	}

	if targetOffset == ctx.Offset {
		return
	}

	from, to, linewise := getOperatorRange(ctx.Text, ctx.Offset, targetOffset, "G", op, count)
	applyOperator(op, from, to, &ctx.OperatorContext, linewise)
	ctx.RecordChange(RecordedChange{Type: "operator", Op: op, Motion: "G", Count: count})
}

// executeOperatorGg executes an operator with gg motion.
func executeOperatorGg(op Operator, count int, ctx *TransitionContext) {
	var targetOffset int
	if count == 1 {
		targetOffset = startOfFirstLine()
	} else {
		targetOffset = goToLine(ctx.Text, count)
	}

	if targetOffset == ctx.Offset {
		return
	}

	from, to, linewise := getOperatorRange(ctx.Text, ctx.Offset, targetOffset, "gg", op, count)
	applyOperator(op, from, to, &ctx.OperatorContext, linewise)
	ctx.RecordChange(RecordedChange{Type: "operator", Op: op, Motion: "gg", Count: count})
}
