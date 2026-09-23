// Package redact detects credentials in text and replaces them, so that
// telemetry payloads can be exported without carrying secrets. It is
// deliberately free of dependencies on the rest of the codebase: it compiles
// from a plain Options value, which keeps it testable in isolation and lets
// callers other than telemetry reuse it later.
//
// The security properties worth knowing before changing anything here:
//
//   - Callers MUST redact before truncating. Truncating first can cut a token
//     in half and leave an unmatched high-entropy fragment behind.
//   - Redaction is re-entrant: an existing marker is a protected region and is
//     never re-wrapped, so a marker produced by a subagent survives into a
//     parent span with its detector name and fingerprint intact.
//   - Patterns are RE2 (Go's regexp): no lookahead, no lookbehind. Detectors
//     that need surrounding context match it and replace a capture group.
package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// markerPrefix opens every replacement marker. Exported behaviour: callers and
// tests look for this to decide whether anything was redacted.
const markerPrefix = "[REDACTED:"

// markerRe matches markers this package emits. Used to find protected regions
// so redaction is idempotent. Custom rule replacements that do not follow the
// marker shape are not protected — documented in docs/telemetry.md.
var markerRe = regexp.MustCompile(`\[REDACTED:[A-Za-z0-9_-]+(?::[0-9a-f]{6})?\]`)

// Mode selects the shape of a replacement marker.
type Mode string

const (
	// ModeFingerprint is the default: "[REDACTED:<detector>:<6 hex>]". The
	// fingerprint lets incident response tell "the same token in 600 spans"
	// from "600 different tokens" without exposing either.
	ModeFingerprint Mode = "fingerprint"
	// ModeStrict emits "[REDACTED:<detector>]" with no fingerprint.
	ModeStrict Mode = "strict"
	// ModeRemove deletes the matched value entirely.
	ModeRemove Mode = "remove"
)

// Modes lists every accepted Mode. The config JSON-Schema enum is generated
// from this, so the published enum cannot drift from what the loader accepts.
var Modes = []Mode{ModeFingerprint, ModeStrict, ModeRemove}

// ValidMode reports whether m is an accepted mode.
func ValidMode(m Mode) bool {
	for _, v := range Modes {
		if v == m {
			return true
		}
	}
	return false
}

// Rule is an operator-supplied detector.
type Rule struct {
	Name        string
	Pattern     string
	Group       int
	Replacement string
}

// Options configures a Redactor.
type Options struct {
	// DisableBuiltins names built-in detectors to switch off, matched
	// case-insensitively against their stable names.
	DisableBuiltins []string
	// EnablePII activates the personal-data detector group (off by default:
	// agent telemetry legitimately carries emails and pod IPs).
	EnablePII bool
	// Rules are operator-supplied detectors, applied after all built-ins.
	Rules []Rule
	// Allowlist holds literal values that are never redacted.
	Allowlist []string
	// Mode selects the marker shape. Empty means ModeFingerprint.
	Mode Mode
}

type detector struct {
	name    string
	re      *regexp.Regexp
	group   int
	replace string // non-empty: use verbatim instead of a computed marker
	accept  func(string) bool

	// starts holds literals that a match ALWAYS begins with. When set, the
	// payload is scanned for those literals with strings.Index — which is
	// SIMD-accelerated and runs at GB/s — and the pattern is applied only at
	// the offsets found, anchored.
	//
	// This is not a micro-optimisation. Running each pattern over a whole
	// 400KB generation payload costs tens of milliseconds: a case-insensitive
	// nine-way alternation measured 51ms on its own, against 1.8ms for the
	// same work done as candidate scanning. Redaction runs on every span of
	// every LLM call, so that difference is on the agent's hot loop.
	starts []string
	// prefilter holds literals, any one of which must be present for the
	// pattern to be worth running at all. Used when a match does not begin
	// with a fixed literal (a PEM block is found by "PRIVATE KEY" but starts
	// at "-----BEGIN"), so only the gate applies and the scan is full-width.
	prefilter []string
	// fold matches starts/prefilter literals case-insensitively. Literals must
	// then be written in lower case.
	fold bool
	// wordStart marks a pattern whose leading `\b` was stripped for the
	// anchored variant. `\b` is evaluated relative to the slice it is applied
	// to, so at a candidate offset it would always see a boundary; the check
	// is done against the original string instead.
	wordStart bool

	anchored *regexp.Regexp // ^(?:re) with any leading \b removed
}

