package telegram

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
)

// Constants matching the TS bridge.
const (
	// MaxTextLength is Telegram's per-message text cap. Longer messages
	// are chunked at Send time.
	MaxTextLength = 4096

	// MaxCaptionLength is Telegram's per-attachment caption cap.
	MaxCaptionLength = 1024

	// MaxFileSize is Telegram's bot-API upload limit (50 MiB). Larger
	// attachments are rejected pre-upload with an error.
	MaxFileSize int64 = 50 * 1024 * 1024
)

// AccessMode is the per-identity access policy for a Telegram bot.
//
//   - AccessPublic: any peer who messages the bot is forwarded to the
//     orchestrator. Used for bots that are open to the world.
//   - AccessPrivate: peers must first redeem a pairing code via the
//     `/pair <code>` chat command to be added to the bridge_allowlist;
//     inbound from non-allowlisted peers is intercepted with a hint.
type AccessMode int

const (
	AccessPublic AccessMode = iota
	AccessPrivate
)

// AllowlistChecker lets the adapter consult the bridge's per-identity
// allowlist (bridge_allowlist table) without importing internal/bridge/store
// directly. The adapter receives both checker + adder closures at
// construction time so persistence stays at the orchestrator boundary.
type AllowlistChecker func(ctx context.Context, peerID string) (bool, error)

// AllowlistAdder inserts a peer into the per-identity allowlist after
// a successful /pair command. Idempotent — duplicate adds are a no-op.
type AllowlistAdder func(ctx context.Context, peerID string) error

// Identity configures one Telegram bot identity.
type Identity struct {
	ID     string
	Token  string
	Access AccessMode
	// PairingCodeHash is the sha256-hex of the pairing code. The adapter
	// hashes incoming /pair codes and compares against this value;
	// equality grants the peer access. Required when Access == Private.
	PairingCodeHash string
	GroupsEnabled   bool
	// Inbound controls whether Start opens the long-poll loop.
	// bridge.InboundDisabled skips the long-poll goroutine entirely;
	// outbound Send / Render / interactive-question posting remain
	// active. Used by orchestrator-mediated-inbound deployments. Empty
	// or bridge.InboundEnabled keeps today's behaviour.
	Inbound string
}

// Options bundles construction-time knobs. The HTTPClient and ServerURL
// fields allow tests to point the adapter at an httptest.Server mimicking
// the Telegram API.
type Options struct {
	HTTPClient   *http.Client
	ServerURL    string
	PollTimeout  time.Duration
	MediaDir     string
	Allowlisted  AllowlistChecker
	AddAllowlist AllowlistAdder
}

// Adapter is the bridge.Adapter implementation for one configured
// Telegram bot identity.
type Adapter struct {
	id        Identity
	mediaDir  string
	allowlist AllowlistChecker
	addAllow  AllowlistAdder

	bot   *tgbot.Bot
	token string

	me atomic.Value // *models.User; set during Start

	// inbound holds the orchestrator's bridge.Inbound channel for the
	// duration of a Start/Stop cycle. Stored via atomic.Value so the
	// stateless dispatchUpdate handler can read it without locking.
	inbound atomic.Value // chan<- bridge.Inbound

	cancel context.CancelFunc

	mu       sync.Mutex
	started  atomic.Bool
	stopping atomic.Bool

	statusVal     atomic.Value // string
	lastError     atomic.Value // string
	lastInboundAt atomic.Int64
	lastFailureAt atomic.Int64

	// fileBaseURL is the base URL for inbound file downloads. Empty
	// means use the Telegram default (https://api.telegram.org).
	fileBaseURL atomic.Value // string

	// toolCardsOnce lazy-initialises the tool-call → message reference
	// cache used by RichRenderer.Render to coalesce a tool's call+result
	// into a single edit_message_text'd message. See bridge-tool-render-native.
	toolCardsOnce  sync.Once
	toolCardsCache *toolCardCache

	// multiSelectOnce lazy-initialises the multi-select state map
	// used by SendInteractiveMultiSelect (toggle/apply lifecycle).
	multiSelectOnce  sync.Once
	multiSelectStateMap *multiSelectState
}

// toolCards returns the adapter's tool-card cache, lazy-initialised
// on first use.
func (a *Adapter) toolCards() *toolCardCache {
	a.toolCardsOnce.Do(func() {
		a.toolCardsCache = newToolCardCache()
	})
	return a.toolCardsCache
}

