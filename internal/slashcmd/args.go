package slashcmd

import "regexp"

// namedArgPattern matches a named placeholder such as $TARGET. The name must
// start with an uppercase letter so shell/env references written in lowercase
// and dollar amounts ($50) are not mistaken for parameters.
var namedArgPattern = regexp.MustCompile(`\$([A-Z][A-Z0-9_]*)`)

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
	matches := namedArgPattern.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}

	names := make([]string, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		name := m[1]
		if name == argumentsPlaceholder || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
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

	return namedArgPattern.ReplaceAllStringFunc(content, func(match string) string {
		name := match[1:]
		if name == argumentsPlaceholder {
			return match
		}
		return values[name]
	})
}
