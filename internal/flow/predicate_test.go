package flow

import (
	"errors"
	"testing"
)

func TestEvaluatePredicate_Composite(t *testing.T) {
	tests := []struct {
		name      string
		predicate string
		args      map[string]any
		want      bool
		wantErr   bool
	}{
		// AND
		{
			"and both true",
			`sizeof ${args.blockers} == 0 && ${args.build_service_snapshots} == true`,
			map[string]any{"blockers": []any{}, "build_service_snapshots": true},
			true, false,
		},
		{
			"and first false short-circuits",
			`sizeof ${args.blockers} == 0 && ${args.build_service_snapshots} == true`,
			map[string]any{"blockers": []any{"x"}, "build_service_snapshots": true},
			false, false,
		},
		{
			"and second false",
			`sizeof ${args.blockers} == 0 && ${args.build_service_snapshots} == true`,
			map[string]any{"blockers": []any{}, "build_service_snapshots": false},
			false, false,
		},

		// OR
		{
			"or first true short-circuits",
			`${args.status} == done || ${args.status} == ready`,
			map[string]any{"status": "done"},
			true, false,
		},
		{
			"or second true",
			`${args.status} == done || ${args.status} == ready`,
			map[string]any{"status": "ready"},
			true, false,
		},
		{
			"or both false",
			`${args.status} == done || ${args.status} == ready`,
			map[string]any{"status": "pending"},
			false, false,
		},

		// Three-term AND (mirrors the user-provided example)
		{
			"three-term and all true",
			`sizeof ${args.blockers} == 0 && ${args.build_service_snapshots} != true && ${args.trigger_review} == true`,
			map[string]any{"blockers": []any{}, "build_service_snapshots": false, "trigger_review": true},
			true, false,
		},
		{
			"three-term and last false",
			`sizeof ${args.blockers} == 0 && ${args.build_service_snapshots} != true && ${args.trigger_review} == true`,
			map[string]any{"blockers": []any{}, "build_service_snapshots": false, "trigger_review": false},
			false, false,
		},

		// Precedence: && binds tighter than ||
		// `a || b && c` parses as `a || (b && c)`
		{
			"precedence a true, b&&c irrelevant",
			`${args.a} == 1 || ${args.b} == 1 && ${args.c} == 1`,
			map[string]any{"a": 1, "b": 0, "c": 0},
			true, false,
		},
		{
			"precedence a false, b&&c true",
			`${args.a} == 1 || ${args.b} == 1 && ${args.c} == 1`,
			map[string]any{"a": 0, "b": 1, "c": 1},
			true, false,
		},
		{
			"precedence a false, only b true (c false) — overall false",
			`${args.a} == 1 || ${args.b} == 1 && ${args.c} == 1`,
			map[string]any{"a": 0, "b": 1, "c": 0},
			false, false,
		},

		// Parentheses override precedence
		// `(a || b) && c` requires c true regardless of a/b.
		{
			"parens force && over ||  — c false fails",
			`(${args.a} == 1 || ${args.b} == 1) && ${args.c} == 1`,
			map[string]any{"a": 1, "b": 0, "c": 0},
			false, false,
		},
		{
			"parens force && over ||  — a true and c true",
			`(${args.a} == 1 || ${args.b} == 1) && ${args.c} == 1`,
			map[string]any{"a": 1, "b": 0, "c": 1},
			true, false,
		},

		// Nested parentheses
		{
			"nested parens",
			`((${args.a} == 1 && ${args.b} == 1) || ${args.c} == 1) && ${args.d} == 1`,
			map[string]any{"a": 0, "b": 0, "c": 1, "d": 1},
			true, false,
		},
		{
			"nested parens — inner and outer false",
			`((${args.a} == 1 && ${args.b} == 1) || ${args.c} == 1) && ${args.d} == 1`,
			map[string]any{"a": 0, "b": 1, "c": 0, "d": 1},
			false, false,
		},

		// Regex literal must not be split by `|` inside /.../
		{
			"regex with alternation in AND",
			`${args.workflow} =~ /IMPL|REVIEW/ && ${args.status} == ready`,
			map[string]any{"workflow": "IMPL", "status": "ready"},
			true, false,
		},
		{
			"regex with alternation in OR",
			`${args.workflow} =~ /IMPL|REVIEW/ || ${args.status} == done`,
			map[string]any{"workflow": "OTHER", "status": "done"},
			true, false,
		},
		{
			"regex with escaped slash",
			`${args.path} =~ /^a\/b$/ && ${args.ok} == true`,
			map[string]any{"path": "a/b", "ok": true},
			true, false,
		},

		// Missing arg → atom false; composes with ||
		{
			"missing arg in && — overall false, no error",
			`${args.missing} == x && ${args.present} == y`,
			map[string]any{"present": "y"},
			false, false,
		},
		{
			"missing arg in || other true",
			`${args.missing} == x || ${args.present} == y`,
			map[string]any{"present": "y"},
			true, false,
		},

		// Composite spans args + step scopes
		{
			"mixed scopes",
			`${args.status} == ready && ${step.iteration} == 1`,
			map[string]any{"status": "ready"},
			true, false,
		},

		// Malformed input
		{"trailing &&", `${args.a} == 1 &&`, map[string]any{"a": 1}, false, true},
		{"leading ||", `|| ${args.a} == 1`, map[string]any{"a": 1}, false, true},
		{"empty parens", `() && ${args.a} == 1`, map[string]any{"a": 1}, false, true},
		{"unclosed paren", `(${args.a} == 1 && ${args.b} == 1`, map[string]any{"a": 1, "b": 1}, false, true},
		{"extra closing paren", `${args.a} == 1)`, map[string]any{"a": 1}, false, true},
		{"empty expression", ``, map[string]any{}, false, true},
		{"only parens", `()`, map[string]any{}, false, true},
		{"only operator", `&&`, map[string]any{}, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step var "iteration" supplied so mixed-scope cases work.
			got, err := evaluatePredicate(tt.predicate, tt.args, map[string]any{"iteration": 1})
			if (err != nil) != tt.wantErr {
				t.Fatalf("evaluatePredicate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidPredicate) && !isStepVarErr(err) {
					t.Errorf("expected ErrInvalidPredicate (or step-var error), got %v", err)
				}
				return
			}
			if got != tt.want {
				t.Errorf("evaluatePredicate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// isStepVarErr returns true if the error came from the unknown-step-variable
// path inside evaluateAtom (which does not wrap ErrInvalidPredicate).
func isStepVarErr(err error) bool {
	return err != nil && !errors.Is(err, ErrInvalidPredicate)
}

func TestEvaluatePredicate_ShortCircuit(t *testing.T) {
	// && short-circuit: right side references an unknown step var. If
	// short-circuit works correctly when left is false, the right side is
	// never evaluated and no error is returned.
	got, err := evaluatePredicate(
		`${args.a} == 1 && ${step.bogus} == 1`,
		map[string]any{"a": 0},
		map[string]any{},
	)
	if err != nil {
		t.Fatalf("expected no error from short-circuited &&, got %v", err)
	}
	if got {
		t.Errorf("expected false, got true")
	}

	// || short-circuit: left true → right's bogus step var never read.
	got, err = evaluatePredicate(
		`${args.a} == 1 || ${step.bogus} == 1`,
		map[string]any{"a": 1},
		map[string]any{},
	)
	if err != nil {
		t.Fatalf("expected no error from short-circuited ||, got %v", err)
	}
	if !got {
		t.Errorf("expected true, got false")
	}
}

// TestEvaluatePredicate_SingleAtomBackwardCompat is a regression guard for
// the composite-operator refactor: every predicate in this table is a single
// atom that must (a) tokenize as exactly one tokAtom (i.e. take the legacy
// fast path through evaluateAtom), (b) return the same boolean as a direct
// evaluateAtom call, and (c) produce no parse error. The cases cover the
// real-world predicate shapes seen in shipped flows — sizeof on
// arrays/objects, enum equality, boolean equality, quoted-empty-string
// equality (which compares against the literal two-char string `""`), and
// missing-arg semantics.
func TestEvaluatePredicate_SingleAtomBackwardCompat(t *testing.T) {
	cases := []struct {
		predicate string
		args      map[string]any
		want      bool
	}{
		// sizeof on arrays
		{`sizeof ${args.items} == 0`, map[string]any{"items": []any{}}, true},
		{`sizeof ${args.items} == 0`, map[string]any{"items": []any{"x"}}, false},
		{`sizeof ${args.items} != 0`, map[string]any{"items": []any{"x"}}, true},
		{`sizeof ${args.items} != 0`, map[string]any{"items": []any{}}, false},

		// enum-style equality
		{`${args.status} == IMPLEMENTATION`, map[string]any{"status": "IMPLEMENTATION"}, true},
		{`${args.status} == IMPLEMENTATION`, map[string]any{"status": "CORRECTION"}, false},
		{`${args.status} == CORRECTION`, map[string]any{"status": "CORRECTION"}, true},
		{`${args.status} == SKIP`, map[string]any{"status": "SKIP"}, true},

		// boolean equality
		{`${args.flag} == true`, map[string]any{"flag": true}, true},
		{`${args.flag} != true`, map[string]any{"flag": false}, true},

		// Quoted-empty-string equality matches only the literal two-char
		// string `""` (preserves prior atomRegex semantics).
		{`${args.value} == ""`, map[string]any{"value": `""`}, true},
		{`${args.value} == ""`, map[string]any{"value": "abc"}, false},
		{`${args.value} != ""`, map[string]any{"value": "abc"}, true},

		// Missing-arg → false (no error) for the args scope.
		{`${args.status} == IMPLEMENTATION`, map[string]any{}, false},
		{`sizeof ${args.items} == 0`, map[string]any{}, false},
	}

	for _, c := range cases {
		t.Run(c.predicate, func(t *testing.T) {
			got, err := evaluatePredicate(c.predicate, c.args, nil)
			if err != nil {
				t.Fatalf("evaluatePredicate(%q) returned error: %v", c.predicate, err)
			}
			if got != c.want {
				t.Errorf("evaluatePredicate(%q) = %v, want %v", c.predicate, got, c.want)
			}

			gotAtom, errAtom := evaluateAtom(c.predicate, c.args, nil)
			if errAtom != nil {
				t.Fatalf("evaluateAtom(%q) returned error: %v", c.predicate, errAtom)
			}
			if gotAtom != got {
				t.Errorf("composite vs atom mismatch for %q: composite=%v atom=%v", c.predicate, got, gotAtom)
			}

			tokens, err := tokenizePredicate(c.predicate)
			if err != nil {
				t.Fatalf("tokenizePredicate(%q) error: %v", c.predicate, err)
			}
			if len(tokens) != 1 || tokens[0].kind != tokAtom {
				t.Errorf("expected single atom token for %q, got %d tokens", c.predicate, len(tokens))
			}
		})
	}
}

func TestTokenizePredicate(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string // text per token (kind names for operators/parens)
	}{
		{
			"single atom",
			`${args.x} == done`,
			[]string{"${args.x} == done"},
		},
		{
			"and",
			`${args.x} == 1 && ${args.y} == 2`,
			[]string{"${args.x} == 1", "&&", "${args.y} == 2"},
		},
		{
			"or with parens",
			`(${args.x} == 1 || ${args.y} == 2) && ${args.z} == 3`,
			[]string{"(", "${args.x} == 1", "||", "${args.y} == 2", ")", "&&", "${args.z} == 3"},
		},
		{
			"regex with pipe preserved",
			`${args.x} =~ /a|b/ && ${args.y} == 1`,
			[]string{"${args.x} =~ /a|b/", "&&", "${args.y} == 1"},
		},
		{
			"regex with parens preserved",
			`${args.x} =~ /(foo|bar)/ || ${args.y} == 1`,
			[]string{"${args.x} =~ /(foo|bar)/", "||", "${args.y} == 1"},
		},
		{
			"sizeof prefix",
			`sizeof ${args.items} != 0 && ${args.ok} == true`,
			[]string{"sizeof ${args.items} != 0", "&&", "${args.ok} == true"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, err := tokenizePredicate(tt.input)
			if err != nil {
				t.Fatalf("tokenizePredicate() error = %v", err)
			}
			if len(tokens) != len(tt.want) {
				t.Fatalf("got %d tokens, want %d (%v)", len(tokens), len(tt.want), tokensToStrings(tokens))
			}
			for i, tok := range tokens {
				if tok.text != tt.want[i] {
					t.Errorf("token[%d] = %q, want %q", i, tok.text, tt.want[i])
				}
			}
		})
	}
}

func tokensToStrings(tokens []predicateToken) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = t.text
	}
	return out
}

func TestAtomEndsWithRegexOp(t *testing.T) {
	tests := []struct {
		atom string
		want bool
	}{
		// Standalone =~ — whitespace before.
		{`${args.x} =~`, true},
		{`${args.x} =~ `, true}, // trailing whitespace tolerated
		// Standalone =~ — directly after `}`.
		{`${args.x}=~`, true},
		// Bare operator.
		{`=~`, true},
		// Tails of larger tokens — must NOT be treated as regex op.
		{`${args.x} ==~`, false}, // typo: extra `=`
		{`${args.x} == foo=~`, false},
		{`${args.x} != foo=~`, false},
		{`${args.x} !=~`, false},
		// No =~ at all.
		{`${args.x} == 1`, false},
		{``, false},
	}
	for _, tt := range tests {
		t.Run(tt.atom, func(t *testing.T) {
			if got := atomEndsWithRegexOp(tt.atom); got != tt.want {
				t.Errorf("atomEndsWithRegexOp(%q) = %v, want %v", tt.atom, got, tt.want)
			}
		})
	}
}

// Regression: a malformed predicate that puts `=~` in the RHS of `==` must
// not enter regex mode in the tokenizer (which would then swallow the
// composite operator after it). The result should be the same as if no
// regex mode were ever entered — atom-by-atom evaluation, no parse error.
func TestTokenizePredicate_RegexOpNotInRHS(t *testing.T) {
	tokens, err := tokenizePredicate(`${args.x} == foo=~/bar && ${args.y} == 1`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{`${args.x} == foo=~/bar`, "&&", `${args.y} == 1`}
	if len(tokens) != len(want) {
		t.Fatalf("got %d tokens, want %d: %v", len(tokens), len(want), tokensToStrings(tokens))
	}
	for i, tok := range tokens {
		if tok.text != want[i] {
			t.Errorf("token[%d] = %q, want %q", i, tok.text, want[i])
		}
	}
}

func TestTokenizePredicate_Errors(t *testing.T) {
	cases := []string{
		`${args.x == 1`,          // unclosed placeholder
		`${args.x} =~ /unclosed`, // unclosed regex
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := tokenizePredicate(c); err == nil {
				t.Errorf("expected error for %q", c)
			}
		})
	}
}
