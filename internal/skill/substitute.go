package skill

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SubstituteParams holds context for skill content substitution.
type SubstituteParams struct {
	Args      string
	SkillDir  string
	SessionID string
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

	positional := splitArgs(params.Args)

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
	if !hadArguments && params.Args != "" {
		content = fmt.Sprintf("%s\n\nARGUMENTS: %s", content, params.Args)
	}

	return content
}

// splitArgs splits an argument string into positional arguments.
// Handles simple space-separated values.
func splitArgs(args string) []string {
	if args == "" {
		return nil
	}
	return strings.Fields(args)
}
