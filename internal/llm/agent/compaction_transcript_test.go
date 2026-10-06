package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
)

func TestSummarizerTranscript(t *testing.T) {
	big := strings.Repeat("a", summarizerToolPayloadMaxTokens*message.BytesPerTokenEta) + "MIDDLE" +
		strings.Repeat("b", summarizerToolPayloadMaxTokens*message.BytesPerTokenEta)
	history := []message.Message{
		textMsg(message.User, "find the bug"),
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "SECRET THOUGHTS", Signature: "sig"},
			message.ToolSearchContent{ToolUseID: "srv-1", Name: "tool_search_tool_regex", References: []string{"jira_get", "jira_search"}},
			message.TextContent{Text: "looking"},
			message.ToolCall{ID: "call-1", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call-1", Name: "bash", Content: big, IsError: true},
		}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "only reasoning"},
		}},
	}

	got, stats := summarizerTranscript(history)
	if len(got) != 3 {
		t.Fatalf("transcript has %d messages, want 3 (the reasoning-only one left out): %v", len(got), texts(got))
	}
	for i, m := range got {
		if len(m.Parts) != 1 {
			t.Fatalf("message %d has %d parts, want one text part", i, len(m.Parts))
		}
		if _, ok := m.Parts[0].(message.TextContent); !ok {
			t.Fatalf("message %d part is %T, want text", i, m.Parts[0])
		}
		if m.Role != history[i].Role {
			t.Errorf("message %d role = %s, want %s kept", i, m.Role, history[i].Role)
		}
	}
	assistant := firstText(got[1])
	for _, want := range []string{"[tool_search] found tools: jira_get, jira_search", "looking", `[tool_call bash id=call-1] {"command":"ls"}`} {
		if !strings.Contains(assistant, want) {
			t.Errorf("assistant entry %q does not contain %q", assistant, want)
		}
	}
	if strings.Contains(assistant, "SECRET THOUGHTS") {
		t.Errorf("reasoning leaked into the transcript: %q", assistant)
	}
	result := firstText(got[2])
	if !strings.HasPrefix(result, "[tool_result bash id=call-1] (error) aaa") || !strings.HasSuffix(result, "bbb") {
		t.Errorf("tool result entry = %.80q..., want the header, then head and tail", result)
	}
	if strings.Contains(result, "MIDDLE") || !strings.Contains(result, "tokens omitted ...]") {
		t.Errorf("oversized tool result was not cut in the middle")
	}
	if est := message.EstimateTokens(got[2:], nil, message.BytesPerTokenEta); est > summarizerToolPayloadMaxTokens+100 {
		t.Errorf("capped tool result estimates %d tokens, want about %d", est, summarizerToolPayloadMaxTokens)
	}
	if stats.truncatedToolPayloads != 1 {
		t.Errorf("truncatedToolPayloads = %d, want 1", stats.truncatedToolPayloads)
	}
}

func TestCapPayload_RuneBoundaries(t *testing.T) {
	s := strings.Repeat("é", 5000) // 2 bytes per rune
	got, cut := capPayload(s, 100)
	if !cut {
		t.Fatal("want a cut")
	}
	if !strings.HasPrefix(got, "é") || !strings.HasSuffix(got, "é") || strings.ContainsRune(got, '�') {
		t.Errorf("cut split a rune")
	}
}

func TestSummarizerRequest_IsOneTextMessage(t *testing.T) {
	transcript, _ := summarizerTranscript([]message.Message{
		textMsg(message.User, "PREVIOUS SUMMARY"),
		{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "c", Name: "bash", Input: "{}"}}},
		{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "c", Name: "bash", Content: "ok"}}},
	})
	req := summarizerRequest(transcript, textMsg(message.User, "summarize"))
	if len(req) != 1 || req[0].Role != message.User || len(req[0].Parts) != 1 {
		t.Fatalf("request = %+v, want one user message with one part", req)
	}
	text := firstText(req[0])
	prev := strings.Index(text, "PREVIOUS SUMMARY")
	call := strings.Index(text, "[tool_call bash id=c]")
	res := strings.Index(text, "[tool_result bash id=c] ok")
	prompt := strings.LastIndex(text, "summarize")
	if prev < 0 || call < prev || res < call || prompt < res {
		t.Errorf("request text out of order or incomplete:\n%s", text)
	}
}

// The summarizer is sent no tools; a request that still replays native tool
// blocks, server tool-search blocks or signed thinking can be rejected
// upstream. Every summarizer request carries text only.
func TestPerformSynchronousCompaction_SendsNoToolBlocks(t *testing.T) {
	a, _, summarizer, _ := newCompactingAgent(t, 960, nil)
	const sess = "sess-text-only"
	seedHistory(t, a, sess,
		textMsg(message.User, "do it"),
		message.Message{Role: message.Assistant, Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "hmm", Signature: "sig"},
			message.ToolSearchContent{ToolUseID: "srv", Name: "tool_search_tool_regex", References: []string{"x"}},
			message.ToolCall{ID: "c1", Name: "bash", Input: "{}", Finished: true},
		}},
		message.Message{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "c1", Name: "bash", Content: "out"},
		}},
	)
	if err := a.performSynchronousCompaction(context.Background(), sess, compactionTriggerManual); err != nil {
		t.Fatal(err)
	}
	if len(summarizer.requests) != 1 {
		t.Fatalf("summarizer requests = %d, want 1", len(summarizer.requests))
	}
	for _, m := range summarizer.requests[0] {
		for _, p := range m.Parts {
			if _, ok := p.(message.TextContent); !ok {
				t.Errorf("summarizer request carries a %T part, want text only", p)
			}
		}
	}
}

func TestSummarizerBudget_AgentCap(t *testing.T) {
	var log []string
	a := &agent{
		messages:                 newMemMessages(),
		sessions:                 &memSessions{},
		summarizeProvider:        &compactionProvider{name: "summarizer", window: 100_000, log: &log},
		summarizerMaxInputTokens: 2500,
	}
	if got := a.summarizerBudget(); got != 2500 {
		t.Fatalf("summarizerBudget = %d, want the agent cap 2500", got)
	}
	a.summarizerMaxInputTokens = 1_000_000
	if got := a.summarizerBudget(); got != 90_000 {
		t.Fatalf("summarizerBudget = %d, want 0.9 x window when the cap is larger", got)
	}

	// The cap trims like the window does.
	a.summarizerMaxInputTokens = 2500
	big := strings.Repeat("y", 3900)
	const sess = "sess-cap"
	seedHistory(t, a, sess, textMsg(message.User, big), textMsg(message.Assistant, big), textMsg(message.User, big), textMsg(message.Assistant, "latest"))
	_, _ = a.sessions.Save(context.Background(), session.Session{ID: sess})
	got, stats, err := a.summarizerInputWithStats(context.Background(), sess, textMsg(message.User, "summarize"))
	if err != nil {
		t.Fatal(err)
	}
	if stats.trimmedMessages == 0 || stats.budget != 2500 || firstText(got[len(got)-2]) != "latest" {
		t.Errorf("stats = %+v, input = %v; want the oldest dropped under the 2500 cap", stats, texts(got))
	}
}
