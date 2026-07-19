package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
)

func newReasoningTestClient(t *testing.T) *anthropicClient {
	t.Helper()
	client, ok := newAnthropicClient(providerClientOptions{
		apiKey: "test-key",
	}).(*anthropicClient)
	if !ok {
		t.Fatal("newAnthropicClient did not return *anthropicClient")
	}
	return client
}

func TestConvertMessagesReplaysSignedThinkingVerbatim(t *testing.T) {
	a := newReasoningTestClient(t)

	msgs := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "do the thing"},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: "I'll check A first; if empty then B", Signature: "sig-1"},
				message.TextContent{Text: "checking"},
				message.ToolCall{ID: "tc1", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
			},
		},
	}

	converted := a.convertMessages(msgs)
	if len(converted) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(converted))
	}
	blocks := converted[1].Content
	if len(blocks) != 3 {
		t.Fatalf("expected thinking+text+tool_use blocks, got %d", len(blocks))
	}
	th := blocks[0].OfThinking
	if th == nil {
		t.Fatalf("first block is not a thinking block: %+v", blocks[0])
	}
	if th.Thinking != "I'll check A first; if empty then B" || th.Signature != "sig-1" {
		t.Fatalf("thinking block not replayed verbatim: %+v", th)
	}
	if blocks[1].OfText == nil || blocks[2].OfToolUse == nil {
		t.Fatalf("expected text then tool_use after thinking, got %+v", blocks)
	}
}

func TestConvertMessagesSkipsUnsignedReasoning(t *testing.T) {
	a := newReasoningTestClient(t)

	msgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				// Streamed preview / legacy row: no signature.
				message.ReasoningContent{Thinking: "unsigned preview"},
				message.TextContent{Text: "answer"},
			},
		},
	}

	converted := a.convertMessages(msgs)
	if len(converted) != 1 {
		t.Fatalf("expected 1 message, got %d", len(converted))
	}
	if len(converted[0].Content) != 1 || converted[0].Content[0].OfText == nil {
		t.Fatalf("expected only the text block, got %+v", converted[0].Content)
	}
}

func TestConvertMessagesReplaysRedactedThinking(t *testing.T) {
	a := newReasoningTestClient(t)

	msgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{Redacted: true, Data: "opaque=="},
				message.TextContent{Text: "answer"},
			},
		},
	}

	converted := a.convertMessages(msgs)
	blocks := converted[0].Content
	if len(blocks) != 2 {
		t.Fatalf("expected redacted+text blocks, got %d", len(blocks))
	}
	rt := blocks[0].OfRedactedThinking
	if rt == nil || rt.Data != "opaque==" {
		t.Fatalf("redacted block not replayed verbatim: %+v", blocks[0])
	}
}

func TestConvertMessagesSkipsForeignProviderReasoning(t *testing.T) {
	// A signed block minted by a different provider family (mid-session
	// model switch, summarizer on other-provider history) must not be
	// replayed: cross-vendor signature handling is undocumented and
	// absence merely forfeits continuity.
	a, ok := newAnthropicClient(providerClientOptions{
		apiKey: "test-key",
		model:  models.SupportedModels[models.Claude46Opus],
	}).(*anthropicClient)
	if !ok {
		t.Fatal("newAnthropicClient did not return *anthropicClient")
	}

	kimiSigned := message.Message{
		Role:  message.Assistant,
		Model: models.KimiK3,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "kimi thoughts", Signature: "kimi-sig"},
			message.TextContent{Text: "answer"},
		},
	}
	converted := a.convertMessages([]message.Message{kimiSigned})
	if len(converted[0].Content) != 1 || converted[0].Content[0].OfText == nil {
		t.Fatalf("foreign-provider reasoning must be skipped, got %+v", converted[0].Content)
	}

	// Same provider (any Claude model on the anthropic provider) replays.
	sameProvider := kimiSigned
	sameProvider.Model = models.Claude5Sonnet
	converted = a.convertMessages([]message.Message{sameProvider})
	if len(converted[0].Content) != 2 || converted[0].Content[0].OfThinking == nil {
		t.Fatalf("same-provider reasoning must replay, got %+v", converted[0].Content)
	}

	// Unknown/legacy model IDs keep replaying (rows predate model tracking).
	legacy := kimiSigned
	legacy.Model = ""
	converted = a.convertMessages([]message.Message{legacy})
	if len(converted[0].Content) != 2 || converted[0].Content[0].OfThinking == nil {
		t.Fatalf("legacy rows without model must replay, got %+v", converted[0].Content)
	}
}

