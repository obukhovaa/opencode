package flow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// atomRegex matches a single predicate atom of the form:
//
//	[sizeof ]${args|step.KEY} OP VALUE
//
// where OP is ==, !=, or =~. The value is captured greedily up to end of input,
// so callers must strip composite-operator tails before passing a string in.
var atomRegex = regexp.MustCompile(`^(?:(sizeof)\s+)?\$\{(args|step)\.([^}]+)\}\s*(==|!=|=~)\s*(.+)$`)

// evaluatePredicate evaluates a flow-rule predicate expression. The expression
// is a single atom (e.g. `${args.status} == done`) or a boolean composition of
// atoms via `&&` and `||`, optionally grouped with parentheses. `&&` binds
// tighter than `||`. Atoms inside `${...}` placeholders or `/.../` regex
// literals are tokenized as a whole — composite operators inside them are
// ignored. Evaluation is short-circuited.
func evaluatePredicate(predicate string, args map[string]any, stepVars map[string]any) (bool, error) {
	tokens, err := tokenizePredicate(strings.TrimSpace(predicate))
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalidPredicate, err)
	}
	if len(tokens) == 0 {
		return false, fmt.Errorf("%w: empty", ErrInvalidPredicate)
	}
	p := &predicateParser{tokens: tokens}
	node, err := p.parseOr()
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalidPredicate, err)
	}
	if p.pos != len(tokens) {
		return false, fmt.Errorf("%w: unexpected token %q after expression", ErrInvalidPredicate, tokens[p.pos].text)
	}
	return node.eval(args, stepVars)
}

// evaluateAtom evaluates a single predicate atom (no &&, ||, or parens).
func evaluateAtom(atom string, args map[string]any, stepVars map[string]any) (bool, error) {
	matches := atomRegex.FindStringSubmatch(strings.TrimSpace(atom))
	if matches == nil {
		return false, fmt.Errorf("%w: %q", ErrInvalidPredicate, atom)
	}

	prefix := matches[1]
	scope := matches[2]
	key := matches[3]
	op := matches[4]
	expected := strings.TrimSpace(matches[5])

	var actual any
	var ok bool
	switch scope {
	case "step":
		// Closed namespace — unknown keys are flow-author bugs, not "missing optional".
		actual, ok = stepVars[key]
		if !ok {
			return false, fmt.Errorf("unknown step variable %q", key)
		}
	case "args":
		// Dot-path support: ${args.reviewer.email} walks nested maps.
		// Top-level exact-key match still wins first for backward
		// compatibility with flat keys that literally contain a dot.
		actual, ok = resolveArgsPath(args, key)
		if !ok {
			return false, nil
		}
	default:
		return false, fmt.Errorf("unknown scope %q", scope)
	}
	actualStr := fmt.Sprintf("%v", actual)

	if prefix == "sizeof" {
		actualStr = resolveSizeof(actual)
	}

	switch op {
	case "==":
		return actualStr == expected, nil
	case "!=":
		return actualStr != expected, nil
	case "=~":
		if len(expected) < 2 || expected[0] != '/' || expected[len(expected)-1] != '/' {
			return false, fmt.Errorf("regex pattern must be delimited by /: %q", expected)
		}
		pattern := expected[1 : len(expected)-1]
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false, fmt.Errorf("invalid regex pattern %q: %w", pattern, err)
		}
		return re.MatchString(actualStr), nil
	default:
		return false, fmt.Errorf("unknown operator %q", op)
	}
}

func resolveSizeof(value any) string {
	switch v := value.(type) {
	case []any:
		return strconv.Itoa(len(v))
	case map[string]any:
		return strconv.Itoa(len(v))
	default:
		return strconv.Itoa(len(fmt.Sprintf("%v", v)))
	}
}

// --- composite expression tokenizer / parser ---------------------------------

type tokenKind int

const (
	tokAtom tokenKind = iota
	tokAnd
	tokOr
	tokLParen
	tokRParen
)

type predicateToken struct {
	kind tokenKind
	text string // populated for tokAtom; used in error messages otherwise
}

