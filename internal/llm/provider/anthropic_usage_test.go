package provider

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// GENAI-414: a Bedrock stream through LiteLLM repeats the cumulative input
// and cache counts on message_delta. The accumulator must take them once, not
// add them to message_start's, or every call reports a doubled context. This
// pins that the doubled cache_read seen in Langfuse is not produced here.
func TestAnthropicUsage_MessageDeltaRepeatsCumulativeCounts(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3990,"cache_creation_input_tokens":31338,"cache_read_input_tokens":692129,"output_tokens":4}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":3990,"cache_creation_input_tokens":31338,"cache_read_input_tokens":692129,"output_tokens":120}}`,
		`{"type":"message_stop"}`,
	}
	var msg anthropic.Message
	for _, raw := range events {
		var ev anthropic.MessageStreamEventUnion
		if err := ev.UnmarshalJSON([]byte(raw)); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if err := msg.Accumulate(ev); err != nil {
			t.Fatalf("accumulate %s: %v", raw, err)
		}
	}
	got := (&anthropicClient{}).usage(msg)
	want := TokenUsage{InputTokens: 3990, CacheCreationTokens: 31338, CacheReadTokens: 692129, OutputTokens: 120}
	if got != want {
		t.Errorf("usage = %+v, want %+v", got, want)
	}
}

// GENAI-450: a response that ran a server-side tool search sampled twice, and
// its usage sums both passes. These are the shapes the LiteLLM Bedrock route
// returned for one search over a 9.6K prompt, cold and then warm: the last
// pass, the context the next request continues from, is half the input side,
// and the output stays whole.
func TestAnthropicUsage_ToolSearchReportsTheLastPass(t *testing.T) {
	const searchContent = `[
		{"type":"server_tool_use","id":"srvtoolu_1","name":"tool_search_tool_regex","input":{"query":"weather"}},
		{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"get_weather"}]}},
		{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Oslo"}}
	]`
	cases := []struct {
		name    string
		content string
		usage   string
		want    *TokenUsage
	}{
		{
			name:    "cold cache: pass one writes the prefix, pass two reads it",
			content: searchContent,
			usage:   `{"input_tokens":355,"cache_creation_input_tokens":9612,"cache_read_input_tokens":9612,"output_tokens":108}`,
			want:    &TokenUsage{InputTokens: 177, CacheCreationTokens: 4806, CacheReadTokens: 4806, OutputTokens: 108},
		},
		{
			name:    "warm cache: both passes read the prefix",
			content: searchContent,
			usage:   `{"input_tokens":355,"cache_creation_input_tokens":0,"cache_read_input_tokens":19224,"output_tokens":108}`,
			want:    &TokenUsage{InputTokens: 177, CacheReadTokens: 9612, OutputTokens: 108},
		},
		{
			name:    "per-pass iterations are read when the upstream sends them",
			content: searchContent,
			usage: `{"input_tokens":355,"cache_creation_input_tokens":0,"cache_read_input_tokens":19224,"output_tokens":108,"iterations":[
				{"type":"message","input_tokens":170,"cache_creation_input_tokens":0,"cache_read_input_tokens":9612,"output_tokens":40},
				{"type":"message","input_tokens":185,"cache_creation_input_tokens":0,"cache_read_input_tokens":9612,"output_tokens":68}]}`,
			want: &TokenUsage{InputTokens: 185, CacheReadTokens: 9612, OutputTokens: 68},
		},
		{
			name: "two searches in one pass are one boundary",
			content: `[
				{"type":"server_tool_use","id":"srvtoolu_1","name":"tool_search_tool_regex","input":{"query":"a"}},
				{"type":"server_tool_use","id":"srvtoolu_2","name":"tool_search_tool_regex","input":{"query":"b"}},
				{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"tool_search_tool_search_result","tool_references":[]}},
				{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_2","content":{"type":"tool_search_tool_search_result","tool_references":[]}},
				{"type":"text","text":"done"}
			]`,
			usage: `{"input_tokens":300,"cache_creation_input_tokens":0,"cache_read_input_tokens":20000,"output_tokens":90}`,
			want:  &TokenUsage{InputTokens: 150, CacheReadTokens: 10000, OutputTokens: 90},
		},
		{
			name: "a search the turn paused on has no pass after it",
			content: `[
				{"type":"server_tool_use","id":"srvtoolu_1","name":"tool_search_tool_regex","input":{"query":"a"}},
				{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"tool_search_tool_search_result","tool_references":[]}}
			]`,
			usage: `{"input_tokens":300,"cache_creation_input_tokens":0,"cache_read_input_tokens":10000,"output_tokens":40}`,
		},
		{
			name:    "a single pass names no last pass",
			content: `[{"type":"text","text":"ok"}]`,
			usage:   `{"input_tokens":300,"cache_creation_input_tokens":0,"cache_read_input_tokens":10000,"output_tokens":40}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"id":"msg_1","type":"message","role":"assistant","model":"m","stop_reason":"tool_use","content":` + tc.content + `,"usage":` + tc.usage + `}`
			var msg anthropic.Message
			if err := msg.UnmarshalJSON([]byte(raw)); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := (&anthropicClient{}).usage(msg)
			if got.InputTokens != msg.Usage.InputTokens || got.CacheReadTokens != msg.Usage.CacheReadInputTokens {
				t.Errorf("billed counts changed: %+v", got)
			}
			switch {
			case tc.want == nil && got.LastPass != nil:
				t.Errorf("LastPass = %+v, want nil", *got.LastPass)
			case tc.want != nil && got.LastPass == nil:
				t.Errorf("LastPass = nil, want %+v", *tc.want)
			case tc.want != nil && *got.LastPass != *tc.want:
				t.Errorf("LastPass = %+v, want %+v", *got.LastPass, *tc.want)
			}
		})
	}
}