func TestPreparedMessagesEmptyHistoryDoesNotPanic(t *testing.T) {
	// convertMessages can return an empty slice (the only user message had
	// no renderable content); preparedMessages must not index into it.
	a := newReasoningTestClient(t)
	params := a.preparedMessages(context.Background(), nil, nil)
	if len(params.Messages) != 0 {
		t.Fatalf("expected empty messages passthrough, got %d", len(params.Messages))
	}
}

func TestConvertMessagesCacheLandsAfterReasoning(t *testing.T) {
	a := newReasoningTestClient(t)

	msgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: "think", Signature: "sig"},
				message.ToolCall{ID: "tc1", Name: "bash", Input: `{}`, Finished: true},
			},
		},
	}

	converted := a.convertMessages(msgs)
	blocks := converted[0].Content
	if len(blocks) != 2 {
		t.Fatalf("expected thinking+tool_use, got %d blocks", len(blocks))
	}
	// The message is within the trailing cache window (single message), so
	// the breakpoint must land on the last block — the tool_use — and the
	// thinking block must carry no cache marker (the param type has none).
	tu := blocks[1].OfToolUse
	if tu == nil {
		t.Fatalf("expected tool_use last, got %+v", blocks[1])
	}
	if tu.CacheControl.Type == "" {
		t.Fatal("expected cache_control on the last (tool_use) block")
	}
}

func TestReasoningPartsExtraction(t *testing.T) {
	a := newReasoningTestClient(t)

	// Decode from wire JSON: the SDK's union accessors re-parse from the
	// captured raw JSON, so hand-built structs would come back empty.
	var resp anthropic.Message
	wire := `{"role":"assistant","content":[
		{"type":"thinking","thinking":"block one","signature":"s1"},
		{"type":"text","text":"interleaved"},
		{"type":"thinking","thinking":"block two","signature":"s2"},
		{"type":"redacted_thinking","data":"opaque=="},
		{"type":"tool_use","id":"tc1","name":"bash","input":{}}
	]}`
	if err := json.Unmarshal([]byte(wire), &resp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	parts := a.reasoningParts(resp)
	if len(parts) != 3 {
		t.Fatalf("expected 3 reasoning parts, got %d", len(parts))
	}
	if parts[0].Thinking != "block one" || parts[0].Signature != "s1" {
		t.Fatalf("first block mangled: %+v", parts[0])
	}
	if parts[1].Thinking != "block two" || parts[1].Signature != "s2" {
		t.Fatalf("second block mangled: %+v", parts[1])
	}
	if !parts[2].Redacted || parts[2].Data != "opaque==" {
		t.Fatalf("redacted block mangled: %+v", parts[2])
	}
}

func TestReasoningPartsEmptyTextStillReplayed(t *testing.T) {
	// Providers that omit thinking display return signed blocks with empty
	// text; the replay contract says echo them including empty-text blocks.
	a := newReasoningTestClient(t)

	var resp anthropic.Message
	wire := `{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig-only"}]}`
	if err := json.Unmarshal([]byte(wire), &resp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	parts := a.reasoningParts(resp)
	if len(parts) != 1 || parts[0].Signature != "sig-only" {
		t.Fatalf("empty-text signed block not extracted: %+v", parts)
	}

	msgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: "", Signature: "sig-only"},
				message.TextContent{Text: "answer"},
			},
		},
	}
	converted := a.convertMessages(msgs)
	blocks := converted[0].Content
	if len(blocks) != 2 || blocks[0].OfThinking == nil || blocks[0].OfThinking.Signature != "sig-only" {
		t.Fatalf("empty-text signed block not replayed: %+v", blocks)
	}
}
