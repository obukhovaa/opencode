package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
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
			message.ToolSearchContent{ToolUseID: "srv-1", Name: "tool_search_tool_regex", Input: `{"query":"jira"}`, References: []string{"jira_get", "jira_search"}},
			message.TextContent{Text: "looking"},
			message.ToolCall{ID: "call-1", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			// No Name on the result: it is taken from the call.
			message.ToolResult{ToolCallID: "call-1", Content: big, IsError: true},
		}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "only reasoning"},
		}},
		{Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "here is a file"},
			message.BinaryContent{Path: "spec.pdf", MIMEType: "application/pdf", Data: []byte("%PDF")},
		}},
	}

	got, stats := summarizerTranscript(history)
	if len(got) != 4 {
		t.Fatalf("transcript has %d messages, want 4 (the reasoning-only one left out): %v", len(got), texts(got))
	}
	wantRoles := []message.MessageRole{message.User, message.Assistant, message.Tool, message.User}
	for i, m := range got {
		if len(m.Parts) != 1 {
			t.Fatalf("message %d has %d parts, want one text part", i, len(m.Parts))
		}
		if _, ok := m.Parts[0].(message.TextContent); !ok {
			t.Fatalf("message %d part is %T, want text", i, m.Parts[0])
		}
		if m.Role != wantRoles[i] {
			t.Errorf("message %d role = %s, want %s kept", i, m.Role, wantRoles[i])
		}
	}
	assistant := firstText(got[1])
	for _, want := range []string{
		`[tool_search tool_search_tool_regex id=srv-1] {"query":"jira"} -> found: jira_get, jira_search`,
		"looking",
		`[tool_call bash id=call-1] {"command":"ls"}`,
	} {
		if !strings.Contains(assistant, want) {
			t.Errorf("assistant entry %q does not contain %q", assistant, want)
		}
	}
	if strings.Contains(assistant, "SECRET THOUGHTS") {
		t.Errorf("reasoning leaked into the transcript: %q", assistant)
	}
	result := firstText(got[2])
	if !strings.HasPrefix(result, "[tool_result bash id=call-1] (error) aaa") || !strings.HasSuffix(result, "bbb") {
		t.Errorf("tool result entry = %.80q..., want the header with the call's name, then head and tail", result)
	}
	if strings.Contains(result, "MIDDLE") || !strings.Contains(result, "tokens omitted ...]") {
		t.Errorf("oversized tool result was not cut in the middle")
	}
	if est := message.EstimateTokens(got[2:3], nil, message.BytesPerTokenEta); est > summarizerToolPayloadMaxTokens+100 {
		t.Errorf("capped tool result estimates %d tokens, want about %d", est, summarizerToolPayloadMaxTokens)
	}
	if stats.truncatedToolPayloads != 1 {
		t.Errorf("truncatedToolPayloads = %d, want 1", stats.truncatedToolPayloads)
	}
	if att := firstText(got[3]); !strings.Contains(att, "here is a file") || !strings.Contains(att, "[attachment spec.pdf (application/pdf) omitted]") {
		t.Errorf("attachment entry = %q, want the text and a placeholder for the binary", att)
	}
}

