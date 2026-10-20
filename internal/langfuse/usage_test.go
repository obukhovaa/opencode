package langfuse

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

// Thinking is a breakdown of Output, so it is reported beside the totals
// and never folded into them; zero leaves the key out.
func TestSetUsage_Thinking(t *testing.T) {
	cases := []struct {
		name     string
		thinking int64
		want     string
	}{
		{"reported", 61, "61"},
		{"absent", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, sr := newTestClient()
			span := c.GenerationStart(context.Background(), GenerationParams{Name: "gen", Model: "claude"})
			span.SetUsage(&Usage{Input: 10, Output: 108, Total: 118, Thinking: tc.thinking})
			span.End()

			raw, ok := attrValue(sr.Ended(), "gen", "langfuse.observation.usage_details")
			if !ok {
				t.Fatal("no usage_details attribute")
			}
			if got := gjson.Get(raw, "thinking").Raw; got != tc.want {
				t.Errorf("usage_details.thinking = %q, want %q (usage_details = %s)", got, tc.want, raw)
			}
			if got := gjson.Get(raw, "output").Int(); got != 108 {
				t.Errorf("usage_details.output = %d, want 108", got)
			}
			if got := gjson.Get(raw, "total").Int(); got != 118 {
				t.Errorf("usage_details.total = %d, want 118", got)
			}
		})
	}
}
