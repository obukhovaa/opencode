package vim

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ResolveMotion resolves a motion key to a new cursor offset.
// Pure function — no side effects.
func ResolveMotion(key string, text string, offset int, count int) int {
	result := offset
	for i := 0; i < count; i++ {
		next := applySingleMotion(key, text, result)
		if next == result {
			break
		}
		result = next
	}
	return result
}

func applySingleMotion(key string, text string, offset int) int {
	switch key {
	case "h":
		return moveLeft(text, offset)
	case "l":
		return moveRight(text, offset)
	case "j":
		return moveDown(text, offset)
	case "k":
		return moveUp(text, offset)
	case "w":
		return nextWord(text, offset, false)
	case "b":
		return prevWord(text, offset, false)
	case "e":
		return endOfWord(text, offset, false)
	case "W":
		return nextWord(text, offset, true)
	case "B":
		return prevWord(text, offset, true)
	case "E":
		return endOfWord(text, offset, true)
	case "0":
		return startOfLine(text, offset)
	case "^":
		return firstNonBlank(text, offset)
	case "$":
		return endOfLine(text, offset)
	default:
		return offset
	}
}

// IsInclusiveMotion returns true if the motion includes the character at the destination.
func IsInclusiveMotion(key string) bool {
	return key == "e" || key == "E" || key == "$"
}

// IsLinewiseMotion returns true if the motion operates on full lines.
func IsLinewiseMotion(key string) bool {
	return key == "j" || key == "k" || key == "G" || key == "gg"
}

// moveLeft moves cursor one character left, stopping at line boundary.
func moveLeft(text string, offset int) int {
	if offset <= 0 {
		return 0
	}
	// Don't cross newline backwards
	if offset > 0 && text[offset-1] == '\n' {
		return offset
	}
	return offset - 1
}

// moveRight moves cursor one character right, stopping at line boundary.
func moveRight(text string, offset int) int {
	if offset >= len(text)-1 {
		return max(0, len(text)-1)
	}
	// Don't move onto or past a newline
	if offset+1 < len(text) && text[offset+1] == '\n' {
		return offset
	}
	if offset < len(text) && text[offset] == '\n' {
		return offset
	}
	return offset + 1
}

// moveDown moves cursor down one logical line.
func moveDown(text string, offset int) int {
	line, col := offsetToLineCol(text, offset)
	lines := strings.Split(text, "\n")
	if line >= len(lines)-1 {
		return offset
	}
	nextLine := line + 1
	nextLineLen := utf8.RuneCountInString(lines[nextLine])
	targetCol := min(col, max(0, nextLineLen-1))
	if nextLineLen == 0 {
		targetCol = 0
	}
	return lineColToOffset(text, nextLine, targetCol)
}

// moveUp moves cursor up one logical line.
func moveUp(text string, offset int) int {
	line, col := offsetToLineCol(text, offset)
	if line <= 0 {
		return offset
	}
	lines := strings.Split(text, "\n")
	prevLine := line - 1
	prevLineLen := utf8.RuneCountInString(lines[prevLine])
	targetCol := min(col, max(0, prevLineLen-1))
	if prevLineLen == 0 {
		targetCol = 0
	}
	return lineColToOffset(text, prevLine, targetCol)
}

// nextWord moves to the start of the next word.
func nextWord(text string, offset int, bigWord bool) int {
	if offset >= len(text) {
		return offset
	}

	pos := offset
	n := len(text)

	// Skip current token
	if bigWord {
		// WORD: skip non-whitespace
		for pos < n && !isWhitespace(rune(text[pos])) {
			pos++
		}
	} else {
		ch := rune(text[pos])
		if isWordChar(ch) {
			for pos < n && isWordChar(rune(text[pos])) {
				pos++
			}
		} else if isPunctuation(ch) {
			for pos < n && isPunctuation(rune(text[pos])) {
				pos++
			}
		} else {
			pos++
		}
	}

	// Skip whitespace (including newlines)
	for pos < n && isWhitespace(rune(text[pos])) {
		pos++
	}

	if pos >= n {
		return max(0, n-1)
	}
	return pos
}

// prevWord moves to the start of the previous word.
func prevWord(text string, offset int, bigWord bool) int {
	if offset <= 0 {
		return 0
	}
	pos := offset

	// Skip whitespace backwards
	for pos > 0 && isWhitespace(rune(text[pos-1])) {
		pos--
	}

	if pos == 0 {
		return 0
	}

	// Find start of word
	if bigWord {
		for pos > 0 && !isWhitespace(rune(text[pos-1])) {
			pos--
		}
	} else {
		ch := rune(text[pos-1])
		if isWordChar(ch) {
			for pos > 0 && isWordChar(rune(text[pos-1])) {
				pos--
			}
		} else if isPunctuation(ch) {
			for pos > 0 && isPunctuation(rune(text[pos-1])) {
				pos--
			}
		} else {
			pos--
		}
	}

	return pos
}

// endOfWord moves to the end of the current/next word.
func endOfWord(text string, offset int, bigWord bool) int {
	n := len(text)
	if offset >= n-1 {
		return max(0, n-1)
	}
	pos := offset + 1

	// Skip whitespace
	for pos < n && isWhitespace(rune(text[pos])) {
		pos++
	}
	if pos >= n {
		return n - 1
	}

	// Find end of word
	if bigWord {
		for pos < n-1 && !isWhitespace(rune(text[pos+1])) {
			pos++
		}
	} else {
		ch := rune(text[pos])
		if isWordChar(ch) {
			for pos < n-1 && isWordChar(rune(text[pos+1])) {
				pos++
			}
		} else if isPunctuation(ch) {
			for pos < n-1 && isPunctuation(rune(text[pos+1])) {
				pos++
			}
		}
	}

	return pos
}

