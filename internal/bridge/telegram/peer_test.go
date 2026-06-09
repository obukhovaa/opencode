package telegram

import "testing"

func TestIsPeerID(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"12345":         true,
		"-100123":       true, // supergroup chat_id
		"0":             true,
		"":              false,
		"@username":     false,
		"abc":           false,
		"12.5":          false,
		"  12345  ":     true, // whitespace tolerated
		"12345\nignore": false,
	}
	for in, want := range tests {
		if got := IsPeerID(in); got != want {
			t.Errorf("IsPeerID(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParsePeerID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		want   int64
		errExp bool
	}{
		{"12345", 12345, false},
		{"-100123456789", -100123456789, false},
		{"0", 0, false},
		{"@username", 0, true},
		{"", 0, true},
		{"  -777  ", -777, false},
	}
	for _, c := range cases {
		got, err := ParsePeerID(c.in)
		if c.errExp {
			if err == nil {
				t.Errorf("ParsePeerID(%q) expected err", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParsePeerID(%q) = %d/%v, want %d/nil", c.in, got, err, c.want)
		}
	}
}

func TestStripMention(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, bot, want string
	}{
		{"@routerbot hello", "routerbot", "hello"},
		{"hello @routerbot world", "routerbot", "hello  world"},
		{"@ROUTERBOT hi", "routerbot", "hi"}, // case-insensitive
		{"no mention", "routerbot", "no mention"},
		{"@routerbot", "routerbot", ""},
		{"@routerbot hello", "", "@routerbot hello"}, // empty botUsername → noop
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
	tests := []struct {
		in, bot string
		want    bool
	}{
		{"hello @routerbot", "routerbot", true},
		{"@ROUTERBOT hi", "routerbot", true},
		{"plain text", "routerbot", false},
		{"hi @other", "routerbot", false},
		{"hi", "", false},
	}
	for _, c := range tests {
		got := MentionsBot(c.in, c.bot)
		if got != c.want {
			t.Errorf("MentionsBot(%q, %q) = %v, want %v", c.in, c.bot, got, c.want)
		}
	}
}

func TestExtractPairingCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"/pair abc123", "abc123"},
		{"/pair@routerbot abc123", "abc123"},
		{"/PAIR abc", "abc"}, // case-insensitive command
		{"/pair", ""},        // no code
		{"hello", ""},
		{"", ""},
	}
	for _, c := range cases {
		got := extractPairingCode(c.in)
		if got != c.want {
			t.Errorf("extractPairingCode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHashPairingCode(t *testing.T) {
	t.Parallel()
	// SHA256("hello") = 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
	got := hashPairingCode("hello")
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Errorf("hashPairingCode(hello) = %q, want %q", got, want)
	}
}