// multiSelectStates returns the adapter's in-flight multi-select
// state map, lazy-initialised on first use.
func (a *Adapter) multiSelectStates() *multiSelectState {
	a.multiSelectOnce.Do(func() {
		a.multiSelectStateMap = newMultiSelectState()
	})
	return a.multiSelectStateMap
}

// New constructs a Telegram adapter. The bot is initialized but not
// started — call Start to begin the long-poll loop.
func New(id Identity, opts Options) (*Adapter, error) {
	token := strings.TrimSpace(id.Token)
	if token == "" {
		return nil, errors.New("telegram: token is required")
	}
	if id.Access == AccessPrivate && id.PairingCodeHash == "" {
		return nil, errors.New("telegram: pairingCodeHash is required for private access mode")
	}
	if id.Access == AccessPrivate && (opts.Allowlisted == nil || opts.AddAllowlist == nil) {
		return nil, errors.New("telegram: Allowlisted + AddAllowlist callbacks are required for private access mode")
	}

	a := &Adapter{
		id:        id,
		mediaDir:  opts.MediaDir,
		allowlist: opts.Allowlisted,
		addAllow:  opts.AddAllowlist,
		token:     token,
	}
	a.statusVal.Store("disabled")
	a.lastError.Store("")

	botOpts := []tgbot.Option{
		tgbot.WithDefaultHandler(a.dispatchUpdate),
	}
	if opts.HTTPClient != nil {
		timeout := opts.PollTimeout
		if timeout == 0 {
			timeout = 60 * time.Second
		}
		botOpts = append(botOpts, tgbot.WithHTTPClient(timeout, opts.HTTPClient))
	}
	if opts.ServerURL != "" {
		botOpts = append(botOpts, tgbot.WithServerURL(opts.ServerURL))
		// Tests provide a fake server; skip the lib's startup getMe()
		// probe so test setup doesn't need to model that endpoint.
		botOpts = append(botOpts, tgbot.WithSkipGetMe())
	}

	b, err := tgbot.New(token, botOpts...)
	if err != nil {
		return nil, fmt.Errorf("telegram: bot init: %w", err)
	}
	a.bot = b
	return a, nil
}

// Channel implements bridge.Adapter.
func (a *Adapter) Channel() string { return "telegram" }

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

// SetMe injects the bot user identity. Tests use this when WithSkipGetMe
// is active; production code leaves it for the lib's first getMe() call.
func (a *Adapter) SetMe(u *models.User) {
	if u != nil {
		a.me.Store(u)
	}
}

// Me returns the bot user (or nil before Start populates it).
func (a *Adapter) Me() *models.User {
	if v := a.me.Load(); v != nil {
		return v.(*models.User)
	}
	return nil
}

// Bot returns the underlying *tgbot.Bot. Tests use this to drive
// dispatchUpdate synthetically; the orchestrator does not call it.
func (a *Adapter) Bot() *tgbot.Bot { return a.bot }

// Start implements bridge.Adapter. The library's long-poll loop runs in
// its own goroutine; Start returns once the loop has been kicked off.
//
// When Identity.Inbound == bridge.InboundDisabled the long-poll goroutine
// is skipped: no getMe round-trip, no /getUpdates. Outbound SendMessage /
// EditMessageText paths keep working because they only need the REST bot
// client + token, set up in New.
func (a *Adapter) Start(ctx context.Context, inbound chan<- bridge.Inbound) error {
	if !a.started.CompareAndSwap(false, true) {
		return errors.New("telegram: adapter already started")
	}
	a.inbound.Store(inbound)

	if bridge.IsInboundDisabled(a.id.Inbound) {
		a.statusVal.Store("running")
		logging.Info("telegram: inbound disabled — long-poll listener skipped; outbound active",
			"identity", a.id.ID)
		return nil
	}

	// Resolve bot identity if it hasn't been injected via SetMe. The lib
	// auto-fetches via GetMe unless WithSkipGetMe was set; we mirror that
	// for production callers who didn't override.
	if a.Me() == nil {
		me, err := a.bot.GetMe(ctx)
		if err == nil && me != nil {
			a.me.Store(me)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.statusVal.Store("running")
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Error("telegram: long-poll goroutine panicked", "identity", a.id.ID, "panic", r)
			}
		}()
		a.bot.Start(runCtx)
	}()
	return nil
}

// Stop cancels the long-poll loop. Idempotent.
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

