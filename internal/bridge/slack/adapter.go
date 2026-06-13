package slack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
)

// Constants matching the TS bridge.
const (
	// MaxTextLength is Slack's per-message text cap.
	MaxTextLength = 39_000

	// MaxFileSize is Slack's files.uploadV2 limit (1 GiB). Larger
	// attachments are rejected pre-upload.
	MaxFileSize int64 = 1 * 1024 * 1024 * 1024
)

// Identity configures one Slack app identity.
type Identity struct {
	ID            string
	BotToken      string // xoxb-...
	AppToken      string // xapp-... (Socket Mode app-level token)
	GroupsEnabled bool   // accept messages in public/private channels (not just DMs); spec calls this out as per-identity
}

// Options bundles construction-time knobs. Tests use APIURL +
// HTTPClient to point the REST surface at an httptest.Server; production
// callers leave them blank.
type Options struct {
	APIURL     string
	HTTPClient *http.Client
	MediaDir   string
}

// Adapter is the bridge.Adapter implementation for one Slack app identity.
type Adapter struct {
	id       Identity
	mediaDir string

	api    *slackgo.Client
	socket *socketmode.Client

	mu        sync.Mutex
	started   atomic.Bool
	stopping  atomic.Bool
	botUserID atomic.Value // string; set by Start (AuthTest)
	cancel    context.CancelFunc
	inbound   atomic.Value // chan<- bridge.Inbound

	statusVal     atomic.Value // string
	lastError     atomic.Value // string
	lastInboundAt atomic.Int64
	lastFailureAt atomic.Int64

	// fileBaseURL overrides the URL prefix for file_private downloads.
	// Tests set this so the adapter fetches from their mock server.
	fileBaseURL atomic.Value // string

	// toolCardsOnce lazy-initialises the tool-call → message reference
	// cache used by RichRenderer.Render to coalesce a tool's call+result
	// into a single chat.update'd message. See bridge-tool-render-native.
	toolCardsOnce  sync.Once
	toolCardsCache *toolCardCache
}

// toolCards returns the adapter's lazily-initialised tool-card cache.
func (a *Adapter) toolCards() *toolCardCache {
	a.toolCardsOnce.Do(func() {
		a.toolCardsCache = newToolCardCache()
	})
	return a.toolCardsCache
}

// New constructs a Slack adapter. Bot and app tokens are required.
func New(id Identity, opts Options) (*Adapter, error) {
	bot := strings.TrimSpace(id.BotToken)
	app := strings.TrimSpace(id.AppToken)
	if bot == "" {
		return nil, errors.New("slack: bot token is required")
	}
	if app == "" {
		return nil, errors.New("slack: app token is required")
	}

	clientOpts := []slackgo.Option{slackgo.OptionAppLevelToken(app)}
	if opts.APIURL != "" {
		clientOpts = append(clientOpts, slackgo.OptionAPIURL(opts.APIURL))
	}
	if opts.HTTPClient != nil {
		clientOpts = append(clientOpts, slackgo.OptionHTTPClient(opts.HTTPClient))
	}
	api := slackgo.New(bot, clientOpts...)
	// Socket Mode client wraps the REST client and inherits its options.
	smc := socketmode.New(api)

	a := &Adapter{
		id:       id,
		mediaDir: opts.MediaDir,
		api:      api,
		socket:   smc,
	}
	a.statusVal.Store("disabled")
	a.lastError.Store("")
	a.botUserID.Store("")
	return a, nil
}

// Channel implements bridge.Adapter.
func (a *Adapter) Channel() string { return "slack" }

// Identity implements bridge.Adapter.
func (a *Adapter) Identity() string { return a.id.ID }

// Status implements bridge.Adapter.
func (a *Adapter) Status() bridge.AdapterStatus {
	return bridge.AdapterStatus{
		Status:        getString(&a.statusVal),
		LastError:     getString(&a.lastError),
		LastInboundAt: a.lastInboundAt.Load(),
		LastFailureAt: a.lastFailureAt.Load(),
	}
}

