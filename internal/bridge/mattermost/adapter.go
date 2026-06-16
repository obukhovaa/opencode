package mattermost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
)

// Constants matching the TS bridge.
const (
	MaxTextLength        = 16_383
	BaseReconnectDelay   = time.Second
	MaxReconnectDelay    = 30 * time.Second
	MaxReconnectAttempts = 20
	// Mattermost file-upload limit is configurable per-server; this is a
	// safe lower bound that matches the default install. Operators with
	// larger maxFileSize will see successful uploads above this — the
	// limit is enforced only client-side as a sanity check.
	DefaultMaxFileSize int64 = 100 * 1024 * 1024 // 100 MiB
)

// Adapter is the bridge.Adapter implementation for one configured
// Mattermost identity (server URL + access token + identity id).
type Adapter struct {
	identityID    string
	serverURL     string
	accessToken   string
	groupsEnabled bool
	access        string
	inboundMode   string
	mediaDir      string
	maxFileSize   int64
	allowlist     bridge.AllowlistChecker

	client        *Client
	dialer        *websocket.Dialer
	authPredicate func(WSEvent) (ok bool, fail bool) // optional override; tests use this

	mu       sync.Mutex
	botUser  User // filled by Start
	conn     *wsConn
	stopping atomic.Bool
	started  atomic.Bool

	// status reporting (atomic fields)
	statusVal     atomic.Value // string
	lastError     atomic.Value // string
	lastInboundAt atomic.Int64
	lastFailureAt atomic.Int64

	// toolCardsOnce lazy-initialises the tool-call → post-id cache used
	// by RichRenderer.Render to coalesce a tool's call+result into a
	// single PUT /posts/{id} update. See bridge-tool-render-native.
	toolCardsOnce  sync.Once
	toolCardsCache *toolCardCache
}

// toolCards returns the adapter's tool-card cache, lazy-initialised.
func (a *Adapter) toolCards() *toolCardCache {
	a.toolCardsOnce.Do(func() {
		a.toolCardsCache = newToolCardCache()
	})
	return a.toolCardsCache
}

// Identity configures one Mattermost adapter instance.
type Identity struct {
	ID            string
	ServerURL     string
	AccessToken   string
	GroupsEnabled bool
	// Access is "private" or empty/public. In private mode the adapter
	// gates inbound against the bridge_allowlist table. See spec:
	// mattermost-inbound-allowlist.
	Access string
	// Inbound controls whether Start opens the WebSocket listener.
	// bridge.InboundDisabled skips the WebSocket dial + read loop;
	// outbound posting / file uploads / interactive-attachment posting
	// remain active. Used by orchestrator-mediated-inbound deployments.
	// Empty or bridge.InboundEnabled keeps today's behaviour.
	Inbound string
}

// AccessPrivate is the Identity.Access value that enables per-peer
// inbound gating; empty or any other string is treated as public.
const AccessPrivate = "private"

// Options bundles construction-time knobs. HTTPClient and Dialer default to
// sensible production choices but tests typically override them.
type Options struct {
	HTTPClient *http.Client
	Dialer     *websocket.Dialer
	// MediaDir is the directory the adapter downloads inbound files to.
	// Required when inbound posts may carry file_ids; otherwise the
	// adapter silently skips file persistence.
	MediaDir string
	// MaxFileSize overrides the per-attachment upload limit. Mattermost
	// server admins can raise/lower the limit per the chat-bridge-adapters
	// spec ("Mattermost configurable"); operators configure this via
	// .opencode.json or via a future identity-CRUD field. Zero falls
	// back to DefaultMaxFileSize.
	MaxFileSize int64
	// Allowlisted is consulted at dispatchPosted when the identity is
	// configured for private access. Nil checker in private mode is
	// treated as public with a warn at startup.
	Allowlisted bridge.AllowlistChecker
}

// New constructs an Adapter from the supplied identity. ServerURL and
// AccessToken are required; an empty value returns an error.
func New(id Identity, opts Options) (*Adapter, error) {
	url := strings.TrimSpace(id.ServerURL)
	tok := strings.TrimSpace(id.AccessToken)
	if url == "" {
		return nil, errors.New("mattermost: server URL is required")
	}
	if tok == "" {
		return nil, errors.New("mattermost: access token is required")
	}
	maxFileSize := opts.MaxFileSize
	if maxFileSize <= 0 {
		maxFileSize = DefaultMaxFileSize
	}
	a := &Adapter{
		identityID:    id.ID,
		serverURL:     url,
		accessToken:   tok,
		groupsEnabled: id.GroupsEnabled,
		access:        id.Access,
		inboundMode:   id.Inbound,
		mediaDir:      opts.MediaDir,
		maxFileSize:   maxFileSize,
		allowlist:     opts.Allowlisted,
		client:        NewClient(url, tok, opts.HTTPClient),
		dialer:        opts.Dialer,
	}
	if a.dialer == nil {
		a.dialer = websocket.DefaultDialer
	}
	a.statusVal.Store("disabled")
	a.lastError.Store("")
	if id.Access == AccessPrivate && opts.Allowlisted == nil {
		logging.Warn("mattermost: private mode set but no allowlist checker — treating as public",
			"identity", id.ID)
	}
	return a, nil
}

