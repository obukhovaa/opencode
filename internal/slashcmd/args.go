package slashcmd

import (
	"regexp"
	"strings"
)

// namedArgPattern matches a named placeholder such as $TARGET. The name must
// start with an uppercase letter so shell/env references written in lowercase
// and dollar amounts ($50) are not mistaken for parameters.
var namedArgPattern = regexp.MustCompile(`\$([A-Z][A-Z0-9_]*)`)

// shellMarkupSpan matches an !`cmd` shell-markup span, mirroring the pattern in
// internal/llm/tools/shell. Named-placeholder handling skips these spans: a
// $HOME, $CI or $POD_NAME inside one is an environment reference belonging to
// the command line the author wrote, not a parameter for the user to fill.
// Without the exclusion, a body reading "Run !`ls $HOME` and summarize" would
// present HOME as an argument field, substitute the user's first word into the
// shell command, and — because a bound name suppresses the ARGUMENTS fallback —
// drop the rest of their arguments from the prompt entirely.
var shellMarkupSpan = regexp.MustCompile("!`[^`]+`")

// outsideShellMarkup rewrites each run of content that sits outside a
// shell-markup span with fn, leaving the spans themselves byte-identical.
func outsideShellMarkup(content string, fn func(string) string) string {
	spans := shellMarkupSpan.FindAllStringIndex(content, -1)
	if len(spans) == 0 {
		return fn(content)
	}

	var b strings.Builder
	last := 0
	for _, s := range spans {
		b.WriteString(fn(content[last:s[0]]))
		b.WriteString(content[s[0]:s[1]])
		last = s[1]
	}
	b.WriteString(fn(content[last:]))
	return b.String()
}

// argumentsPlaceholder is excluded from the named set: $ARGUMENTS has dedicated
// whole-string semantics (see skill.SubstituteContent) rather than binding to
// one positional slot.
const argumentsPlaceholder = "ARGUMENTS"

// NamedPlaceholders returns the named placeholders declared in content, in
// first-appearance order, deduplicated, excluding $ARGUMENTS.
//
// The order is the contract between three sites that must agree: the argument
// dialog presents its fields in this order, staged editor text lists the values
// in this order, and the expander binds names[i] to positional argument i.
func NamedPlaceholders(content string) []string {
	var names []string
	seen := make(map[string]bool)
	outsideShellMarkup(content, func(segment string) string {
		for _, m := range namedArgPattern.FindAllStringSubmatch(segment, -1) {
			name := m[1]
			if name == argumentsPlaceholder || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
		return segment
	})
	return names
}

// bindNamed substitutes each named placeholder with the positional argument at
// its index in names. A name with no corresponding positional argument, and any
// named placeholder not present in names at all, binds to the empty string —
// an unfilled parameter must not leak `$FOO` into the prompt. $ARGUMENTS is left
// untouched for skill.SubstituteContent to handle.
func bindNamed(content string, names []string, positional []string) string {
	if len(names) == 0 {
		return content
	}

	values := make(map[string]string, len(names))
	for i, name := range names {
		if i < len(positional) {
			values[name] = positional[i]
		} else {
			values[name] = ""
		}
	}

	// Shell-markup spans are skipped for the same reason NamedPlaceholders
	// skips them: a $HOME in there is the author's environment reference, and
	// blanking it would corrupt the command that is about to run.
	return outsideShellMarkup(content, func(segment string) string {
		return namedArgPattern.ReplaceAllStringFunc(segment, func(match string) string {
			name := match[1:]
			if name == argumentsPlaceholder {
				return match
			}
			return values[name]
		})
	})
}
