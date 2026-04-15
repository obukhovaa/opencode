package vim

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// OperatorContext provides the interface for operator execution.
type OperatorContext struct {
	Text         string
	Offset       int
	SetText      func(string)
	SetOffset    func(int)
	EnterInsert  func(int)
	GetRegister  func() (string, bool) // content, linewise
	SetRegister  func(string, bool)
	GetLastFind  func() *FindRecord
	SetLastFind  func(FindType, string)
	RecordChange func(RecordedChange)
}

// ExecuteOperatorMotion executes an operator with a simple motion.
func ExecuteOperatorMotion(op Operator, motion string, count int, ctx *OperatorContext) {
	target := ResolveMotion(motion, ctx.Text, ctx.Offset, count)
	if target == ctx.Offset {
		return
	}

	from, to, linewise := getOperatorRange(ctx.Text, ctx.Offset, target, motion, op, count)
	applyOperator(op, from, to, ctx, linewise)
	ctx.RecordChange(RecordedChange{Type: "operator", Op: op, Motion: motion, Count: count})
}

// ExecuteOperatorFind executes an operator with a find motion.
func ExecuteOperatorFind(op Operator, findType FindType, char string, count int, ctx *OperatorContext) {
	targetOffset := findCharacter(ctx.Text, ctx.Offset, char, findType, count)
	if targetOffset == -1 {
		return
	}

	from, to := getOperatorRangeForFind(ctx.Offset, targetOffset)
	applyOperator(op, from, to, ctx, false)
	ctx.SetLastFind(findType, char)
	ctx.RecordChange(RecordedChange{Type: "operatorFind", Op: op, Find: findType, Char: char, Count: count})
}

// ExecuteOperatorTextObj executes an operator with a text object.
func ExecuteOperatorTextObj(op Operator, scope TextObjScope, objType string, count int, ctx *OperatorContext) {
	r := FindTextObject(ctx.Text, ctx.Offset, objType, scope == ScopeInner)
	if r == nil {
		return
	}
	applyOperator(op, r.Start, r.End, ctx, false)
	ctx.RecordChange(RecordedChange{Type: "operatorTextObj", Op: op, ObjType: objType, Scope: scope, Count: count})
}

// ExecuteLineOp executes a line operation (dd, cc, yy).
func ExecuteLineOp(op Operator, count int, ctx *OperatorContext) {
	text := ctx.Text
	lines := strings.Split(text, "\n")
	currentLine := strings.Count(text[:min(ctx.Offset, len(text))], "\n")
	linesToAffect := min(count, len(lines)-currentLine)
	lineStart := startOfLine(text, ctx.Offset)

	lineEnd := lineStart
	for i := 0; i < linesToAffect; i++ {
		idx := strings.Index(text[lineEnd:], "\n")
		if idx == -1 {
			lineEnd = len(text)
		} else {
			lineEnd += idx + 1
		}
	}

	content := text[lineStart:lineEnd]
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	ctx.SetRegister(content, true)

	switch op {
	case OpYank:
		ctx.SetOffset(lineStart)
	case OpDelete:
		deleteStart := lineStart
		deleteEnd := lineEnd

		if deleteEnd == len(text) && deleteStart > 0 && text[deleteStart-1] == '\n' {
			deleteStart--
		}

		newText := text[:deleteStart] + text[deleteEnd:]
		ctx.SetText(newText)
		maxOff := max(0, len(newText)-1)
		ctx.SetOffset(min(deleteStart, maxOff))
	case OpChange:
		if len(lines) == 1 {
			ctx.SetText("")
			ctx.EnterInsert(0)
		} else {
			beforeLines := lines[:currentLine]
			afterLines := lines[currentLine+linesToAffect:]
			parts := make([]string, 0, len(beforeLines)+1+len(afterLines))
			parts = append(parts, beforeLines...)
			parts = append(parts, "")
			parts = append(parts, afterLines...)
			newText := strings.Join(parts, "\n")
			ctx.SetText(newText)
			ctx.EnterInsert(lineStart)
		}
	}

	ctx.RecordChange(RecordedChange{Type: "operator", Op: op, Motion: string(op[0]), Count: count})
}

// ExecuteX executes the x command (delete character under cursor).
func ExecuteX(count int, ctx *OperatorContext) {
	from := ctx.Offset
	if from >= len(ctx.Text) {
		return
	}

	to := from
	for i := 0; i < count && to < len(ctx.Text); i++ {
		_, size := utf8.DecodeRuneInString(ctx.Text[to:])
		to += size
	}

	deleted := ctx.Text[from:to]
	newText := ctx.Text[:from] + ctx.Text[to:]
	ctx.SetRegister(deleted, false)
	ctx.SetText(newText)
	maxOff := max(0, len(newText)-1)
	ctx.SetOffset(min(from, maxOff))
	ctx.RecordChange(RecordedChange{Type: "x", Count: count})
}