// Channel implements bridge.Adapter.
func (a *Adapter) Channel() string { return "mattermost" }

// Identity implements bridge.Adapter.
func (a *Adapter) Identity() string { return a.identityID }

// InboundActive implements bridge.AdapterInboundActiver. See the
// Slack adapter's implementation for the rationale — mediated-mode
// adapters skip the per-identity lock to enable multi-runner.
func (a *Adapter) InboundActive() bool {
	return !bridge.IsInboundDisabled(a.inboundMode)
}

// Client exposes the underlying REST client for tests and the
// router_send agent tool wiring.
func (a *Adapter) Client() *Client { return a.client }

// BotUser returns the resolved bot user. Zero-value before Start completes.
func (a *Adapter) BotUser() User {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.botUser
}

// Status implements bridge.Adapter.
func (a *Adapter) Status() bridge.AdapterStatus {
	return bridge.AdapterStatus{
		Status:        getString(&a.statusVal),
		LastError:     getString(&a.lastError),
		LastInboundAt: a.lastInboundAt.Load(),
		LastFailureAt: a.lastFailureAt.Load(),
	}
}

// Start implements bridge.Adapter. It blocks until the initial bot-user
// fetch and WebSocket auth handshake complete (or fail), then dispatches
// inbound events asynchronously. The supplied context is the parent for
// all background work — cancel it to shut the adapter down.
//
// When Identity.Inbound == bridge.InboundDisabled the WebSocket listener
// is skipped entirely: no getMe round-trip, no WS dial. Outbound posting
// keeps working because Send / SendInteractiveAttachment only depend on
// the REST client + access token, set up in New.
func (a *Adapter) Start(ctx context.Context, inbound chan<- bridge.Inbound) error {
	if !a.started.CompareAndSwap(false, true) {
		return errors.New("mattermost: adapter already started")
	}

	if bridge.IsInboundDisabled(a.inboundMode) {
		a.statusVal.Store("running")
		logging.Info("mattermost: inbound disabled — WebSocket listener skipped; outbound active",
			"identity", a.identityID)
		return nil
	}

	botUser, err := a.client.GetMe(ctx)
	if err != nil {
		a.fail("getMe: " + err.Error())
		return fmt.Errorf("mattermost: GetMe: %w", err)
	}

	a.mu.Lock()
	a.botUser = botUser
	a.mu.Unlock()

	conn, err := a.dialAndAuth(ctx)
	if err != nil {
		a.fail("websocket connect: " + err.Error())
		return err
	}
	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()
	a.statusVal.Store("running")

	go a.run(ctx, inbound)
	return nil
}

// dialAndAuth establishes a fresh WebSocket connection with auth challenge.
func (a *Adapter) dialAndAuth(ctx context.Context) (*wsConn, error) {
	return Connect(ctx, a.client.WebSocketURL(), a.client.AuthToken(), WithDialer(a.dialer))
}

// run is the inbound event loop. It owns the WebSocket reader and the
// reconnect logic. Adapter goroutines MUST recover panics — a panic here
// would otherwise take down the bridge orchestrator's supervisor.
func (a *Adapter) run(ctx context.Context, inbound chan<- bridge.Inbound) {
	defer func() {
		if r := recover(); r != nil {
			logging.Error("mattermost adapter goroutine panicked", "identity", a.identityID, "panic", r)
		}
		a.cleanup()
	}()

	for {
		err := a.readLoop(ctx, inbound)
		if a.stopping.Load() || ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrAuthFailed) {
			a.fail("authentication failed: " + err.Error())
			return
		}
		// Schedule reconnect with exponential backoff.
		if !a.reconnect(ctx) {
			return
		}
	}
}

