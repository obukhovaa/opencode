package chat

import (
	"fmt"
	"testing"

	"charm.land/bubbles/v2/viewport"
	"github.com/opencode-ai/opencode/internal/message"
)

func completedToolHistory(n int) []message.Message {
	msgs := make([]message.Message, 0, n*2+1)
	for i := 0; i < n; i++ {
		id := fmt.Sprint(i)
		msgs = append(msgs, message.Message{Parts: []message.ContentPart{message.ToolCall{ID: id, Name: "bash", Finished: true}}}, message.Message{Parts: []message.ContentPart{message.ToolResult{ToolCallID: id, Content: "done"}}})
	}
	return append(msgs, message.Message{Parts: []message.ContentPart{message.ToolCall{ID: "pending", Name: "task", Finished: true}}})
}
func TestPendingToolHistory(t *testing.T) {
	got := countToolsWithoutResponse(completedToolHistory(5000))
	if got.tools != 0 || got.subagents != 1 {
		t.Fatalf("counts: %+v", got)
	}
}
func BenchmarkPendingToolHistory(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			msgs := completedToolHistory(n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				countToolsWithoutResponse(msgs)
			}
		})
	}
}
func TestViewportCacheTracksScrollAndSize(t *testing.T) {
	m := &messagesCmp{width: 20, height: 4, viewport: viewport.New()}
	m.viewport.SetWidth(20)
	m.viewport.SetHeight(2)
	m.viewport.SetContent("one\ntwo\nthree\nfour")
	first := m.cachedViewport()
	if m.cachedViewport() != first {
		t.Fatal("unstable cached view")
	}
	m.viewport.SetYOffset(1)
	if m.cachedViewport() == first {
		t.Fatal("scroll reused stale viewport")
	}
	m.width = 10
	m.viewport.SetWidth(10)
	m.cachedViewport()
	if m.viewportCacheWidth != 10 {
		t.Fatal("resize cache stale")
	}
}