// SetBotUserID injects the bot's Slack user ID. Tests use this to skip
// the AuthTest round-trip; production code lets Start populate it.
func (a *Adapter) SetBotUserID(id string) { a.botUserID.Store(id) }

// BotUserID returns the bot's resolved Slack user ID (or "" before Start).
func (a *Adapter) BotUserID() string { return getString(&a.botUserID) }

// SetFileBaseURL overrides the URL prefix used for downloading inbound
// files. Tests point this at their mock server.
func (a *Adapter) SetFileBaseURL(base string) {
	a.fileBaseURL.Store(strings.TrimRight(base, "/"))
}

// API returns the underlying *slackgo.Client. Production callers don't
// need this; tests use it to drive bound HTTP fixtures.
func (a *Adapter) API() *slackgo.Client { return a.api }

// Start implements bridge.Adapter. AuthTest is called first to learn the
// bot's user ID; then the Socket Mode connection runs in its own
// goroutine. Start returns immediately after the goroutine is launched.
func (a *Adapter) Start(ctx context.Context, inbound chan<- bridge.Inbound) error {
	if !a.started.CompareAndSwap(false, true) {
		return errors.New("slack: adapter already started")
	}
	a.inbound.Store(inbound)

	// AuthTest reveals the bot user ID we need for own-message + mention
	// filtering. Tests inject via SetBotUserID before Start to skip this.
	if a.BotUserID() == "" {
		resp, err := a.api.AuthTestContext(ctx)
		if err == nil && resp != nil {
			a.botUserID.Store(resp.UserID)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.statusVal.Store("running")

	// Socket Mode dispatch goroutine. Reads from a.socket.Events and
	// fans into a.dispatchEvent. Errors from the lib's reconnect loop are
	// logged; the lib itself reconnects per the chat-bridge spec ("Socket
	// Mode handshake retries and reconnects MUST rely on the library's
	// built-in behavior rather than re-implementing in-process retry
	// logic"). When runCtx is cancelled the lib's Run() returns.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Error("slack: dispatch goroutine panicked",
					"identity", a.id.ID, "panic", r)
			}
		}()
		for {
			select {
			case <-runCtx.Done():
				return
			case ev, ok := <-a.socket.Events:
				if !ok {
					return
				}
				a.dispatchSocketModeEvent(runCtx, ev)
			}
		}
	}()

	// Run the Socket Mode client (blocking until disconnect). Wrapped in
	// its own goroutine so Start can return after kickoff.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Error("slack: Socket Mode Run panicked",
					"identity", a.id.ID, "panic", r)
			}
		}()
		if err := a.socket.RunContext(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			logging.Warn("slack: Socket Mode Run returned",
				"identity", a.id.ID, "err", err)
		}
	}()
	return nil
}

// Stop cancels the Socket Mode connection. Idempotent.
func (a *Adapter) Stop() error {
	if !a.stopping.CompareAndSwap(false, true) {
		return nil
	}
	if a.cancel != nil {
		a.cancel()
	}
	a.statusVal.Store("disabled")
	return nil
}