// ResolveUserToDM implements bridge.Adapter. Telegram has no concept of
// "user-id needs DM resolution" — chat_id IS the destination — so this is
// always a passthrough.
func (a *Adapter) ResolveUserToDM(_ context.Context, peerID string) (string, error) {
	return peerID, nil
}

// SendInteractiveQuestion implements bridge.InteractiveQuestionSender —
// renders the question as a Telegram message with an inline keyboard
// (one button per choice). The button's `callback_data` carries the
// callback payload; when the reviewer clicks, Telegram delivers a
// `callback_query` Update which dispatchUpdate routes to
// handleCallbackQuery.
func (a *Adapter) SendInteractiveQuestion(ctx context.Context, peer bridge.PeerRef, prompt string, choices []bridge.QuestionChoice) (string, error) {
	chatID, err := ParsePeerID(peer.PeerID)
	if err != nil {
		return "", err
	}
	if len(choices) == 0 {
		return "", errors.New("telegram: SendInteractiveQuestion requires at least one choice")
	}

	rows := make([][]models.InlineKeyboardButton, 0, len(choices))
	for _, c := range choices {
		// Telegram limits callback_data to 64 bytes. We only carry the
		// canonical answer label; the bridge maps it back to a choice
		// via the same numeric-or-label parser used for text replies.
		data := c.Value
		if len(data) > 60 {
			data = data[:60]
		}
		btn := models.InlineKeyboardButton{
			Text:         c.Label,
			CallbackData: data,
		}
		rows = append(rows, []models.InlineKeyboardButton{btn})
	}
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: rows}

	text := appendCustomAnswerHint(prompt, choices[0].Custom)
	_, err = a.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: markup,
	})
	if err != nil {
		return "", fmt.Errorf("telegram: SendInteractiveQuestion: %w", err)
	}
	// Telegram chat IDs are stable across messages — no thread-style
	// mutation needed, so return "" to signal "no binding update".
	return "", nil
}

// SendInteractiveMultiSelect implements bridge.InteractiveMultiSelectSender
// for Telegram. Renders a stateful inline keyboard where each option is a
// toggle button (clicking flips a tick prefix), plus a final Submit row.
// The selection state lives in adapter.multiSelectStates() keyed by
// message_id; on Submit the adapter emits a comma-separated inbound and
// clears state. State entries TTL-evict after 30 minutes (D5).
//
// callback_data shapes:
//   - "ms:t:<i>" — toggle option at index i
//   - "ms:submit" — submit current selection
//
// Telegram callback_data has a 64-byte limit; "ms:t:99" is 7 bytes so
// up to 100 options fit comfortably; we cap at MultiSelectMaxOptions
// to match Slack's render limit.
const MultiSelectMaxOptions = 100

func (a *Adapter) SendInteractiveMultiSelect(ctx context.Context, peer bridge.PeerRef, prompt string, choices []bridge.QuestionChoice) (string, error) {
	chatID, err := ParsePeerID(peer.PeerID)
	if err != nil {
		return "", err
	}
	if len(choices) == 0 {
		return "", errors.New("telegram: SendInteractiveMultiSelect requires at least one choice")
	}
	if len(choices) > MultiSelectMaxOptions {
		return "", errors.New("telegram: too many options for multi-select inline keyboard")
	}

	rows := buildMultiSelectKeyboard(choices, nil)
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	text := appendCustomAnswerHint(prompt, choices[0].Custom)
	msg, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: markup,
	})
	if err != nil {
		return "", fmt.Errorf("telegram: SendInteractiveMultiSelect: %w", err)
	}
	if msg != nil {
		entry := &multiSelectEntry{
			ChatID:   msg.Chat.ID,
			Selected: map[string]bool{},
			Labels:   map[string]string{},
			Order:    make([]string, 0, len(choices)),
		}
		for _, c := range choices {
			entry.Order = append(entry.Order, c.Value)
			entry.Labels[c.Value] = c.Label
		}
		a.multiSelectStates().put(msg.ID, entry)
	}
	return "", nil
}

