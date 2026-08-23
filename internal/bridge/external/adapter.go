// Package external implements the bridge.Adapter for the "external"
// channel — a relay channel with no chat-platform connection of its own.
// Every outbound message/question is POSTed to the orchestrator's
// /router/external/outbound endpoint, which fans it out over SSE to a
// non-chat consumer (e.g. the "c3" service). This lets router_send and
// the interactive-question flow work identically whether the peer is
// Slack, Mattermost, or this channel — the agent code doesn't know or
// care which.
//
// Inbound never arrives here directly: it reaches this pod exclusively
// via the orchestrator's forward to the existing /router/inbound
// endpoint, exactly like every other channel's mediated-inbound mode.
// Start is therefore a no-op and InboundActive always reports false.
package external

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
)

// defaultHTTPTimeout bounds a relay POST when the caller doesn't supply
// its own *http.Client. Mirrors the mattermost adapter's use of a
// request-scoped timeout.
const defaultHTTPTimeout = 10 * time.Second

// Identity configures one external relay consumer.
type Identity struct {
	ID string
	// RelayBaseURL is the orchestrator's base URL (e.g.
	// OPENCODE_BRIDGE_REGISTRAR_URL) that /router/external/outbound is
	// joined onto. Empty disables the adapter (see New's doc comment).
	RelayBaseURL string
	// RelayCredential is the Basic-auth password POSTed to
	// /router/external/outbound — the same shared secret as
	// OPENCODE_BRIDGE_REGISTRAR_PASSWORD. Empty disables the adapter.
	RelayCredential string
}

// Options bundles construction-time knobs.
type Options struct {
	// HTTPClient defaults to one with defaultHTTPTimeout when nil.
	HTTPClient *http.Client
}

// Adapter is the bridge.Adapter implementation for one configured
// external identity.
type Adapter struct {
	identityID      string
	relayBaseURL    string
	relayCredential string
	httpClient      *http.Client
	// jobID is the orchestrator job this pod is executing, resolved once at
	// construction from OPENCODE_BRIDGE_JOB_ID. Empty when opencode runs
	// outside a job.
	jobID string

	disabled       bool
	disabledReason string

	statusVal     atomic.Value // string
	lastError     atomic.Value // string
	lastFailureAt atomic.Int64
}

// New constructs an Adapter from the supplied identity. Unlike most
// adapters, an empty RelayBaseURL / RelayCredential does NOT return an
// error — it returns a non-nil, constructible-but-disabled Adapter that
// implements the full bridge.Adapter contract and fails every Send /
// SendInteractiveQuestion call with a clear error, logged once here at
// construction. This matters because cmd/serve.go's adapter factory
// switch needs LaunchAdapter/RegisterAdapter to succeed even when an
// identity is Enabled: true in config but its delivery config resolves
// to empty from environment at construction time (see
// ChannelsConfig.External / cmd/serve.go's "external" case) — a hard
// construction error there would prevent the identity from ever being
// registered, and future re-configuration (env var added later, pod
// restarted) would have no adapter to hot-reconfigure.
func New(id Identity, opts Options) (*Adapter, error) {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	a := &Adapter{
		identityID: id.ID,
		// Both trimmed: a value that arrived with surrounding whitespace
		// (env var, hand-edited JSON) would otherwise pass the
		// "configured" check below and then fail at the wire — a 401 that
		// reads like an orchestrator problem rather than a config typo.
		relayBaseURL:    strings.TrimSpace(id.RelayBaseURL),
		relayCredential: strings.TrimSpace(id.RelayCredential),
		httpClient:      client,
		// Resolved once here rather than per send: this is a process-level
		// value, and every other adapter input is settled at construction.
		jobID: os.Getenv("OPENCODE_BRIDGE_JOB_ID"),
	}

	var missing []string
	if a.relayBaseURL == "" {
		missing = append(missing, "relayBaseURL")
	}
	if a.relayCredential == "" {
		missing = append(missing, "relayCredential")
	}
	if len(missing) > 0 {
		a.disabled = true
		a.disabledReason = "missing " + strings.Join(missing, " and ")
		logging.Warn("bridge: external adapter disabled — missing relay configuration",
			"identity", id.ID, "missing", strings.Join(missing, ","))
	}

	a.statusVal.Store(a.currentStatusString())
	a.lastError.Store("")
	return a, nil
}