// SendInteractiveQuestion implements bridge.InteractiveQuestionSender —
// renders the question as a Slack `chat.postMessage` with an actions
// block (one button per choice). The button's `value` field carries the
// callback payload; when the reviewer clicks, Socket Mode delivers a
// `block_actions` envelope that handleInteractiveCallback parses.
func (a *Adapter) SendInteractiveQuestion(ctx context.Context, peer bridge.PeerRef, prompt string, choices []bridge.QuestionChoice) (string, error) {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return "", ErrInvalidPeerID
	}
	if len(choices) == 0 {
		return "", errors.New("slack: SendInteractiveQuestion requires at least one choice")
	}

	textBlock := slackgo.NewTextBlockObject(slackgo.MarkdownType, prompt, false, false)
	header := slackgo.NewSectionBlock(textBlock, nil, nil)

	buttons := make([]slackgo.BlockElement, 0, len(choices))
	for i, c := range choices {
		btnID := fmt.Sprintf("router_q_%d", i)
		btnText := slackgo.NewTextBlockObject(slackgo.PlainTextType, c.Label, false, false)
		btn := slackgo.NewButtonBlockElement(btnID, c.Value, btnText)
		buttons = append(buttons, btn)
	}
	actions := slackgo.NewActionBlock("router_question", buttons...)

	blocks := []slackgo.Block{header, actions}
	if choices[0].Custom {
		blocks = append(blocks, customAnswerHintBlock())
	}
	opts := []slackgo.MsgOption{slackgo.MsgOptionBlocks(blocks...)}
	if parsed.ThreadTS != "" {
		opts = append(opts, slackgo.MsgOptionTS(parsed.ThreadTS))
	}
	_, ts, err := a.api.PostMessageContext(ctx, parsed.ChannelID, opts...)
	if err != nil {
		return "", fmt.Errorf("slack: SendInteractiveQuestion: %w", err)
	}
	// Mutation guard: only echo the resolved composite peer-id when the
	// bind STARTED channel-only AND the post is on a channel (not DM).
	// When the bind was already composite (e.g. "C123|<thread_ts>"), the
	// post uses MsgOptionTS(threadTS) and lands INSIDE that thread; the
	// returned `ts` is the new MESSAGE'S ts (different from the thread
	// ts) — mutating to that would BREAK subsequent inbound routing
	// because the reviewer's reply arrives keyed by the THREAD ts, not
	// the message ts. See bridge-question-binding-anchoring scenario
	// "Subsequent question in the same thread is a no-op for the binding".
	if parsed.ThreadTS != "" || IsDM(parsed.ChannelID) {
		return "", nil
	}
	resolved := FormatPeerID(Peer{ChannelID: parsed.ChannelID, ThreadTS: ts})
	return resolved, nil
}

// MultiSelectMaxOptions is Slack's documented upper bound for the
// multi_static_select element's options list. Prompts with more
// choices fall back to a numbered-text render that accepts a typed
// comma-separated reply (parseQuestionAnswers handles it).
const MultiSelectMaxOptions = 50

// SendInteractiveMultiSelect implements bridge.InteractiveMultiSelectSender.
// Renders a `Multiple = true` question as a Slack Block Kit message with
// a multi_static_select element + Apply button. The reviewer toggles
// selections in the menu; on Apply, Slack delivers a `block_actions`
// envelope whose state holds the selected option list — the adapter's
// callback handler reads it and pushes a comma-separated inbound for the
// bridge's parseQuestionAnswers to consume.
//
// When len(choices) > MultiSelectMaxOptions, returns ErrTooManyOptions
// so the router falls back to the numbered-text path.
func (a *Adapter) SendInteractiveMultiSelect(ctx context.Context, peer bridge.PeerRef, prompt string, choices []bridge.QuestionChoice) (string, error) {
	parsed := ParsePeerID(peer.PeerID)
	if parsed.ChannelID == "" {
		return "", ErrInvalidPeerID
	}
	if len(choices) == 0 {
		return "", errors.New("slack: SendInteractiveMultiSelect requires at least one choice")
	}
	if len(choices) > MultiSelectMaxOptions {
		return "", ErrTooManyOptions
	}

	textBlock := slackgo.NewTextBlockObject(slackgo.MarkdownType, prompt, false, false)
	header := slackgo.NewSectionBlock(textBlock, nil, nil)

	options := make([]*slackgo.OptionBlockObject, 0, len(choices))
	for _, c := range choices {
		label := slackgo.NewTextBlockObject(slackgo.PlainTextType, c.Label, false, false)
		options = append(options, slackgo.NewOptionBlockObject(c.Value, label, nil))
	}
	multi := slackgo.NewOptionsMultiSelectBlockElement(
		slackgo.MultiOptTypeStatic,
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "Select options", false, false),
		"router_multi_select",
		options...,
	)
	applyBtn := slackgo.NewButtonBlockElement(
		"router_multi_apply",
		"submit",
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "Apply", false, false),
	)
	actions := slackgo.NewActionBlock("router_multi", multi, applyBtn)

	blocks := []slackgo.Block{header, actions}
	if choices[0].Custom {
		blocks = append(blocks, customAnswerHintBlock())
	}
	opts := []slackgo.MsgOption{slackgo.MsgOptionBlocks(blocks...)}
	if parsed.ThreadTS != "" {
		opts = append(opts, slackgo.MsgOptionTS(parsed.ThreadTS))
	}
	_, ts, err := a.api.PostMessageContext(ctx, parsed.ChannelID, opts...)
	if err != nil {
		return "", fmt.Errorf("slack: SendInteractiveMultiSelect: %w", err)
	}
	// Same guard as SendInteractiveQuestion: only mutate when bind
	// started channel-only.
	if parsed.ThreadTS != "" || IsDM(parsed.ChannelID) {
		return "", nil
	}
	resolved := FormatPeerID(Peer{ChannelID: parsed.ChannelID, ThreadTS: ts})
	return resolved, nil
}