// gate reports whether this detector's pattern is worth running at all.
func (d detector) gate(s, lower string) bool {
	lits := d.starts
	if len(lits) == 0 {
		lits = d.prefilter
	}
	if len(lits) == 0 {
		return true
	}
	hay := s
	if d.fold {
		hay = lower
	}
	for _, lit := range lits {
		if strings.Contains(hay, lit) {
			return true
		}
	}
	return false
}

// find returns the submatch index slices for this detector over s, using
// candidate scanning when the detector declares fixed start literals and a
// full scan otherwise. Offsets are always relative to s.
func (d detector) find(s, lower string) [][]int {
	if len(d.starts) == 0 || d.anchored == nil {
		return d.re.FindAllStringSubmatchIndex(s, -1)
	}

	hay := s
	if d.fold {
		hay = lower
	}
	var out [][]int
	seen := make(map[int]bool)
	for _, lit := range d.starts {
		for off := 0; off < len(hay); {
			j := strings.Index(hay[off:], lit)
			if j < 0 {
				break
			}
			at := off + j
			off = at + 1
			if seen[at] {
				continue
			}
			if d.wordStart && at > 0 && isWordByte(s[at-1]) {
				continue
			}
			m := d.anchored.FindStringSubmatchIndex(s[at:])
			if m == nil {
				continue
			}
			seen[at] = true
			shifted := make([]int, len(m))
			for i, v := range m {
				if v < 0 {
					shifted[i] = v
					continue
				}
				shifted[i] = v + at
			}
			out = append(out, shifted)
		}
	}
	return out
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// compileAnchored builds the ^-anchored variant used for candidate scanning.
// A leading \b is stripped and recorded, because at a candidate offset it
// would be evaluated against the slice rather than the whole payload.
func compileAnchored(d *detector) {
	if len(d.starts) == 0 {
		return
	}
	pat := d.re.String()
	if rest, ok := strings.CutPrefix(pat, `\b`); ok {
		pat = rest
		d.wordStart = true
	}
	// Flags written as (?i)/(?s) must stay leftmost in the expression.
	var flags string
	for _, f := range []string{"(?i)", "(?s)", "(?is)", "(?si)"} {
		if rest, ok := strings.CutPrefix(pat, f); ok {
			flags, pat = f, rest
			break
		}
	}
	d.anchored = regexp.MustCompile(flags + "^(?:" + pat + ")")
}

// Redactor rewrites credentials out of text. Build one with New and reuse it;
// it is immutable and safe for concurrent use.
type Redactor struct {
	dets  []detector
	allow map[string]struct{}
	mode  Mode
	// needsFold is true when any detector prefilters case-insensitively, so
	// the lowercased copy is built only when it will actually be consulted.
	needsFold bool
}

// Disabled returns a Redactor that returns its input unchanged.
func Disabled() *Redactor { return &Redactor{} }

// New builds a Redactor from opts. Invalid operator rules are skipped here;
// callers validate them up front with ValidateRule so a typo fails config
// loading rather than silently degrading the filter.
func New(opts Options) *Redactor {
	disabled := make(map[string]struct{}, len(opts.DisableBuiltins))
	for _, n := range opts.DisableBuiltins {
		disabled[strings.ToLower(strings.TrimSpace(n))] = struct{}{}
	}

	var dets []detector
	for _, d := range builtins() {
		if _, off := disabled[d.name]; off {
			continue
		}
		compileAnchored(&d)
		dets = append(dets, d)
	}
	if opts.EnablePII {
		for _, d := range piiDetectors() {
			if _, off := disabled[d.name]; off {
				continue
			}
			compileAnchored(&d)
			dets = append(dets, d)
		}
	}
	// Operator rules last: built-ins win a length tie, so a rule cannot
	// silently take over attribution for a shape we already name.
	for _, r := range opts.Rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			continue
		}
		dets = append(dets, detector{
			name: r.Name, re: re, group: r.Group, replace: r.Replacement,
		})
	}

	allow := make(map[string]struct{}, len(opts.Allowlist))
	for _, a := range opts.Allowlist {
		allow[a] = struct{}{}
	}

	mode := opts.Mode
	if mode == "" {
		mode = ModeFingerprint
	}
	var needsFold bool
	for _, d := range dets {
		if d.fold && (len(d.prefilter) > 0 || len(d.starts) > 0) {
			needsFold = true
			break
		}
	}
	return &Redactor{dets: dets, allow: allow, mode: mode, needsFold: needsFold}
}

type span struct {
	start, end, order int
	name, replace     string
}