func (a *Adapter) currentStatusString() string {
	if a.disabled {
		return "disabled"
	}
	return "running"
}

// Channel implements bridge.Adapter.
func (a *Adapter) Channel() string { return "external" }

// Identity implements bridge.Adapter.
func (a *Adapter) Identity() string { return a.identityID }

// InboundActive implements bridge.AdapterInboundActiver. Always false:
// this adapter never opens a platform listener — inbound arrives only
// via the orchestrator's forward to /router/inbound, so acquiring the
// per-identity single-listener lock in RegisterAdapter would be pure
// overhead (and would incorrectly block a second runner from also
// registering this identity).
func (a *Adapter) InboundActive() bool { return false }

// Start implements bridge.Adapter. No-op: there is nothing to listen to.
func (a *Adapter) Start(ctx context.Context, inbound chan<- bridge.Inbound) error {
	return nil
}

// ResolveUserToDM implements bridge.Adapter. The external channel's
// peerId (<aid>:<flow_id>:<run_id>) has no user-id/DM-channel
// distinction — identity passthrough.
func (a *Adapter) ResolveUserToDM(ctx context.Context, peerID string) (string, error) {
	return peerID, nil
}

// Status implements bridge.Adapter. Status is "disabled" when
// misconfigured, else "running" — there is no persistent connection to
// report "degraded"/"error" against. LastError/LastFailureAt carry
// per-relay-call diagnostic honesty instead: a "running" adapter whose
// relay calls are all failing is NOT masked as healthy, it just isn't
// reported as a connection-level state this adapter doesn't have.
func (a *Adapter) Status() bridge.AdapterStatus {
	return bridge.AdapterStatus{
		Status:        a.currentStatusString(),
		LastError:     getString(&a.lastError),
		LastInboundAt: 0, // this adapter never receives directly
		LastFailureAt: a.lastFailureAt.Load(),
	}
}

// errDisabled is returned by Send/SendInteractiveQuestion when the
// adapter was constructed without relay configuration. No HTTP call is
// attempted.
func (a *Adapter) errDisabled() error {
	return errors.New("bridge: external adapter disabled (missing relay configuration): " + a.disabledReason)
}

// Send implements bridge.Adapter. Builds a relay frame (kind "message"
// or "ack" per out.IsAck) and POSTs it to the identity's relay endpoint.
// Never reports Delivered: true on anything but a 202 response — a
// withheld-attachment (metadata-only) send is still Delivered: true as
// long as the POST itself succeeds; that's by design, not a partial
// failure.
func (a *Adapter) Send(ctx context.Context, out bridge.Outbound) bridge.SendResult {
	if a.disabled {
		return bridge.SendResult{Err: a.errDisabled()}
	}

	sessionID, ok := bridge.SessionIDFromContext(ctx)
	if !ok {
		logging.Debug("bridge/external: Send called with no sessionId on context — relaying with empty sessionId",
			"identity", a.identityID)
	}

	kind := relayKindMessage
	if out.IsAck {
		kind = relayKindAck
	}

	var attachments []relayAttachmentMeta
	for _, att := range out.Attachments {
		attachments = append(attachments, relayAttachmentMeta{
			FileName: att.FileName,
			MimeType: att.MimeType,
			Size:     len(att.Content),
		})
	}

	frame := relayFrame{
		JobID:       a.jobID,
		Peer:        out.Peer,
		SessionID:   sessionID,
		Kind:        kind,
		Text:        out.Text,
		Attachments: attachments,
		TS:          nowUnixMilli(),
	}

	if err := a.postRelay(ctx, frame); err != nil {
		a.recordFailure(err)
		return bridge.SendResult{Err: err}
	}
	return bridge.SendResult{Delivered: true}
}