// startOfLine returns the offset of the first character on the current line.
func startOfLine(text string, offset int) int {
	if offset <= 0 || len(text) == 0 {
		return 0
	}
	idx := strings.LastIndex(text[:offset], "\n")
	if idx == -1 {
		return 0
	}
	return idx + 1
}

// firstNonBlank returns the offset of the first non-blank character on the current line.
func firstNonBlank(text string, offset int) int {
	lineStart := startOfLine(text, offset)
	pos := lineStart
	for pos < len(text) && text[pos] != '\n' && isWhitespace(rune(text[pos])) {
		pos++
	}
	if pos < len(text) && text[pos] == '\n' {
		return lineStart
	}
	return pos
}

// endOfLine returns the offset of the last character on the current line.
func endOfLine(text string, offset int) int {
	if len(text) == 0 {
		return 0
	}
	idx := strings.Index(text[offset:], "\n")
	if idx == -1 {
		return max(0, len(text)-1)
	}
	endPos := offset + idx
	if endPos == offset {
		return offset // on the newline itself
	}
	return endPos - 1
}

// startOfFirstLine returns offset 0.
func startOfFirstLine() int {
	return 0
}

// startOfLastLine returns the offset of the first character on the last line.
func startOfLastLine(text string) int {
	idx := strings.LastIndex(text, "\n")
	if idx == -1 {
		return 0
	}
	return idx + 1
}

// goToLine returns the offset of the start of the given line (1-indexed).
func goToLine(text string, lineNum int) int {
	lines := strings.Split(text, "\n")
	targetLine := min(lineNum-1, len(lines)-1)
	targetLine = max(0, targetLine)
	offset := 0
	for i := 0; i < targetLine; i++ {
		offset += len(lines[i]) + 1
	}
	return offset
}

// findCharacter finds a character on the current line using f/F/t/T.
// Returns -1 if not found.
func findCharacter(text string, offset int, char string, findType FindType, count int) int {
	if len(text) == 0 || len(char) == 0 {
		return -1
	}

	lineStart := startOfLine(text, offset)
	lineEndIdx := strings.Index(text[offset:], "\n")
	lineEnd := len(text)
	if lineEndIdx != -1 {
		lineEnd = offset + lineEndIdx
	}

	ch := char[0]
	found := 0

	switch findType {
	case FindF: // f - find forward
		for i := offset + 1; i < lineEnd; i++ {
			if text[i] == ch {
				found++
				if found == count {
					return i
				}
			}
		}
	case FindB: // F - find backward
		for i := offset - 1; i >= lineStart; i-- {
			if text[i] == ch {
				found++
				if found == count {
					return i
				}
			}
		}
	case FindT: // t - to forward (one before)
		for i := offset + 1; i < lineEnd; i++ {
			if text[i] == ch {
				found++
				if found == count {
					if i-1 > offset {
						return i - 1
					}
					return offset
				}
			}
		}
	case FindR: // T - to backward (one after)
		for i := offset - 1; i >= lineStart; i-- {
			if text[i] == ch {
				found++
				if found == count {
					if i+1 < offset {
						return i + 1
					}
					return offset
				}
			}
		}
	}

	return -1
}

// offsetToLineCol converts a byte offset to (line, col) (both 0-indexed).
// offsetToLineCol converts a byte offset to a (line, column) pair.
//
// The column counts RUNES, not bytes, because the only consumers of a column
// here are the textarea's cursor accessors and they index runes: bubbles stores
// a line as []rune, so Column() returns a rune index and SetCursorColumn clamps
// against a rune count. Returning a byte column made every non-ASCII draft
// misbehave — the cursor landed elsewhere than it was drawn, and a visual
// operator then acted on the wrong bytes, splitting characters.
func offsetToLineCol(text string, offset int) (int, int) {
	if offset <= 0 || len(text) == 0 {
		return 0, 0
	}
	offset = min(offset, len(text))
	before := text[:offset]
	line := strings.Count(before, "\n")
	if lastNL := strings.LastIndex(before, "\n"); lastNL >= 0 {
		before = before[lastNL+1:]
	}
	return line, utf8.RuneCountInString(before)
}

// lineColToOffset converts a (line, rune column) pair to a byte offset. The
// column is measured in runes — see offsetToLineCol for why.
//
// A column past the end of its line clamps to the line's end rather than
// spilling into the next one; every caller either takes the column straight
// from the textarea (which cannot exceed its line) or clamps it first.
func lineColToOffset(text string, line, col int) int {
	offset := 0
	for i := 0; i < line; i++ {
		idx := strings.Index(text[offset:], "\n")
		if idx == -1 {
			return len(text)
		}
		offset += idx + 1
	}

	rest := text[offset:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	for range col {
		if rest == "" {
			break
		}
		_, size := utf8.DecodeRuneInString(rest)
		rest = rest[size:]
		offset += size
	}
	return min(offset, len(text))
}

// getLineStartOffset calculates the byte offset of the start of a given line.
func getLineStartOffset(lines []string, lineIndex int) int {
	offset := 0
	for i := 0; i < lineIndex; i++ {
		offset += len(lines[i]) + 1
	}
	return offset
}

// Character classification for vim word motions
func isWordChar(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_'
}

func isWhitespace(ch rune) bool {
	return unicode.IsSpace(ch)
}

func isPunctuation(ch rune) bool {
	return !isWordChar(ch) && !isWhitespace(ch)
}