// buildMultiSelectKeyboard constructs the inline-keyboard rows for a
// multi-select prompt. Each choice gets its own row; selected items
// render with a "✓ " prefix on the label. A final row contains a
// "Submit" button.
func buildMultiSelectKeyboard(choices []bridge.QuestionChoice, selected map[string]bool) [][]models.InlineKeyboardButton {
	rows := make([][]models.InlineKeyboardButton, 0, len(choices)+1)
	for i, c := range choices {
		label := c.Label
		if selected != nil && selected[c.Value] {
			label = "✓ " + label
		}
		rows = append(rows, []models.InlineKeyboardButton{{
			Text:         label,
			CallbackData: fmt.Sprintf("ms:t:%d", i),
		}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{
		Text:         "Submit",
		CallbackData: "ms:submit",
	}})
	return rows
}

// handleCallbackQuery converts an inline-keyboard click into a
// bridge.Inbound carrying the button's callback_data so the bridge's
// QuestionRouter parses it as a question reply.
//
// Multi-select prefix routing (cb.Data prefix):
//   - "ms:t:<i>" — toggle option index <i>, edit reply markup, swallow
//   - "ms:submit" — emit comma-separated inbound, clear state, swallow
//   - anything else — emit inbound carrying cb.Data verbatim (single-select)
func (a *Adapter) handleCallbackQuery(ctx context.Context, cb *models.CallbackQuery) {
	if cb == nil || cb.Data == "" {
		return
	}
	msg := cb.Message.Message
	if msg == nil || msg.Chat.ID == 0 {
		return
	}
	// Telegram requires us to answer the callback so the spinner stops;
	// best-effort, ignore errors.
	_, _ = a.bot.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{
		CallbackQueryID: cb.ID,
	})

	// Multi-select state routing.
	if strings.HasPrefix(cb.Data, "ms:") {
		a.handleMultiSelectCallback(ctx, cb, msg)
		return
	}

	chatID := strconv.FormatInt(msg.Chat.ID, 10)
	a.pushInbound(ctx, bridge.Inbound{
		Peer: bridge.PeerRef{
			Channel:  "telegram",
			Identity: a.id.ID,
			PeerID:   chatID,
		},
		Text:       cb.Data,
		AuthorID:   strconv.FormatInt(cb.From.ID, 10),
		ReceivedAt: time.Now().UnixMilli(),
	})

	// Replace the inline keyboard + prefix the prompt with a confirmation
	// so the reviewer cannot re-click. Inbound was already pushed; update
	// failures are non-fatal. See bridge-question-answered-widget-update.
	a.updateAnsweredWidget(ctx, msg, []string{cb.Data})
}

// appendCustomAnswerHint adds the "type your own answer" discoverability
// suffix when the prompt allows custom answers. The hint is italicised
// and separated from the prompt by a blank line per
// bridge-question-custom-answer-hint.
func appendCustomAnswerHint(prompt string, custom bool) string {
	if !custom {
		return prompt
	}
	return prompt + "\n\n_Or reply with your own answer._"
}

// updateAnsweredWidget removes a question message's inline keyboard and
// rewrites its text with a "✓ Answered: <labels>" prefix. Both API calls
// are best-effort: failure logs warn-level but never propagates.
func (a *Adapter) updateAnsweredWidget(ctx context.Context, msg *models.Message, labels []string) {
	if msg == nil || msg.Chat.ID == 0 {
		return
	}
	clean := make([]string, 0, len(labels))
	for _, l := range labels {
		if l = strings.TrimSpace(l); l != "" {
			clean = append(clean, l)
		}
	}
	if len(clean) == 0 {
		return
	}
	_, err := a.bot.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{
		ChatID:    msg.Chat.ID,
		MessageID: msg.ID,
	})
	if err != nil {
		logging.Warn("telegram: clear keyboard for answered widget failed",
			"message_id", msg.ID, "err", err)
	}
	newText := fmt.Sprintf("✓ Answered: %s\n\n%s", strings.Join(clean, ", "), msg.Text)
	_, err = a.bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID:    msg.Chat.ID,
		MessageID: msg.ID,
		Text:      newText,
	})
	if err != nil {
		logging.Warn("telegram: prefix answered widget text failed",
			"message_id", msg.ID, "err", err)
	}
}

