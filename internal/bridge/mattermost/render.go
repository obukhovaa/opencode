package mattermost

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
)

// toolCardCacheTTL bounds how long the adapter remembers a posted
// tool-call post id so the matching tool-result can update it via
// PUT /posts/{id}. Matches Slack/Telegram (5 minutes).
const toolCardCacheTTL = 5 * time.Minute

// toolCardRef records the Mattermost post coordinates of a posted
// tool-call card so the result can update it in place.
type toolCardRef struct {
	ChannelID string
	PostID    string
	PostedAt  time.Time
}

// toolCardCache maps (channelID, callID) -> toolCardRef with TTL.
type toolCardCache struct {
	mu  sync.Mutex
	m   map[string]toolCardRef
	ttl time.Duration
}

func newToolCardCache() *toolCardCache {
	return &toolCardCache{m: map[string]toolCardRef{}, ttl: toolCardCacheTTL}
}

func (c *toolCardCache) key(channelID, callID string) string {
	return channelID + "::" + callID
}

func (c *toolCardCache) store(channelID, callID, postID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[c.key(channelID, callID)] = toolCardRef{ChannelID: channelID, PostID: postID, PostedAt: time.Now()}
}

func (c *toolCardCache) consume(channelID, callID string) (toolCardRef, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.key(channelID, callID)
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

// MultiSelectMaxOptions is the upper option-count for Mattermost
// multi-select attachment-action widgets. Mattermost doesn't publish a
// hard cap but lots of options degrade the modal UX; match the others.
const MultiSelectMaxOptions = 50

// SendInteractiveMultiSelect implements bridge.InteractiveMultiSelectSender
// for Mattermost via the `attachment.actions` `select` widget with
// `multiselect: true`. On submit, Mattermost POSTs to the action URL
// with `selected_options` populated; the bridge's parseQuestionAnswers
// then handles the comma-separated reply.
//
// NOTE: Mattermost's attachment-action POST handler lives outside the
// bridge package today (it would need a route registered on the orchestrator's
// HTTP mux). For this change we render the widget but the handler stub
// is documented in tasks/9.2 — the action_url SHOULD point at
// /router/mattermost/interactive once that route exists.
func (a *Adapter) SendInteractiveMultiSelect(ctx context.Context, peer bridge.PeerRef, prompt string, choices []bridge.QuestionChoice) (string, error) {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return "", fmt.Errorf("mattermost: invalid peer-id %q", peer.PeerID)
	}
	if len(choices) == 0 {
		return "", fmt.Errorf("mattermost: SendInteractiveMultiSelect requires at least one choice")
	}
	if len(choices) > MultiSelectMaxOptions {
		return "", fmt.Errorf("mattermost: too many options for multi-select")
	}
	options := make([]map[string]any, 0, len(choices))
	for _, c := range choices {
		options = append(options, map[string]any{
			"text":  c.Label,
			"value": c.Value,
		})
	}
	action := map[string]any{
		"name":        "router_multi_select",
		"type":        "select",
		"multiselect": true,
		"options":     options,
		// Integration is Mattermost's webhook target. Leave url empty
		// for now; the handler registration is tracked under
		// bridge-question-multi-select tasks Phase D §9.2.
		"integration": map[string]any{
			"url": "",
		},
	}
	att := map[string]any{
		"color":   "#0066cc",
		"pretext": prompt,
		"actions": []map[string]any{action},
	}
	props := map[string]any{"attachments": []map[string]any{att}}
	post, err := a.client.CreatePost(ctx, CreatePostInput{
		ChannelID: parsed.ChannelID,
		RootID:    parsed.RootPostID,
		Props:     props,
	})
	if err != nil {
		return "", fmt.Errorf("mattermost: SendInteractiveMultiSelect: %w", err)
	}
	resolved := ""
	if parsed.RootPostID == "" {
		resolved = FormatPeerID(Peer{ChannelID: post.ChannelID, RootPostID: post.ID})
	}
	return resolved, nil
}

