package bridge

import "context"

// RenderKind is the tagged-union discriminator for RenderHint. Each kind
// describes a distinct visual shape adapters render natively (Slack Block
// Kit, Telegram MarkdownV2, Mattermost Props["attachments"]).
//
// New kinds extend the enum; adapters that don't support a new kind fall
// back to the Outbound.Text path automatically (RichRenderer.Render
// returns SendResult{Err: ErrRenderUnsupported}, the dispatcher catches
// and retries via Adapter.Send).
type RenderKind int

const (
	// RenderKindToolCall renders a tool invocation header (pending state).
	// Caller populates ToolName, CallID, Status="pending", Params.
	RenderKindToolCall RenderKind = iota + 1

	// RenderKindToolResult renders a tool's completion (ok/error state).
	// Caller populates ToolName, CallID, Status ("ok"|"error"), Preview,
	// DurationMs. When the adapter has captured the call's posted message
	// reference, the result updates the same message in place (chat.update
	// on Slack; editMessageText on Telegram; PUT /posts/{id} on Mattermost).
	RenderKindToolResult

	// RenderKindTable renders a tabular result (e.g. /sessions). Caller
	// populates Headers + Rows. Adapter rendering varies — Mattermost uses
	// native markdown tables; Slack uses monospaced pipe-separated rows;
	// Telegram uses pre-formatted HTML or bullet list.
	RenderKindTable

	// RenderKindList renders an ordered or unordered list with optional
	// markers (e.g. /agent's active-agent indicator). Caller populates
	// Title and Items.
	RenderKindList

	// RenderKindQuestion is reserved for richer question-prompt rendering
	// (today's question router has its own InteractiveQuestionSender path;
	// this kind is the structured-output fallback for non-button platforms).
	RenderKindQuestion

	// RenderKindStatus renders a single-line status / informational reply
	// (e.g. command success / failure confirmations). Caller populates Body.
	RenderKindStatus
)

// ListItem is a single entry in a RenderKindList payload. Marker is an
// optional platform-rendered tag (e.g. "active", "(default)") shown
// next to the label — adapters MAY render this as a badge, suffix, or
// emoji depending on the platform's idiom.
type ListItem struct {
	Label    string
	Sublabel string
	Marker   string
}

// RenderHint is the tagged-union payload Outbound.Render carries when
// the caller wants platform-native rich rendering. Concrete fields are
// populated per Kind; unused fields are zero-valued and ignored. Callers
// SHALL NOT construct RenderHint literals directly — use the
// kind-specific constructors below (NewToolCallHint, NewTableHint, etc.)
// so future field additions don't silently zero on existing call sites.
//
// See spec: openspec/changes/chat-bridge-rich-rendering/specs/
// bridge-outbound-render-hint/spec.md
type RenderHint struct {
	Kind RenderKind

	// Tool fields (RenderKindToolCall, RenderKindToolResult).
	ToolName   string
	CallID     string            // short ID#suffix for pairing call ↔ result
	Status     string            // "pending" | "ok" | "error"
	Params     map[string]string // key→short-value pairs for the call header
	Preview    string            // truncated body for the result
	DurationMs int64

	// Table fields (RenderKindTable).
	Headers []string
	Rows    [][]string

	// List fields (RenderKindList).
	Title       string
	Items       []ListItem
	ActiveLabel string

	// Body is the generic-text field used by RenderKindStatus and as a
	// fallback for RenderKindQuestion.
	Body string
}

// NewToolCallHint constructs a RenderHint for a tool invocation header.
// CallID should be the short suffix used to pair calls with results
// (typically the trailing 6 chars of the provider's tool-call ID).
func NewToolCallHint(tool, callID string, params map[string]string) *RenderHint {
	return &RenderHint{
		Kind:     RenderKindToolCall,
		ToolName: tool,
		CallID:   callID,
		Status:   "pending",
		Params:   params,
	}
}

// NewToolResultHint constructs a RenderHint for a tool's completion.
// status MUST be "ok" or "error"; preview is the truncated body text;
// durationMs is the elapsed time in milliseconds (0 if unknown).
func NewToolResultHint(tool, callID, status, preview string, durationMs int64) *RenderHint {
	return &RenderHint{
		Kind:       RenderKindToolResult,
		ToolName:   tool,
		CallID:     callID,
		Status:     status,
		Preview:    preview,
		DurationMs: durationMs,
	}
}

