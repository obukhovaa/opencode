package vim

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SelectionRange resolves a visual selection to a [from, to) byte range over
// text.
//
// Charwise selections are inclusive of the character under the cursor — that is
// what makes `vd` on a single character delete it — so the range extends one
// rune past the later end. Linewise selections cover every line either end
// touches, in full, including the trailing newline when there is one.
func SelectionRange(text string, anchor, cursor int, linewise bool) (from, to int) {
	from = clampOffset(text, min(anchor, cursor))
	to = clampOffset(text, max(anchor, cursor))

	if linewise {
		from = startOfLine(text, from)
		if nl := strings.IndexByte(text[to:], '\n'); nl == -1 {
			to = len(text)
		} else {
			to += nl + 1
		}
		return from, to
	}

	if to < len(text) {
		// Advance by a whole rune: a byte-wise increment would split a
		// multi-byte character and corrupt the text on delete.
		_, size := utf8.DecodeRuneInString(text[to:])
		to += size
	}
	return from, to
}

func clampOffset(text string, offset int) int {
	if offset < 0 {
		return 0
	}
	if offset > len(text) {
		return len(text)
	}
	return offset
}

// ExecuteVisualOperator applies d/c/y to a resolved selection.
//
// It delegates to applyOperator — the same function the operator-motion and
// text-object paths use — so visual mode inherits register semantics, cursor
// placement and change handling rather than reimplementing them.
func ExecuteVisualOperator(op Operator, from, to int, linewise bool, ctx *OperatorContext) {
	if from >= to {
		return
	}
	applyOperator(op, from, to, ctx, linewise)
	ctx.RecordChange(RecordedChange{
		Type:     "visual",
		Op:       op,
		Span:     to - from,
		Linewise: linewise,
	})
}

// ExecuteVisualCase applies ~ / u / U to a selection. mode is 'u' to lowercase,
// 'U' to uppercase, and '~' to toggle.
func ExecuteVisualCase(mode rune, from, to int, ctx *OperatorContext) {
	if from >= to {
		return
	}
	var b strings.Builder
	b.Grow(to - from)
	for _, r := range ctx.Text[from:to] {
		switch mode {
		case 'u':
			b.WriteRune(unicode.ToLower(r))
		case 'U':
			b.WriteRune(unicode.ToUpper(r))
		default:
			switch {
			case unicode.IsUpper(r):
				b.WriteRune(unicode.ToLower(r))
			case unicode.IsLower(r):
				b.WriteRune(unicode.ToUpper(r))
			default:
				b.WriteRune(r)
			}
		}
	}
	ctx.SetText(ctx.Text[:from] + b.String() + ctx.Text[to:])
	ctx.SetOffset(from)
	ctx.RecordChange(RecordedChange{Type: "visual", Op: Operator("case-" + string(mode)), Span: to - from})
}

// ExecuteVisualIndent applies > or < to every line the selection touches.
func ExecuteVisualIndent(dir rune, from, to int, ctx *OperatorContext) {
	text := ctx.Text
	firstLine, _ := offsetToLineCol(text, from)
	lastLine, _ := offsetToLineCol(text, max(from, to-1))

	lines := strings.Split(text, "\n")
	const indent = "  "
	for i := firstLine; i <= lastLine && i < len(lines); i++ {
		switch {
		case dir == '>':
			lines[i] = indent + lines[i]
		case strings.HasPrefix(lines[i], indent):
			lines[i] = lines[i][len(indent):]
		case strings.HasPrefix(lines[i], "\t"):
			lines[i] = lines[i][1:]
		default:
			lines[i] = strings.TrimLeft(lines[i], " ")
		}
	}

	newText := strings.Join(lines, "\n")
	ctx.SetText(newText)
	// Vim leaves the cursor on the first non-blank of the first affected line.
	lineStart := getLineStartOffset(lines, firstLine)
	ctx.SetOffset(lineStart + len(lines[firstLine]) - len(strings.TrimLeft(lines[firstLine], " \t")))
	ctx.RecordChange(RecordedChange{Type: "visual", Op: Operator("indent-" + string(dir)), Span: to - from})
}

// ExecuteVisualJoin joins every line the selection touches into one.
func ExecuteVisualJoin(from, to int, ctx *OperatorContext) {
	text := ctx.Text
	firstLine, _ := offsetToLineCol(text, from)
	lastLine, _ := offsetToLineCol(text, max(from, to-1))
	if lastLine <= firstLine {
		// A selection within one line joins it with the next, matching what J
		// does in NORMAL mode.
		lastLine = firstLine + 1
	}

	lines := strings.Split(text, "\n")
	if firstLine >= len(lines) {
		return
	}
	lastLine = min(lastLine, len(lines)-1)
	if lastLine <= firstLine {
		return
	}

	joined := lines[firstLine]
	for i := firstLine + 1; i <= lastLine; i++ {
		next := strings.TrimLeft(lines[i], " \t")
		if joined != "" && next != "" {
			joined += " "
		}
		joined += next
	}

	merged := append([]string{}, lines[:firstLine]...)
	merged = append(merged, joined)
	merged = append(merged, lines[lastLine+1:]...)

	newText := strings.Join(merged, "\n")
	ctx.SetText(newText)
	ctx.SetOffset(min(from, max(0, len(newText)-1)))
	ctx.RecordChange(RecordedChange{Type: "visual", Op: Operator("join"), Span: to - from})
}

// ExecuteVisualPaste replaces the selection with the register's contents. The
// selection's own text becomes the new register content, which is vim's
// behavior and is what makes a visual paste swappable.
func ExecuteVisualPaste(from, to int, ctx *OperatorContext) {
	if from >= to {
		return
	}
	content, linewise := ctx.GetRegister()
	replaced := ctx.Text[from:to]

	if content == "" {
		// Nothing to paste: deleting the selection anyway would silently lose
		// text the user cannot get back from an empty register.
		return
	}
	if linewise && !strings.HasPrefix(content, "\n") {
		content = "\n" + strings.TrimSuffix(content, "\n")
	}

	newText := ctx.Text[:from] + content + ctx.Text[to:]
	ctx.SetText(newText)
	ctx.SetRegister(replaced, false)
	ctx.SetOffset(min(from+len(content)-1, max(0, len(newText)-1)))
	ctx.RecordChange(RecordedChange{Type: "visual", Op: Operator("paste"), Span: to - from})
}