// Render implements bridge.RichRenderer for Mattermost. Uses
// Post.Props["attachments"] (Slack-attachment-compatible schema) for
// structured content.
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

func (a *Adapter) renderToolCall(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return bridge.SendResult{Err: fmt.Errorf("mattermost: invalid peer-id %q", peer.PeerID)}
	}
	props := map[string]any{"attachments": []map[string]any{buildToolCallAttachment(hint)}}
	post, err := a.client.CreatePost(ctx, CreatePostInput{
		ChannelID: parsed.ChannelID,
		Message:   "", // attachment carries the content
		RootID:    parsed.RootPostID,
		Props:     props,
	})
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("mattermost: tool-call render: %w", err)}
	}
	a.toolCards().store(parsed.ChannelID, hint.CallID, post.ID)
	resolved := ""
	if parsed.RootPostID == "" {
		resolved = FormatPeerID(Peer{ChannelID: post.ChannelID, RootPostID: post.ID})
	}
	return bridge.SendResult{Delivered: true, ResolvedPeer: resolved}
}

func (a *Adapter) renderToolResult(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return bridge.SendResult{Err: fmt.Errorf("mattermost: invalid peer-id %q", peer.PeerID)}
	}
	att := buildToolResultAttachment(hint)
	if ref, ok := a.toolCards().consume(parsed.ChannelID, hint.CallID); ok {
		props := map[string]any{"attachments": []map[string]any{att}}
		_, err := a.client.UpdatePost(ctx, UpdatePostInput{
			PostID: ref.PostID,
			Props:  props,
		})
		if err == nil {
			return bridge.SendResult{Delivered: true}
		}
		logging.Warn("bridge: mattermost UpdatePost for tool result failed, posting fresh", "err", err)
	}
	props := map[string]any{"attachments": []map[string]any{att}}
	post, err := a.client.CreatePost(ctx, CreatePostInput{
		ChannelID: parsed.ChannelID,
		RootID:    parsed.RootPostID,
		Props:     props,
	})
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("mattermost: tool-result render: %w", err)}
	}
	resolved := ""
	if parsed.RootPostID == "" {
		resolved = FormatPeerID(Peer{ChannelID: post.ChannelID, RootPostID: post.ID})
	}
	return bridge.SendResult{Delivered: true, ResolvedPeer: resolved}
}

func (a *Adapter) renderList(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return bridge.SendResult{Err: fmt.Errorf("mattermost: invalid peer-id %q", peer.PeerID)}
	}
	att := buildListAttachment(hint)
	props := map[string]any{"attachments": []map[string]any{att}}
	post, err := a.client.CreatePost(ctx, CreatePostInput{
		ChannelID: parsed.ChannelID,
		RootID:    parsed.RootPostID,
		Props:     props,
	})
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("mattermost: list render: %w", err)}
	}
	resolved := ""
	if parsed.RootPostID == "" {
		resolved = FormatPeerID(Peer{ChannelID: post.ChannelID, RootPostID: post.ID})
	}
	return bridge.SendResult{Delivered: true, ResolvedPeer: resolved}
}

func (a *Adapter) renderTable(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return bridge.SendResult{Err: fmt.Errorf("mattermost: invalid peer-id %q", peer.PeerID)}
	}
	// Mattermost renders markdown tables natively in attachment.text.
	att := map[string]any{
		"color": "#888888",
		"text":  buildMarkdownTable(hint),
	}
	props := map[string]any{"attachments": []map[string]any{att}}
	post, err := a.client.CreatePost(ctx, CreatePostInput{
		ChannelID: parsed.ChannelID,
		RootID:    parsed.RootPostID,
		Props:     props,
	})
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("mattermost: table render: %w", err)}
	}
	resolved := ""
	if parsed.RootPostID == "" {
		resolved = FormatPeerID(Peer{ChannelID: post.ChannelID, RootPostID: post.ID})
	}
	return bridge.SendResult{Delivered: true, ResolvedPeer: resolved}
}

