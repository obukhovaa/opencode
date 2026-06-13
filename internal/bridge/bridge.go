// Package bridge implements the in-process chat-platform bridge for opencode.
//
// The bridge connects opencode to external chat platforms (Telegram, Slack,
// Mattermost), routing inbound messages to per-session agent runs and fanning
// agent output back to bound peers. It replaces the prior out-of-process
// openwork-router Node bridge that talked to opencode via HTTP/SSE loopback.
//
// High-level pieces:
//
//   - One Adapter implementation per chat platform under internal/bridge/<platform>/.
//   - A binding store (bridge_sessions, bridge_allowlist) implemented in
//     internal/bridge/store.go on top of sqlc-generated SQLite / MySQL queries.
//   - A per-sessionId dispatch goroutine that owns agent.Run invocation and
//     parts-event demultiplexing for one bound session at a time.
//   - HTTP routes mounted under /router/* on the existing opencode API mux.
//   - A router_send agent tool (registered conditionally) for in-process
//     outbound message delivery from inside an agent turn.
//
// The bridge starts iff config.Get().Router != nil and at least one channel
// has at least one enabled identity. Otherwise it stays silently disabled and
// the rest of opencode is unaffected.
package bridge

import (
	"context"
)

// Attachment is the bridge-local file-attachment value type. It mirrors
// internal/Attachment field-for-field — the orchestrator service
// translates between the two at the agent.Run boundary. This indirection
// exists because internal/config imports internal/bridge for the bridge.Config
// type used under .opencode.json's "router" key; any transitive import of
// internal/config from internal/bridge (e.g. via internal/message → db →
// config) would create a package import cycle.
type Attachment struct {
	FilePath string
	FileName string
	MimeType string
	Content  []byte
}

// PeerRef identifies a chat-platform peer that a session may be bound to.
//
// Channel is "telegram", "slack", or "mattermost". Identity is the
// configured identity ID within that channel (e.g. "default"). PeerID is the
// platform-specific destination identifier — see internal/bridge/adapter.go
// for per-platform formats. Mention, when set, is the platform-native ping
// handle prefixed to the first outbound message for this binding only.
type PeerRef struct {
	Channel  string `json:"channel"`
	Identity string `json:"identity"`
	PeerID   string `json:"peerId"`
	Mention  string `json:"mention,omitempty"`
}

// Inbound is the normalized representation of a chat-platform message
// received from any adapter. Adapters do per-platform parsing (mention
// extraction, file download, from_bot filtering) then push Inbound values
// onto the orchestrator's inbound channel.
type Inbound struct {
	Peer        PeerRef
	Text        string
	Attachments []Attachment
	// AuthorID is the platform-reported author of the message (used in
	// channel/thread contexts where a single binding row can receive
	// messages from multiple users; the attribution prefix uses AuthorID
	// rather than the binding's peer_id).
	AuthorID string
	// ReceivedAt is the unix-millis timestamp at which the adapter
	// received the message (used for /router/health lastInboundAt).
	ReceivedAt int64
	// Command, when set, is the parsed chat-command name without the
	// leading slash (e.g. "model", "session"). Empty for non-command
	// inbound. The orchestrator dispatches commands in-process via
	// direct service calls.
	Command string
	// CommandArgs is the remainder of the message after the command name,
	// if Command is set.
	CommandArgs string
}

// Outbound is the normalized representation of an outbound message the
// orchestrator hands to an adapter for delivery. One Outbound value is
// produced per (peer, message) pair during fan-out — the orchestrator
// resolves multi-peer sessions to N Outbound values before dispatching
// adapters in parallel.
type Outbound struct {
	Peer        PeerRef
	Text        string
	Attachments []Attachment
	// Mention, when non-empty, is the platform-native ping prefix that
	// the orchestrator wants prepended to Text for this delivery only.
	// The orchestrator passes this through from the binding row's
	// mention_handle column on the first outbound for the binding and
	// nils it for subsequent outbounds (see chat-bridge-router-initiated
	// spec). The adapter does NOT decide whether to prepend.
	Mention string
	// Render, when non-nil, requests platform-native structured rendering
	// (Slack Block Kit, Telegram MarkdownV2, Mattermost attachments) via
	// the optional RichRenderer interface. When the destination adapter
	// does NOT satisfy RichRenderer, the bridge falls through to the
	// existing Text path verbatim — callers are REQUIRED to populate
	// Text as the plain-text fallback for minimal adapters. See
	// bridge-outbound-render-hint capability.
	Render *RenderHint
}

// SendResult reports the outcome of a single Adapter.Send call. Adapters
// MUST populate Delivered honestly — true means the platform API accepted
// the payload; false plus an Err means the platform rejected it. ResolvedPeer
// reports any platform-side resolution (e.g. Slack channel→thread mutation:
// the adapter returns the ts; Slack U-id→DM: the adapter returns the D-id).
// The orchestrator persists the resolved form back to bridge_sessions when
// appropriate.
type SendResult struct {
	Delivered    bool
	Err          error
	ResolvedPeer string
}

