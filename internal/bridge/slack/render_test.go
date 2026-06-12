package slack

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
)

func TestRenderToolCallProducesPendingHeader(t *testing.T) {
	resetCapturedBlocks()
	mock := newMockServer(t)
	mock.server.Config.Handler = blockCapturingHandler(mock.server.Config.Handler, t, mock)

	a, err := New(Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, Options{APIURL: mock.URL() + "/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	resetCapturedBlocks()
	hint := bridge.NewToolCallHint("bash", "abc123", map[string]string{"command": "ls -la"})
	res := a.Render(context.Background(),
		bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D012345"},
		hint,
	)
	if res.Err != nil {
		t.Fatalf("Render: %v", res.Err)
	}
	if !res.Delivered {
		t.Fatal("Render did not report Delivered=true")
	}
	captures := capturedBlocks()
	if len(captures) == 0 {
		t.Fatalf("no blocks captured")
	}
	bs := captures[len(captures)-1]
	if len(bs) < 1 {
		t.Fatalf("expected at least 1 block (header), got %d", len(bs))
	}
	header := bs[0]
	if header["type"] != "section" {
		t.Errorf("first block type = %v; want section", header["type"])
	}
	textObj, _ := header["text"].(map[string]any)
	if !strings.Contains(textObj["text"].(string), "bash") || !strings.Contains(textObj["text"].(string), "abc123") {
		t.Errorf("header missing tool/callID: %v", textObj["text"])
	}
	if !strings.Contains(textObj["text"].(string), "⏳") {
		t.Errorf("pending state should have spinner glyph: %v", textObj["text"])
	}
	// Params context block (second block).
	if len(bs) >= 2 {
		ctx := bs[1]
		if ctx["type"] != "context" {
			t.Errorf("second block type = %v; want context", ctx["type"])
		}
	}
}

func TestRenderToolResultUpdatesCachedCallCard(t *testing.T) {
	resetCapturedBlocks()
	mock := newMockServer(t)
	mock.server.Config.Handler = blockCapturingHandler(mock.server.Config.Handler, t, mock)

	a, err := New(Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, Options{APIURL: mock.URL() + "/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	resetCapturedBlocks()
	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D012345"}
	// Phase 1: tool call (gets cached).
	a.Render(context.Background(), peer, bridge.NewToolCallHint("read", "xyz", nil))

	// The mock's default chat.update handler returns OK with empty body
	// — slack-go's UpdateMessageContext only needs an "ok":true response.
	resetCapturedBlocks()
	// Phase 2: tool result. The renderToolResult path SHOULD call
	// chat.update (not chat.postMessage). Mock server distinguishes.
	res := a.Render(context.Background(), peer, bridge.NewToolResultHint("read", "xyz", "ok", "12 lines", 850))
	if res.Err != nil {
		t.Fatalf("Render: %v", res.Err)
	}
	if !res.Delivered {
		t.Fatal("Render did not report Delivered=true")
	}
	// Verify update was called (cached ref was consumed). After the
	// consume, a second result for the same callID falls through to
	// post — we don't assert update was called via the mock's spy
	// machinery, just verify consume is idempotent.
	if _, ok := a.toolCards().consume("D012345", "xyz"); ok {
		t.Error("cache entry should have been consumed by renderToolResult")
	}
}

func TestRenderToolResultFallsBackOnCacheMiss(t *testing.T) {
	resetCapturedBlocks()
	mock := newMockServer(t)
	mock.server.Config.Handler = blockCapturingHandler(mock.server.Config.Handler, t, mock)

	a, err := New(Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, Options{APIURL: mock.URL() + "/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	resetCapturedBlocks()
	// No prior call cached — result should post fresh, not panic.
	res := a.Render(context.Background(),
		bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D012345"},
		bridge.NewToolResultHint("bash", "unseen", "error", "permission denied", 0),
	)
	if res.Err != nil {
		t.Fatalf("Render: %v", res.Err)
	}
	captures := capturedBlocks()
	if len(captures) == 0 {
		t.Fatalf("expected fresh chat.postMessage on cache miss")
	}
	header := captures[len(captures)-1][0]
	textObj, _ := header["text"].(map[string]any)
	headerText, _ := textObj["text"].(string)
	if !strings.Contains(headerText, "✗") {
		t.Errorf("error result missing error glyph: %v", headerText)
	}
}

func TestRenderListWithActiveMarker(t *testing.T) {
	resetCapturedBlocks()
	mock := newMockServer(t)
	mock.server.Config.Handler = blockCapturingHandler(mock.server.Config.Handler, t, mock)

	a, err := New(Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, Options{APIURL: mock.URL() + "/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	resetCapturedBlocks()
	hint := bridge.NewListHint("Available agents", []bridge.ListItem{
		{Label: "coder", Marker: "active"},
		{Label: "architect"},
	}, "active")
	res := a.Render(context.Background(),
		bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D012345"},
		hint,
	)
	if res.Err != nil {
		t.Fatalf("Render: %v", res.Err)
	}
	captures := capturedBlocks()
	if len(captures) == 0 {
		t.Fatalf("no blocks captured")
	}
	bs := captures[len(captures)-1]
	if bs[0]["type"] != "header" {
		t.Errorf("first block should be header, got %v", bs[0]["type"])
	}
	body := bs[1]
	textObj, _ := body["text"].(map[string]any)
	bodyText, _ := textObj["text"].(string)
	if !strings.Contains(bodyText, "coder") || !strings.Contains(bodyText, "architect") {
		t.Errorf("list body missing items: %v", bodyText)
	}
	if !strings.Contains(bodyText, "🟢") {
		t.Errorf("active marker should render with green dot: %v", bodyText)
	}
}

func TestRenderTableHasMonospaceBlock(t *testing.T) {
	resetCapturedBlocks()
	mock := newMockServer(t)
	mock.server.Config.Handler = blockCapturingHandler(mock.server.Config.Handler, t, mock)

	a, err := New(Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, Options{APIURL: mock.URL() + "/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	resetCapturedBlocks()
	hint := bridge.NewTableHint(
		[]string{"ID", "Title"},
		[][]string{{"s1", "First"}, {"s2", "Second"}},
	)
	res := a.Render(context.Background(),
		bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D012345"},
		hint,
	)
	if res.Err != nil {
		t.Fatalf("Render: %v", res.Err)
	}
	captures := capturedBlocks()
	if len(captures) == 0 {
		t.Fatalf("no blocks captured")
	}
	textObj, _ := captures[len(captures)-1][0]["text"].(map[string]any)
	body, _ := textObj["text"].(string)
	if !strings.Contains(body, "```") {
		t.Errorf("table should be in code fence, got: %v", body)
	}
	if !strings.Contains(body, "ID") || !strings.Contains(body, "s2") {
		t.Errorf("table missing header/row content: %v", body)
	}
}

func TestSlackSendInteractiveMultiSelectPostsMultiStaticSelect(t *testing.T) {
	resetCapturedBlocks()
	mock := newMockServer(t)
	mock.server.Config.Handler = blockCapturingHandler(mock.server.Config.Handler, t, mock)

	a, err := New(Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, Options{APIURL: mock.URL() + "/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	resetCapturedBlocks()
	resolved, err := a.SendInteractiveMultiSelect(context.Background(),
		bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D012345"},
		"Pick capabilities",
		[]bridge.QuestionChoice{
			{Label: "auth", Value: "auth"},
			{Label: "billing", Value: "billing"},
			{Label: "ui", Value: "ui"},
		},
	)
	if err != nil {
		t.Fatalf("SendInteractiveMultiSelect: %v", err)
	}
	if resolved == "" {
		t.Errorf("resolved peer-id is empty; want composite for DM channel? — D-prefix should still return non-empty if mock returns ts")
	}
	captures := capturedBlocks()
	if len(captures) == 0 {
		t.Fatalf("no blocks captured")
	}
	bs := captures[len(captures)-1]
	// Find actions block.
	var actions map[string]any
	for _, b := range bs {
		if b["type"] == "actions" {
			actions = b
			break
		}
	}
	if actions == nil {
		t.Fatalf("no actions block: %+v", bs)
	}
	elements, _ := actions["elements"].([]any)
	if len(elements) != 2 {
		t.Fatalf("expected 2 elements (multi_static_select + button), got %d", len(elements))
	}
	first, _ := elements[0].(map[string]any)
	if first["type"] != "multi_static_select" {
		t.Errorf("first element type = %v; want multi_static_select", first["type"])
	}
	second, _ := elements[1].(map[string]any)
	if second["type"] != "button" {
		t.Errorf("second element type = %v; want button", second["type"])
	}
}

func TestSendInteractiveMultiSelectRejectsTooManyOptions(t *testing.T) {
	resetCapturedBlocks()
	mock := newMockServer(t)
	a, err := New(Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, Options{APIURL: mock.URL() + "/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	choices := make([]bridge.QuestionChoice, 0, MultiSelectMaxOptions+1)
	for i := 0; i <= MultiSelectMaxOptions; i++ {
		choices = append(choices, bridge.QuestionChoice{Label: "x", Value: "x"})
	}
	_, err = a.SendInteractiveMultiSelect(context.Background(),
		bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D012345"},
		"too many", choices,
	)
	if err == nil {
		t.Fatal("expected ErrTooManyOptions, got nil")
	}
}

func TestToolCardCacheTTLEvicts(t *testing.T) {
	t.Parallel()
	c := newToolCardCache()
	c.ttl = 10 * time.Millisecond
	c.store("C123", "abc", "1781200000.000")
	time.Sleep(15 * time.Millisecond)
	if _, ok := c.consume("C123", "abc"); ok {
		t.Error("expired entry should not be returned by consume")
	}
}

// resetCapturedBlocks clears the package-level capture slice between
// tests to avoid bleed across rapid t.Parallel runs.
func resetCapturedBlocks() {
	capturedBlocksMu.Lock()
	defer capturedBlocksMu.Unlock()
	capturedBlocksJ = nil
}
