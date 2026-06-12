package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
)

// toolCardCacheTTL bounds how long the adapter remembers a posted
// tool-call message id so the matching tool-result can EditMessageText
// it in place. 5 minutes covers virtually every realistic tool latency.
const toolCardCacheTTL = 5 * time.Minute

// multiSelectStateTTL is how long the adapter remembers a multi-select
// keyboard's selection state. 30 minutes per the spec (D5 / O3) — gives
// reviewers who step away enough breathing room.
const multiSelectStateTTL = 30 * time.Minute

// toolCardRef records the Telegram message coordinates of a posted
// tool-call message so the matching tool-result can EditMessageText.
type toolCardRef struct {
	ChatID    int64
	MessageID int
	PostedAt  time.Time
}

// toolCardCache maps (chatID, callID) -> toolCardRef with TTL eviction.
type toolCardCache struct {
	mu  sync.Mutex
	m   map[string]toolCardRef
	ttl time.Duration
}

func newToolCardCache() *toolCardCache {
	return &toolCardCache{m: map[string]toolCardRef{}, ttl: toolCardCacheTTL}
}

func (c *toolCardCache) key(chatID int64, callID string) string {
	return strconv.FormatInt(chatID, 10) + "::" + callID
}

func (c *toolCardCache) store(chatID int64, callID string, messageID int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[c.key(chatID, callID)] = toolCardRef{ChatID: chatID, MessageID: messageID, PostedAt: time.Now()}
}

func (c *toolCardCache) consume(chatID int64, callID string) (toolCardRef, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.key(chatID, callID)
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

// multiSelectState records the selection state of an in-flight
// multi-select keyboard, keyed by Telegram message_id.
type multiSelectState struct {
	mu  sync.Mutex
	m   map[int]*multiSelectEntry
	ttl time.Duration
}

type multiSelectEntry struct {
	ChatID    int64
	Selected  map[string]bool // key: option Value (canonical answer label)
	Labels    map[string]string
	Order     []string // stable order of the original choices
	UpdatedAt time.Time
}

func newMultiSelectState() *multiSelectState {
	return &multiSelectState{m: map[int]*multiSelectEntry{}, ttl: multiSelectStateTTL}
}

func (s *multiSelectState) put(messageID int, e *multiSelectEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.UpdatedAt = time.Now()
	s.m[messageID] = e
}

func (s *multiSelectState) get(messageID int) (*multiSelectEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[messageID]
	if !ok {
		return nil, false
	}
	if time.Since(e.UpdatedAt) > s.ttl {
		delete(s.m, messageID)
		return nil, false
	}
	return e, true
}

func (s *multiSelectState) delete(messageID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, messageID)
}

// Render implements bridge.RichRenderer for Telegram.
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
	chatID, err := ParsePeerID(peer.PeerID)
	if err != nil {
		return bridge.SendResult{Err: err}
	}
	text := buildTelegramToolCallText(hint)
	msg, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("telegram: tool-call render: %w", err)}
	}
	if msg != nil {
		a.toolCards().store(msg.Chat.ID, hint.CallID, msg.ID)
	}
	return bridge.SendResult{Delivered: true}
}

func (a *Adapter) renderToolResult(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	chatID, err := ParsePeerID(peer.PeerID)
	if err != nil {
		return bridge.SendResult{Err: err}
	}
	text := buildTelegramToolResultText(hint)
	// Look up the cached call card to update in place.
	if chatNum, perr := strconv.ParseInt(strings.TrimPrefix(strings.TrimPrefix(strconv.FormatInt(0, 10), ""), ""), 10, 64); perr == nil {
		_ = chatNum
	}
	chatNum, perr := chatIDToInt64(chatID)
	if perr == nil {
		if ref, ok := a.toolCards().consume(chatNum, hint.CallID); ok {
			_, eerr := a.bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{
				ChatID:    ref.ChatID,
				MessageID: ref.MessageID,
				Text:      text,
				ParseMode: models.ParseModeMarkdown,
			})
			if eerr == nil {
				return bridge.SendResult{Delivered: true}
			}
			logging.Warn("bridge: telegram EditMessageText for tool result failed, posting fresh", "err", eerr)
		}
	}
	_, err = a.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("telegram: tool-result render: %w", err)}
	}
	return bridge.SendResult{Delivered: true}
}

