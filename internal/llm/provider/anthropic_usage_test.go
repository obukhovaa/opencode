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
