package mattermost

import (
	"context"
	"errors"
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
// for Mattermost. Multi-select stays descoped to the numbered-text /
// comma-separated-reply fallback per design.md D11 ("Mattermost
// attachment actions have no multi-select-with-apply semantics; high
// cost, marginal path") and specs/mattermost-question-actions/spec.md
// ("Multi-select prompts SHALL continue to use the numbered-text and
// comma-separated-reply fallback... no multi-select widget is posted").
//
// This always returns an error so the question router
// (tryInteractiveSend) falls back to text — per task E.2, the chosen
// alternative to wiring buildMultiSelectAttachment's integration.url for
// real. Rendering a widget whose submit goes nowhere (the pre-existing
// behaviour: integration.url was hardcoded to "") is worse than not
// rendering one at all, so the widget is no longer posted.
func (a *Adapter) SendInteractiveMultiSelect(ctx context.Context, peer bridge.PeerRef, prompt string, choices []bridge.QuestionChoice) (string, error) {
	return "", errMultiSelectNotSupported
}

// errMultiSelectNotSupported is SendInteractiveMultiSelect's sentinel —
// a package-level value (not just an inline error) so tests can assert
// the exact no-widget-posted contract via errors.Is rather than message
// text.
var errMultiSelectNotSupported = errors.New("mattermost: multi-select is not supported; falls back to numbered text")

// errActionURLUnavailable is returned by SendInteractiveQuestion when
// the adapter has no orchestrator URL to derive an attachment-action
// integration.url from. Per specs/mattermost-question-actions/spec.md
// ("A missing action URL falls back to text rather than rendering a
// dead button"), failing the send — not posting a widget with an empty
// URL — is what makes the question router's tryInteractiveSend fall
// back to numbered text.
var errActionURLUnavailable = errors.New("mattermost: no orchestrator action URL configured (OPENCODE_BRIDGE_REGISTRAR_URL unset)")

// SendInteractiveQuestion implements bridge.InteractiveQuestionSender for
// Mattermost via the `attachment.actions` button widget — one action per
// choice, each carrying an integration.url pointing at the
// orchestrator's `/router/mattermost/attachment-action` endpoint and an
// integration.context of {peerId, requestId, choice, token}. The token
// is a keyed MAC over (channel, identity, peerId, requestId, choice) —
// see computeActionToken — which the orchestrator recomputes to verify
// the click before acting on it (Mattermost message actions carry no
// platform signature of their own).
//
// Returns errActionURLUnavailable when the adapter has no action URL
// (the pod's OPENCODE_BRIDGE_REGISTRAR_URL is unset) — this is a SEND
// FAILURE, not a degraded post, so the question router falls back to
// numbered text instead of posting a button that submits nowhere.
func (a *Adapter) SendInteractiveQuestion(ctx context.Context, peer bridge.PeerRef, prompt string, choices []bridge.QuestionChoice) (string, error) {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return "", fmt.Errorf("mattermost: invalid peer-id %q", peer.PeerID)
	}
	if len(choices) == 0 {
		return "", errors.New("mattermost: SendInteractiveQuestion requires at least one choice")
	}
	if a.actionURL == "" {
		return "", errActionURLUnavailable
	}

	requestID := newActionRequestID()
	actions := make([]map[string]any, 0, len(choices))
	for i, c := range choices {
		token := computeActionToken(a.actionSecret, "mattermost", a.identityID, peer.PeerID, requestID, c.Value)
		actions = append(actions, map[string]any{
			"id":   fmt.Sprintf("router_q_%d", i),
			"name": c.Label,
			"integration": map[string]any{
				"url": a.actionURL,
				"context": map[string]any{
					"peerId":    peer.PeerID,
					"requestId": requestID,
					"choice":    c.Value,
					"token":     token,
				},
			},
		})
	}
	att := map[string]any{
		"color":   "#0066cc",
		"pretext": defaultQuestionPrompt(prompt),
		"actions": actions,
	}
	if choices[0].Custom {
		att["footer"] = "💬 Or reply in this thread (@-mention required)"
	}
	props := map[string]any{"attachments": []map[string]any{att}}
	post, err := a.client.CreatePost(ctx, CreatePostInput{
		ChannelID: parsed.ChannelID,
		RootID:    parsed.RootPostID,
		Props:     props,
	})
	if err != nil {
		return "", fmt.Errorf("mattermost: SendInteractiveQuestion: %w", err)
	}
	resolved := ""
	if parsed.RootPostID == "" {
		resolved = FormatPeerID(Peer{ChannelID: post.ChannelID, RootPostID: post.ID})
	}
	return resolved, nil
}

// defaultQuestionPrompt returns a non-empty header string for a
// Mattermost interactive question widget — mirrors slack/render.go's
// helper of the same name/behaviour so the two platforms present
// identical fallback wording when an agent renders the narrative via a
// preceding router_send and passes only `options`.
func defaultQuestionPrompt(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return "Please choose one:"
	}
	return prompt
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

// buildMultiSelectAttachment composes the Slack-compatible attachment
// dict for a multi-select question widget. Extracted so unit tests can
// assert on shape without driving the full SendInteractiveMultiSelect
// flow. The Custom flag on the first choice gates the discoverability
// `footer` per bridge-question-custom-answer-hint.
func buildMultiSelectAttachment(prompt string, choices []bridge.QuestionChoice) map[string]any {
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
		"integration": map[string]any{"url": ""},
	}
	att := map[string]any{
		"color":   "#0066cc",
		"pretext": prompt,
		"actions": []map[string]any{action},
	}
	if len(choices) > 0 && choices[0].Custom {
		att["footer"] = "💬 Or reply in this thread (@-mention required)"
	}
	return att
}

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
