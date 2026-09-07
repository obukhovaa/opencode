package slack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/markdown"
)

// decodedBlock mirrors the JSON shape of a slackgo.MarkdownBlock, used to
// decode the "blocks" form field the mock server captures.
type decodedBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func decodeMarkdownBlocks(t *testing.T, raw string) []decodedBlock {
	t.Helper()
	if raw == "" {
		return nil
	}
	var blocks []decodedBlock
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		t.Fatalf("decode blocks %q: %v", raw, err)
	}
	return blocks
}

func TestSendGFMProseProducesMarkdownBlocks(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	gfm := "## Heading\n\n**bold** text and a [link](https://example.com)\n\n- item one\n- item two\n\n| a | b |\n| - | - |\n| 1 | 2 |"
	r := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Text: gfm,
	})
	if !r.Delivered {
		t.Fatalf("send: %v", r.Err)
	}

	posts := mock.Posts()
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(posts))
	}
	blocks := decodeMarkdownBlocks(t, posts[0].Blocks)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (short GFM text fits in one chunk)", len(blocks))
	}
	if blocks[0].Type != "markdown" {
		t.Errorf("block type = %q, want markdown", blocks[0].Type)
	}
	// GFM syntax must be preserved verbatim — we do NOT rewrite it, we
	// let Slack's markdown-block parser render it.
	for _, want := range []string{"**bold**", "## Heading", "- item one", "[link](https://example.com)", "| a | b |"} {
		if !strings.Contains(blocks[0].Text, want) {
			t.Errorf("block text missing %q; got %q", want, blocks[0].Text)
		}
	}

	// The top-level text fallback must still be set (notification /
	// accessibility preview).
	if posts[0].Text == "" {
		t.Errorf("top-level text fallback is empty")
	}
}

func TestSendNormalizesLegacyLinkSyntaxInBlocks(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	r := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Text: "See <https://example.com|label> for details.",
	})
	if !r.Delivered {
		t.Fatalf("send: %v", r.Err)
	}

	posts := mock.Posts()
	blocks := decodeMarkdownBlocks(t, posts[0].Blocks)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if !strings.Contains(blocks[0].Text, "[label](https://example.com)") {
		t.Errorf("block text = %q, want normalized link", blocks[0].Text)
	}
	if strings.Contains(blocks[0].Text, "<https://example.com|label>") {
		t.Errorf("block text still contains raw mrkdwn link syntax: %q", blocks[0].Text)
	}
}

func TestSendMentionAppearsExactlyOnceInBlocks(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	r := a.Send(context.Background(), bridge.Outbound{
		Peer:    bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Mention: "<@U999>",
		Text:    "your build finished",
	})
	if !r.Delivered {
		t.Fatalf("send: %v", r.Err)
	}

	posts := mock.Posts()
	blocks := decodeMarkdownBlocks(t, posts[0].Blocks)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if n := strings.Count(blocks[0].Text, "<@U999>"); n != 1 {
		t.Errorf("mention appears %d times in block text, want 1: %q", n, blocks[0].Text)
	}
	// Mention must land INSIDE the rendered content, not as a separate
	// unstyled prefix outside of it.
	if !strings.HasPrefix(blocks[0].Text, "<@U999>") {
		t.Errorf("block text = %q, want mention-prefixed content", blocks[0].Text)
	}
}

func TestSendOversizedTextChunksWithinBudgetAndMarker(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	text := strings.Repeat("word ", 3000) // ~15,000 runes, over the 12,000 budget
	r := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Text: text,
	})
	if !r.Delivered {
		t.Fatalf("send: %v", r.Err)
	}

	posts := mock.Posts()
	blocks := decodeMarkdownBlocks(t, posts[0].Blocks)
	if len(blocks) == 0 || len(blocks) > 4 {
		t.Fatalf("blocks = %d, want 1..4", len(blocks))
	}
	sum := 0
	for _, b := range blocks {
		sum += utf8.RuneCountInString(b.Text)
	}
	if sum > MarkdownPayloadBudget {
		t.Errorf("sum(runeLen(blocks)) = %d, want <= %d", sum, MarkdownPayloadBudget)
	}
	last := blocks[len(blocks)-1].Text
	if !strings.HasSuffix(last, markdown.TruncationMarker) {
		t.Errorf("last block does not end with truncation marker: %q", tailRunes(last, 40))
	}
}