// readLoop reads events from the current WebSocket connection until the
// connection closes (whether by remote, by us, or by context cancellation).
// Returns the error that caused the loop to exit.
func (a *Adapter) readLoop(ctx context.Context, inbound chan<- bridge.Inbound) error {
	a.mu.Lock()
	conn := a.conn
	a.mu.Unlock()
	if conn == nil {
		return errors.New("mattermost: no connection")
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		ev, err := conn.ReadEvent()
		if err != nil {
			// Distinguish auth failure from generic transport errors.
			if errors.Is(err, ErrAuthFailed) {
				return err
			}
			return err
		}
		switch ev.Event {
		case "posted":
			a.dispatchPosted(ctx, ev, inbound)
		case "post_edited", "post_deleted":
			// Spec requires subscription to these events but the
			// bridge's chat-surface model is append-only: an edit
			// or deletion on the platform side does not retroactively
			// rewrite the agent's prompt history. We acknowledge the
			// event (it reaches the dispatcher; chat-bridge-adapters
			// "subscribe to posted, post_edited, post_deleted") and
			// drop it — no inbound is generated, no agent.Run fires.
			logging.Debug("mattermost: ignored event",
				"identity", a.identityID, "event", ev.Event)
		}
	}
}

// isInboundAllowed reports whether an inbound from (peerID, authorID,
// channelID) is permitted. Public mode (default) always allows. Private
// mode with no checker fails-open (logged warn at construction). Private
// mode with a checker tries the peer-id, then the author-id, then the
// channel-id in turn.
func (a *Adapter) isInboundAllowed(ctx context.Context, peerID, authorID, channelID string) bool {
	if a.access != AccessPrivate || a.allowlist == nil {
		return true
	}
	for _, c := range []string{peerID, authorID, channelID} {
		if c == "" {
			continue
		}
		ok, err := a.allowlist(ctx, c)
		if err != nil {
			logging.Warn("mattermost: allowlist lookup failed — failing closed",
				"identity", a.identityID, "identifier", c, "err", err)
			return false
		}
		if ok {
			return true
		}
	}
	return false
}

// dispatchPosted converts a "posted" WS event to a bridge.Inbound and
// pushes it onto the supplied channel. Per the spec, bot/webhook posts are
// filtered; channels require groupsEnabled + @mention; unknown channel
// types are ignored.
func (a *Adapter) dispatchPosted(ctx context.Context, ev WSEvent, inbound chan<- bridge.Inbound) {
	post, err := ParsePostFromEvent(ev)
	if err != nil {
		logging.Debug("mattermost dispatch: parse post", "err", err)
		return
	}

	bot := a.BotUser()
	// Skip our own posts.
	if bot.ID != "" && post.UserID == bot.ID {
		return
	}
	if isFromBotOrWebhook(post) {
		return
	}

	channelType := EventChannelType(ev)
	// Per the chat-bridge-adapters spec: D (direct DM) always responds;
	// G (group DM), O (public channel) and P (private channel) all gate
	// on the per-identity groupsEnabled flag PLUS a bot @mention. The
	// earlier implementation lumped G in with D and bypassed the gate —
	// that violated the spec's "Group DM honors per-identity
	// groupsEnabled" scenario.
	isDirectDM := channelType == "D"
	isGroupDM := channelType == "G"
	isChannel := channelType == "O" || channelType == "P"
	if !isDirectDM && !isGroupDM && !isChannel {
		// Unknown channel type — ignore quietly. The TS adapter
		// emits a debug log here; matching.
		return
	}

	requiresGroupsGate := isGroupDM || isChannel
	if requiresGroupsGate {
		if !a.groupsEnabled {
			return
		}
		if bot.Username == "" {
			return
		}
		if !strings.Contains(post.Message, "@"+bot.Username) {
			return
		}
	}

	text := post.Message
	if requiresGroupsGate {
		text = StripMention(text, bot.Username)
	}
	text = strings.TrimSpace(text)

	rootPost := post.RootID
	if rootPost == "" {
		rootPost = post.ID
	}
	peer := bridge.PeerRef{
		Channel:  "mattermost",
		Identity: a.identityID,
		PeerID:   FormatPeerID(Peer{ChannelID: post.ChannelID, RootPostID: rootPost}),
	}
	if !a.isInboundAllowed(ctx, peer.PeerID, post.UserID, post.ChannelID) {
		logging.Info("mattermost: inbound dropped — not allowlisted",
			"identity", a.identityID, "peer_id", peer.PeerID, "author_id", post.UserID, "channel", post.ChannelID)
		return
	}

	in := bridge.Inbound{
		Peer:       peer,
		Text:       text,
		AuthorID:   post.UserID,
		ReceivedAt: time.Now().UnixMilli(),
	}

	// Persist any attached files into the bridge media store. Failures
	// are non-fatal — log and continue with whatever attachments did
	// land. The agent will see the local paths via bridge.Attachment.
	for _, fid := range post.FileIDs {
		att, err := a.downloadAttachment(ctx, fid)
		if err != nil {
			logging.Warn("mattermost: download attachment failed",
				"identity", a.identityID, "file_id", fid, "err", err)
			continue
		}
		in.Attachments = append(in.Attachments, att)
	}

	if in.Text == "" && len(in.Attachments) == 0 {
		// Nothing meaningful to forward (e.g. an edited post that
		// stripped to empty after mention removal).
		return
	}

	a.lastInboundAt.Store(time.Now().UnixMilli())
	select {
	case inbound <- in:
	case <-ctx.Done():
	}
}