// ErrTooManyOptions signals the caller that the choice list exceeds
// Slack's multi-select widget capacity — the question router catches
// this and falls back to the numbered-text path.
var ErrTooManyOptions = errors.New("slack: too many options for multi_static_select widget")

// ResolveUserToDM implements bridge.Adapter. Slack user-id form (U-prefix)
// is resolved to a DM channel via conversations.open; D-prefix and C-prefix
// (channel) values pass through unchanged.
func (a *Adapter) ResolveUserToDM(ctx context.Context, peerID string) (string, error) {
	if !LooksLikeUserID(peerID) {
		return peerID, nil
	}
	ch, _, _, err := a.api.OpenConversationContext(ctx, &slackgo.OpenConversationParameters{
		Users: []string{peerID},
	})
	if err != nil {
		return "", fmt.Errorf("slack: conversations.open: %w", err)
	}
	if ch == nil || ch.ID == "" {
		return "", errors.New("slack: conversations.open returned empty channel")
	}
	return ch.ID, nil
}

// dispatchSocketModeEvent handles one Socket Mode envelope. ack is always
// called (per the lib's contract); the inner Events API event is then
// classified into message / app_mention / other.
func (a *Adapter) dispatchSocketModeEvent(ctx context.Context, env socketmode.Event) {
	defer func() {
		if r := recover(); r != nil {
			logging.Error("slack: dispatchSocketModeEvent panic",
				"identity", a.id.ID, "panic", r)
		}
	}()

	// Acknowledge envelopes so Slack doesn't retry-storm.
	if env.Request != nil && (env.Type == socketmode.EventTypeEventsAPI || env.Type == socketmode.EventTypeInteractive) {
		_ = a.socket.Ack(*env.Request)
	}

	switch env.Type {
	case socketmode.EventTypeEventsAPI:
		apiEvent, ok := env.Data.(slackevents.EventsAPIEvent)
		if !ok || apiEvent.Type != slackevents.CallbackEvent {
			return
		}
		switch ev := apiEvent.InnerEvent.Data.(type) {
		case *slackevents.MessageEvent:
			a.handleMessageEvent(ctx, ev)
		case *slackevents.AppMentionEvent:
			a.handleAppMention(ctx, ev)
		}
	case socketmode.EventTypeInteractive:
		// block_actions click → synthesize an inbound carrying the
		// button's value so the bridge's QuestionRouter parses it as
		// a question reply.
		callback, ok := env.Data.(slackgo.InteractionCallback)
		if !ok {
			return
		}
		a.handleInteractiveCallback(ctx, callback)
	}
}