// String returns s with every detected credential replaced.
func (r *Redactor) String(s string) string {
	if r == nil || len(r.dets) == 0 || s == "" {
		return s
	}

	merged := r.coveredSpans(s)
	if len(merged) == 0 {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, m := range merged {
		b.WriteString(s[last:m.start])
		b.WriteString(r.marker(m, s[m.start:m.end]))
		last = m.end
	}
	b.WriteString(s[last:])
	return b.String()
}

// coveredSpans resolves every detector hit into the final, non-overlapping set
// of ranges that will be replaced.
func (r *Redactor) coveredSpans(s string) []span {
	var lower string
	if r.needsFold {
		lower = strings.ToLower(s)
	}

	// Markers already in the input are protected: re-wrapping one would
	// destroy the detector name and fingerprint underneath it.
	var protected [][]int
	if strings.Contains(s, markerPrefix) {
		protected = markerRe.FindAllStringIndex(s, -1)
	}

	var hits []span
	for i, d := range r.dets {
		if !d.gate(s, lower) {
			continue
		}
		for _, m := range d.find(s, lower) {
			if 2*d.group+1 >= len(m) {
				continue
			}
			start, end := m[2*d.group], m[2*d.group+1]
			if start < 0 || end <= start {
				continue
			}
			if d.accept != nil && !d.accept(s[m[0]:m[1]]) {
				continue
			}
			if _, ok := r.allow[s[start:end]]; ok {
				continue
			}
			if overlapsAny(start, end, protected) {
				continue
			}
			hits = append(hits, span{start, end, i, d.name, d.replace})
		}
	}
	if len(hits) == 0 {
		return nil
	}

	// Longest match wins; ties break by detector declaration order. Sorting by
	// (start asc, end desc, order asc) and then absorbing anything that starts
	// inside the previous winner gives exactly that, deterministically.
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].start != hits[b].start {
			return hits[a].start < hits[b].start
		}
		if hits[a].end != hits[b].end {
			return hits[a].end > hits[b].end
		}
		return hits[a].order < hits[b].order
	})

	var merged []span
	for _, h := range hits {
		if n := len(merged); n > 0 && h.start < merged[n-1].end {
			if h.end > merged[n-1].end {
				merged[n-1].end = h.end
			}
			continue
		}
		merged = append(merged, h)
	}
	return merged
}

func (r *Redactor) marker(sp span, value string) string {
	if sp.replace != "" {
		return sp.replace
	}
	switch r.mode {
	case ModeRemove:
		return ""
	case ModeStrict:
		return markerPrefix + sp.name + "]"
	default:
		return markerPrefix + sp.name + ":" + Fingerprint(value) + "]"
	}
}

// Fingerprint returns the first 6 hex characters of the SHA-256 of value. Two
// occurrences of one secret share a fingerprint; two secrets almost never do.
// It is one-way: nothing of the original survives in it.
func Fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:6]
}

func overlapsAny(start, end int, regions [][]int) bool {
	for _, p := range regions {
		if start < p[1] && p[0] < end {
			return true
		}
	}
	return false
}

// BuiltinNames returns the stable names of every built-in detector, secret and
// PII alike. Config validation uses it to reject a disableBuiltins entry that
// names nothing.
func BuiltinNames() []string {
	var names []string
	for _, d := range builtins() {
		names = append(names, d.name)
	}
	for _, d := range piiDetectors() {
		names = append(names, d.name)
	}
	return names
}

// ValidateRule reports why a rule cannot be used, or nil. Callers run this at
// config load: a security filter that silently drops a mistyped rule is worse
// than one that never existed, because the operator believes they are covered.
func ValidateRule(r Rule) error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("rule name is required")
	}
	if r.Pattern == "" {
		return fmt.Errorf("rule %q: pattern is required", r.Name)
	}
	re, err := regexp.Compile(r.Pattern)
	if err != nil {
		// RE2 has no lookaround, and it is the most likely first-contact
		// mistake for a rule author. Say so instead of echoing the parser.
		if strings.Contains(r.Pattern, "(?=") || strings.Contains(r.Pattern, "(?!") ||
			strings.Contains(r.Pattern, "(?<=") || strings.Contains(r.Pattern, "(?<!") {
			return fmt.Errorf("rule %q: pattern uses lookahead/lookbehind, which Go's regexp (RE2) does not support; "+
				"match the surrounding text and put the secret in a capture group, then set \"group\" to that group's index", r.Name)
		}
		return fmt.Errorf("rule %q: invalid pattern: %w", r.Name, err)
	}
	if r.Group < 0 {
		return fmt.Errorf("rule %q: group must not be negative", r.Name)
	}
	if r.Group > re.NumSubexp() {
		return fmt.Errorf("rule %q: group %d does not exist in pattern (it defines %d capture group(s))",
			r.Name, r.Group, re.NumSubexp())
	}
	return nil
}