// downloadAttachment fetches the file body and persists it under MediaDir.
// Returns a bridge.Attachment carrying the on-disk path. If MediaDir is
// empty the file is read into memory only.
func (a *Adapter) downloadAttachment(ctx context.Context, fileID string) (bridge.Attachment, error) {
	body, err := a.client.DownloadFile(ctx, fileID)
	if err != nil {
		return bridge.Attachment{}, err
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		return bridge.Attachment{}, fmt.Errorf("mattermost: read attachment: %w", err)
	}

	att := bridge.Attachment{
		FileName: fileID,
		Content:  data,
	}
	if a.mediaDir != "" {
		if err := os.MkdirAll(a.mediaDir, 0o700); err != nil {
			return att, fmt.Errorf("mattermost: media dir: %w", err)
		}
		path := filepath.Join(a.mediaDir, fileID)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return att, fmt.Errorf("mattermost: write attachment: %w", err)
		}
		att.FilePath = path
	}
	return att, nil
}

// Send implements bridge.Adapter. Each call posts one message to one peer
// (single-peer; multi-peer fan-out is the orchestrator's responsibility).
// File attachments are uploaded first, then a single create_post attaches
// them via file_ids.
func (a *Adapter) Send(ctx context.Context, out bridge.Outbound) bridge.SendResult {
	peer := ParsePeerID(out.Peer.PeerID)
	if peer.ChannelID == "" {
		return bridge.SendResult{Err: fmt.Errorf("mattermost: invalid peerId %q", out.Peer.PeerID)}
	}

	text := bridge.PrependMentionIfMissing(out.Mention, out.Text)
	// Mattermost counts MaxTextLength in characters, not bytes. Slicing
	// at a byte boundary that lands mid-codepoint produces invalid UTF-8
	// that the server can reject and renders as the replacement
	// character. Cap by rune so the cut always lands on a codepoint
	// boundary.
	text = truncateRunes(text, MaxTextLength)

	var fileIDs []string
	if len(out.Attachments) > 0 {
		ups := make([]FileUpload, 0, len(out.Attachments))
		for _, att := range out.Attachments {
			if int64(len(att.Content)) > a.maxFileSize {
				a.recordFailure(fmt.Errorf("mattermost: attachment %q exceeds %d byte limit", att.FileName, a.maxFileSize))
				return bridge.SendResult{Err: fmt.Errorf("mattermost: oversize attachment %q", att.FileName)}
			}
			ups = append(ups, FileUpload{
				Filename: nonEmpty(att.FileName, "attachment"),
				Data:     att.Content,
			})
		}
		infos, err := a.client.UploadFiles(ctx, peer.ChannelID, ups)
		if err != nil {
			a.recordFailure(err)
			return bridge.SendResult{Err: fmt.Errorf("mattermost upload: %w", err)}
		}
		for _, fi := range infos {
			fileIDs = append(fileIDs, fi.ID)
		}
	}

	post, err := a.client.CreatePost(ctx, CreatePostInput{
		ChannelID: peer.ChannelID,
		Message:   text,
		RootID:    peer.RootPostID,
		FileIDs:   fileIDs,
	})
	if err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: fmt.Errorf("mattermost createPost: %w", err)}
	}

	// If we posted to a channel without a thread, surface the created
	// post's ID so the orchestrator can mutate the binding to
	// channelID|postID per chat-bridge-router-initiated semantics.
	resolved := ""
	if peer.RootPostID == "" {
		resolved = FormatPeerID(Peer{ChannelID: post.ChannelID, RootPostID: post.ID})
	}
	return bridge.SendResult{Delivered: true, ResolvedPeer: resolved}
}