// handleInteractiveCallback converts a Slack `block_actions` click into
// a bridge.Inbound whose Text is the action's value (the question
// choice's canonical answer label). The bridge's QuestionRouter does
// the rest.
//
// Single-select (button): action.Value carries the answer.
// Multi-select (Apply button): the most recent multi_static_select
// state lives on callback.BlockActionState (or in the action's
// SelectedOptions field). We snapshot it as a comma-separated list and
// emit one inbound — the bridge's parseQuestionAnswers honours the same
// format as a comma-separated typed reply.
func (a *Adapter) handleInteractiveCallback(ctx context.Context, callback slackgo.InteractionCallback) {
	if len(callback.ActionCallback.BlockActions) == 0 {
		return
	}
	action := callback.ActionCallback.BlockActions[0]
	value := action.Value
	// Multi-select Apply button: collect SelectedOptions from the message
	// state (the multi_static_select element's accumulated selection).
	if action.ActionID == "router_multi_apply" {
		selected := collectMultiSelectAnswers(callback)
		if len(selected) == 0 {
			// Nothing selected — silently ignore; reviewer can re-submit.
			return
		}
		value = strings.Join(selected, ", ")
	} else if action.ActionID == "router_multi_select" {
		// Toggle event for the multi-select widget — DO NOT emit an
		// inbound; wait for the Apply button. Slack delivers a
		// block_actions on every selection change but we coalesce into
		// the Apply press to give the reviewer one chance to revise.
		return
	}
	if value == "" {
		return
	}
	channel := callback.Channel.ID
	if channel == "" {
		return
	}
	// Thread context: only meaningful for channel peers (C-prefix). DMs
	// (D-prefix) don't have threads — the bound peer_id stays as the
	// flat D-channel id, so we MUST NOT append the message's ts here.
	// Without this guard the synthesized inbound's peer_id is
	// "D<id>|<ts>", which doesn't match the bound "D<id>"; the bridge
	// then routes the click to a fresh session and the QuestionRouter
	// can't reconcile the answer with its pending request.
	var peerID string
	if IsDM(channel) {
		peerID = FormatPeerID(Peer{ChannelID: channel})
	} else {
		threadTS := callback.Message.ThreadTimestamp
		if threadTS == "" {
			threadTS = callback.Message.Timestamp
		}
		peerID = FormatPeerID(Peer{ChannelID: channel, ThreadTS: threadTS})
	}

	a.pushInbound(ctx, bridge.Inbound{
		Peer: bridge.PeerRef{
			Channel:  "slack",
			Identity: a.id.ID,
			PeerID:   peerID,
		},
		Text:       value,
		AuthorID:   callback.User.ID,
		ReceivedAt: time.Now().UnixMilli(),
	})

	// Replace the actionable widget with a confirmation. Inbound has
	// already been pushed; update failure is non-fatal — log warn and
	// move on. See spec: bridge-question-answered-widget-update.
	a.updateAnsweredWidget(ctx, callback, value)
}

// updateAnsweredWidget rewrites the question message to drop its
// actions block and add a "✓ Answered: …" confirmation section.
// Selected labels for multi-select come from the comma-joined `value`
// the caller already produced for the inbound.
func (a *Adapter) updateAnsweredWidget(ctx context.Context, callback slackgo.InteractionCallback, value string) {
	if callback.Container.ChannelID == "" || callback.Container.MessageTs == "" {
		return
	}
	labels := splitMultiSelectValue(value)
	originalBlocks := callback.Message.Blocks.BlockSet
	fallback := callback.Message.Text
	newBlocks := buildAnsweredBlocks(originalBlocks, fallback, labels)
	_, _, _, err := a.api.UpdateMessageContext(ctx,
		callback.Container.ChannelID,
		callback.Container.MessageTs,
		slackgo.MsgOptionBlocks(newBlocks...),
	)
	if err != nil {
		logging.Warn("bridge: slack answered widget update failed",
			"channel", callback.Container.ChannelID,
			"ts", callback.Container.MessageTs,
			"err", err,
		)
	}
}