// SendInteractiveQuestion implements bridge.InteractiveQuestionSender.
// Builds a "question" relay frame. requestId is mandatory — unlike
// sessionId, a missing requestId means the consumer has no way to
// answer against this question, so it IS an error rather than a
// best-effort empty value.
func (a *Adapter) SendInteractiveQuestion(ctx context.Context, peer bridge.PeerRef, prompt string, choices []bridge.QuestionChoice) (string, error) {
	if a.disabled {
		return "", a.errDisabled()
	}

	sessionID, ok := bridge.SessionIDFromContext(ctx)
	if !ok {
		logging.Debug("bridge/external: SendInteractiveQuestion called with no sessionId on context — relaying with empty sessionId",
			"identity", a.identityID)
	}

	eq, ok := bridge.ExternalQuestionFromContext(ctx)
	if !ok || eq.RequestID == "" {
		return "", errors.New("bridge/external: SendInteractiveQuestion requires a requestId on context (bridge.ContextWithExternalQuestion) — refusing to relay an unanswerable question")
	}

	// A question with nothing to choose from is not answerable, so it is
	// rejected rather than relayed. The router's shouldUseInteractive gate
	// already requires at least one option, which is exactly why this is
	// worth asserting here: the mattermost adapter makes the same check,
	// and without it the `custom` flag below would be derived from no
	// choice at all.
	if len(choices) == 0 {
		return "", errors.New("bridge/external: SendInteractiveQuestion requires at least one choice")
	}

	relayChoices := make([]relayChoice, 0, len(choices))
	// Custom is a per-prompt flag replicated on every choice, read off the
	// first per bridge.QuestionChoice's contract. Defaulting to false keeps
	// the fail-safe direction: never advertise custom answers the prompt
	// did not enable.
	custom := false
	for i, c := range choices {
		relayChoices = append(relayChoices, relayChoice{
			Label:  c.Label,
			Value:  c.Value,
			Custom: c.Custom,
		})
		if i == 0 {
			custom = c.Custom
		}
	}

	frame := relayFrame{
		JobID:     a.jobID,
		Peer:      peer,
		SessionID: sessionID,
		Kind:      relayKindQuestion,
		Question: &relayQuestion{
			RequestID: eq.RequestID,
			Prompt:    prompt,
			Choices:   relayChoices,
			Multiple:  eq.Multiple,
			Custom:    custom,
		},
		TS: nowUnixMilli(),
	}

	if err := a.postRelay(ctx, frame); err != nil {
		a.recordFailure(err)
		return "", err
	}
	// Always empty: the external channel has no thread/root-post
	// concept for the binding's peer_id to mutate to.
	return "", nil
}

// recordFailure updates lastFailureAt / lastError without changing the
// adapter's disabled/running status — per-send delivery failures don't
// degrade the adapter as a whole (mirrors mattermost.Adapter.recordFailure).
func (a *Adapter) recordFailure(err error) {
	if err == nil {
		return
	}
	a.lastError.Store(redactCredential(err.Error(), a.relayCredential))
	a.lastFailureAt.Store(time.Now().UnixMilli())
}

func getString(v *atomic.Value) string {
	s, _ := v.Load().(string)
	return s
}

// redactCredential replaces the relay credential in s with "<redacted>".
// Mirrors mattermost.redactToken's approach — bridge error messages may
// include URLs/headers that get logged via /router/health, and must not
// surface secrets.
func redactCredential(s, credential string) string {
	if credential == "" {
		return s
	}
	return strings.ReplaceAll(s, credential, "<redacted>")
}
