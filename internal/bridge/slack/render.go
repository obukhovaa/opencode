package slack

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
)

// toolCardCacheTTL bounds how long the adapter remembers the message
// reference for a tool-call card so the matching tool-result can update
// it in place. 5 minutes covers virtually every realistic tool latency
// (file reads, bash, gitlab MCP) while keeping the in-memory footprint
// bounded. Misses fall back to a fresh chat.postMessage — see D6.
const toolCardCacheTTL = 5 * time.Minute

// toolCardRef records the Slack message coordinates of a posted tool-call
// card so the matching tool-result event can chat.update it.
type toolCardRef struct {
	ChannelID string
	TS        string
	PostedAt  time.Time
}

// toolCardCache maps callID -> toolCardRef with eviction on TTL miss.
// Keyed by the dispatcher's short callID suffix (formatToolParamMap-side
// trims the provider-issued ID to the last 6 chars) AND the destination
// channel so two concurrent calls to the same tool across different
// channels don't collide.
type toolCardCache struct {
	mu  sync.Mutex
	m   map[string]toolCardRef
	ttl time.Duration
}

func newToolCardCache() *toolCardCache {
	return &toolCardCache{m: map[string]toolCardRef{}, ttl: toolCardCacheTTL}
}

func (c *toolCardCache) key(channel, callID string) string {
	return channel + "::" + callID
}

func (c *toolCardCache) store(channel, callID, ts string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[c.key(channel, callID)] = toolCardRef{ChannelID: channel, TS: ts, PostedAt: time.Now()}
}

func (c *toolCardCache) consume(channel, callID string) (toolCardRef, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.key(channel, callID)
	ref, ok := c.m[k]
	if !ok {
		return toolCardRef{}, false
	}
	if time.Since(ref.PostedAt) > c.ttl {
		delete(c.m, k)
		return toolCardRef{}, false
	}
	delete(c.m, k)
	return ref, true
}

// Render implements bridge.RichRenderer for Slack. Dispatches per
// RenderKind to the platform-native Block Kit construction. Unknown
// kinds return ErrRenderUnsupported so the bridge falls back to text.
func (a *Adapter) Render(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	if hint == nil {
		return bridge.SendResult{Err: bridge.ErrRenderUnsupported}
	}
	switch hint.Kind {
	case bridge.RenderKindToolCall:
		return a.renderToolCall(ctx, peer, hint)
	case bridge.RenderKindToolResult:
		return a.renderToolResult(ctx, peer, hint)
	case bridge.RenderKindList:
		return a.renderList(ctx, peer, hint)
	case bridge.RenderKindTable:
		return a.renderTable(ctx, peer, hint)
	case bridge.RenderKindStatus:
		return a.renderStatus(ctx, peer, hint)
	default:
		return bridge.SendResult{Err: bridge.ErrRenderUnsupported}
	}
}

// renderToolCall posts the pending tool-call card and stores the
// resulting ts in the cache so the paired result can update it.
func (a *Adapter) renderToolCall(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return bridge.SendResult{Err: ErrInvalidPeerID}
	}
	blocks := buildToolCallBlocks(hint)
	opts := []slackgo.MsgOption{slackgo.MsgOptionBlocks(blocks...)}
	if parsed.ThreadTS != "" {
		opts = append(opts, slackgo.MsgOptionTS(parsed.ThreadTS))
	}
	_, ts, err := a.api.PostMessageContext(ctx, parsed.ChannelID, opts...)
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("slack: tool-call render: %w", err)}
	}
	// Cache for the paired result event.
	a.toolCards().store(parsed.ChannelID, hint.CallID, ts)
	resolved := ""
	if parsed.ThreadTS == "" && !IsDM(parsed.ChannelID) && ts != "" {
		resolved = FormatPeerID(Peer{ChannelID: parsed.ChannelID, ThreadTS: ts})
	}
	return bridge.SendResult{Delivered: true, ResolvedPeer: resolved}
}

// renderToolResult tries to chat.update the paired tool-call card; on
// cache miss or TTL eviction, falls back to a fresh chat.postMessage.
// Matches D6.
func (a *Adapter) renderToolResult(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return bridge.SendResult{Err: ErrInvalidPeerID}
	}
	blocks := buildToolResultBlocks(hint)
	// Cache lookup: did we post the call card?
	if ref, ok := a.toolCards().consume(parsed.ChannelID, hint.CallID); ok {
		_, _, _, err := a.api.UpdateMessageContext(ctx, ref.ChannelID, ref.TS, slackgo.MsgOptionBlocks(blocks...))
		if err == nil {
			return bridge.SendResult{Delivered: true}
		}
		// Update failed — fall through to a fresh post (best effort).
		logging.Warn("bridge: slack chat.update for tool result failed, posting fresh", "err", err)
	}
	opts := []slackgo.MsgOption{slackgo.MsgOptionBlocks(blocks...)}
	if parsed.ThreadTS != "" {
		opts = append(opts, slackgo.MsgOptionTS(parsed.ThreadTS))
	}
	_, ts, err := a.api.PostMessageContext(ctx, parsed.ChannelID, opts...)
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("slack: tool-result render: %w", err)}
	}
	resolved := ""
	if parsed.ThreadTS == "" && !IsDM(parsed.ChannelID) && ts != "" {
		resolved = FormatPeerID(Peer{ChannelID: parsed.ChannelID, ThreadTS: ts})
	}
	return bridge.SendResult{Delivered: true, ResolvedPeer: resolved}
}