// splitMultiSelectValue splits the comma-joined Apply value back into
// labels; single-select inbounds carry one element.
func splitMultiSelectValue(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = append(out, strings.TrimSpace(value))
	}
	return out
}

// collectMultiSelectAnswers extracts the selected values from a multi-
// select widget's state attached to a block_actions callback. The
// state shape is callback.BlockActionState.Values[blockID][actionID]
// .SelectedOptions for multi_static_select. Returns the slice of
// canonical answer labels (Option.Value) in selection order; nil when
// no state OR no selection.
func collectMultiSelectAnswers(callback slackgo.InteractionCallback) []string {
	if callback.BlockActionState == nil {
		return nil
	}
	for _, blockState := range callback.BlockActionState.Values {
		if action, ok := blockState["router_multi_select"]; ok {
			out := make([]string, 0, len(action.SelectedOptions))
			for _, opt := range action.SelectedOptions {
				out = append(out, opt.Value)
			}
			return out
		}
	}
	return nil
}

// SetInbound stores the orchestrator's inbound channel. Tests use this
// to inject a synthetic channel before driving HandleMessageEvent /
// HandleAppMention; production code goes through Start instead.
func (a *Adapter) SetInbound(ch chan<- bridge.Inbound) {
	a.inbound.Store(ch)
}

// HandleMessageEvent is the test-driven entry point: tests construct
// a synthetic *slackevents.MessageEvent and call this directly. Production
// dispatch goes through dispatchSocketModeEvent.
func (a *Adapter) HandleMessageEvent(ctx context.Context, ev *slackevents.MessageEvent) {
	a.handleMessageEvent(ctx, ev)
}

// HandleAppMention is the test-driven entry point for app_mention events.
func (a *Adapter) HandleAppMention(ctx context.Context, ev *slackevents.AppMentionEvent) {
	a.handleAppMention(ctx, ev)
}

// handleMessageEvent processes a `message` Events API event. Per the spec,
// only DMs are routed automatically; channel messages require an explicit
// @mention (handled by handleAppMention).
func (a *Adapter) handleMessageEvent(ctx context.Context, ev *slackevents.MessageEvent) {
	if ev == nil || ev.Channel == "" {
		return
	}
	// Subtype filtering: skip bot/edits/joins, accept the empty subtype
	// (regular user message) and `file_share` (user attached a file).
	if ev.BotID != "" {
		return
	}
	if ev.SubType != "" && ev.SubType != "file_share" {
		return
	}
	if ev.User != "" && ev.User == a.BotUserID() {
		return
	}

	// Only DMs are routed automatically. Public/private channel messages
	// flow through handleAppMention.
	if !IsDM(ev.Channel) {
		return
	}

	peerID := FormatPeerID(Peer{ChannelID: ev.Channel, ThreadTS: ev.ThreadTimeStamp})
	// MessageEvent's custom UnmarshalJSON normalises top-level fields
	// (including `files`) into ev.Message, even for regular messages.
	// See slackevents/inner_events.go's UnmarshalJSON.
	var files []slackgo.File
	if ev.Message != nil {
		files = ev.Message.Files
	}
	atts := a.downloadFiles(ctx, peerID, files)
	text := strings.TrimSpace(ev.Text)
	if text == "" && len(atts) == 0 {
		return
	}

	a.pushInbound(ctx, bridge.Inbound{
		Peer: bridge.PeerRef{
			Channel:  "slack",
			Identity: a.id.ID,
			PeerID:   peerID,
		},
		Text:        text,
		Attachments: atts,
		AuthorID:    ev.User,
		ReceivedAt:  time.Now().UnixMilli(),
	})
}