func TestToolSearchTranscriptLine(t *testing.T) {
	base := message.ToolSearchContent{ToolUseID: "srv", Name: "tool_search_tool_regex", Input: `{"query":"x"}`}
	none := base
	failed := base
	failed.ErrorCode = "too_many_requests"
	for _, tc := range []struct {
		name string
		in   message.ToolSearchContent
		want string
	}{
		{"nothing found", none, `[tool_search tool_search_tool_regex id=srv] {"query":"x"} -> found: none`},
		{"error", failed, `[tool_search tool_search_tool_regex id=srv] {"query":"x"} -> error too_many_requests`},
	} {
		if got := toolSearchTranscriptLine(tc.in); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
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
	if same, cut := capPayload("short", 100); cut || same != "short" {
		t.Errorf("short payload changed: %q %v", same, cut)
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
	stats, err := a.performSynchronousCompaction(context.Background(), sess, compactionTriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if len(summarizer.requests) != 1 {
		t.Fatalf("summarizer requests = %d, want 1", len(summarizer.requests))
	}
	for _, m := range summarizer.requests[0] {
		if m.Role != message.User {
			t.Errorf("summarizer request carries a %s message, want user only", m.Role)
		}
		for _, p := range m.Parts {
			if _, ok := p.(message.TextContent); !ok {
				t.Errorf("summarizer request carries a %T part, want text only", p)
			}
		}
	}
	if stats.messages != 3 || stats.estimatedTokens == 0 {
		t.Errorf("stats = %+v, want 3 transcript messages and a size", stats)
	}
}

// The trim runs on the transcript entries, which keep their IDs and roles, so
// its pairing rule still applies: a tool result never survives without its
// call at the head of the input.
func TestSummarizerInput_TranscriptKeepsPairsThroughTrim(t *testing.T) {
	big := strings.Repeat("y", 3900)
	var log []string
	a := &agent{
		messages:          newMemMessages(),
		sessions:          &memSessions{},
		summarizeProvider: &compactionProvider{name: "summarizer", window: 3000, log: &log},
	}
	const sess = "sess-pairs"
	seedHistory(t, a, sess,
		textMsg(message.User, big),
		message.Message{Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: big},
			message.ToolCall{ID: "c1", Name: "bash", Input: "{}", Finished: true},
		}},
		message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "c1", Name: "bash", Content: big}}},
		textMsg(message.Assistant, "latest"),
	)
	_, _ = a.sessions.Save(context.Background(), session.Session{ID: sess})
	got, stats, err := a.summarizerInputWithStats(context.Background(), sess, textMsg(message.User, "summarize"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		if strings.HasPrefix(firstText(m), "[tool_result") {
			t.Fatalf("a tool result survived without its call: %v", texts(got))
		}
	}
	if stats.trimmedMessages == 0 || stats.messages != len(got)-1 || stats.estimatedTokens <= 0 {
		t.Errorf("stats = %+v for input %v", stats, texts(got))
	}
}

func TestSummarizerBudget(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	const main = config.AgentName("budget-main")
	cases := []struct {
		name       string
		window     int64
		agentCap   int64
		cfgMain    int64
		cfgSummary int64
		want       int64
	}{
		{"window only", 1_000_000, 0, 0, 0, 900_000},
		{"registry cap below the window fraction", 100_000, 2500, 0, 0, 2500},
		{"registry cap above the window fraction is ignored", 100_000, 1_000_000, 0, 0, 90_000},
		{"summarizer agent cap applies to every agent", 1_000_000, 0, 0, 200_000, 200_000},
		{"main agent config wins over the summarizer's", 1_000_000, 0, 150_000, 200_000, 150_000},
		{"registry value wins over both", 1_000_000, 100_000, 150_000, 200_000, 100_000},
		{"no window, cap only", 0, 0, 0, 50_000, 50_000},
		{"unbounded", 0, 0, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config.Get().Agents[main] = config.Agent{SummarizerMaxInputTokens: tc.cfgMain}
			config.Get().Agents[config.AgentSummarizer] = config.Agent{SummarizerMaxInputTokens: tc.cfgSummary}
			var log []string
			a := &agent{agentID: main, summarizerMaxInputTokens: tc.agentCap,
				summarizeProvider: &compactionProvider{name: "summarizer", window: tc.window, log: &log}}
			if got := a.summarizerBudget(); got != tc.want {
				t.Errorf("summarizerBudget = %d, want %d", got, tc.want)
			}
		})
	}
}

// The cap trims like the window does.
func TestSummarizerInput_AgentCapTrims(t *testing.T) {
	var log []string
	a := &agent{
		messages:                 newMemMessages(),
		sessions:                 &memSessions{},
		summarizeProvider:        &compactionProvider{name: "summarizer", window: 100_000, log: &log},
		summarizerMaxInputTokens: 2500,
	}
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