func TestSendBlockErrorFallsBackAndLatches(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})
	mock.postMessageErrors = []string{"invalid_blocks"}

	r := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Text: "hello **world**",
	})
	if !r.Delivered {
		t.Fatalf("send: %v (want delivered via plain-text fallback)", r.Err)
	}

	posts := mock.Posts()
	if len(posts) != 2 {
		t.Fatalf("posts = %d, want 2 (blocks attempt + plain-text retry)", len(posts))
	}
	if posts[0].Blocks == "" {
		t.Errorf("first attempt carried no blocks; expected a blocks attempt first")
	}
	if posts[1].Blocks != "" {
		t.Errorf("retry attempt carried blocks; want plain text only")
	}
	if posts[1].Text != "hello **world**" {
		t.Errorf("retry text = %q, want original text", posts[1].Text)
	}

	// Second, unrelated Send on the same Adapter: the latch must skip
	// the blocks attempt entirely — exactly one more plain-text post,
	// no blocks.
	r2 := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Text: "second message",
	})
	if !r2.Delivered {
		t.Fatalf("second send: %v", r2.Err)
	}
	posts = mock.Posts()
	if len(posts) != 3 {
		t.Fatalf("posts = %d, want 3 (latched: no blocks attempt on second Send)", len(posts))
	}
	if posts[2].Blocks != "" {
		t.Errorf("post after latch carried blocks; want plain text only")
	}
	if posts[2].Text != "second message" {
		t.Errorf("post after latch text = %q, want %q", posts[2].Text, "second message")
	}
}

func TestSendAmbiguousBlockErrorRetriesWithoutLatching(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})
	mock.postMessageErrors = []string{"invalid_arguments"}

	r := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Text: "hello **world**",
	})
	if !r.Delivered {
		t.Fatalf("send: %v (want delivered via plain-text fallback)", r.Err)
	}

	posts := mock.Posts()
	if len(posts) != 2 {
		t.Fatalf("posts = %d, want 2 (blocks attempt + plain-text retry)", len(posts))
	}
	if posts[0].Blocks == "" {
		t.Errorf("first attempt carried no blocks; expected a blocks attempt first")
	}
	if posts[1].Blocks != "" {
		t.Errorf("retry attempt carried blocks; want plain text only")
	}

	// invalid_arguments is ambiguous (Slack also returns it for
	// unrelated reasons) so it must NOT set the sticky latch: the next
	// Send should still attempt blocks.
	r2 := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Text: "second message",
	})
	if !r2.Delivered {
		t.Fatalf("second send: %v", r2.Err)
	}
	posts = mock.Posts()
	if len(posts) != 3 {
		t.Fatalf("posts = %d, want 3", len(posts))
	}
	if posts[2].Blocks == "" {
		t.Errorf("post after ambiguous error carried no blocks; want blocks attempted again (latch NOT set)")
	}
}

func TestSendUnrelatedErrorDoesNotRetry(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})
	mock.postMessageErrors = []string{"channel_not_found"}

	r := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Text: "hello",
	})
	if r.Delivered {
		t.Fatalf("delivered = true, want false (unrelated error)")
	}
	if r.Err == nil || !strings.Contains(r.Err.Error(), "channel_not_found") {
		t.Errorf("err = %v, want channel_not_found", r.Err)
	}

	posts := mock.Posts()
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want exactly 1 (no retry for an unrelated error)", len(posts))
	}
}

func TestSendEmptyTextWithAttachmentsSkipsTextPost(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	r := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
		Attachments: []bridge.Attachment{
			{FileName: "f.txt", Content: []byte("data")},
		},
	})
	if !r.Delivered {
		t.Fatalf("send: %v", r.Err)
	}
	if posts := mock.Posts(); len(posts) != 0 {
		t.Errorf("posts = %d, want 0 (no text part)", len(posts))
	}
	if uploads := mock.Uploads(); len(uploads) != 1 {
		t.Errorf("uploads = %d, want 1", len(uploads))
	}
}

func TestSendThreadingPreservedWithBlocks(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	// Thread peer: MsgOptionTS must still be applied.
	r1 := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "C123|1700000000.000100"},
		Text: "**hi** thread",
	})
	if !r1.Delivered {
		t.Fatalf("thread send: %v", r1.Err)
	}
	if r1.ResolvedPeer != "" {
		t.Errorf("ResolvedPeer = %q, want empty for an already-composite peer", r1.ResolvedPeer)
	}

	// Channel-only peer: still returns the rewritten ResolvedPeer.
	r2 := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "C0DEF456"},
		Text: "**hi** channel",
	})
	if !r2.Delivered {
		t.Fatalf("channel send: %v", r2.Err)
	}
	if r2.ResolvedPeer != "C0DEF456|1700000123.000200" {
		t.Errorf("ResolvedPeer = %q", r2.ResolvedPeer)
	}

	posts := mock.Posts()
	if len(posts) != 2 {
		t.Fatalf("posts = %d, want 2", len(posts))
	}
	if posts[0].ThreadTS != "1700000000.000100" {
		t.Errorf("post 0 ThreadTS = %q, want thread ts", posts[0].ThreadTS)
	}
	if posts[1].ThreadTS != "" {
		t.Errorf("post 1 ThreadTS = %q, want empty (channel-only peer)", posts[1].ThreadTS)
	}
	// Both still went through the blocks path.
	for i, p := range posts {
		if p.Blocks == "" {
			t.Errorf("post %d carried no blocks", i)
		}
	}
}

func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
