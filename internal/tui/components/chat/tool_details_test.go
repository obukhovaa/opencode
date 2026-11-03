package chat

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

func numberedLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%02d", i+1)
	}
	return strings.Join(lines, "\n")
}

func TestTruncateLines(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		limit      int
		want       string
		wantHidden int
	}{
		{name: "under limit", content: "a\nb", limit: 3, want: "a\nb"},
		{name: "at limit", content: "a\nb\nc", limit: 3, want: "a\nb\nc"},
		{name: "over limit", content: "a\nb\nc\nd\ne", limit: 3, want: "a\nb\nc", wantHidden: 2},
		{name: "no limit", content: "a\nb\nc\nd", limit: 0, want: "a\nb\nc\nd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, hidden := truncateLines(tt.content, tt.limit)
			if got != tt.want || hidden != tt.wantHidden {
				t.Errorf("truncateLines() = %q, %d; want %q, %d", got, hidden, tt.want, tt.wantHidden)
			}
		})
	}
}

func TestTruncateRenderedSkipsPadding(t *testing.T) {
	got, hidden := truncateRendered("\n  \nl1\nl2\nl3\nl4\n\n", 3)
	if got != "l1\nl2\nl3" || hidden != 1 {
		t.Errorf("truncateRendered() = %q, %d; want %q, 1", got, hidden, "l1\nl2\nl3")
	}
	got, hidden = truncateRendered("\nl1\nl2\n\n", 3)
	if got != "\nl1\nl2" || hidden != 0 {
		t.Errorf("short output: truncateRendered() = %q, %d; want it untouched apart from trailing blanks", got, hidden)
	}
}

func TestRenderToolResponseCollapsesByDefault(t *testing.T) {
	call := message.ToolCall{ID: "1", Name: tools.BashToolName, Input: `{"command":"seq 10"}`, Finished: true}
	result := message.ToolResult{ToolCallID: "1", Content: numberedLines(10)}

	collapsed := ansi.Strip(renderToolResponse(call, result, 80, false))
	for _, want := range []string{"line-01", "line-03", "+7 lines (ctrl+o to expand)"} {
		if !strings.Contains(collapsed, want) {
			t.Errorf("collapsed output missing %q:\n%s", want, collapsed)
		}
	}
	if strings.Contains(collapsed, "line-04") {
		t.Errorf("collapsed output shows line 4:\n%s", collapsed)
	}

	expanded := ansi.Strip(renderToolResponse(call, result, 80, true))
	if !strings.Contains(expanded, "line-10") {
		t.Errorf("expanded output missing the last line:\n%s", expanded)
	}
	if strings.Contains(expanded, "to expand") {
		t.Errorf("expanded output still carries the expand hint:\n%s", expanded)
	}
}

func TestRenderToolResponseShortOutputHasNoHint(t *testing.T) {
	call := message.ToolCall{ID: "1", Name: tools.GlobToolName, Input: `{"pattern":"*.go"}`, Finished: true}
	result := message.ToolResult{ToolCallID: "1", Content: "a.go\nb.go"}

	got := ansi.Strip(renderToolResponse(call, result, 80, false))
	if strings.Contains(got, "to expand") {
		t.Errorf("two-line result got a hint:\n%s", got)
	}
}

func TestRenderToolResponseExpandedHitsSafetyCap(t *testing.T) {
	call := message.ToolCall{ID: "1", Name: tools.GrepToolName, Input: `{"pattern":"x"}`, Finished: true}
	result := message.ToolResult{ToolCallID: "1", Content: numberedLines(expandedResultLines + 5)}

	got := ansi.Strip(renderToolResponse(call, result, 80, true))
	if !strings.Contains(got, "+5 lines not shown") {
		t.Errorf("expanded output past the cap lacks the not-shown note:\n%s", got[len(got)-200:])
	}
}

func TestRenderToolParamsBashExpandedShowsWholeCommand(t *testing.T) {
	call := message.ToolCall{
		Name:  tools.BashToolName,
		Input: `{"command":"echo one\necho two","description":"Print two lines"}`,
	}

	collapsed := ansi.Strip(renderToolParams(60, call, false))
	if !strings.Contains(collapsed, "Print two lines") || !strings.Contains(collapsed, "echo one echo two") {
		t.Errorf("collapsed params = %q; want the description and the command on one line", collapsed)
	}

	expanded := ansi.Strip(renderToolParams(60, call, true))
	if !strings.Contains(expanded, "echo one\necho two") {
		t.Errorf("expanded params = %q; want the command with its line breaks", expanded)
	}
}