// ExecuteReplace executes the r command (replace character under cursor).
func ExecuteReplace(char string, count int, ctx *OperatorContext) {
	offset := ctx.Offset
	newText := ctx.Text

	for i := 0; i < count && offset < len(newText); i++ {
		_, size := utf8.DecodeRuneInString(newText[offset:])
		newText = newText[:offset] + char + newText[offset+size:]
		offset += len(char)
	}

	ctx.SetText(newText)
	ctx.SetOffset(max(0, offset-len(char)))
	ctx.RecordChange(RecordedChange{Type: "replace", Char: char, Count: count})
}

// ExecuteToggleCase executes the ~ command.
func ExecuteToggleCase(count int, ctx *OperatorContext) {
	startOffset := ctx.Offset
	if startOffset >= len(ctx.Text) {
		return
	}

	newText := ctx.Text
	offset := startOffset
	toggled := 0

	for offset < len(newText) && toggled < count {
		r, size := utf8.DecodeRuneInString(newText[offset:])
		if r == utf8.RuneError && size <= 1 {
			offset += size
			toggled++
			continue
		}

		var toggled_r rune
		if unicode.IsUpper(r) {
			toggled_r = unicode.ToLower(r)
		} else if unicode.IsLower(r) {
			toggled_r = unicode.ToUpper(r)
		} else {
			toggled_r = r
		}

		if toggled_r != r {
			replacement := string(toggled_r)
			newText = newText[:offset] + replacement + newText[offset+size:]
			offset += len(replacement)
		} else {
			offset += size
		}
		toggled++
	}

	ctx.SetText(newText)
	ctx.SetOffset(offset)
	ctx.RecordChange(RecordedChange{Type: "toggleCase", Count: count})
}

// ExecuteJoin executes the J command (join lines).
func ExecuteJoin(count int, ctx *OperatorContext) {
	text := ctx.Text
	lines := strings.Split(text, "\n")
	currentLine, _ := offsetToLineCol(text, ctx.Offset)

	if currentLine >= len(lines)-1 {
		return
	}

	linesToJoin := min(count, len(lines)-currentLine-1)
	joinedLine := lines[currentLine]
	cursorPos := len(joinedLine)

	for i := 1; i <= linesToJoin; i++ {
		nextLine := strings.TrimLeftFunc(lines[currentLine+i], unicode.IsSpace)
		if len(nextLine) > 0 {
			if !strings.HasSuffix(joinedLine, " ") && len(joinedLine) > 0 {
				joinedLine += " "
			}
			joinedLine += nextLine
		}
	}

	newLines := make([]string, 0, len(lines)-linesToJoin)
	newLines = append(newLines, lines[:currentLine]...)
	newLines = append(newLines, joinedLine)
	newLines = append(newLines, lines[currentLine+linesToJoin+1:]...)

	newText := strings.Join(newLines, "\n")
	ctx.SetText(newText)
	ctx.SetOffset(getLineStartOffset(newLines, currentLine) + cursorPos)
	ctx.RecordChange(RecordedChange{Type: "join", Count: count})
}

// ExecutePaste executes the p/P command.
func ExecutePaste(after bool, count int, ctx *OperatorContext) {
	register, linewise := ctx.GetRegister()
	if register == "" {
		return
	}

	if linewise {
		content := strings.TrimSuffix(register, "\n")
		text := ctx.Text
		lines := strings.Split(text, "\n")
		currentLine, _ := offsetToLineCol(text, ctx.Offset)

		insertLine := currentLine
		if after {
			insertLine = currentLine + 1
		}

		contentLines := strings.Split(content, "\n")
		var repeatedLines []string
		for i := 0; i < count; i++ {
			repeatedLines = append(repeatedLines, contentLines...)
		}

		newLines := make([]string, 0, len(lines)+len(repeatedLines))
		newLines = append(newLines, lines[:insertLine]...)
		newLines = append(newLines, repeatedLines...)
		newLines = append(newLines, lines[insertLine:]...)

		newText := strings.Join(newLines, "\n")
		ctx.SetText(newText)
		ctx.SetOffset(getLineStartOffset(newLines, insertLine))
	} else {
		textToInsert := strings.Repeat(register, count)
		insertPoint := ctx.Offset
		if after && ctx.Offset < len(ctx.Text) {
			insertPoint = ctx.Offset + 1
		}

		newText := ctx.Text[:insertPoint] + textToInsert + ctx.Text[insertPoint:]
		newOffset := insertPoint + len(textToInsert) - 1
		ctx.SetText(newText)
		ctx.SetOffset(max(insertPoint, newOffset))
	}
}

