package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestSendGFMProseProducesHTMLParseMode mirrors the Slack-side
// TestSendGFMProseProducesMarkdownBlocks: a GFM outbound message is sent
// with ParseMode HTML and the converted text carries the expected tags.
func TestSendGFMProseProducesHTMLParseMode(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	gfm := "# Heading\n\n**bold** and `inline code`\n\n```go\nfunc main() {}\n```"
	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Text: gfm,
	})
	if !res.Delivered {
		t.Fatalf("Send err: %v", res.Err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.sendMsg) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", len(mock.sendMsg))
	}
	call := mock.sendMsg[0]
	if call.ParseMode != string(models.ParseModeHTML) {
		t.Errorf("ParseMode = %q, want %q", call.ParseMode, models.ParseModeHTML)
	}
	for _, want := range []string{"<b>Heading</b>", "<b>bold</b>", "<code>inline code</code>", `<pre><code class="language-go">`} {
		if !strings.Contains(call.Text, want) {
			t.Errorf("sent text missing %q; got %q", want, call.Text)
		}
	}
}

// TestSendMultiChunkPreservesContentAndParseMode verifies a message
// longer than MarkdownChunkLimit is split into multiple sendMessage
// calls, each independently converted with ParseMode HTML, and that no
// word is lost across the split.
func TestSendMultiChunkPreservesContentAndParseMode(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteString("word")
		sb.WriteString("_")
		sb.WriteString("0123456789 ")
		if (i+1)%6 == 0 {
			sb.WriteString("\n\n")
		}
	}
	text := sb.String()
	if len(text) <= MarkdownChunkLimit {
		t.Fatalf("fixture too short (%d runes) to force chunking", len(text))
	}

	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Text: text,
	})
	if !res.Delivered {
		t.Fatalf("Send err: %v", res.Err)
	}

	mock.mu.Lock()
	calls := append([]sendCall(nil), mock.sendMsg...)
	mock.mu.Unlock()
	if len(calls) < 2 {
		t.Fatalf("sendMessage calls = %d, want >= 2 (chunking expected)", len(calls))
	}
	for i, c := range calls {
		if c.ParseMode != string(models.ParseModeHTML) {
			t.Errorf("call %d ParseMode = %q, want %q", i, c.ParseMode, models.ParseModeHTML)
		}
		if len([]rune(c.Text)) > MaxTextLength {
			t.Errorf("call %d text rune length %d exceeds MaxTextLength %d", i, len([]rune(c.Text)), MaxTextLength)
		}
	}

	// No content loss: every "wordN_digits" token from the source
	// appears in the concatenation of all sent (HTML-escaped) chunks.
	wantWords := strings.Fields(text)
	var gotConcat strings.Builder
	for _, c := range calls {
		gotConcat.WriteString(c.Text)
		gotConcat.WriteString(" ")
	}
	got := gotConcat.String()
	for _, w := range wantWords {
		if !strings.Contains(got, w) {
			t.Errorf("word %q missing from concatenated sent output", w)
		}
	}
}

// TestSendParseErrorRetriesChunkWithoutParseMode verifies that a
// can't-parse-entities-class error on the HTML attempt triggers exactly
// one retry for that chunk with no ParseMode and the original markdown
// text, and that Send still reports success.
func TestSendParseErrorRetriesChunkWithoutParseMode(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})
	mock.sendMessageErrors = []string{"Bad Request: can't parse entities: Unsupported start tag \"x\" at byte offset 4"}

	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Text: "hello **world**",
	})
	if !res.Delivered {
		t.Fatalf("Send err: %v (want delivered via plain-text retry)", res.Err)
	}

	mock.mu.Lock()
	calls := append([]sendCall(nil), mock.sendMsg...)
	mock.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("sendMessage calls = %d, want 2 (HTML attempt + plain-text retry)", len(calls))
	}
	if calls[0].ParseMode != string(models.ParseModeHTML) {
		t.Errorf("first call ParseMode = %q, want HTML", calls[0].ParseMode)
	}
	if calls[1].ParseMode != "" {
		t.Errorf("retry call ParseMode = %q, want empty (no ParseMode)", calls[1].ParseMode)
	}
	if calls[1].Text != "hello **world**" {
		t.Errorf("retry text = %q, want original markdown text", calls[1].Text)
	}
}

// TestSendUnrelatedErrorDoesNotRetryAndSurfaces verifies an unrelated
// send failure (not entity/parse-shaped) is not retried and IS
// surfaced as the Send result's error.
func TestSendUnrelatedErrorDoesNotRetryAndSurfaces(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})
	mock.sendMessageErrors = []string{"Bad Request: chat not found"}

	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Text: "hello",
	})
	if res.Delivered {
		t.Fatalf("Delivered = true, want false (unrelated error)")
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "chat not found") {
		t.Errorf("err = %v, want it to mention chat not found", res.Err)
	}

	mock.mu.Lock()
	got := len(mock.sendMsg)
	mock.mu.Unlock()
	if got != 1 {
		t.Fatalf("sendMessage calls = %d, want exactly 1 (no retry for an unrelated error)", got)
	}
}