func TestVisibleNestedCalls(t *testing.T) {
	var calls []message.ToolCall
	for i := 1; i <= 5; i++ {
		calls = append(calls, message.ToolCall{ID: fmt.Sprintf("n%d", i)})
	}

	got, skipped := visibleNestedCalls(calls, false)
	if skipped != 2 || len(got) != 3 || got[0].ID != "n3" || got[2].ID != "n5" {
		t.Errorf("collapsed: got %d calls from %s, skipped %d; want n3..n5, skipped 2", len(got), got[0].ID, skipped)
	}
	if hint := nestedCallsHint(skipped); !strings.Contains(hint, "+2 earlier tool uses (ctrl+o to expand)") {
		t.Errorf("hint = %q", hint)
	}
	if hint := nestedCallsHint(1); !strings.Contains(hint, "+1 earlier tool use ") {
		t.Errorf("singular hint = %q", hint)
	}

	got, skipped = visibleNestedCalls(calls, true)
	if skipped != 0 || len(got) != 5 {
		t.Errorf("expanded: got %d calls, skipped %d; want all 5", len(got), skipped)
	}

	got, skipped = visibleNestedCalls(calls[:3], false)
	if skipped != 0 || len(got) != 3 {
		t.Errorf("at the limit: got %d calls, skipped %d; want 3, 0", len(got), skipped)
	}
}

func TestCacheItemFreshTracksExpandedMode(t *testing.T) {
	item := cacheItem{width: 80, expanded: false}
	if !item.fresh(80, false) {
		t.Error("same width and mode should be fresh")
	}
	if item.fresh(80, true) {
		t.Error("a mode flip must invalidate the cached render")
	}
	if item.fresh(100, false) {
		t.Error("a width change must invalidate the cached render")
	}
}

func toolTranscript(count int) []message.Message {
	var messages []message.Message
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("a%02d", i)
		callID := fmt.Sprintf("call%02d", i)
		messages = append(messages,
			message.Message{
				ID:        id,
				SessionID: "test-session",
				Role:      message.Assistant,
				Parts: []message.ContentPart{message.ToolCall{
					ID: callID, Name: tools.BashToolName, Input: `{"command":"seq 20"}`, Finished: true,
				}},
			},
			message.Message{
				ID:        "result" + id,
				SessionID: "test-session",
				Role:      message.Tool,
				Parts: []message.ContentPart{message.ToolResult{
					ToolCallID: callID, Content: numberedLines(20),
				}},
			},
		)
	}
	return messages
}

func applyRenders(t *testing.T, m *messagesCmp, cmd tea.Cmd) {
	t.Helper()
	for range 10 {
		results := renderResults(t, cmd)
		if len(results) == 0 {
			return
		}
		if len(results) != 1 {
			t.Fatalf("got %d concurrent render results, want 1", len(results))
		}
		_, cmd = m.Update(results[0])
	}
	t.Fatal("rendering did not settle after 10 results")
}