// handleMultiSelectCallback owns the toggle/submit lifecycle for a
// stateful multi-select keyboard. Called when cb.Data starts with "ms:".
//
// Toggle ("ms:t:<i>"): flips selected[options[i]], edits the message's
// reply_markup to redraw the keyboard with the new tick state. The
// adapter swallows the callback (no inbound emitted).
//
// Submit ("ms:submit"): collects all selected option values in their
// original choice order, emits a single comma-separated inbound, clears
// state, and edits the message to remove the keyboard (so the reviewer
// sees the question as resolved).
//
// On state-map miss (TTL evicted, restart between post and click) — the
// callback is swallowed silently; reviewer's click does nothing visible.
// Acceptable degradation per D5; preventing the click producing a
// nonsensical inbound is more important than perfect ergonomics.
func (a *Adapter) handleMultiSelectCallback(ctx context.Context, cb *models.CallbackQuery, msg *models.Message) {
	entry, ok := a.multiSelectStates().get(msg.ID)
	if !ok {
		logging.Info("telegram: multi-select state miss; ignoring callback",
			"message_id", msg.ID, "data", cb.Data)
		return
	}
	if cb.Data == "ms:submit" {
		selected := make([]string, 0, len(entry.Selected))
		for _, k := range entry.Order {
			if entry.Selected[k] {
				selected = append(selected, k)
			}
		}
		// Clear state regardless of whether anything was selected — the
		// submit click consumes the message-state binding.
		a.multiSelectStates().delete(msg.ID)
		if len(selected) == 0 {
			// Nothing selected — just clear the keyboard, no inbound, no
			// confirmation text (there's no answer to confirm).
			_, _ = a.bot.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{
				ChatID:    msg.Chat.ID,
				MessageID: msg.ID,
			})
			return
		}
		chatID := strconv.FormatInt(msg.Chat.ID, 10)
		a.pushInbound(ctx, bridge.Inbound{
			Peer: bridge.PeerRef{
				Channel:  "telegram",
				Identity: a.id.ID,
				PeerID:   chatID,
			},
			Text:       strings.Join(selected, ", "),
			AuthorID:   strconv.FormatInt(cb.From.ID, 10),
			ReceivedAt: time.Now().UnixMilli(),
		})
		// Map state values → display labels for the confirmation prefix.
		labels := make([]string, 0, len(selected))
		for _, v := range selected {
			if lbl, ok := entry.Labels[v]; ok && lbl != "" {
				labels = append(labels, lbl)
			} else {
				labels = append(labels, v)
			}
		}
		a.updateAnsweredWidget(ctx, msg, labels)
		return
	}
	// Toggle: "ms:t:<i>"
	if !strings.HasPrefix(cb.Data, "ms:t:") {
		return
	}
	idx, err := strconv.Atoi(strings.TrimPrefix(cb.Data, "ms:t:"))
	if err != nil || idx < 0 || idx >= len(entry.Order) {
		return
	}
	value := entry.Order[idx]
	entry.Selected[value] = !entry.Selected[value]
	a.multiSelectStates().put(msg.ID, entry) // refreshes TTL
	// Rebuild keyboard with the new tick state and edit-message.
	choices := make([]bridge.QuestionChoice, 0, len(entry.Order))
	for _, v := range entry.Order {
		choices = append(choices, bridge.QuestionChoice{Label: entry.Labels[v], Value: v})
	}
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: buildMultiSelectKeyboard(choices, entry.Selected)}
	_, err = a.bot.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{
		ChatID:      msg.Chat.ID,
		MessageID:   msg.ID,
		ReplyMarkup: markup,
	})
	if err != nil {
		logging.Warn("telegram: EditMessageReplyMarkup failed for multi-select toggle",
			"message_id", msg.ID, "err", err)
	}
}

// pushInbound is a small helper for handleCallbackQuery (and any other
// adapter-internal path) that needs to publish an Inbound onto the
// orchestrator channel without going through handleMessage's mention/
// allowlist gates.
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

// dispatchUpdate is the library's default handler — invoked once per
// incoming Telegram Update on the long-poll loop. Handles Update.Message
// (regular text/media inbound) and Update.CallbackQuery (inline-keyboard
// click); other update types are ignored.
func (a *Adapter) dispatchUpdate(ctx context.Context, _ *tgbot.Bot, upd *models.Update) {
	defer func() {
		if r := recover(); r != nil {
			logging.Error("telegram: dispatchUpdate panic", "identity", a.id.ID, "panic", r)
		}
	}()
	if upd == nil {
		return
	}
	switch {
	case upd.CallbackQuery != nil:
		a.handleCallbackQuery(ctx, upd.CallbackQuery)
	case upd.Message != nil:
		a.handleMessage(ctx, upd.Message)
	}
}

