package provider

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestToolSearchRefsFromStartEvent(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantID  string
		wantRef []string
	}{
		{
			name:    "success with references",
			raw:     `{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"gitlab_get_merge_request"},{"type":"tool_reference","tool_name":"gitlab_get_merge_request_diffs"}]}}`,
			wantID:  "srvtoolu_1",
			wantRef: []string{"gitlab_get_merge_request", "gitlab_get_merge_request_diffs"},
		},
		{
			name:    "error result carries no references",
			raw:     `{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_2","content":{"type":"tool_search_tool_result_error","error_code":"too_many_requests"}}`,
			wantID:  "srvtoolu_2",
			wantRef: nil,
		},
		{
			name:    "missing tool_use_id",
			raw:     `{"type":"tool_search_tool_result","content":{"tool_references":[{"tool_name":"x"}]}}`,
			wantID:  "",
			wantRef: []string{"x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, names := toolSearchRefsFromStartEvent(tt.raw)
			if id != tt.wantID {
				t.Errorf("id = %q, want %q", id, tt.wantID)
			}
			if !reflect.DeepEqual(names, tt.wantRef) {
				t.Errorf("names = %v, want %v", names, tt.wantRef)
			}
		})
	}
}

func TestApplyStreamedToolSearchRefs(t *testing.T) {
	streamed := map[string][]string{
		"srvtoolu_1": {"gitlab_get_merge_request", "gitlab_get_merge_request_diffs"},
		"srvtoolu_9": {"unused"},
	}
	parts := []message.ToolSearchContent{
		{ToolUseID: "srvtoolu_1", Name: "tool_search_tool_regex"},                                 // empty refs -> backfilled
		{ToolUseID: "srvtoolu_2", Name: "tool_search_tool_regex", References: []string{"kept"}},   // already has refs -> untouched
		{ToolUseID: "srvtoolu_3", Name: "tool_search_tool_regex", ErrorCode: "too_many_requests"}, // error -> untouched
	}
	out := applyStreamedToolSearchRefs(parts, streamed)

	if got := out[0].References; !reflect.DeepEqual(got, []string{"gitlab_get_merge_request", "gitlab_get_merge_request_diffs"}) {
		t.Errorf("part[0] refs = %v, want the two captured tools", got)
	}
	if got := out[1].References; !reflect.DeepEqual(got, []string{"kept"}) {
		t.Errorf("part[1] refs = %v, want existing refs preserved", got)
	}
	if len(out[2].References) != 0 {
		t.Errorf("part[2] refs = %v, want error part untouched", out[2].References)
	}

	// No-op when nothing was captured.
	orig := []message.ToolSearchContent{{ToolUseID: "srvtoolu_1"}}
	if out := applyStreamedToolSearchRefs(orig, map[string][]string{}); len(out[0].References) != 0 {
		t.Errorf("empty streamed map must be a no-op, got %v", out[0].References)
	}
}

// TestStreamingRecoversDroppedToolSearchRefs is the end-to-end guard for the
// streaming reference-capture fix: it drives the exact content_block_* events
// through the SDK accumulator (which drops tool_references), runs the same raw
// capture the stream loop performs, and asserts the merged result carries the
// references. If a future SDK bump stops dropping them, toolSearchParts alone
// already returns the refs and the merge is a harmless no-op — the test still
// passes.
func TestStreamingRecoversDroppedToolSearchRefs(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"tool_search_tool_regex","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"pattern\":\"gitlab\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"gitlab_get_merge_request"},{"type":"tool_reference","tool_name":"gitlab_get_merge_request_diffs"}]}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
	}

	acc := &anthropic.Message{}
	streamedRefs := map[string][]string{}
	for _, raw := range events {
		var ev anthropic.MessageStreamEventUnion
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if err := acc.Accumulate(ev); err != nil {
			t.Fatalf("accumulate: %v", err)
		}
		// Mirror the stream loop's capture.
		if start, ok := ev.AsAny().(anthropic.ContentBlockStartEvent); ok &&
			start.ContentBlock.Type == "tool_search_tool_result" {
			if id, names := toolSearchRefsFromStartEvent(start.ContentBlock.RawJSON()); id != "" && len(names) > 0 {
				streamedRefs[id] = names
			}
		}
	}

	a := &anthropicClient{}
	parts := applyStreamedToolSearchRefs(a.toolSearchParts(*acc), streamedRefs)
	if len(parts) != 1 {
		t.Fatalf("expected 1 tool-search part, got %d", len(parts))
	}
	if want := []string{"gitlab_get_merge_request", "gitlab_get_merge_request_diffs"}; !reflect.DeepEqual(parts[0].References, want) {
		t.Fatalf("recovered references = %v, want %v", parts[0].References, want)
	}
}
