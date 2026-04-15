package vim

import "strings"

// TextObjectRange represents the boundaries of a text object.
// Start is inclusive, End is exclusive.
type TextObjectRange struct {
	Start int
	End   int
}

// Delimiter pairs for text objects.
var pairs = map[string][2]string{
	"(":  {"(", ")"},
	")":  {"(", ")"},
	"b":  {"(", ")"},
	"[":  {"[", "]"},
	"]":  {"[", "]"},
	"{":  {"{", "}"},
	"}":  {"{", "}"},
	"B":  {"{", "}"},
	"<":  {"<", ">"},
	">":  {"<", ">"},
	"\"": {"\"", "\""},
	"'":  {"'", "'"},
	"`":  {"`", "`"},
}

// FindTextObject finds a text object at the given position.
// Returns nil if no text object found.
func FindTextObject(text string, offset int, objType string, isInner bool) *TextObjectRange {
	if objType == "w" {
		return findWordObject(text, offset, isInner, isWordChar)
	}
	if objType == "W" {
		return findWordObject(text, offset, isInner, func(ch rune) bool {
			return !isWhitespace(ch)
		})
	}

	pair, ok := pairs[objType]
	if !ok {
		return nil
	}

	open, close := pair[0], pair[1]
	if open == close {
		return findQuoteObject(text, offset, open, isInner)
	}
	return findBracketObject(text, offset, open, close, isInner)
}

func findWordObject(text string, offset int, isInner bool, isWord func(rune) bool) *TextObjectRange {
	if offset >= len(text) {
		return nil
	}

	ch := rune(text[offset])
	start := offset
	end := offset

	if isWord(ch) {
		for start > 0 && isWord(rune(text[start-1])) {
			start--
		}
		for end < len(text) && isWord(rune(text[end])) {
			end++
		}
	} else if isWhitespace(ch) {
		for start > 0 && isWhitespace(rune(text[start-1])) {
			start--
		}
		for end < len(text) && isWhitespace(rune(text[end])) {
			end++
		}
		return &TextObjectRange{Start: start, End: end}
	} else {
		// Punctuation
		for start > 0 && isPunctuation(rune(text[start-1])) {
			start--
		}
		for end < len(text) && isPunctuation(rune(text[end])) {
			end++
		}
	}

	if !isInner {
		// Include surrounding whitespace
		if end < len(text) && isWhitespace(rune(text[end])) {
			for end < len(text) && isWhitespace(rune(text[end])) {
				end++
			}
		} else if start > 0 && isWhitespace(rune(text[start-1])) {
			for start > 0 && isWhitespace(rune(text[start-1])) {
				start--
			}
		}
	}

	return &TextObjectRange{Start: start, End: end}
}

func findQuoteObject(text string, offset int, quote string, isInner bool) *TextObjectRange {
	lineStart := strings.LastIndex(text[:offset], "\n") + 1
	lineEndIdx := strings.Index(text[offset:], "\n")
	lineEnd := len(text)
	if lineEndIdx != -1 {
		lineEnd = offset + lineEndIdx
	}
	line := text[lineStart:lineEnd]
	posInLine := offset - lineStart

	// Find all quote positions in the line
	var positions []int
	q := quote[0]
	for i := 0; i < len(line); i++ {
		if line[i] == q {
			positions = append(positions, i)
		}
	}

	// Pair quotes: 0-1, 2-3, 4-5, etc.
	for i := 0; i < len(positions)-1; i += 2 {
		qs := positions[i]
		qe := positions[i+1]
		if qs <= posInLine && posInLine <= qe {
			if isInner {
				return &TextObjectRange{Start: lineStart + qs + 1, End: lineStart + qe}
			}
			return &TextObjectRange{Start: lineStart + qs, End: lineStart + qe + 1}
		}
	}

	return nil
}

func findBracketObject(text string, offset int, open, close string, isInner bool) *TextObjectRange {
	openCh := open[0]
	closeCh := close[0]

	depth := 0
	start := -1

	// Search backward for matching open bracket
	for i := offset; i >= 0; i-- {
		if text[i] == closeCh && i != offset {
			depth++
		} else if text[i] == openCh {
			if depth == 0 {
				start = i
				break
			}
			depth--
		}
	}
	if start == -1 {
		return nil
	}

	// Search forward for matching close bracket
	depth = 0
	end := -1
	for i := start + 1; i < len(text); i++ {
		if text[i] == openCh {
			depth++
		} else if text[i] == closeCh {
			if depth == 0 {
				end = i
				break
			}
			depth--
		}
	}
	if end == -1 {
		return nil
	}

	if isInner {
		return &TextObjectRange{Start: start + 1, End: end}
	}
	return &TextObjectRange{Start: start, End: end + 1}
}