// AdapterStatus is the per-identity health snapshot reported via /router/health.
type AdapterStatus struct {
	// Status is one of "running", "degraded", "disabled", "error".
	Status string
	// LastError carries the most recent error message visible to operators.
	// Tokens MUST be redacted.
	LastError string
	// LastInboundAt is the unix-millis timestamp of the last received
	// inbound message, or 0 if none yet.
	LastInboundAt int64
	// LastFailureAt is the unix-millis timestamp of the last outbound
	// delivery failure, or 0 if none. Distinct from LastInboundAt so
	// transient delivery failures surface independently of inbound flow.
	LastFailureAt int64
	// BoundSessions is the count of active bridge_sessions rows for
	// this identity.
	BoundSessions int
}

// QuestionChoice is one option in a platform-native interactive
// question UI. Label is what the reviewer sees on the button; Value is
// the opaque callback payload the adapter forwards back when the
// reviewer clicks. The bridge's question router encodes the question's
// canonical answer label in Value so reply parsing is the same code
// path as the numbered-text fallback.
type QuestionChoice struct {
	Label string
	Value string
	// Custom is a per-prompt flag replicated on every choice (the bridge's
	// question router sets it from `Prompt.IsCustomEnabled`). Adapters
	// read `choices[0].Custom` to decide whether to render the
	// "type your own answer" hint per bridge-question-custom-answer-hint.
	Custom bool
}

// AllowlistChecker reports whether the given peer / author / channel
// identifier is authorised for inbound on a given identity. Adapters
// receiving this callback consult it at the inbound entry point when
// their identity is configured for `access: "private"`. The Service
// implementation wires the callback to `Store.IsAllowlisted` for a
// specific (projectID, channel, identityID) triple.
//
// `identifier` is matched as an exact string against the
// `bridge_allowlist` table — callers MAY invoke it multiple times to
// check different shapes (e.g. peerID, then authorID, then channel-id
// prefix of a composite peer) per the per-adapter spec.
type AllowlistChecker func(ctx context.Context, identifier string) (bool, error)

// InteractiveQuestionSender is an OPTIONAL contract per-platform
// adapters MAY satisfy to render a question with platform-native UI
// (Slack interactive blocks, Telegram inline keyboards, etc).
//
// Adapters that don't satisfy this interface fall back to the
// numbered-text rendering. Adapters that DO satisfy it but fail at
// send time (missing scope, deprecated feature) MUST return an error
// — the bridge's question router catches it and retries with text.
//
// Returns the resolved peer-id of the posted message (e.g. Slack's
// "<channel>|<thread_ts>" when the message opens a new thread). The
// bridge service mutates the binding row's peer_id to this value so
// the reviewer's reply — which arrives keyed by the thread's
// composite peer-id — finds the original session instead of falling
// through resolveBinding's ErrNotFound branch and spawning a fresh
// coder session. Empty resolvedPeer means "no mutation needed"
// (e.g. Telegram chat-id stays stable).
type InteractiveQuestionSender interface {
	SendInteractiveQuestion(ctx context.Context, peer PeerRef, prompt string, choices []QuestionChoice) (resolvedPeer string, err error)
}

// Adapter is the contract every per-platform implementation satisfies.
//
// The orchestrator constructs one Adapter per configured identity. Adapters
// run independently — failure or disconnect of one identity MUST NOT affect
// others. Every Adapter goroutine MUST wrap work in defer recover() so a
// panic in one identity cannot kill the opencode API server.
type Adapter interface {
	// Channel returns the platform name ("telegram", "slack", "mattermost").
	Channel() string

	// Identity returns the identity ID this adapter instance owns.
	Identity() string

	// Start connects to the platform and begins delivering inbound
	// messages onto the supplied channel. Start MUST return quickly —
	// long-running work (long-poll loops, WebSocket lifecycle) belongs in
	// background goroutines launched by Start. Stop is signalled by
	// context cancellation.
	Start(ctx context.Context, inbound chan<- Inbound) error

	// Send delivers an outbound message to the supplied peer. Adapters
	// MUST enforce per-platform size limits BEFORE attempting upload and
	// return a non-nil SendResult.Err in that case. ResolvedPeer is
	// populated when the adapter performed platform-side resolution
	// (Slack channel→thread, Slack U-id→DM, Mattermost root post capture).
	Send(ctx context.Context, out Outbound) SendResult

	// ResolveUserToDM resolves a user-id form to a DM channel form
	// (Slack U-id → D-id; Mattermost user-id → DM channel). Adapters
	// for which this distinction does not apply (Telegram) MUST return
	// the input peerID unchanged with a nil error.
	ResolveUserToDM(ctx context.Context, peerID string) (string, error)

	// Status returns the current health snapshot for this identity.
	Status() AdapterStatus
}
