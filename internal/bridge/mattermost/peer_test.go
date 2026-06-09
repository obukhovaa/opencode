package mattermost

import "testing"

func TestPeerIDEncodingDM(t *testing.T) {
	t.Parallel()
	if got := ParsePeerID("abcdef123456"); got != (Peer{ChannelID: "abcdef123456"}) {
		t.Errorf("Parse DM = %+v", got)
	}
	if got := FormatPeerID(Peer{ChannelID: "abcdef123456"}); got != "abcdef123456" {
		t.Errorf("Format DM = %q", got)
	}
}

func TestPeerIDEncodingThread(t *testing.T) {
	t.Parallel()
	want := Peer{ChannelID: "ch123", RootPostID: "post456"}
	if got := ParsePeerID("ch123|post456"); got != want {
		t.Errorf("Parse thread = %+v", got)
	}
	if got := FormatPeerID(want); got != "ch123|post456" {
		t.Errorf("Format thread = %q", got)
	}
}

func TestPeerIDEncodingEmpty(t *testing.T) {
	t.Parallel()
	if got := ParsePeerID(""); got != (Peer{}) {
		t.Errorf("Parse empty = %+v", got)
	}
	if got := ParsePeerID("  "); got != (Peer{}) {
		t.Errorf("Parse whitespace = %+v", got)
	}
}

func TestStripMentionRemovesBotMentionAndLeadingPunct(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, bot, want string
	}{
		{"@mybot hello", "mybot", "hello"},
		{"@mybot: hello", "mybot", "hello"},
		{"@mybot - hello", "mybot", "hello"},
		{"hello @mybot world", "mybot", "hello   world"},
		{"hello", "mybot", "hello"},
		{"@mybot", "mybot", ""},
	}
	for _, tt := range tests {
		got := StripMention(tt.in, tt.bot)
		if got != tt.want {
			t.Errorf("StripMention(%q, %q) = %q, want %q", tt.in, tt.bot, got, tt.want)
		}
	}
}

func TestStripMentionNoBotUsername(t *testing.T) {
	t.Parallel()
	if got := StripMention("@mybot hello", ""); got != "@mybot hello" {
		t.Errorf("got %q", got)
	}
}

func TestStripMentionMultipleMentions(t *testing.T) {
	t.Parallel()
	// Each @mybot becomes a space; collapsed whitespace is preserved as-is
	// in the spec's stripMattermostMention (which only trims leading/trailing).
	got := StripMention("@mybot @mybot @mybot hello", "mybot")
	// TS impl: ` ${a}.split(token).join(' ')` then trim leading punct & whitespace.
	// "@mybot @mybot @mybot hello" → "   hello" (each token replaced, but
	// the StripMention regex trims everything leading)
	// After our implementation: spaces remain in the middle if any.
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestStripMentionEmptyText(t *testing.T) {
	t.Parallel()
	if got := StripMention("", "mybot"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestLooksLikeUserID(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"abcdefghijklmnopqrstuvwxyz":   true,  // 26 lowercase chars
		"abcdefghijklmnopqrstuvwxy":    false, // 25 chars
		"abcdefghijklmnopqrstuvwxyzz":  false, // 27 chars
		"abcdefghijklmnopqrstu1234567": false, // 27 chars
		"abcdefghijklmnopqrstu12345":   true,  // 26 chars including digits
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ":   false, // uppercase
		"":                             false,
		"D012345":                      false, // too short, uppercase
	}
	for in, want := range tests {
		if got := LooksLikeUserID(in); got != want {
			t.Errorf("LooksLikeUserID(%q) = %v, want %v", in, got, want)
		}
	}
}