// TestSendTooLongErrorRetriesAsPlainText covers the one construct whose
// HTML conversion GROWS the parsed length: a horizontal rule becomes 10
// em-dashes, so a source chunk made almost entirely of `---` lines can
// exceed Telegram's 4,096-character "after entities parsing" cap even
// though the raw chunk is only 3,500 runes. Without "message is too long"
// in isParseError's needle list the message was dropped outright instead
// of degrading to the (shorter) plain-text form.
func TestSendTooLongErrorRetriesAsPlainText(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})
	mock.sendMessageErrors = []string{"Bad Request: message is too long"}

	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Text: strings.Repeat("---\n", 400),
	})
	if !res.Delivered {
		t.Fatalf("Send err: %v (want delivered via plain-text retry)", res.Err)
	}

	mock.mu.Lock()
	calls := append([]sendCall(nil), mock.sendMsg...)
	mock.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("sendMessage calls = %d, want 2 (HTML attempt + plain-text retry)", len(calls))
	}
	if calls[1].ParseMode != "" {
		t.Errorf("retry call ParseMode = %q, want empty (no ParseMode)", calls[1].ParseMode)
	}
	if !strings.Contains(calls[1].Text, "---") {
		t.Errorf("retry text = %q, want the original markdown source", calls[1].Text)
	}
}

// TestSendParseErrorDoesNotLatchAcrossMessages verifies there is no
// sticky latch: after a parse-error retry on one Send, the NEXT Send
// still attempts ParseModeHTML fresh.
func TestSendParseErrorDoesNotLatchAcrossMessages(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})
	mock.sendMessageErrors = []string{"Bad Request: can't parse entities: bad tag"}

	res1 := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Text: "first **message**",
	})
	if !res1.Delivered {
		t.Fatalf("first Send err: %v", res1.Err)
	}

	res2 := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Text: "second **message**",
	})
	if !res2.Delivered {
		t.Fatalf("second Send err: %v", res2.Err)
	}

	mock.mu.Lock()
	calls := append([]sendCall(nil), mock.sendMsg...)
	mock.mu.Unlock()
	// 2 calls for the first message (HTML fail + plain retry), 1 call
	// for the second (HTML attempt succeeds — no latch skipping it).
	if len(calls) != 3 {
		t.Fatalf("sendMessage calls = %d, want 3", len(calls))
	}
	if calls[2].ParseMode != string(models.ParseModeHTML) {
		t.Errorf("third call (second message) ParseMode = %q, want HTML — latch must not persist across messages", calls[2].ParseMode)
	}
}

// TestSendMentionSurvivesConversionExactlyOnce verifies the mention
// prepend still happens exactly once and survives HTML conversion.
func TestSendMentionSurvivesConversionExactlyOnce(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	res := a.Send(context.Background(), bridge.Outbound{
		Peer:    bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Mention: "@reviewer",
		Text:    "your build finished",
	})
	if !res.Delivered {
		t.Fatalf("Send err: %v", res.Err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.sendMsg) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", len(mock.sendMsg))
	}
	got := mock.sendMsg[0].Text
	if n := strings.Count(got, "@reviewer"); n != 1 {
		t.Errorf("mention appears %d times, want 1: %q", n, got)
	}
	if !strings.HasPrefix(got, "@reviewer") {
		t.Errorf("text = %q, want mention-prefixed content", got)
	}
}

// TestSendEmptyTextIsNoOp verifies empty outbound text sends no
// sendMessage call and still reports delivered (nothing to attach).
func TestSendEmptyTextIsNoOp(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Text: "",
	})
	if !res.Delivered {
		t.Fatalf("Send err: %v", res.Err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.sendMsg) != 0 {
		t.Errorf("sendMessage calls = %d, want 0", len(mock.sendMsg))
	}
}

// TestSendEscapesPlainAngleBracketsAndAmpersand verifies literal
// "<", ">", "&" outside any markdown construct are escaped in the sent
// HTML without corrupting adjacent emitted tags.
func TestSendEscapesPlainAngleBracketsAndAmpersand(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", Token: "tg-token"})

	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Text: "a < b && c > d, and **bold** stays bold",
	})
	if !res.Delivered {
		t.Fatalf("Send err: %v", res.Err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	got := mock.sendMsg[0].Text
	if !strings.Contains(got, "a &lt; b &amp;&amp; c &gt; d") {
		t.Errorf("text = %q, want escaped angle brackets/ampersand", got)
	}
	if !strings.Contains(got, "<b>bold</b>") {
		t.Errorf("text = %q, want bold tag preserved alongside escaped prose", got)
	}
}
