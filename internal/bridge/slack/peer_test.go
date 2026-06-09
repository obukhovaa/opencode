package slack

import "testing"

func TestPeerIDEncodingDM(t *testing.T) {
	t.Parallel()
	if got := ParsePeerID("D123"); got != (Peer{ChannelID: "D123"}) {
		t.Errorf("ParsePeerID(D123) = %+v", got)
	}
	if got := FormatPeerID(Peer{ChannelID: "D123"}); got != "D123" {
		t.Errorf("FormatPeerID = %q", got)
	}
}

func TestPeerIDEncodingThread(t *testing.T) {
	t.Parallel()
	want := Peer{ChannelID: "C123", ThreadTS: "1700000000.000100"}
	if got := ParsePeerID("C123|1700000000.000100"); got != want {
		t.Errorf("Parse thread = %+v", got)
	}
	if got := FormatPeerID(want); got != "C123|1700000000.000100" {
		t.Errorf("Format thread = %q", got)
	}
}

func TestPeerIDEncodingEmptyAndMalformed(t *testing.T) {
	t.Parallel()
	if got := ParsePeerID(""); got != (Peer{}) {
		t.Errorf("Parse empty = %+v", got)
	}
	if got := ParsePeerID("  "); got != (Peer{}) {
		t.Errorf("Parse whitespace = %+v", got)
	}
	if got := ParsePeerID("|missing-channel"); got != (Peer{ChannelID: ""}) {
		t.Errorf("Parse leading | = %+v", got)
	}
}

func TestStripMention(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, bot, want string
	}{
		{"<@UBOT> hello", "UBOT", "hello"},
		{"<@UBOT>: hello", "UBOT", "hello"},
		{"<@UBOT> - hello", "UBOT", "hello"},
		{"hello", "UBOT", "hello"},
		{"hello <@UBOT> world", "UBOT", "hello   world"},
	}
	for _, c := range cases {
		got := StripMention(c.in, c.bot)
		if got != c.want {
			t.Errorf("StripMention(%q, %q) = %q, want %q", c.in, c.bot, got, c.want)
		}
	}
}

func TestMentionsBot(t *testing.T) {
	t.Parallel()
	if !MentionsBot("hi <@UBOT>", "UBOT") {
		t.Error("MentionsBot: expected true")
	}
	if MentionsBot("hi <@OTHER>", "UBOT") {
		t.Error("MentionsBot: expected false on different user")
	}
	if MentionsBot("hi", "") {
		t.Error("MentionsBot: expected false for empty bot id")
	}
}

func TestLooksLikeUserID(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"U01ABC123": true,
		"UBOT":      true,
		"D012345":   false,
		"C0DEF456":  false,
		"":          false,
		"u01abc":    false, // lowercase
	}
	for in, want := range tests {
		if got := LooksLikeUserID(in); got != want {
			t.Errorf("LooksLikeUserID(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsDM(t *testing.T) {
	t.Parallel()
	if !IsDM("D012345") {
		t.Error("D-prefix should be DM")
	}
	if IsDM("C012345") {
		t.Error("C-prefix should not be DM")
	}
	if IsDM("") {
		t.Error("empty should not be DM")
	}
}
