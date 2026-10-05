package redact

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The corpus is the contract for this package: section 1/2 values must be
// redacted, section 3/4 values must survive untouched. It is parsed rather than
// duplicated in Go so the committed markdown stays the single source of truth —
// a reviewer reads corpus.md, not a table literal.

var (
	sectionRe  = regexp.MustCompile(`(?m)^##+\s+(\d+\w*)\.\s`)
	inlineRe   = regexp.MustCompile("`([^`]+)`")
	fenceSplit = regexp.MustCompile("(?s)```\n(.*?)```")
)

type corpusCase struct {
	section string
	value   string
}

// loadCorpus returns the must-redact and must-not-redact cases from
// testdata/corpus.md. Inline values are taken only from list items and their
// indented continuations, so prose that mentions a pattern in backticks (the
// section 3 preamble cites `sk-[A-Za-z0-9_-]{20,}`) is not mistaken for a case.
func loadCorpus(t *testing.T) (mustRedact, mustNot []corpusCase) {
	t.Helper()
	raw, err := os.ReadFile("testdata/corpus.md")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	text := string(raw)

	idx := sectionRe.FindAllStringSubmatchIndex(text, -1)
	if len(idx) == 0 {
		t.Fatal("corpus has no numbered sections")
	}
	for i, m := range idx {
		name := text[m[2]:m[3]]
		end := len(text)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		body := text[m[1]:end]

		var cases []corpusCase
		for _, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			isItem := strings.HasPrefix(trimmed, "- ")
			isCont := line != trimmed && strings.HasPrefix(trimmed, "`")
			if !isItem && !isCont {
				continue
			}
			for _, sm := range inlineRe.FindAllStringSubmatch(line, -1) {
				cases = append(cases, corpusCase{name, sm[1]})
			}
		}
		for _, fm := range fenceSplit.FindAllStringSubmatch(body, -1) {
			block := strings.TrimRight(fm[1], "\n")
			if block != "" {
				cases = append(cases, corpusCase{name, block})
			}
		}

		switch {
		case strings.HasPrefix(name, "1"), strings.HasPrefix(name, "2"):
			mustRedact = append(mustRedact, cases...)
		case strings.HasPrefix(name, "3"), strings.HasPrefix(name, "4"):
			mustNot = append(mustNot, cases...)
		default:
			t.Fatalf("corpus section %q is neither must-redact (1,2) nor must-not (3,4)", name)
		}
	}
	return mustRedact, mustNot
}

func TestCorpus_MustRedact(t *testing.T) {
	cases, _ := loadCorpus(t)
	if len(cases) == 0 {
		t.Fatal("no must-redact cases parsed from corpus")
	}
	r := New(Options{})
	for _, c := range cases {
		t.Run(short(c.value), func(t *testing.T) {
			got := r.String(c.value)
			if !strings.Contains(got, markerPrefix) {
				t.Errorf("section %s: value was NOT redacted\n  in:  %q\n  out: %q", c.section, c.value, got)
			}
		})
	}
}

func TestCorpus_MustNotRedact(t *testing.T) {
	_, cases := loadCorpus(t)
	if len(cases) == 0 {
		t.Fatal("no must-not-redact cases parsed from corpus")
	}
	r := New(Options{})
	for _, c := range cases {
		t.Run(short(c.value), func(t *testing.T) {
			if got := r.String(c.value); got != c.value {
				t.Errorf("section %s: FALSE POSITIVE, value was altered\n  in:  %q\n  out: %q", c.section, c.value, got)
			}
		})
	}
}

// TestCorpus_NoLiveCredentials guards the D0 rule: every must-redact value is
// defused. A value that carries none of the agreed markers is one somebody
// pasted in raw.
func TestCorpus_NoLiveCredentials(t *testing.T) {
	cases, _ := loadCorpus(t)
	for _, c := range cases {
		low := strings.ToLower(c.value)
		if strings.Contains(low, "example") || strings.Contains(low, "deadbeef") ||
			strings.Contains(low, "0000") || strings.Contains(low, "donotuse") {
			continue
		}
		t.Errorf("section %s: must-redact value carries no defusing marker, is it real?\n  %q",
			c.section, c.value)
	}
}

func short(s string) string {
	s = strings.ReplaceAll(s, "\n", "_")
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}