// handleMessage classifies one incoming message and either forwards it to
// the orchestrator, intercepts it as a pairing-code redemption, or drops
// it (own/bot post, groups-disabled, no @mention in group, etc).
//
// The classification order matches the TS bridge:
//
//  1. drop bot-originated and self posts
//  2. group/supergroup/channel routing: drop if groupsEnabled=false;
//     otherwise require @mention and strip
//  3. private-mode pairing: if peer is not allowlisted, intercept and
//     process /pair <code>
//  4. forward to orchestrator
func (a *Adapter) handleMessage(ctx context.Context, msg *models.Message) {
	if msg == nil || msg.Chat.ID == 0 {
		return
	}

	// Own / bot filter. Telegram marks bot users via User.IsBot; the
	// bridge's own outbound posts have From.ID == me.ID.
	if msg.From != nil {
		if msg.From.IsBot {
			return
		}
		if me := a.Me(); me != nil && msg.From.ID == me.ID {
			return
		}
	}

	chatID := strconv.FormatInt(msg.Chat.ID, 10)
	chatType := string(msg.Chat.Type)
	isGroup := chatType == "group" || chatType == "supergroup" || chatType == "channel"

	if isGroup && !a.id.GroupsEnabled {
		return
	}

	text := msg.Text
	if text == "" {
		text = msg.Caption
	}

	if isGroup {
		me := a.Me()
		if me == nil || me.Username == "" {
			return
		}
		if !MentionsBot(text, me.Username) {
			return
		}
		text = StripMention(text, me.Username)
	}

	// Pairing-code gate (private identities only). Public identities
	// skip this whole section.
	if a.id.Access == AccessPrivate {
		ok, err := a.allowlist(ctx, chatID)
		if err != nil {
			logging.Warn("telegram: allowlist check failed",
				"identity", a.id.ID, "chat", chatID, "err", err)
			return
		}
		if !ok {
			a.handlePairingAttempt(ctx, chatID, strings.TrimSpace(text))
			return
		}
	}

	// Build inbound (text + media) for the orchestrator.
	attachments := a.downloadMediaAttachments(ctx, chatID, msg)
	cleanText := strings.TrimSpace(text)
	if cleanText == "" && len(attachments) == 0 {
		return
	}

	in := bridge.Inbound{
		Peer: bridge.PeerRef{
			Channel:  "telegram",
			Identity: a.id.ID,
			PeerID:   chatID,
		},
		Text:        cleanText,
		Attachments: attachments,
		ReceivedAt:  time.Now().UnixMilli(),
	}
	if msg.From != nil {
		in.AuthorID = strconv.FormatInt(msg.From.ID, 10)
	}

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

// inboundChan returns the orchestrator's inbound channel for this
// adapter (set by Start), or nil if Start has not yet been called.
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

// handlePairingAttempt intercepts inbound from a non-allowlisted peer in
// AccessPrivate mode. If the text is `/pair <code>` and SHA256(code)
// equals the identity's PairingCodeHash, the peer is added to the
// allowlist and a "Pairing successful" reply is sent. Other inputs reply
// with usage instructions.
func (a *Adapter) handlePairingAttempt(ctx context.Context, chatID, text string) {
	code := extractPairingCode(text)
	if code == "" {
		a.replyText(ctx, chatID,
			"This Telegram bot is private. Send /pair <code> to request access.")
		return
	}
	if hashPairingCode(code) != a.id.PairingCodeHash {
		a.replyText(ctx, chatID, "Invalid pairing code. Try again with /pair <code>.")
		return
	}
	if err := a.addAllow(ctx, chatID); err != nil {
		logging.Warn("telegram: allowlist add failed",
			"identity", a.id.ID, "chat", chatID, "err", err)
		a.replyText(ctx, chatID, "Internal error registering your pairing. Try again.")
		return
	}
	a.replyText(ctx, chatID, "Pairing successful. This chat is now linked.")
}

// extractPairingCode parses "/pair <code>" or "/pair@bot <code>".
func extractPairingCode(text string) string {
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return ""
	}
	head := strings.ToLower(parts[0])
	if head != "/pair" && !strings.HasPrefix(head, "/pair@") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// hashPairingCode returns hex(sha256(code)).
func hashPairingCode(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// replyText is a small helper for sending a plain text response from an
// adapter-internal path (pairing replies, error hints). Failures are
// logged but not propagated.
func (a *Adapter) replyText(ctx context.Context, chatID, text string) {
	chatIDInt, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return
	}
	_, err = a.bot.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: chatIDInt,
		Text:   text,
	})
	if err != nil {
		logging.Warn("telegram: replyText failed",
			"identity", a.id.ID, "chat", chatID, "err", err)
	}
}