func (a *Adapter) renderStatus(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return bridge.SendResult{Err: fmt.Errorf("mattermost: invalid peer-id %q", peer.PeerID)}
	}
	body := hint.Body
	if body == "" {
		body = "—"
	}
	post, err := a.client.CreatePost(ctx, CreatePostInput{
		ChannelID: parsed.ChannelID,
		RootID:    parsed.RootPostID,
		Message:   body,
	})
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("mattermost: status render: %w", err)}
	}
	resolved := ""
	if parsed.RootPostID == "" {
		resolved = FormatPeerID(Peer{ChannelID: post.ChannelID, RootPostID: post.ID})
	}
	return bridge.SendResult{Delivered: true, ResolvedPeer: resolved}
}

// --- attachment builders ----------------------------------------------------

func buildToolCallAttachment(hint *bridge.RenderHint) map[string]any {
	att := map[string]any{
		"color":   "#0066cc",
		"pretext": fmt.Sprintf("⏳ **%s** `#%s`", hint.ToolName, hint.CallID),
	}
	if len(hint.Params) > 0 {
		att["fields"] = buildParamsFields(hint.Params)
	}
	return att
}

func buildToolResultAttachment(hint *bridge.RenderHint) map[string]any {
	emoji := "✓"
	color := "good"
	switch hint.Status {
	case "error":
		emoji = "✗"
		color = "danger"
	case "pending":
		emoji = "⏳"
		color = "#888888"
	}
	duration := ""
	if hint.DurationMs > 0 {
		duration = " · " + formatDuration(hint.DurationMs)
	}
	att := map[string]any{
		"color":   color,
		"pretext": fmt.Sprintf("%s **%s** `#%s`%s", emoji, hint.ToolName, hint.CallID, duration),
	}
	if len(hint.Params) > 0 {
		att["fields"] = buildParamsFields(hint.Params)
	}
	if hint.Preview != "" {
		// Wrap preview in a code fence; replace any backticks to avoid
		// breaking the fence.
		body := "```\n" + strings.ReplaceAll(hint.Preview, "```", "ʼʼʼ") + "\n```"
		att["text"] = body
	}
	return att
}

func buildParamsFields(params map[string]string) []map[string]any {
	out := make([]map[string]any, 0, len(params))
	for _, k := range sortedKeys(params) {
		out = append(out, map[string]any{
			"title": k,
			"value": params[k],
			"short": len(params[k]) < 40,
		})
	}
	return out
}

func buildListAttachment(hint *bridge.RenderHint) map[string]any {
	var b strings.Builder
	for _, item := range hint.Items {
		b.WriteString("- **")
		b.WriteString(item.Label)
		b.WriteString("**")
		if item.Marker != "" {
			if item.Marker == hint.ActiveLabel {
				b.WriteString(" 🟢 _" + item.Marker + "_")
			} else {
				b.WriteString(" _" + item.Marker + "_")
			}
		}
		if item.Sublabel != "" {
			b.WriteString("\n   ")
			b.WriteString(item.Sublabel)
		}
		b.WriteString("\n")
	}
	att := map[string]any{
		"color": "#888888",
		"text":  strings.TrimRight(b.String(), "\n"),
	}
	if hint.Title != "" {
		att["pretext"] = "**" + hint.Title + "**"
	}
	return att
}

func buildMarkdownTable(hint *bridge.RenderHint) string {
	if len(hint.Rows) == 0 {
		return "_empty table_"
	}
	cols := len(hint.Headers)
	if cols == 0 && len(hint.Rows) > 0 {
		cols = len(hint.Rows[0])
	}
	var b strings.Builder
	if len(hint.Headers) == cols {
		b.WriteString("| ")
		b.WriteString(strings.Join(hint.Headers, " | "))
		b.WriteString(" |\n")
		// Separator row.
		b.WriteString("|")
		for i := 0; i < cols; i++ {
			b.WriteString(" --- |")
		}
		b.WriteByte('\n')
	}
	for _, row := range hint.Rows {
		b.WriteString("| ")
		for i := 0; i < cols; i++ {
			val := ""
			if i < len(row) {
				val = row[i]
			}
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(val)
		}
		b.WriteString(" |\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

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

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