func TestToolDetailsExpandOnlyNearViewportAsynchronously(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMessagesForTest(t, app.NewForTest(ctx, &editorFakeAgent{}), 80)
	m.messages = toolTranscript(30)
	m.currentMsgID = "a29"
	m.renderViewSync()
	m.viewport.GotoBottom()
	before := m.uiMessages

	_, cmd := m.Update(ToggleToolDetailsMsg{})
	if !m.rendering {
		t.Fatal("toggle did not start an asynchronous render")
	}
	if !reflect.DeepEqual(m.uiMessages, before) {
		t.Fatal("toggle synchronously replaced the transcript")
	}
	results := renderResults(t, cmd)
	if len(results) != 1 {
		t.Fatalf("toggle produced %d render results, want 1", len(results))
	}
	_, cmd = m.Update(results[0])
	applyRenders(t, m, cmd)

	expanded := 0
	for _, item := range m.cachedContent {
		if item.expanded {
			expanded++
		}
	}
	if expanded == 0 || expanded >= 30 {
		t.Fatalf("expanded %d of 30 messages, want a bounded non-zero subset", expanded)
	}
	if m.cachedContent["a00"].expanded {
		t.Error("far above-viewport message was expanded")
	}
	if !m.cachedContent["a29"].expanded {
		t.Error("message at the bottom of the viewport was not expanded")
	}

	m.viewport.SetYOffset(0)
	m.userScrolledUp = true
	applyRenders(t, m, m.renderVisibleToolDetails())
	if !m.cachedContent["a00"].expanded {
		t.Error("scrolling did not expand the newly visible message")
	}
	if !m.cachedContent["a29"].expanded {
		t.Error("previously expanded message collapsed while scrolling")
	}

	m.viewport.GotoBottom()
	m.userScrolledUp = false
	applyRenders(t, m, m.renderVisibleToolDetails())
	if !m.cachedContent["a00"].expanded {
		t.Error("message collapsed after leaving the viewport")
	}
	for i := 1; i < len(m.uiMessages); i++ {
		want := m.uiMessages[i-1].position + m.uiMessages[i-1].height + 1
		if m.uiMessages[i].position != want {
			t.Fatalf("message %d position = %d, want %d after cached expansion", i, m.uiMessages[i].position, want)
		}
	}
	m.viewport.SetYOffset(0)
	m.userScrolledUp = true
	if cmd := m.renderVisibleToolDetails(); cmd != nil {
		t.Error("revisiting an already expanded viewport triggered another render")
	}
}

func TestToolDetailsDoubleToggleDropsStaleRender(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMessagesForTest(t, app.NewForTest(ctx, &editorFakeAgent{}), 80)
	m.messages = toolTranscript(5)
	m.renderViewSync()
	before := m.uiMessages

	_, first := m.Update(ToggleToolDetailsMsg{})
	firstResults := renderResults(t, first)
	if len(firstResults) != 1 {
		t.Fatalf("first toggle produced %d render results, want 1", len(firstResults))
	}
	_, second := m.Update(ToggleToolDetailsMsg{})
	m.Update(firstResults[0])
	if !reflect.DeepEqual(m.uiMessages, before) {
		t.Fatal("stale expanded render replaced the transcript")
	}
	if !m.rendering {
		t.Fatal("stale result cleared the second render's in-flight state")
	}
	applyRenders(t, m, second)
	for id, item := range m.cachedContent {
		if item.expanded {
			t.Errorf("%s stayed expanded after the second toggle", id)
		}
	}
}

func TestRenderAnchorPreservesMessageAndType(t *testing.T) {
	m := newMessagesForTest(t, app.NewForTest(context.Background(), &editorFakeAgent{}), 80)
	m.uiMessages = []uiMessage{
		{ID: "a", messageType: assistantMessageType, position: 0, height: 10},
		{ID: "a", messageType: toolMessageType, position: 11, height: 20},
		{ID: "b", messageType: toolMessageType, position: 32, height: 20},
	}
	m.viewport.SetContent(strings.Repeat("line\n", 200))
	m.viewport.SetYOffset(15)

	anchor := m.captureRenderAnchor()
	if anchor.id != "a" || anchor.messageType != toolMessageType || anchor.offset != 4 {
		t.Fatalf("anchor = %+v, want tool message a with offset 4", anchor)
	}
	m.uiMessages[1].position = 101
	m.restoreRenderAnchor(anchor)
	if got := m.viewport.YOffset(); got != 105 {
		t.Fatalf("restored YOffset = %d, want 105", got)
	}
}

func TestRenderFinishedRestoresPreviousAnchor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMessagesForTest(t, app.NewForTest(ctx, &editorFakeAgent{}), 80)
	m.uiMessages = []uiMessage{
		{ID: "a", messageType: toolMessageType, position: 0, height: 10},
		{ID: "b", messageType: toolMessageType, position: 11, height: 20},
	}
	m.viewport.SetContent(strings.Repeat("line\n", 200))
	m.viewport.SetYOffset(12)
	m.userScrolledUp = true
	m.rendering = true
	m.renderGeneration = 1

	m.Update(renderFinishedMsg{
		generation: 1,
		uiMessages: []uiMessage{
			{ID: "a", messageType: toolMessageType, position: 0, height: 50},
			{ID: "b", messageType: toolMessageType, position: 51, height: 20},
		},
		cacheUpdates:    map[string]cacheItem{},
		viewportContent: strings.Repeat("line\n", 200),
	})
	if got := m.viewport.YOffset(); got != 52 {
		t.Fatalf("restored YOffset = %d, want 52", got)
	}
}
