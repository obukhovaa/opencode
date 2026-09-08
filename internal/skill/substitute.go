package skill

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// SubstituteParams holds context for skill content substitution.
type SubstituteParams struct {
	Args      string
	SkillDir  string
	SessionID string
	// SuppressArgsAppend disables step 6 (appending "ARGUMENTS: <value>" when
	// the content declared no placeholder). Callers set it when the arguments
	// were already consumed by a placeholder the caller substituted itself —
	// named $FOO placeholders in custom commands — so the values are not
	// restated at the end of the prompt.
	SuppressArgsAppend bool
}

var (
	// $ARGUMENTS[N] — positional argument by index (0-based)
	indexedArgPattern = regexp.MustCompile(`\$ARGUMENTS\[(\d+)\]`)
	// $N — shorthand positional, single digit only (0-9), with word boundary
	// to avoid matching dollar amounts like $50 or $100.
	shorthandArgPattern = regexp.MustCompile(`\$(\d)\b`)
)

// HasArgumentPatterns reports whether content contains $ARGUMENTS, $ARGUMENTS[N], or $N patterns.
func HasArgumentPatterns(content string) bool {
	return strings.Contains(content, "$ARGUMENTS") || shorthandArgPattern.MatchString(content)
}

// ExtractPositionalIndices returns sorted unique positional indices from $N and $ARGUMENTS[N] patterns.
func ExtractPositionalIndices(content string) []int {
	seen := make(map[int]bool)

	for _, m := range indexedArgPattern.FindAllStringSubmatch(content, -1) {
		if idx, err := strconv.Atoi(m[1]); err == nil {
			seen[idx] = true
		}
	}

	for _, m := range shorthandArgPattern.FindAllStringSubmatch(content, -1) {
		if idx, err := strconv.Atoi(m[1]); err == nil {
			seen[idx] = true
		}
	}

	indices := make([]int, 0, len(seen))
	for idx := range seen {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	return indices
}

// SubstituteContent replaces variables in skill content with actual values.
// Substitution order:
//  1. ${SKILL_DIR} / ${CLAUDE_SKILL_DIR}
//  2. ${SESSION_ID} / ${CLAUDE_SESSION_ID}
//  3. $ARGUMENTS[N] — positional args
//  4. $ARGUMENTS — full args string
//  5. $N — shorthand positional
//  6. If $ARGUMENTS was absent and args are non-empty, append "ARGUMENTS: <value>"
func SubstituteContent(content string, params SubstituteParams) string {
	hadArguments := strings.Contains(content, "$ARGUMENTS") || shorthandArgPattern.MatchString(content)

	// 1. Skill directory
	content = strings.ReplaceAll(content, "${SKILL_DIR}", params.SkillDir)
	content = strings.ReplaceAll(content, "${CLAUDE_SKILL_DIR}", params.SkillDir)

	// 2. Session ID
	content = strings.ReplaceAll(content, "${SESSION_ID}", params.SessionID)
	content = strings.ReplaceAll(content, "${CLAUDE_SESSION_ID}", params.SessionID)

	positional := SplitArgs(params.Args)

	// 3. $ARGUMENTS[N]
	content = indexedArgPattern.ReplaceAllStringFunc(content, func(match string) string {
		subs := indexedArgPattern.FindStringSubmatch(match)
		if len(subs) < 2 {
			return match
		}
		idx, err := strconv.Atoi(subs[1])
		if err != nil || idx < 0 || idx >= len(positional) {
			return ""
		}
		return positional[idx]
	})

	// 4. $ARGUMENTS (bare, not followed by '[')
	content = strings.ReplaceAll(content, "$ARGUMENTS", params.Args)

	// 5. $N shorthand
	content = shorthandArgPattern.ReplaceAllStringFunc(content, func(match string) string {
		subs := shorthandArgPattern.FindStringSubmatch(match)
		if len(subs) < 2 {
			return match
		}
		idx, err := strconv.Atoi(subs[1])
		if err != nil || idx < 0 || idx >= len(positional) {
			return ""
		}
		return positional[idx]
	})

	// 6. Append if $ARGUMENTS was not present
	if !hadArguments && !params.SuppressArgsAppend && params.Args != "" {
		content = fmt.Sprintf("%s\n\nARGUMENTS: %s", content, params.Args)
	}

	return content
}

// SplitArgs splits an argument string into positional arguments, honouring
// single and double quotes so a value containing spaces stays one argument.
// Inside double quotes a backslash escapes the next character; single quotes are
// literal throughout. An unbalanced quote is a parse failure and degrades to a
// plain whitespace split rather than erroring — malformed input must still
// produce usable positionals.
//
// SplitArgs is the inverse of QuoteArg: SplitArgs(QuoteArgs(values))
// returns values unchanged.
func SplitArgs(args string) []string {
	if args == "" {
		return nil
	}

	var (
		out     []string
		cur     strings.Builder
		started bool // cur holds a token, even if it is the empty string ("")
	)

	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}

	runes := []rune(args)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '"':
			started = true
			i++
			closed := false
			for ; i < len(runes); i++ {
				if runes[i] == '\\' && i+1 < len(runes) {
					i++
					cur.WriteRune(runes[i])
					continue
				}
				if runes[i] == '"' {
					closed = true
					break
				}
				cur.WriteRune(runes[i])
			}
			if !closed {
				return strings.Fields(args)
			}
		case c == '\'':
			started = true
			i++
			closed := false
			for ; i < len(runes); i++ {
				if runes[i] == '\'' {
					closed = true
					break
				}
				cur.WriteRune(runes[i])
			}
			if !closed {
				return strings.Fields(args)
			}
		case unicode.IsSpace(c):
			flush()
		default:
			started = true
			cur.WriteRune(c)
		}
	}
	flush()

	if len(out) == 0 {
		return nil
	}
	return out
}

// QuoteArg renders one positional value so that SplitArgs recovers it verbatim.
// Values that are empty or carry whitespace, a quote, or a backslash are wrapped
// in double quotes with the escapable characters escaped; anything else is
// returned unchanged so ordinary arguments stay readable in the editor.
func QuoteArg(value string) string {
	if value == "" {
		return `""`
	}
	if !strings.ContainsAny(value, " \t\n\r\"'\\") {
		return value
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// QuoteArgs joins values into an argument string that SplitArgs round-trips.
func QuoteArgs(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = QuoteArg(v)
	}
	return strings.Join(quoted, " ")
}