// Send implements bridge.Adapter. The platform's per-part shapes
// (sendMessage / sendPhoto / sendAudio / sendDocument) are chosen from
// MIME-type sniffing on attachments; text-only outbound chunks at
// MaxTextLength.
func (a *Adapter) Send(ctx context.Context, out bridge.Outbound) bridge.SendResult {
	chatID, err := ParsePeerID(out.Peer.PeerID)
	if err != nil {
		return bridge.SendResult{Err: err}
	}

	text := out.Text
	if out.Mention != "" {
		text = out.Mention + " " + text
	}

	// Attachments first so the chat-surface ordering matches the agent's
	// intent: file then trailing prose. Per the TS impl, attachments use
	// their caption (text up to MaxCaptionLength) for the first
	// attachment, and any leftover text streams as a final sendMessage
	// in chunks. For simplicity we send text first, then each attachment
	// with no caption — closer to the multi-platform fan-out semantics.
	for _, chunk := range chunkText(text, MaxTextLength) {
		if chunk == "" {
			continue
		}
		if _, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID: chatID,
			Text:   chunk,
		}); err != nil {
			a.recordFailure(err)
			return bridge.SendResult{Err: fmt.Errorf("telegram sendMessage: %w", err)}
		}
	}

	for _, att := range out.Attachments {
		if err := a.sendAttachment(ctx, chatID, att); err != nil {
			a.recordFailure(err)
			return bridge.SendResult{Err: err}
		}
	}

	return bridge.SendResult{Delivered: true}
}

// sendAttachment picks the right Telegram method based on the
// attachment's MIME prefix or filename extension.
func (a *Adapter) sendAttachment(ctx context.Context, chatID int64, att bridge.Attachment) error {
	if int64(len(att.Content)) > MaxFileSize {
		return fmt.Errorf("telegram: attachment %q exceeds %d byte limit", att.FileName, MaxFileSize)
	}
	filename := att.FileName
	if filename == "" {
		filename = "attachment"
	}
	input := &models.InputFileUpload{
		Filename: filename,
		Data:     bytes.NewReader(att.Content),
	}
	mime := strings.ToLower(att.MimeType)
	if mime == "" {
		mime = strings.ToLower(filepath.Ext(filename))
	}
	switch {
	case strings.HasPrefix(mime, "image/") || mime == ".jpg" || mime == ".jpeg" || mime == ".png" || mime == ".gif" || mime == ".webp":
		_, err := a.bot.SendPhoto(ctx, &tgbot.SendPhotoParams{
			ChatID: chatID,
			Photo:  input,
		})
		return err
	case strings.HasPrefix(mime, "audio/") || mime == ".mp3" || mime == ".ogg" || mime == ".wav":
		_, err := a.bot.SendAudio(ctx, &tgbot.SendAudioParams{
			ChatID: chatID,
			Audio:  input,
		})
		return err
	default:
		_, err := a.bot.SendDocument(ctx, &tgbot.SendDocumentParams{
			ChatID:   chatID,
			Document: input,
		})
		return err
	}
}

// chunkText splits text into chunks of at most max UTF-8 codepoints
// (NOT bytes). Telegram's MaxTextLength is counted in characters, not
// bytes — and slicing a UTF-8 string at a byte boundary that lands
// mid-codepoint produces invalid UTF-8 that the Telegram API rejects
// outright. We walk the string by rune and split at codepoint
// boundaries.
func chunkText(text string, max int) []string {
	if text == "" {
		return nil
	}
	if max <= 0 {
		return []string{text}
	}
	// Fast path — count runes; if the whole text fits, no split needed.
	if utf8RuneCount(text) <= max {
		return []string{text}
	}
	var out []string
	var buf strings.Builder
	count := 0
	for _, r := range text {
		buf.WriteRune(r)
		count++
		if count >= max {
			out = append(out, buf.String())
			buf.Reset()
			count = 0
		}
	}
	if buf.Len() > 0 {
		out = append(out, buf.String())
	}
	return out
}