func (a *Adapter) renderList(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return bridge.SendResult{Err: ErrInvalidPeerID}
	}
	blocks := buildListBlocks(hint)
	opts := []slackgo.MsgOption{slackgo.MsgOptionBlocks(blocks...)}
	if parsed.ThreadTS != "" {
		opts = append(opts, slackgo.MsgOptionTS(parsed.ThreadTS))
	}
	_, ts, err := a.api.PostMessageContext(ctx, parsed.ChannelID, opts...)
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("slack: list render: %w", err)}
	}
	resolved := ""
	if parsed.ThreadTS == "" && !IsDM(parsed.ChannelID) && ts != "" {
		resolved = FormatPeerID(Peer{ChannelID: parsed.ChannelID, ThreadTS: ts})
	}
	return bridge.SendResult{Delivered: true, ResolvedPeer: resolved}
}

func (a *Adapter) renderTable(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return bridge.SendResult{Err: ErrInvalidPeerID}
	}
	blocks := buildTableBlocks(hint)
	opts := []slackgo.MsgOption{slackgo.MsgOptionBlocks(blocks...)}
	if parsed.ThreadTS != "" {
		opts = append(opts, slackgo.MsgOptionTS(parsed.ThreadTS))
	}
	_, ts, err := a.api.PostMessageContext(ctx, parsed.ChannelID, opts...)
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("slack: table render: %w", err)}
	}
	resolved := ""
	if parsed.ThreadTS == "" && !IsDM(parsed.ChannelID) && ts != "" {
		resolved = FormatPeerID(Peer{ChannelID: parsed.ChannelID, ThreadTS: ts})
	}
	return bridge.SendResult{Delivered: true, ResolvedPeer: resolved}
}

func (a *Adapter) renderStatus(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return bridge.SendResult{Err: ErrInvalidPeerID}
	}
	text := hint.Body
	if text == "" {
		text = "—"
	}
	opts := []slackgo.MsgOption{slackgo.MsgOptionBlocks(
		slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, text, false, false), nil, nil),
	)}
	if parsed.ThreadTS != "" {
		opts = append(opts, slackgo.MsgOptionTS(parsed.ThreadTS))
	}
	_, ts, err := a.api.PostMessageContext(ctx, parsed.ChannelID, opts...)
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("slack: status render: %w", err)}
	}
	resolved := ""
	if parsed.ThreadTS == "" && !IsDM(parsed.ChannelID) && ts != "" {
		resolved = FormatPeerID(Peer{ChannelID: parsed.ChannelID, ThreadTS: ts})
	}
	return bridge.SendResult{Delivered: true, ResolvedPeer: resolved}
}

// buildToolCallBlocks composes the Block Kit blocks for a pending
// tool call. Header line includes a spinner glyph and ID suffix;
// params (if any) appear in a context block beneath.
func buildToolCallBlocks(hint *bridge.RenderHint) []slackgo.Block {
	header := fmt.Sprintf("⏳ *%s*`#%s`", hint.ToolName, hint.CallID)
	blocks := []slackgo.Block{
		slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, header, false, false), nil, nil),
	}
	if len(hint.Params) > 0 {
		blocks = append(blocks, buildParamsContext(hint.Params))
	}
	return blocks
}

// buildToolResultBlocks composes the Block Kit blocks for a tool
// completion: header line with status emoji + duration, params (if any),
// preview as a monospaced block. The header overwrites the pending
// version when chat.update is in play; falls back to a fresh post
// otherwise.
func buildToolResultBlocks(hint *bridge.RenderHint) []slackgo.Block {
	emoji := "✓"
	switch hint.Status {
	case "error":
		emoji = "✗"
	case "pending":
		emoji = "⏳"
	}
	duration := ""
	if hint.DurationMs > 0 {
		duration = fmt.Sprintf(" · %s", formatDuration(hint.DurationMs))
	}
	header := fmt.Sprintf("%s *%s*`#%s`%s", emoji, hint.ToolName, hint.CallID, duration)
	blocks := []slackgo.Block{
		slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, header, false, false), nil, nil),
	}
	if len(hint.Params) > 0 {
		blocks = append(blocks, buildParamsContext(hint.Params))
	}
	if hint.Preview != "" {
		blocks = append(blocks, slackgo.NewDividerBlock())
		body := "```" + strings.ReplaceAll(hint.Preview, "```", "ʼʼʼ") + "```"
		blocks = append(blocks, slackgo.NewSectionBlock(
			slackgo.NewTextBlockObject(slackgo.MarkdownType, body, false, false),
			nil, nil,
		))
	}
	return blocks
}

