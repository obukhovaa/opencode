package service

import "testing"

func TestSplitChatCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		wantCmd  string
		wantArgs string
	}{
		{"/help", "help", ""},
		{"/model claude-sonnet-4-5", "model", "claude-sonnet-4-5"},
		{"/sessions  ", "sessions", ""},
		{"/agent  hivemind  ", "agent", "hivemind"},
		{"/reset\nfollow-up", "reset", "follow-up"},
		{"", "", ""},
		{"hi", "", ""}, // not a command
	}
	for _, c := range cases {
		cmd, args := splitChatCommand(c.in)
		if cmd != c.wantCmd || args != c.wantArgs {
			t.Errorf("split(%q) = (%q, %q), want (%q, %q)", c.in, cmd, args, c.wantCmd, c.wantArgs)
		}
	}
}