// utf8RuneCount returns the number of runes (codepoints) in s without
// allocating. Avoids the standard utf8.RuneCountInString import only to
// keep the diff narrowly scoped here.
func utf8RuneCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// downloadMediaAttachments fetches every media file referenced by the
// incoming message and returns the resulting bridge.Attachment values.
// Failed downloads are logged and skipped — the inbound proceeds with
// whatever subset succeeded. Telegram's media model has a small set of
// distinct fields (photo / document / audio / voice); we handle the
// common cases the TS bridge handles.
func (a *Adapter) downloadMediaAttachments(ctx context.Context, chatID string, msg *models.Message) []bridge.Attachment {
	type candidate struct {
		fileID   string
		filename string
		mime     string
	}
	var candidates []candidate

	if len(msg.Photo) > 0 {
		largest := msg.Photo[len(msg.Photo)-1]
		filename := "photo-" + largest.FileUniqueID + ".jpg"
		if largest.FileUniqueID == "" {
			filename = "photo-" + largest.FileID + ".jpg"
		}
		candidates = append(candidates, candidate{fileID: largest.FileID, filename: filename, mime: "image/jpeg"})
	}
	if msg.Document != nil {
		filename := msg.Document.FileName
		if filename == "" {
			filename = "document-" + msg.Document.FileID
		}
		candidates = append(candidates, candidate{fileID: msg.Document.FileID, filename: filename, mime: msg.Document.MimeType})
	}
	if msg.Audio != nil {
		filename := msg.Audio.FileName
		if filename == "" {
			filename = "audio-" + msg.Audio.FileID
		}
		candidates = append(candidates, candidate{fileID: msg.Audio.FileID, filename: filename, mime: msg.Audio.MimeType})
	}
	if msg.Voice != nil {
		filename := "voice-" + msg.Voice.FileUniqueID + ".ogg"
		if msg.Voice.FileUniqueID == "" {
			filename = "voice-" + msg.Voice.FileID + ".ogg"
		}
		candidates = append(candidates, candidate{fileID: msg.Voice.FileID, filename: filename, mime: "audio/ogg"})
	}

	out := make([]bridge.Attachment, 0, len(candidates))
	for _, c := range candidates {
		att, err := a.downloadOne(ctx, chatID, c.fileID, c.filename, c.mime)
		if err != nil {
			logging.Warn("telegram: media download failed",
				"identity", a.id.ID, "chat", chatID, "file", c.fileID, "err", err)
			continue
		}
		out = append(out, att)
	}
	return out
}

// downloadOne resolves a Telegram file_id to a downloadable URL via
// getFile, fetches the body, and persists it under MediaDir.
func (a *Adapter) downloadOne(ctx context.Context, chatID, fileID, filename, mime string) (bridge.Attachment, error) {
	file, err := a.bot.GetFile(ctx, &tgbot.GetFileParams{FileID: fileID})
	if err != nil {
		return bridge.Attachment{}, err
	}
	if file == nil || file.FilePath == "" {
		return bridge.Attachment{}, fmt.Errorf("telegram: getFile returned empty path for %s", fileID)
	}
	url := a.fileDownloadURL(file.FilePath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return bridge.Attachment{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return bridge.Attachment{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return bridge.Attachment{}, fmt.Errorf("telegram: download %s: %d %s", fileID, resp.StatusCode, string(body))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return bridge.Attachment{}, err
	}

	att := bridge.Attachment{
		FileName: filename,
		MimeType: mime,
		Content:  data,
	}
	if a.mediaDir != "" {
		if err := os.MkdirAll(a.mediaDir, 0o700); err != nil {
			return att, fmt.Errorf("telegram: media dir: %w", err)
		}
		path := filepath.Join(a.mediaDir, filename)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return att, fmt.Errorf("telegram: write attachment: %w", err)
		}
		att.FilePath = path
	}
	return att, nil
}

// fileDownloadURL returns the canonical https URL for a Telegram file
// path. The format is documented at
// https://core.telegram.org/bots/api#getfile.
//
// Tests override fileBaseURL via SetFileBaseURL to point at a mock
// server; production callers use the Telegram default.
func (a *Adapter) fileDownloadURL(filePath string) string {
	base := a.fileBaseURL.Load()
	if base == nil {
		return "https://api.telegram.org/file/bot" + a.token + "/" + filePath
	}
	return base.(string) + "/file/bot" + a.token + "/" + filePath
}

// SetFileBaseURL overrides the base URL used by inbound file downloads.
// Tests point this at their httptest.Server; production code leaves it
// at the default (https://api.telegram.org).
func (a *Adapter) SetFileBaseURL(base string) {
	a.fileBaseURL.Store(strings.TrimRight(base, "/"))
}

func (a *Adapter) recordFailure(err error) {
	if err == nil {
		return
	}
	a.lastError.Store(redactToken(err.Error(), a.token))
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