// buildParamsContext renders a context block with `key: value` lines for
// each param. Stable iteration order (sorted keys) so two renders of the
// same params produce identical output.
func buildParamsContext(params map[string]string) slackgo.Block {
	keys := sortedKeys(params)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("`%s`: %s", k, params[k]))
	}
	return slackgo.NewContextBlock("",
		slackgo.NewTextBlockObject(slackgo.MarkdownType, strings.Join(parts, "  ·  "), false, false),
	)
}

// buildListBlocks renders a list with optional title + active-marker.
func buildListBlocks(hint *bridge.RenderHint) []slackgo.Block {
	var blocks []slackgo.Block
	if hint.Title != "" {
		blocks = append(blocks, slackgo.NewHeaderBlock(slackgo.NewTextBlockObject(slackgo.PlainTextType, hint.Title, false, false)))
	}
	if len(hint.Items) == 0 {
		blocks = append(blocks, slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, "_no items_", false, false), nil, nil))
		return blocks
	}
	var b strings.Builder
	for i, item := range hint.Items {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("• *")
		b.WriteString(item.Label)
		b.WriteString("*")
		if item.Marker != "" && item.Marker == hint.ActiveLabel {
			b.WriteString("  🟢 _" + item.Marker + "_")
		} else if item.Marker != "" {
			b.WriteString("  _" + item.Marker + "_")
		}
		if item.Sublabel != "" {
			b.WriteString("\n  ")
			b.WriteString(item.Sublabel)
		}
	}
	blocks = append(blocks, slackgo.NewSectionBlock(
		slackgo.NewTextBlockObject(slackgo.MarkdownType, b.String(), false, false),
		nil, nil,
	))
	return blocks
}

// buildTableBlocks renders a monospaced pipe-table. Slack doesn't have
// native tables in Block Kit so we approximate with code-fenced
// monospaced rows.
func buildTableBlocks(hint *bridge.RenderHint) []slackgo.Block {
	if len(hint.Rows) == 0 {
		return []slackgo.Block{
			slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, "_empty table_", false, false), nil, nil),
		}
	}
	// Compute column widths.
	cols := len(hint.Headers)
	if cols == 0 && len(hint.Rows) > 0 {
		cols = len(hint.Rows[0])
	}
	widths := make([]int, cols)
	if len(hint.Headers) == cols {
		for i, h := range hint.Headers {
			if len(h) > widths[i] {
				widths[i] = len(h)
			}
		}
	}
	for _, row := range hint.Rows {
		for i := 0; i < cols && i < len(row); i++ {
			if len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
	}
	var b strings.Builder
	b.WriteString("```\n")
	if len(hint.Headers) == cols {
		writeTableRow(&b, hint.Headers, widths)
		writeTableSep(&b, widths)
	}
	for _, row := range hint.Rows {
		writeTableRow(&b, row, widths)
	}
	b.WriteString("```")
	return []slackgo.Block{
		slackgo.NewSectionBlock(slackgo.NewTextBlockObject(slackgo.MarkdownType, b.String(), false, false), nil, nil),
	}
}

func writeTableRow(b *strings.Builder, row []string, widths []int) {
	for i, w := range widths {
		val := ""
		if i < len(row) {
			val = row[i]
		}
		if i > 0 {
			b.WriteString(" │ ")
		}
		b.WriteString(val)
		for k := len(val); k < w; k++ {
			b.WriteByte(' ')
		}
	}
	b.WriteByte('\n')
}

func writeTableSep(b *strings.Builder, widths []int) {
	for i, w := range widths {
		if i > 0 {
			b.WriteString("─┼─")
		}
		for k := 0; k < w; k++ {
			b.WriteRune('─')
		}
	}
	b.WriteByte('\n')
}

// formatDuration renders a millisecond duration as a compact "1.4s",
// "850ms", "1m2s".
func formatDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	if ms < 60_000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	m := ms / 60_000
	s := (ms % 60_000) / 1000
	return fmt.Sprintf("%dm%ds", m, s)
}

// sortedKeys returns the keys of a string-keyed map in ascending order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Sort via standard library — pulled inline to avoid an extra import
	// in adapter.go which has many already.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
