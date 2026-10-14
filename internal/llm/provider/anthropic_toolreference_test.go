package provider

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

// convertMessagesUnfiltered converts without the declared-tools filter, the
// shape every request had before references were checked against tools[].
func (a *anthropicClient) convertMessagesUnfiltered(messages []message.Message) []anthropic.MessageParam {
	out, _ := a.convertMessages(messages, nil)
	return out
}

func declaredSet(names ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set
}

func nativeToolSearchClient() *anthropicClient {
	return &anthropicClient{
		options: anthropicOptions{disableCache: true},
		providerOptions: providerClientOptions{model: models.Model{
			ID:                       "test-native",
			APIModel:                 "test-native",
			SupportsToolSearch:       true,
			SupportsAdaptiveThinking: true,
		}},
	}
}

// searchTurn is a native tool-search assistant turn whose search sits between
// two signed thinking blocks and is followed by a tool call.
func searchTurn(refs ...string) message.Message {
	offset := 1
	return newMsg(message.Assistant,
		message.ReasoningContent{Thinking: "need tools", Signature: "sig-1"},
		message.ReasoningContent{Thinking: "found them", Signature: "sig-2"},
		message.ToolSearchContent{
			ToolUseID:       "srvtoolu_1",
			Name:            "tool_search_tool_regex",
			Input:           `{"pattern":".*"}`,
			References:      refs,
			ReasoningOffset: &offset,
		},
		message.ToolCall{ID: "toolu_1", Name: "datadog_search_logs", Input: `{}`, Finished: true},
	)
}

func closedSearchHistory(refs ...string) []message.Message {
	return []message.Message{
		newMsg(message.User, message.TextContent{Text: "check the logs"}),
		searchTurn(refs...),
		newMsg(message.Tool, message.ToolResult{ToolCallID: "toolu_1", Name: "datadog_search_logs", Content: "ok"}),
		newMsg(message.Assistant, message.TextContent{Text: "done"}),
		newMsg(message.User, message.TextContent{Text: "next"}),
	}
}

func searchReferences(mp anthropic.MessageParam) [][]string {
	var out [][]string
	for _, b := range mp.Content {
		if b.OfToolSearchToolResult == nil {
			continue
		}
		raw, _ := json.Marshal(b.OfToolSearchToolResult)
		var decoded struct {
			Content struct {
				ToolReferences []struct {
					ToolName string `json:"tool_name"`
				} `json:"tool_references"`
			} `json:"content"`
		}
		_ = json.Unmarshal(raw, &decoded)
		var names []string
		for _, r := range decoded.Content.ToolReferences {
			names = append(names, r.ToolName)
		}
		out = append(out, names)
	}
	return out
}

func countThinking(msgs []anthropic.MessageParam) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.OfThinking != nil || b.OfRedactedThinking != nil {
				n++
			}
		}
	}
	return n
}

func TestConvertMessagesToolReferenceFilter(t *testing.T) {
	tests := []struct {
		name                                     string
		declared                                 map[string]struct{}
		wantThinking, wantServerToolUse, wantTSR int
		wantRefs                                 [][]string
	}{
		{
			name:         "all declared: in-order replay keeps thinking",
			declared:     declaredSet("datadog_search_logs", "jarvis-memory_search"),
			wantThinking: 2, wantServerToolUse: 1, wantTSR: 1,
			wantRefs: [][]string{{"datadog_search_logs", "jarvis-memory_search"}},
		},
		{
			name:         "partially declared: declared refs kept, thinking dropped",
			declared:     declaredSet("datadog_search_logs"),
			wantThinking: 0, wantServerToolUse: 1, wantTSR: 1,
			wantRefs: [][]string{{"datadog_search_logs"}},
		},
		{
			name:         "none declared: search and server_tool_use removed, thinking dropped",
			declared:     declaredSet("view"),
			wantThinking: 0, wantServerToolUse: 0, wantTSR: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := nativeToolSearchClient()
			out, disableThinking := a.convertMessages(closedSearchHistory("datadog_search_logs", "jarvis-memory_search"), tt.declared)
			if disableThinking {
				t.Fatalf("disableThinking = true for a closed tool loop")
			}
			turn := out[1]
			th, srv, tsr, tu := countReplayBlocks(turn)
			if th != tt.wantThinking || srv != tt.wantServerToolUse || tsr != tt.wantTSR || tu != 1 {
				t.Errorf("blocks thinking=%d server_tool_use=%d tsr=%d tool_use=%d, want %d/%d/%d/1",
					th, srv, tsr, tu, tt.wantThinking, tt.wantServerToolUse, tt.wantTSR)
			}
			if got := searchReferences(turn); !reflect.DeepEqual(got, tt.wantRefs) {
				t.Errorf("references = %v, want %v", got, tt.wantRefs)
			}
		})
	}
}

func TestConvertMessagesAllDeclaredIsUnchanged(t *testing.T) {
	a := nativeToolSearchClient()
	history := closedSearchHistory("datadog_search_logs", "jarvis-memory_search")
	want, _ := json.Marshal(a.convertMessagesUnfiltered(history))
	got, disableThinking := a.convertMessages(history, declaredSet("datadog_search_logs", "jarvis-memory_search", "view"))
	gotJSON, _ := json.Marshal(got)
	if string(gotJSON) != string(want) || disableThinking {
		t.Fatalf("all-declared history changed:\n got  %s\n want %s", gotJSON, want)
	}
}

func TestConvertMessagesDoesNotMutateHistory(t *testing.T) {
	a := nativeToolSearchClient()
	history := closedSearchHistory("datadog_search_logs", "jarvis-memory_search")
	_, _ = a.convertMessages(history, declaredSet("view"))
	if refs := history[1].ToolSearchParts()[0].References; len(refs) != 2 {
		t.Fatalf("persisted references mutated: %v", refs)
	}
}