// handleAppMention processes an `app_mention` event — the bot is @-mentioned
// in a public or private channel. Per spec the bridge MUST forward these
// (provided the identity's groupsEnabled is true, or the spec defaults
// app_mentions to "always forward" since the mention is explicit consent).
// The TS bridge always forwards app_mentions; we follow.
func (a *Adapter) handleAppMention(ctx context.Context, ev *slackevents.AppMentionEvent) {
	if ev == nil || ev.Channel == "" {
		return
	}
	if ev.BotID != "" {
		return
	}
	if ev.User != "" && ev.User == a.BotUserID() {
		return
	}

	// app_mention has no SubType; treat it as a normal message. Thread
	// resolution: if ThreadTimeStamp is set, use it; otherwise the mention
	// IS the thread root and we use its own ts.
	rootTS := ev.ThreadTimeStamp
	if rootTS == "" {
		rootTS = ev.TimeStamp
	}
	peerID := FormatPeerID(Peer{ChannelID: ev.Channel, ThreadTS: rootTS})
	atts := a.downloadFiles(ctx, peerID, ev.Files)
	text := StripMention(ev.Text, a.BotUserID())
	if text == "" && len(atts) == 0 {
		return
	}

	a.pushInbound(ctx, bridge.Inbound{
		Peer: bridge.PeerRef{
			Channel:  "slack",
			Identity: a.id.ID,
			PeerID:   peerID,
		},
		Text:        text,
		Attachments: atts,
		AuthorID:    ev.User,
		ReceivedAt:  time.Now().UnixMilli(),
	})
}

// pushInbound forwards an Inbound onto the orchestrator's channel, with
// ctx-cancellation tolerance.
func (a *Adapter) pushInbound(ctx context.Context, in bridge.Inbound) {
	a.lastInboundAt.Store(in.ReceivedAt)
	ch := a.inboundChan()
	if ch == nil {
		return
	}
	select {
	case ch <- in:
	case <-ctx.Done():
	}
}

func (a *Adapter) inboundChan() chan<- bridge.Inbound {
	v := a.inbound.Load()
	if v == nil {
		return nil
	}
	if ch, ok := v.(chan<- bridge.Inbound); ok {
		return ch
	}
	return nil
}

// downloadFiles fetches every inbound attachment. Returns the resulting
// bridge.Attachment slice; individual failures are logged and skipped.
func (a *Adapter) downloadFiles(ctx context.Context, peerID string, files []slackgo.File) []bridge.Attachment {
	if len(files) == 0 {
		return nil
	}
	out := make([]bridge.Attachment, 0, len(files))
	for _, f := range files {
		url := f.URLPrivateDownload
		if url == "" {
			url = f.URLPrivate
		}
		if url == "" || f.ID == "" {
			continue
		}
		// Rewrite to the test base URL if configured. Slack file URLs
		// look like https://files.slack.com/... — tests can swap them
		// for an httptest endpoint by setting SetFileBaseURL.
		if base := getString(&a.fileBaseURL); base != "" {
			// Use base + "/file/" + fileID as the canonical mock path.
			url = base + "/file/" + f.ID
		}
		att, err := a.downloadOne(ctx, peerID, url, f)
		if err != nil {
			logging.Warn("slack: file download failed",
				"identity", a.id.ID, "peer", peerID, "file", f.ID, "err", err)
			continue
		}
		out = append(out, att)
	}
	return out
}

func (a *Adapter) downloadOne(ctx context.Context, peerID, url string, f slackgo.File) (bridge.Attachment, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return bridge.Attachment{}, err
	}
	req.Header.Set("Authorization", "Bearer "+a.id.BotToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return bridge.Attachment{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return bridge.Attachment{}, fmt.Errorf("slack: download %s: %d %s", f.ID, resp.StatusCode, string(body))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return bridge.Attachment{}, err
	}
	filename := f.Name
	if filename == "" {
		filename = "file-" + f.ID
	}
	att := bridge.Attachment{
		FileName: filename,
		MimeType: f.Mimetype,
		Content:  data,
	}
	if a.mediaDir != "" {
		if err := os.MkdirAll(a.mediaDir, 0o700); err != nil {
			return att, fmt.Errorf("slack: media dir: %w", err)
		}
		path := filepath.Join(a.mediaDir, f.ID+"-"+filename)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return att, fmt.Errorf("slack: write attachment: %w", err)
		}
		att.FilePath = path
	}
	return att, nil
}