// tokenizePredicate splits a composite predicate into atoms / && / || / parens.
//
// It is structure-aware: characters inside ${...} placeholders or /.../ regex
// literals (entered after a `=~` operator) are not interpreted as operators
// or parens. This protects expressions such as `${args.x} =~ /IMPL|REVIEW/`
// from being mis-split.
//
// Limitation: literal `&&`, `||`, `(`, or `)` in the right-hand-side of `==` /
// `!=` (i.e. in expected values that are not regex literals) will be
// interpreted as composite tokens. Quoting RHS values is not currently
// supported.
func tokenizePredicate(s string) ([]predicateToken, error) {
	var tokens []predicateToken
	var atom strings.Builder

	flushAtom := func() {
		text := strings.TrimSpace(atom.String())
		atom.Reset()
		if text != "" {
			tokens = append(tokens, predicateToken{kind: tokAtom, text: text})
		}
	}

	placeholderDepth := 0
	inRegex := false
	i := 0
	for i < len(s) {
		c := s[i]

		if placeholderDepth > 0 {
			atom.WriteByte(c)
			switch c {
			case '{':
				placeholderDepth++
			case '}':
				placeholderDepth--
			}
			i++
			continue
		}

		if inRegex {
			atom.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				atom.WriteByte(s[i+1])
				i += 2
				continue
			}
			if c == '/' {
				inRegex = false
			}
			i++
			continue
		}

		if c == '$' && i+1 < len(s) && s[i+1] == '{' {
			atom.WriteByte('$')
			atom.WriteByte('{')
			placeholderDepth = 1
			i += 2
			continue
		}

		if c == '/' && atomEndsWithRegexOp(atom.String()) {
			atom.WriteByte(c)
			inRegex = true
			i++
			continue
		}

		switch {
		case c == '(':
			flushAtom()
			tokens = append(tokens, predicateToken{kind: tokLParen, text: "("})
			i++
			continue
		case c == ')':
			flushAtom()
			tokens = append(tokens, predicateToken{kind: tokRParen, text: ")"})
			i++
			continue
		case c == '&' && i+1 < len(s) && s[i+1] == '&':
			flushAtom()
			tokens = append(tokens, predicateToken{kind: tokAnd, text: "&&"})
			i += 2
			continue
		case c == '|' && i+1 < len(s) && s[i+1] == '|':
			flushAtom()
			tokens = append(tokens, predicateToken{kind: tokOr, text: "||"})
			i += 2
			continue
		}

		atom.WriteByte(c)
		i++
	}

	if placeholderDepth > 0 {
		return nil, fmt.Errorf("unclosed ${...} in predicate")
	}
	if inRegex {
		return nil, fmt.Errorf("unclosed regex literal in predicate")
	}
	flushAtom()
	return tokens, nil
}

// atomEndsWithRegexOp reports whether the in-progress atom ends with a
// standalone `=~` operator (the only context where an unescaped `/` opens a
// regex literal). To qualify, the `=~` must be at the atom's start or be
// preceded by whitespace or `}` (the closing of a `${...}` placeholder) —
// i.e. it must look like a real operator, not a tail of a larger token such
// as `==~` or `foo=~`. Without this guard, malformed predicates like
// `${args.x} == foo=~/bar && ...` would incorrectly enter regex mode and
// swallow the rest of the expression.
func atomEndsWithRegexOp(atom string) bool {
	trimmed := strings.TrimRight(atom, " \t")
	if !strings.HasSuffix(trimmed, "=~") {
		return false
	}
	prefix := trimmed[:len(trimmed)-2]
	if prefix == "" {
		return true
	}
	switch prefix[len(prefix)-1] {
	case ' ', '\t', '}':
		return true
	}
	return false
}

type predicateNode interface {
	eval(args, stepVars map[string]any) (bool, error)
}

type atomNode struct{ text string }

func (n atomNode) eval(args, stepVars map[string]any) (bool, error) {
	return evaluateAtom(n.text, args, stepVars)
}

type andNode struct{ left, right predicateNode }

func (n andNode) eval(args, stepVars map[string]any) (bool, error) {
	l, err := n.left.eval(args, stepVars)
	if err != nil {
		return false, err
	}
	if !l {
		return false, nil
	}
	return n.right.eval(args, stepVars)
}

type orNode struct{ left, right predicateNode }

func (n orNode) eval(args, stepVars map[string]any) (bool, error) {
	l, err := n.left.eval(args, stepVars)
	if err != nil {
		return false, err
	}
	if l {
		return true, nil
	}
	return n.right.eval(args, stepVars)
}

// predicateParser implements recursive-descent parsing with standard
// precedence: `||` binds loosest, `&&` binds tighter, primary terms are
// either atoms or parenthesized sub-expressions.
type predicateParser struct {
	tokens []predicateToken
	pos    int
}

func (p *predicateParser) parseOr() (predicateNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.pos < len(p.tokens) && p.tokens[p.pos].kind == tokOr {
		p.pos++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = orNode{left: left, right: right}
	}
	return left, nil
}

func (p *predicateParser) parseAnd() (predicateNode, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for p.pos < len(p.tokens) && p.tokens[p.pos].kind == tokAnd {
		p.pos++
		right, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		left = andNode{left: left, right: right}
	}
	return left, nil
}

func (p *predicateParser) parsePrimary() (predicateNode, error) {
	if p.pos >= len(p.tokens) {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	t := p.tokens[p.pos]
	switch t.kind {
	case tokLParen:
		p.pos++
		node, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.pos >= len(p.tokens) || p.tokens[p.pos].kind != tokRParen {
			return nil, fmt.Errorf("expected closing parenthesis")
		}
		p.pos++
		return node, nil
	case tokAtom:
		p.pos++
		return atomNode{text: t.text}, nil
	default:
		return nil, fmt.Errorf("unexpected token %q", t.text)
	}
}
