package bridge

import "testing"

// TestPrependMentionIfMissing locks the double-mention fix: when the
// agent's text already starts with the binding's mention (because the
// auto-injected interactive step prompt told it to), the adapter does
// NOT prepend a second copy. When the agent forgets, the adapter
// supplies the prefix as a safety net.
func TestPrependMentionIfMissing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mention string
		text    string
		want    string
	}{
		{
			name:    "empty mention passes text through",
			mention: "",
			text:    "Hi reviewer!",
			want:    "Hi reviewer!",
		},
		{
			name:    "agent typed the mention — no double prefix",
			mention: "@alice",
			text:    "@alice Hi! I'm scoping the change.",
			want:    "@alice Hi! I'm scoping the change.",
		},
		{
			name:    "agent forgot the mention — safety-net prepend",
			mention: "@alice",
			text:    "Hi! I'm scoping the change.",
			want:    "@alice Hi! I'm scoping the change.",
		},
		{
			name:    "leading whitespace before agent's mention is tolerated",
			mention: "@alice",
			text:    "   @alice hello",
			want:    "   @alice hello",
		},
		{
			name:    "empty text with mention emits just the mention",
			mention: "@alice",
			text:    "",
			want:    "@alice",
		},
		{
			name:    "different agent prefix triggers the prepend",
			mention: "@alice",
			text:    "@bob hi",
			want:    "@alice @bob hi",
		},
		{
			name:    "Slack-style angle-bracket mention",
			mention: "<@U123>",
			text:    "<@U123> Hi! Welcome.",
			want:    "<@U123> Hi! Welcome.",
		},
		{
			name:    "Slack mention forgotten by agent",
			mention: "<@U123>",
			text:    "Hi! Welcome.",
			want:    "<@U123> Hi! Welcome.",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := PrependMentionIfMissing(tc.mention, tc.text)
			if got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}