func (a *Adapter) renderList(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	chatID, err := ParsePeerID(peer.PeerID)
	if err != nil {
		return bridge.SendResult{Err: err}
	}
	text := buildTelegramListText(hint)
	_, err = a.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("telegram: list render: %w", err)}
	}
	return bridge.SendResult{Delivered: true}
}

func (a *Adapter) renderTable(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	chatID, err := ParsePeerID(peer.PeerID)
	if err != nil {
		return bridge.SendResult{Err: err}
	}
	text := buildTelegramTableText(hint)
	_, err = a.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("telegram: table render: %w", err)}
	}
	return bridge.SendResult{Delivered: true}
}

func (a *Adapter) renderStatus(ctx context.Context, peer bridge.PeerRef, hint *bridge.RenderHint) bridge.SendResult {
	chatID, err := ParsePeerID(peer.PeerID)
	if err != nil {
		return bridge.SendResult{Err: err}
	}
	body := hint.Body
	if body == "" {
		body = "—"
	}
	_, err = a.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      body,
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("telegram: status render: %w", err)}
	}
	return bridge.SendResult{Delivered: true}
}

// --- text builders ----------------------------------------------------------

// Telegram's "Markdown" (legacy) parse mode is more forgiving than
// MarkdownV2 for free-text content. We use it because tool params and
// previews contain too many otherwise-reserved chars to escape reliably.
// Code blocks (```...```) still render as monospace.

func buildTelegramToolCallText(hint *bridge.RenderHint) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⏳ *%s* `#%s`", hint.ToolName, hint.CallID)
	if len(hint.Params) > 0 {
		b.WriteString("\n```\n")
		for _, k := range sortedKeys(hint.Params) {
			fmt.Fprintf(&b, "%s: %s\n", k, hint.Params[k])
		}
		b.WriteString("```")
	}
	return b.String()
}

func buildTelegramToolResultText(hint *bridge.RenderHint) string {
	emoji := "✓"
	switch hint.Status {
	case "error":
		emoji = "✗"
	case "pending":
		emoji = "⏳"
	}
	duration := ""
	if hint.DurationMs > 0 {
		duration = " · " + formatDuration(hint.DurationMs)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s *%s* `#%s`%s", emoji, hint.ToolName, hint.CallID, duration)
	if len(hint.Params) > 0 {
		b.WriteString("\n```\n")
		for _, k := range sortedKeys(hint.Params) {
			fmt.Fprintf(&b, "%s: %s\n", k, hint.Params[k])
		}
		b.WriteString("```")
	}
	if hint.Preview != "" {
		b.WriteString("\n```\n")
		// Replace any backtick fences inside the preview so we don't
		// close the code block prematurely.
		body := strings.ReplaceAll(hint.Preview, "```", "ʼʼʼ")
		b.WriteString(body)
		b.WriteString("\n```")
	}
	return b.String()
}

func buildTelegramListText(hint *bridge.RenderHint) string {
	var b strings.Builder
	if hint.Title != "" {
		fmt.Fprintf(&b, "*%s*\n", hint.Title)
	}
	for _, item := range hint.Items {
		b.WriteString("• *")
		b.WriteString(item.Label)
		b.WriteString("*")
		if item.Marker == hint.ActiveLabel && item.Marker != "" {
			b.WriteString(" _" + item.Marker + "_")
		} else if item.Marker != "" {
			b.WriteString(" _" + item.Marker + "_")
		}
		if item.Sublabel != "" {
			b.WriteString("\n  ")
			b.WriteString(item.Sublabel)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func buildTelegramTableText(hint *bridge.RenderHint) string {
	if len(hint.Rows) == 0 {
		return "_empty table_"
	}
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
		writeTGRow(&b, hint.Headers, widths)
		writeTGSep(&b, widths)
	}
	for _, row := range hint.Rows {
		writeTGRow(&b, row, widths)
	}
	b.WriteString("```")
	return b.String()
}

func writeTGRow(b *strings.Builder, row []string, widths []int) {
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

func writeTGSep(b *strings.Builder, widths []int) {
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

// chatIDToInt64 unwraps the `any` ChatID into an int64 — Telegram's
// chat ID is always numeric; ParsePeerID returns the original string
// form so we lose typing across the boundary. Local helper avoids
// reimporting strconv at every call site.
func chatIDToInt64(chatID any) (int64, error) {
	switch v := chatID.(type) {
	case int64:
		return v, nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("telegram: unsupported chat-id type %T", chatID)
	}
}