// ExecuteIndent executes >> or << (indent/unindent).
func ExecuteIndent(dir rune, count int, ctx *OperatorContext) {
	text := ctx.Text
	lines := strings.Split(text, "\n")
	currentLine, _ := offsetToLineCol(text, ctx.Offset)
	linesToAffect := min(count, len(lines)-currentLine)
	indent := "  " // two spaces

	for i := range linesToAffect {
		lineIdx := currentLine + i
		line := lines[lineIdx]

		if dir == '>' {
			lines[lineIdx] = indent + line
		} else if strings.HasPrefix(line, indent) {
			lines[lineIdx] = line[len(indent):]
		} else if strings.HasPrefix(line, "\t") {
			lines[lineIdx] = line[1:]
		} else {
			removed := 0
			idx := 0
			for idx < len(line) && removed < len(indent) && isWhitespace(rune(line[idx])) {
				removed++
				idx++
			}
			lines[lineIdx] = line[idx:]
		}
	}

	newText := strings.Join(lines, "\n")
	currentLineText := lines[currentLine]
	firstNB := len(currentLineText) - len(strings.TrimLeft(currentLineText, " \t"))

	ctx.SetText(newText)
	ctx.SetOffset(getLineStartOffset(lines, currentLine) + firstNB)
	ctx.RecordChange(RecordedChange{Type: "indent", Dir: dir, Count: count})
}

// ExecuteOpenLine executes o/O (open line below/above).
func ExecuteOpenLine(direction string, ctx *OperatorContext) {
	text := ctx.Text
	lines := strings.Split(text, "\n")
	currentLine, _ := offsetToLineCol(text, ctx.Offset)

	insertLine := currentLine + 1
	if direction == "above" {
		insertLine = currentLine
	}

	newLines := make([]string, 0, len(lines)+1)
	newLines = append(newLines, lines[:insertLine]...)
	newLines = append(newLines, "")
	newLines = append(newLines, lines[insertLine:]...)

	newText := strings.Join(newLines, "\n")
	ctx.SetText(newText)
	ctx.EnterInsert(getLineStartOffset(newLines, insertLine))
	ctx.RecordChange(RecordedChange{Type: "openLine", Direction: direction})
}

// Internal helpers

func getOperatorRange(text string, cursorOffset, targetOffset int, motion string, op Operator, count int) (from, to int, linewise bool) {
	from = min(cursorOffset, targetOffset)
	to = max(cursorOffset, targetOffset)

	// Special case: cw/cW changes to end of word, not start of next word
	if op == OpChange && (motion == "w" || motion == "W") {
		wordOffset := cursorOffset
		for i := 0; i < count-1; i++ {
			wordOffset = nextWord(text, wordOffset, motion == "W")
		}
		wordEnd := endOfWord(text, wordOffset, motion == "W")
		to = wordEnd + 1
	} else if IsLinewiseMotion(motion) {
		linewise = true
		nextNewline := strings.Index(text[to:], "\n")
		if nextNewline == -1 {
			to = len(text)
			if from > 0 && text[from-1] == '\n' {
				from--
			}
		} else {
			to += nextNewline + 1
		}
	} else if IsInclusiveMotion(motion) && cursorOffset <= targetOffset {
		if to < len(text) {
			to++
		}
	}

	return from, to, linewise
}

func getOperatorRangeForFind(cursorOffset, targetOffset int) (from, to int) {
	from = min(cursorOffset, targetOffset)
	to = max(cursorOffset, targetOffset) + 1
	return from, to
}

func applyOperator(op Operator, from, to int, ctx *OperatorContext, linewise bool) {
	content := ctx.Text[from:to]
	if linewise && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	ctx.SetRegister(content, linewise)

	switch op {
	case OpYank:
		ctx.SetOffset(from)
	case OpDelete:
		newText := ctx.Text[:from] + ctx.Text[to:]
		ctx.SetText(newText)
		maxOff := max(0, len(newText)-1)
		ctx.SetOffset(min(from, maxOff))
	case OpChange:
		newText := ctx.Text[:from] + ctx.Text[to:]
		ctx.SetText(newText)
		ctx.EnterInsert(from)
	}
}
