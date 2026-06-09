package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFileTokensExtractsAttachment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "report.pdf")
	if err := os.WriteFile(path, []byte("pdf-bytes"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	in := "Hello reviewers\nFILE:" + path + "\nDone."
	clean, atts, unsafe := ParseFileTokens(in, root)
	if clean != "Hello reviewers\nDone." {
		t.Errorf("clean = %q", clean)
	}
	if len(atts) != 1 {
		t.Fatalf("atts len = %d", len(atts))
	}
	if string(atts[0].Content) != "pdf-bytes" {
		t.Errorf("content = %q", string(atts[0].Content))
	}
	if atts[0].FileName != "report.pdf" {
		t.Errorf("filename = %q", atts[0].FileName)
	}
	if len(unsafe) != 0 {
		t.Errorf("unsafe = %v", unsafe)
	}
}

func TestParseFileTokensRejectsPathOutsideRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Create a file outside the media root.
	outside := t.TempDir()
	path := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(path, []byte("nope"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	in := "FILE:" + path
	clean, atts, unsafe := ParseFileTokens(in, root)
	if clean != "" {
		t.Errorf("clean = %q, want empty (FILE line removed)", clean)
	}
	if len(atts) != 0 {
		t.Errorf("atts unexpectedly populated: %+v", atts)
	}
	if len(unsafe) != 1 || unsafe[0] != path {
		t.Errorf("unsafe = %v", unsafe)
	}
}

func TestParseFileTokensNoTokens(t *testing.T) {
	t.Parallel()
	in := "just some prose with no FILE: marker on its own line"
	clean, atts, unsafe := ParseFileTokens(in, "/tmp")
	if clean != in {
		t.Errorf("clean = %q", clean)
	}
	if len(atts) != 0 || len(unsafe) != 0 {
		t.Errorf("atts=%v unsafe=%v", atts, unsafe)
	}
}

func TestFormatRelativeAge(t *testing.T) {
	t.Parallel()
	now := int64(1_000_000_000_000) // arbitrary "now"
	cases := []struct {
		past int64
		want string
	}{
		{0, "never"},
		{now - 5_000, "5s ago"},
		{now - 90_000, "1m ago"},
		{now - 7_200_000, "2h ago"},
		{now - 172_800_000, "2d ago"},
		{now + 1000, "0s ago"}, // future-stamped (clock skew) → 0s
	}
	for _, c := range cases {
		got := formatRelativeAge(c.past, now)
		if got != c.want {
			t.Errorf("formatRelativeAge(%d, %d) = %q, want %q", c.past, now, got, c.want)
		}
	}
}

func TestFormatTokens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1500, "1.5k"},
		{12500, "12.5k"},
		{999_999, "1000.0k"},
		{1_500_000, "1.5M"},
	}
	for _, c := range cases {
		got := formatTokens(c.n)
		if got != c.want {
			t.Errorf("formatTokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestParseFileTokensHandlesLeadingWhitespace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "doc.txt")
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// FILE: tokens may have leading whitespace per the TS bridge.
	in := "   FILE:" + path + "\nfollow-up"
	_, atts, _ := ParseFileTokens(in, root)
	if len(atts) != 1 {
		t.Errorf("len(atts) = %d", len(atts))
	}
}

func TestStripAttributionInOutboundFanOut(t *testing.T) {
	t.Parallel()
	// Sanity: StripAttribution returns the wrapped content from a
	// realistic agent echo. The outbound fan-out (send.go) MUST strip
	// before delivery — verified here so a refactor of the strip logic
	// can't silently regress.
	in := "[<@U07BOB> via slack]: please look at this"
	if got := StripAttribution(in); !strings.HasPrefix(got, "please") {
		t.Errorf("got %q", got)
	}
}
