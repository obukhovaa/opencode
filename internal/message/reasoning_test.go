package message

import (
	"testing"
)

func TestReasoningPartsMarshalRoundTrip(t *testing.T) {
	parts := []ContentPart{
		ReasoningContent{Thinking: "plan: check config first", Signature: "sig-abc"},
		ReasoningContent{Redacted: true, Data: "opaque-payload=="},
		TextContent{Text: "answer"},
	}

	data, err := marshallParts(parts)
	if err != nil {
		t.Fatalf("marshallParts: %v", err)
	}
	got, err := unmarshallParts(data)
	if err != nil {
		t.Fatalf("unmarshallParts: %v", err)
	}
	if len(got) != len(parts) {
		t.Fatalf("expected %d parts, got %d", len(parts), len(got))
	}

	rc, ok := got[0].(ReasoningContent)
	if !ok {
		t.Fatalf("part 0: expected ReasoningContent, got %T", got[0])
	}
	if rc.Thinking != "plan: check config first" || rc.Signature != "sig-abc" || rc.Redacted {
		t.Fatalf("signed block did not round-trip verbatim: %+v", rc)
	}

	red, ok := got[1].(ReasoningContent)
	if !ok {
		t.Fatalf("part 1: expected ReasoningContent, got %T", got[1])
	}
	if !red.Redacted || red.Data != "opaque-payload==" || red.Thinking != "" {
		t.Fatalf("redacted block did not round-trip verbatim: %+v", red)
	}
}

func TestReasoningLegacyRowUnmarshal(t *testing.T) {
	// Rows persisted before signatures existed: only the thinking text.
	legacy := []byte(`[{"type":"reasoning","data":{"thinking":"old reasoning"}}]`)
	got, err := unmarshallParts(legacy)
	if err != nil {
		t.Fatalf("unmarshallParts(legacy): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 part, got %d", len(got))
	}
	rc := got[0].(ReasoningContent)
	if rc.Thinking != "old reasoning" || rc.Signature != "" || rc.Redacted {
		t.Fatalf("legacy row mangled: %+v", rc)
	}
}

func TestSetReasoningPartsReplacesPreview(t *testing.T) {
	msg := Message{Role: Assistant}
	// Simulate streamed preview deltas (merged, unsigned)...
	msg.AppendReasoningContent("partial thi")
	msg.AppendReasoningContent("nking")
	msg.AppendContent("final answer")
	msg.AddToolCall(ToolCall{ID: "tc1", Name: "bash"})

	blocks := []ReasoningContent{
		{Thinking: "block one", Signature: "s1"},
		{Thinking: "block two", Signature: "s2"},
	}
	msg.SetReasoningParts(blocks)

	got := msg.ReasoningParts()
	if len(got) != 2 {
		t.Fatalf("expected 2 reasoning parts, got %d", len(got))
	}
	if got[0].Signature != "s1" || got[1].Signature != "s2" {
		t.Fatalf("blocks not replaced in order: %+v", got)
	}
	// Reasoning must remain ahead of the text part (provider emission order).
	firstReasoning, firstText := -1, -1
	for i, p := range msg.Parts {
		switch p.(type) {
		case ReasoningContent:
			if firstReasoning == -1 {
				firstReasoning = i
			}
		case TextContent:
			firstText = i
		}
	}
	if firstReasoning == -1 || firstText == -1 || firstReasoning > firstText {
		t.Fatalf("reasoning parts not positioned before text: reasoning=%d text=%d", firstReasoning, firstText)
	}
	// Other parts survive.
	if len(msg.ToolCalls()) != 1 || msg.Content().Text != "final answer" {
		t.Fatalf("non-reasoning parts lost: %+v", msg.Parts)
	}
}

func TestSetReasoningPartsOnMessageWithoutPreview(t *testing.T) {
	msg := Message{Role: Assistant}
	msg.AppendContent("text only")
	msg.SetReasoningParts([]ReasoningContent{{Thinking: "t", Signature: "s"}})

	if len(msg.ReasoningParts()) != 1 {
		t.Fatalf("expected reasoning part to be inserted")
	}
	if _, ok := msg.Parts[0].(ReasoningContent); !ok {
		t.Fatalf("expected reasoning inserted ahead of text, parts: %+v", msg.Parts)
	}
}

func TestReasoningContentConcatenatesAcrossBlocks(t *testing.T) {
	msg := Message{Role: Assistant, Parts: []ContentPart{
		ReasoningContent{Thinking: "first ", Signature: "s1"},
		ReasoningContent{Thinking: "second", Signature: "s2"},
	}}
	if got := msg.ReasoningContent().Thinking; got != "first second" {
		t.Fatalf("expected concatenated thinking, got %q", got)
	}
}