// Send implements bridge.Adapter. Channel-only peers (C... or G... without
// thread ts) return ResolvedPeer = "<channel>|<ts>" on first post so the
// orchestrator can mutate the binding to channel+thread form.
func (a *Adapter) Send(ctx context.Context, out bridge.Outbound) bridge.SendResult {
	peer := ParsePeerID(out.Peer.PeerID)
	if peer.ChannelID == "" {
		return bridge.SendResult{Err: ErrInvalidPeerID}
	}

	text := out.Text
	if out.Mention != "" {
		text = out.Mention + " " + text
	}
	// Slack counts MaxTextLength in characters, not bytes. Slicing at a
	// byte boundary that lands mid-codepoint produces invalid UTF-8 that
	// the API can reject and renders as the replacement character. Cap
	// by rune so the cut always lands on a codepoint boundary.
	text = truncateRunes(text, MaxTextLength)

	// Text part first.
	resolved := ""
	if text != "" {
		opts := []slackgo.MsgOption{slackgo.MsgOptionText(text, false)}
		if peer.ThreadTS != "" {
			opts = append(opts, slackgo.MsgOptionTS(peer.ThreadTS))
		}
		_, ts, err := a.api.PostMessageContext(ctx, peer.ChannelID, opts...)
		if err != nil {
			a.recordFailure(err)
			return bridge.SendResult{Err: fmt.Errorf("slack postMessage: %w", err)}
		}
		// On the first outbound to a channel-only peer, surface the
		// returned ts so the orchestrator can rewrite the binding's
		// peer_id to channel+thread form. We only do this when no
		// thread ts was supplied (DM-form peers don't need rewriting).
		if peer.ThreadTS == "" && !IsDM(peer.ChannelID) && ts != "" {
			resolved = FormatPeerID(Peer{ChannelID: peer.ChannelID, ThreadTS: ts})
		}
	}

	// Attachments.
	for _, att := range out.Attachments {
		if int64(len(att.Content)) > MaxFileSize {
			err := fmt.Errorf("slack: attachment %q exceeds %d byte limit", att.FileName, MaxFileSize)
			a.recordFailure(err)
			return bridge.SendResult{Err: err}
		}
		filename := att.FileName
		if filename == "" {
			filename = "attachment"
		}
		params := slackgo.UploadFileParameters{
			Filename: filename,
			Reader:   bytes.NewReader(att.Content),
			FileSize: len(att.Content),
			Channel:  peer.ChannelID,
		}
		if peer.ThreadTS != "" {
			params.ThreadTimestamp = peer.ThreadTS
		}
		_, err := a.api.UploadFileContext(ctx, params)
		if err != nil {
			a.recordFailure(err)
			return bridge.SendResult{Err: fmt.Errorf("slack uploadFile: %w", err)}
		}
	}

	return bridge.SendResult{Delivered: true, ResolvedPeer: resolved}
}

func (a *Adapter) recordFailure(err error) {
	if err == nil {
		return
	}
	a.lastError.Store(redactToken(err.Error(), a.id.BotToken))
	a.lastFailureAt.Store(time.Now().UnixMilli())
}

func getString(v *atomic.Value) string {
	if v == nil {
		return ""
	}
	s, _ := v.Load().(string)
	return s
}

// redactToken replaces the bot token in s with "<redacted>".
func redactToken(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<redacted>")
}

// truncateRunes returns s capped to maxRunes codepoints. Cutting a
// UTF-8 string at a byte index can land mid-codepoint and produce
// invalid UTF-8; counting runes guarantees the cut is at a codepoint
// boundary. No ellipsis is appended — the Slack post is rendered as-is.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}