func TestConvertMessagesOpenToolLoopDisablesThinking(t *testing.T) {
	a := nativeToolSearchClient()
	earlier := newMsg(message.Assistant,
		message.ReasoningContent{Thinking: "earlier", Signature: "sig-0"},
		message.TextContent{Text: "hi"},
	)
	open := []message.Message{
		newMsg(message.User, message.TextContent{Text: "hello"}),
		earlier,
		newMsg(message.User, message.TextContent{Text: "check the logs"}),
		searchTurn("datadog_search_logs"),
		newMsg(message.Tool, message.ToolResult{ToolCallID: "toolu_1", Name: "datadog_search_logs", Content: "ok"}),
	}

	params, disableThinking := a.requestFor(context.Background(), open, []tools.BaseTool{newTestTool("view", false)})
	if !disableThinking {
		t.Fatal("disableThinking = false for an open tool loop that lost a reference")
	}
	if params.Thinking.OfAdaptive != nil || params.Thinking.OfEnabled != nil {
		t.Errorf("thinking config sent on the open-loop request: %+v", params.Thinking)
	}
	if n := countThinking(params.Messages); n != 0 {
		t.Errorf("thinking blocks in open-loop request = %d, want 0", n)
	}

	// The model answered: the loop is closed and thinking comes back.
	closed := append(append([]message.Message{}, open...),
		newMsg(message.Assistant,
			message.ReasoningContent{Thinking: "answer", Signature: "sig-3"},
			message.TextContent{Text: "logs look fine"},
		),
		newMsg(message.User, message.TextContent{Text: "thanks"}),
	)
	params, disableThinking = a.requestFor(context.Background(), closed, []tools.BaseTool{newTestTool("view", false)})
	if disableThinking {
		t.Fatal("disableThinking still true after the loop closed")
	}
	if params.Thinking.OfAdaptive == nil {
		t.Errorf("thinking not re-enabled on the next request")
	}
	if n := countThinking(params.Messages); n == 0 {
		t.Errorf("thinking blocks not replayed after the loop closed")
	}
}

func TestConvertMessagesOpenToolLoopAllDeclaredKeepsThinking(t *testing.T) {
	a := nativeToolSearchClient()
	open := []message.Message{
		newMsg(message.User, message.TextContent{Text: "check the logs"}),
		searchTurn("datadog_search_logs"),
		newMsg(message.Tool, message.ToolResult{ToolCallID: "toolu_1", Name: "datadog_search_logs", Content: "ok"}),
	}
	params, disableThinking := a.requestFor(context.Background(), open, []tools.BaseTool{newTestTool("datadog_search_logs", false)})
	if disableThinking || params.Thinking.OfAdaptive == nil {
		t.Fatalf("thinking disabled although every reference is declared")
	}
}

func TestDeclaredToolNamesIncludesDeferred(t *testing.T) {
	tl := []anthropic.ToolUnionParam{
		{OfTool: &anthropic.ToolParam{Name: "view"}},
		{OfTool: &anthropic.ToolParam{Name: "datadog_search_logs", DeferLoading: anthropic.Bool(true)}},
		{OfToolSearchToolRegex20251119: &anthropic.ToolSearchToolRegex20251119Param{}},
	}
	got := declaredToolNames(tl)
	if !reflect.DeepEqual(got, declaredSet("view", "datadog_search_logs")) {
		t.Fatalf("declared = %v", got)
	}
}

func TestRequestOptionsDropsInterleavedThinkingBeta(t *testing.T) {
	if got := withoutInterleavedThinkingBeta("interleaved-thinking-2025-05-14, context-1m-2025-08-07"); got != "context-1m-2025-08-07" {
		t.Errorf("withoutInterleavedThinkingBeta = %q", got)
	}
	if got := withoutInterleavedThinkingBeta("interleaved-thinking-2025-05-14"); got != "" {
		t.Errorf("withoutInterleavedThinkingBeta = %q, want empty", got)
	}

	a := &anthropicClient{betaHeader: "interleaved-thinking-2025-05-14"}
	if n := len(a.requestOptions(false)); n != 0 {
		t.Errorf("requestOptions(false) = %d options, want 0", n)
	}
	if n := len(a.requestOptions(true)); n != 1 {
		t.Errorf("requestOptions(true) = %d options, want 1 (header removal)", n)
	}
	a = &anthropicClient{betaHeader: "context-1m-2025-08-07"}
	if n := len(a.requestOptions(true)); n != 0 {
		t.Errorf("requestOptions(true) without the beta = %d options, want 0", n)
	}
}

// TestCountTokensFiltersUndeclaredReferences keeps the count_tokens payload
// in line with the request: an undeclared reference must not reach it either.
func TestCountTokensFiltersUndeclaredReferences(t *testing.T) {
	srv, _, lastBody := countTokensTestServer(t, 200, `{"input_tokens": 10}`)
	client := newAnthropicClient(providerClientOptions{
		apiKey:  "test-key",
		baseURL: srv.URL,
		model:   models.BedrockAnthropicModels[models.BedrockEUSonnet5],
	}).(*anthropicClient)

	history := closedSearchHistory("datadog_search_logs", "jarvis-memory_search")
	if _, err := client.countTokens(context.Background(), history, []tools.BaseTool{newTestTool("datadog_search_logs", false)}); err != nil {
		t.Fatalf("countTokens: %v", err)
	}
	body := lastBody()
	if strings.Contains(body, "jarvis-memory_search") {
		t.Errorf("count_tokens body carries the undeclared reference: %s", body)
	}
	if !strings.Contains(body, "datadog_search_logs") {
		t.Errorf("count_tokens body lost the declared reference: %s", body)
	}
}