// ResolveUserToDM implements bridge.Adapter. A 26-char user ID is resolved
// to its DM channel via POST /api/v4/channels/direct. Anything else is
// returned unchanged.
func (a *Adapter) ResolveUserToDM(ctx context.Context, peerID string) (string, error) {
	// LooksLikeUserID matches any 26-char base32 ID — but channel IDs
	// have the same shape in Mattermost, so we MUST probe before
	// attempting channels/direct (which 400s for non-user IDs). When
	// the probe fails or the ID doesn't look like a user at all, the
	// peer is assumed to be already in its final form (channel, DM
	// channel, or thread).
	if !LooksLikeUserID(peerID) {
		return peerID, nil
	}
	if !a.client.IsUser(ctx, peerID) {
		return peerID, nil
	}
	bot := a.BotUser()
	if bot.ID == "" {
		return "", errors.New("mattermost: ResolveUserToDM called before bot user resolved")
	}
	return a.client.CreateDirectChannel(ctx, bot.ID, peerID)
}

// reconnect schedules a backoff-delayed reconnect attempt. Returns true if
// a new connection was established, false if the adapter must exit (e.g.
// auth failed, max attempts reached, context cancelled).
func (a *Adapter) reconnect(ctx context.Context) bool {
	for attempt := 1; attempt <= MaxReconnectAttempts; attempt++ {
		if a.stopping.Load() || ctx.Err() != nil {
			return false
		}
		delay := backoffDelay(attempt)
		a.statusVal.Store("degraded")
		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}

		conn, err := a.dialAndAuth(ctx)
		if err != nil {
			a.lastError.Store(fmt.Sprintf("reconnect attempt %d: %v", attempt, err))
			if errors.Is(err, ErrAuthFailed) {
				return false
			}
			continue
		}
		a.mu.Lock()
		a.conn = conn
		a.mu.Unlock()
		a.statusVal.Store("running")
		a.lastError.Store("")
		return true
	}
	a.fail("max reconnect attempts reached")
	return false
}

// backoffDelay returns 1s, 2s, 4s, ..., capped at 30s, with a small jitter
// to avoid thundering herds on simultaneous reconnect.
func backoffDelay(attempt int) time.Duration {
	exp := time.Duration(1) << (attempt - 1) * time.Second
	if exp > MaxReconnectDelay {
		exp = MaxReconnectDelay
	}
	jitter := time.Duration(rand.Intn(500)) * time.Millisecond
	return exp + jitter
}

// Stop cancels in-flight work and closes the WebSocket. The adapter is no
// longer usable after Stop returns.
func (a *Adapter) Stop() error {
	if !a.stopping.CompareAndSwap(false, true) {
		return nil
	}
	a.cleanup()
	return nil
}

func (a *Adapter) cleanup() {
	a.mu.Lock()
	conn := a.conn
	a.conn = nil
	a.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	a.statusVal.Store("disabled")
}

// recordFailure updates lastFailureAt / lastError without changing the
// adapter status (per-peer delivery failures don't degrade the adapter as
// a whole — see /router/health semantics in the bridge-http-api spec).
func (a *Adapter) recordFailure(err error) {
	if err == nil {
		return
	}
	a.lastError.Store(redactToken(err.Error(), a.accessToken))
	a.lastFailureAt.Store(time.Now().UnixMilli())
}

// fail flips the adapter into the "error" status with the supplied reason.
func (a *Adapter) fail(reason string) {
	a.statusVal.Store("error")
	a.lastError.Store(redactToken(reason, a.accessToken))
}

func isFromBotOrWebhook(p *Post) bool {
	if p == nil || p.Props == nil {
		return false
	}
	if v, ok := p.Props["from_bot"]; ok {
		if s, ok := v.(string); ok && s == "true" {
			return true
		}
		if b, ok := v.(bool); ok && b {
			return true
		}
	}
	if v, ok := p.Props["from_webhook"]; ok {
		if s, ok := v.(string); ok && s == "true" {
			return true
		}
		if b, ok := v.(bool); ok && b {
			return true
		}
	}
	return false
}

func getString(v *atomic.Value) string {
	s, _ := v.Load().(string)
	return s
}

func nonEmpty(a, fallback string) string {
	if a == "" {
		return fallback
	}
	return a
}

// redactToken replaces the access token in s with "<redacted>". Bridge
// error messages may include URLs that get logged via /router/health —
// don't surface tokens.
func redactToken(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<redacted>")
}

// truncateRunes returns s capped to maxRunes codepoints. Cutting a
// UTF-8 string at a byte index can land mid-codepoint and produce
// invalid UTF-8; counting runes guarantees the cut is at a codepoint
// boundary. No ellipsis is appended — the Mattermost post is rendered
// as-is.
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
