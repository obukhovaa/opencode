package clitool

import (
	"fmt"
	"strings"

	"github.com/opencode-ai/opencode/internal/permission"
)

// PolicyError reports an argument vector the manifest's policy refuses. It
// is a model-visible error (the call returns an error response and the run
// continues), never a permission denial.
type PolicyError struct {
	Arg     string // the offending argument, or the joined vector
	Pattern string // the pattern that matched (deny) or the allow set
	Reason  string
}

func (e *PolicyError) Error() string {
	return "rejected by the tool's argument policy: " + e.Reason
}

// JoinArgs renders the model-influenced argument vector the way a human
// would type it after the binary. It is the input for both the hard policy's
// joined-string patterns and the per-call permission globs.
func JoinArgs(args []string) string { return strings.Join(args, " ") }

// CheckArgs enforces the manifest's argument policy on the model-influenced
// vector (after structured rendering, before prefixArgs are prepended):
//  1. any argument containing NUL is rejected;
//  2. every deny pattern is matched against each single argument and
//     against the joined string — one hit rejects the call;
//  3. when allow patterns exist, the joined string must match one of them.
func (m *Manifest) CheckArgs(args []string) error {
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return &PolicyError{Arg: a, Reason: "an argument contains a NUL byte"}
		}
	}
	joined := JoinArgs(args)
	for _, pattern := range m.Args.Deny {
		for _, a := range args {
			if permission.MatchWildcard(pattern, a) {
				return &PolicyError{Arg: a, Pattern: pattern,
					Reason: fmt.Sprintf("argument %q matches deny pattern %q", a, pattern)}
			}
		}
		if permission.MatchWildcard(pattern, joined) {
			return &PolicyError{Arg: joined, Pattern: pattern,
				Reason: fmt.Sprintf("arguments %q match deny pattern %q", joined, pattern)}
		}
	}
	if len(m.Args.Allow) > 0 {
		for _, pattern := range m.Args.Allow {
			if permission.MatchWildcard(pattern, joined) {
				return nil
			}
		}
		return &PolicyError{Arg: joined, Pattern: strings.Join(m.Args.Allow, ", "),
			Reason: fmt.Sprintf("arguments %q match none of the allow patterns [%s]", joined, strings.Join(m.Args.Allow, ", "))}
	}
	return nil
}