// NewTableHint constructs a RenderHint for tabular data. Headers may be
// nil for unheaded tables; rows MUST have len == len(headers) per row
// when headers is non-nil (caller responsibility).
func NewTableHint(headers []string, rows [][]string) *RenderHint {
	return &RenderHint{
		Kind:    RenderKindTable,
		Headers: headers,
		Rows:    rows,
	}
}

// NewListHint constructs a RenderHint for an item list. title may be
// empty (no header rendered); activeLabel is the Marker value treated
// as the active selection (e.g. "active") so adapters can highlight it.
func NewListHint(title string, items []ListItem, activeLabel string) *RenderHint {
	return &RenderHint{
		Kind:        RenderKindList,
		Title:       title,
		Items:       items,
		ActiveLabel: activeLabel,
	}
}

// NewStatusHint constructs a RenderHint for a single-line status reply.
func NewStatusHint(body string) *RenderHint {
	return &RenderHint{
		Kind: RenderKindStatus,
		Body: body,
	}
}

// RichRenderer is an OPTIONAL contract per-platform adapters MAY satisfy
// to render Outbound payloads with platform-native structured UI when
// the caller supplies an Outbound.Render hint. Adapters that don't
// satisfy this interface fall through to the existing Adapter.Send path
// (Outbound.Text rendering) — backwards compatible with every existing
// call site.
//
// Adapters that DO satisfy it but fail to render a specific RenderKind
// MUST return SendResult{Err: ErrRenderUnsupported} so the bridge can
// retry via the text path; other errors are surfaced as delivery
// failures the same way Adapter.Send does today.
type RichRenderer interface {
	Render(ctx context.Context, peer PeerRef, hint *RenderHint) SendResult
}

// InteractiveMultiSelectSender is an OPTIONAL contract adapters MAY
// satisfy to render a Prompt.Multiple=true question with a single-submit
// multi-select widget (Slack multi_static_select + Apply, Telegram
// stateful inline keyboard + Submit, Mattermost attachment-action with
// multiple: true). Adapters that don't satisfy it fall back to the
// single-select InteractiveQuestionSender path; the bridge's
// parseQuestionAnswers already handles comma-separated typed replies
// for Multiple prompts so the fallback works correctly.
//
// Returns the resolved peer-id of the posted message — same semantics
// as InteractiveQuestionSender (e.g. Slack's "<channel>|<thread_ts>"
// when the post opens a new thread). The bridge mutates the binding
// row's peer_id to this value so the reviewer's Apply-click arrives
// keyed by the thread.
type InteractiveMultiSelectSender interface {
	SendInteractiveMultiSelect(ctx context.Context, peer PeerRef, prompt string, choices []QuestionChoice) (resolvedPeer string, err error)
}

// ErrRenderUnsupported is the sentinel error a RichRenderer returns
// when it cannot render the requested RenderKind. Callers use it to
// fall back to the Outbound.Text path.
var ErrRenderUnsupported = renderUnsupportedError{}

type renderUnsupportedError struct{}

func (renderUnsupportedError) Error() string { return "bridge: render kind not supported by adapter" }

// CommandReply is the return shape of a chat-command handler. Text is
// the plain-text fallback ALWAYS populated for adapters that don't
// render rich content; Hint is the optional structured render that
// RichRenderer-capable adapters consume. A nil *CommandReply (or one
// with empty Text and nil Hint) suppresses any reply.
//
// See spec: openspec/changes/chat-bridge-rich-rendering/specs/
// bridge-command-render-native/spec.md
type CommandReply struct {
	Text string
	Hint *RenderHint
}

// IsEmpty reports whether the reply carries any content to send. A nil
// reply OR a reply with no Text and no Hint is treated as "no reply"
// (matches the legacy behaviour of returning the empty string from
// command handlers).
func (r *CommandReply) IsEmpty() bool {
	if r == nil {
		return true
	}
	return r.Text == "" && r.Hint == nil
}
