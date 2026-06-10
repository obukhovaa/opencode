package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// RouterSendToolName is the canonical agent-facing tool name.
const RouterSendToolName = "router_send"

// BridgeSender is the contract internal/llm/tools requires from the
// chat-bridge orchestrator so this package does NOT have to import
// internal/bridge/service (which would create a cycle —
// internal/bridge/service imports internal/llm/agent which imports
// internal/llm/tools).
//
// Production wiring: internal/bridge/service.Service satisfies the
// interface via its existing Send method.
type BridgeSender interface {
	// Send delivers a single-peer outbound message in-process. The
	// implementation MUST NOT issue an HTTP loopback to /router/send —
	// the agent tool flow lives entirely inside the same process.
	//
	// Returns a bridge.SendResult shaping the per-peer outcome. The
	// SendResult.ResolvedPeer field carries any platform-side rewrite
	// (Slack U-id → DM channel etc) so the tool can echo it back to
	// the agent's response.
	Send(ctx context.Context, peer bridge.PeerRef, text string, mention string, attachments []bridge.Attachment) (bridge.SendResult, error)

	// HasBoundPeers reports whether a session has any active
	// bridge_sessions rows. Used by the dynamic description to list
	// currently-bound peers for the agent. Implementations MAY
	// reasonably no-op (returning nil, nil) when describing all peers
	// is expensive; the tool degrades gracefully.
	BoundPeersSnapshot(ctx context.Context) []bridge.PeerRef
}

// RouterSendDeps bundles the tool's runtime dependencies. The tool
// constructor takes this rather than a long arg list so future
// additions don't churn every callsite.
type RouterSendDeps struct {
	// Sender is the in-process bridge handle. Required.
	Sender BridgeSender

	// Cfg is the chat-bridge configuration snapshot the tool reads to
	// build its dynamic description. Captured at registration time per
	// the chat-bridge-agent-tool spec ("Tool description enumerates
	// configured channels — built at registration time from a snapshot
	// of cfg.Router").
	Cfg *bridge.Config

	// MediaRoot is the absolute path to <Data.Directory>/bridge/media/.
	// File attachments in the tool input MUST be under this root; the
	// tool rejects any path outside it.
	MediaRoot string
}

// routerSendTool is the BaseTool implementation.
type routerSendTool struct {
	deps        RouterSendDeps
	description string
}

// routerSendParams is the JSON input schema the agent fills in.
type routerSendParams struct {
	Channel  string   `json:"channel"`
	Identity string   `json:"identity"`
	PeerID   string   `json:"peerId"`
	Text     string   `json:"text"`
	Mention  string   `json:"mention,omitempty"`
	Files    []string `json:"files,omitempty"`
}

// routerSendResponse is the single-peer response shape per the
// chat-bridge-agent-tool spec: `{delivered: bool, error?: string,
// resolvedPeerId?: string}`. Multi-peer fan-out within a single call
// is NOT supported in v1.
type routerSendResponse struct {
	Delivered      bool   `json:"delivered"`
	Error          string `json:"error,omitempty"`
	ResolvedPeerID string `json:"resolvedPeerId,omitempty"`
}

// NewRouterSendTool constructs the tool. Callers MUST check
// ShouldRegisterRouterSend before invoking this — the tool's
// description and runtime behaviour assume a configured router.
func NewRouterSendTool(deps RouterSendDeps) BaseTool {
	return &routerSendTool{
		deps:        deps,
		description: buildRouterSendDescription(deps),
	}
}

// ShouldRegisterRouterSend reports whether the tool should appear in
// an agent's tool list per the chat-bridge-agent-tool spec:
//
//  1. The agent must be in mode "agent" (NOT "subagent" — caller
//     enforces this).
//  2. cfg.Router != nil AND at least one channel has at least one
//     enabled identity.
//
// Returns false when either condition fails. Caller MUST also check
// the agent's mode independently — this function only validates the
// router-config side.
func ShouldRegisterRouterSend(cfg *bridge.Config) bool {
	return cfg != nil && cfg.AnyChannelEnabled()
}

func (t *routerSendTool) Info() ToolInfo {
	return ToolInfo{
		Name:        RouterSendToolName,
		Description: t.description,
		Parameters: map[string]any{
			"channel": map[string]any{
				"type":        "string",
				"description": `Chat platform: "telegram" | "slack" | "mattermost".`,
				"enum":        []string{"telegram", "slack", "mattermost"},
			},
			"identity": map[string]any{
				"type":        "string",
				"description": "The configured identity ID on that channel. See the tool description for the available identities.",
			},
			"peerId": map[string]any{
				"type":        "string",
				"description": "Platform-specific destination. Telegram: numeric chat_id. Slack: D<channel>, C<channel>[|<ts>], or U<user>. Mattermost: <channelID>[|<rootPostID>] or 26-char user ID.",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "The message body. Required unless files is non-empty.",
			},
			"mention": map[string]any{
				"type":        "string",
				"description": "Optional ping handle prefixed to text. Platform-native format (Slack <@U...>, Mattermost @username, Telegram unused).",
			},
			"files": map[string]any{
				"type":        "array",
				"description": "Local paths to attach. Each path MUST be inside the bridge media store. Subject to per-platform size limits.",
				"items":       map[string]any{"type": "string"},
			},
		},
		Required: []string{"channel", "identity", "peerId"},
	}
}

func (t *routerSendTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var p routerSendParams
	if err := json.Unmarshal([]byte(call.Input), &p); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("invalid input: %s", err.Error())), nil
	}

	if errResp, ok := t.validateInput(p); !ok {
		return errResp, nil
	}

	attachments, errResp := t.loadAttachments(p.Files)
	if errResp != nil {
		return *errResp, nil
	}

	peer := bridge.PeerRef{
		Channel:  p.Channel,
		Identity: p.Identity,
		PeerID:   p.PeerID,
	}
	result, err := t.deps.Sender.Send(ctx, peer, p.Text, p.Mention, attachments)
	if err != nil {
		// Top-level error means the bridge couldn't even attempt the
		// delivery (no adapter, resolution failure). Surface it without
		// the delivered=true flag.
		return jsonResponse(routerSendResponse{Error: err.Error()}), nil
	}
	resp := routerSendResponse{Delivered: result.Delivered, ResolvedPeerID: result.ResolvedPeer}
	if !result.Delivered && result.Err != nil {
		resp.Error = redactToken(result.Err.Error())
	}
	return jsonResponse(resp), nil
}

func (t *routerSendTool) AllowParallelism(_ ToolCall, _ []ToolCall) bool {
	// Per-peer sends are independent; allow the agent to fan multiple
	// router_send calls in parallel for performance. Order across
	// different peers isn't meaningful.
	return true
}

func (t *routerSendTool) IsBaseline() bool { return true }

// validateInput performs the chat-bridge-agent-tool spec's required
// input validation: unknown channel, unknown identity, malformed
// peerId, oversize file, empty text — each returns a tool error
// response (not a panic).
func (t *routerSendTool) validateInput(p routerSendParams) (ToolResponse, bool) {
	if p.Channel == "" || p.Identity == "" || p.PeerID == "" {
		return NewTextErrorResponse("channel, identity, peerId are required"), false
	}
	switch p.Channel {
	case "telegram", "slack", "mattermost":
	default:
		return NewTextErrorResponse(fmt.Sprintf("unknown channel %q; supported: telegram, slack, mattermost", p.Channel)), false
	}
	if !t.identityConfigured(p.Channel, p.Identity) {
		return NewTextErrorResponse(
			fmt.Sprintf("unknown identity %q on channel %q; configured: %s",
				p.Identity, p.Channel, strings.Join(t.configuredIdentities(p.Channel), ", ")),
		), false
	}
	if strings.TrimSpace(p.Text) == "" && len(p.Files) == 0 {
		return NewTextErrorResponse("text or files is required"), false
	}
	return ToolResponse{}, true
}

// loadAttachments reads each FILE path, validating it's under the
// configured media root. Mirrors the path-safety logic in
// internal/bridge/service/media.go.
func (t *routerSendTool) loadAttachments(paths []string) ([]bridge.Attachment, *ToolResponse) {
	if len(paths) == 0 {
		return nil, nil
	}
	if t.deps.MediaRoot == "" {
		resp := NewTextErrorResponse("attachments not supported (no media root configured)")
		return nil, &resp
	}
	atts := make([]bridge.Attachment, 0, len(paths))
	for _, path := range paths {
		abs, err := safePath(path, t.deps.MediaRoot)
		if err != nil {
			resp := NewTextErrorResponse(fmt.Sprintf("file %q: %s", path, err.Error()))
			return nil, &resp
		}
		// Pre-flight the size before reading the file into memory.
		// Without this check a multi-GB attachment would OOM the
		// process before the per-platform size limit (50 MiB Telegram,
		// 1 GiB Slack) ever ran. The cap here is a safety upper bound;
		// the adapter still re-validates against its platform-specific
		// limit before upload.
		stat, err := os.Stat(abs)
		if err != nil {
			resp := NewTextErrorResponse(fmt.Sprintf("file %q: %s", path, err.Error()))
			return nil, &resp
		}
		if stat.Size() > routerSendMaxAttachmentBytes {
			resp := NewTextErrorResponse(fmt.Sprintf(
				"file %q exceeds router_send attachment size cap (%d bytes; cap %d bytes)",
				path, stat.Size(), routerSendMaxAttachmentBytes))
			return nil, &resp
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			resp := NewTextErrorResponse(fmt.Sprintf("file %q: %s", path, err.Error()))
			return nil, &resp
		}
		atts = append(atts, bridge.Attachment{
			FilePath: abs,
			FileName: lastSegment(abs),
			Content:  data,
		})
	}
	return atts, nil
}

// routerSendMaxAttachmentBytes is the tool-side hard ceiling on a
// single attachment. Set above Slack's 1 GiB cap so platform-specific
// limits remain the binding constraint, but low enough that a single
// runaway attachment can't exhaust the process's heap.
const routerSendMaxAttachmentBytes = 2 * 1024 * 1024 * 1024 // 2 GiB

// identityConfigured reports whether the named identity exists in the
// snapshot.
func (t *routerSendTool) identityConfigured(channel, id string) bool {
	if t.deps.Cfg == nil {
		return false
	}
	switch channel {
	case "telegram":
		if c := t.deps.Cfg.Channels.Telegram; c != nil {
			for _, b := range c.Bots {
				if b.ID == id && b.Enabled {
					return true
				}
			}
		}
	case "slack":
		if c := t.deps.Cfg.Channels.Slack; c != nil {
			for _, a := range c.Apps {
				if a.ID == id && a.Enabled {
					return true
				}
			}
		}
	case "mattermost":
		if c := t.deps.Cfg.Channels.Mattermost; c != nil {
			for _, m := range c.Instances {
				if m.ID == id && m.Enabled {
					return true
				}
			}
		}
	}
	return false
}

// configuredIdentities returns the enabled identity IDs for a channel.
// Sorted so error messages are stable.
func (t *routerSendTool) configuredIdentities(channel string) []string {
	out := []string{}
	if t.deps.Cfg == nil {
		return out
	}
	switch channel {
	case "telegram":
		if c := t.deps.Cfg.Channels.Telegram; c != nil {
			for _, b := range c.Bots {
				if b.Enabled {
					out = append(out, b.ID)
				}
			}
		}
	case "slack":
		if c := t.deps.Cfg.Channels.Slack; c != nil {
			for _, a := range c.Apps {
				if a.Enabled {
					out = append(out, a.ID)
				}
			}
		}
	case "mattermost":
		if c := t.deps.Cfg.Channels.Mattermost; c != nil {
			for _, m := range c.Instances {
				if m.Enabled {
					out = append(out, m.ID)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// buildRouterSendDescription assembles the agent-friendly tool
// description from the cfg.Router snapshot. Per the
// chat-bridge-agent-tool spec, the description enumerates each
// channel + identity + peer-id format + currently-bound peers.
func buildRouterSendDescription(deps RouterSendDeps) string {
	var b strings.Builder
	b.WriteString("Send a chat message to a bound chat-platform peer. Use this when an external reviewer needs the agent's output mid-run. The bridge delivers the message in-process — no HTTP loopback.\n\n")
	b.WriteString("Available channels and identities:\n")
	if deps.Cfg == nil {
		b.WriteString("  (no channels configured)\n")
	} else {
		writeChannelDescription(&b, "telegram", "numeric chat_id (e.g. 344281281)", deps.Cfg.Channels.Telegram, func() []string {
			if deps.Cfg.Channels.Telegram == nil {
				return nil
			}
			ids := []string{}
			for _, b := range deps.Cfg.Channels.Telegram.Bots {
				if b.Enabled {
					ids = append(ids, b.ID)
				}
			}
			return ids
		})
		writeChannelDescription(&b, "slack", "D<channel> for DMs, C<channel>[|<ts>] for channel/thread, U<user> auto-resolves to DM", deps.Cfg.Channels.Slack, func() []string {
			if deps.Cfg.Channels.Slack == nil {
				return nil
			}
			ids := []string{}
			for _, a := range deps.Cfg.Channels.Slack.Apps {
				if a.Enabled {
					ids = append(ids, a.ID)
				}
			}
			return ids
		})
		writeChannelDescription(&b, "mattermost", "<channelID>[|<rootPostID>] for thread, 26-char user-id auto-resolves to DM", deps.Cfg.Channels.Mattermost, func() []string {
			if deps.Cfg.Channels.Mattermost == nil {
				return nil
			}
			ids := []string{}
			for _, m := range deps.Cfg.Channels.Mattermost.Instances {
				if m.Enabled {
					ids = append(ids, m.ID)
				}
			}
			return ids
		})
	}
	if deps.Sender != nil {
		bound := deps.Sender.BoundPeersSnapshot(context.Background())
		if len(bound) > 0 {
			b.WriteString("\nCurrently bound peers (you can reach these without learning new IDs):\n")
			for _, p := range bound {
				fmt.Fprintf(&b, "  - %s:%s:%s\n", p.Channel, p.Identity, p.PeerID)
			}
		}
	}
	return b.String()
}

func writeChannelDescription(b *strings.Builder, name, peerFormat string, channelEnabled any, identitiesFn func() []string) {
	enabled := false
	switch c := channelEnabled.(type) {
	case *bridge.TelegramChannelConfig:
		enabled = c != nil && c.Enabled
	case *bridge.SlackChannelConfig:
		enabled = c != nil && c.Enabled
	case *bridge.MattermostChannelConfig:
		enabled = c != nil && c.Enabled
	}
	if !enabled {
		return
	}
	ids := identitiesFn()
	if len(ids) == 0 {
		return
	}
	sort.Strings(ids)
	fmt.Fprintf(b, "  %s — peerId format: %s — identities: %s\n",
		name, peerFormat, strings.Join(ids, ", "))
}

// jsonResponse marshals v as the tool's text response. The agent
// receives this as the tool result and parses it.
func jsonResponse(v any) ToolResponse {
	data, err := json.Marshal(v)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("marshal response: %s", err.Error()))
	}
	return NewTextResponse(string(data))
}

// redactToken scrubs common token shapes from an error message before
// surfacing to the agent. We don't have the actual token here (the
// bridge already redacts at its boundary), but defensively scrub anything
// that looks like xoxb-/xapp-/Bearer .
func redactToken(s string) string {
	for _, prefix := range []string{"xoxb-", "xapp-", "Bearer "} {
		s = scrubPrefix(s, prefix)
	}
	return s
}

func scrubPrefix(s, prefix string) string {
	i := strings.Index(s, prefix)
	if i < 0 {
		return s
	}
	// Find end of token (whitespace or end-of-string).
	end := i + len(prefix)
	for end < len(s) && s[end] != ' ' && s[end] != '"' && s[end] != '\'' {
		end++
	}
	return s[:i] + prefix + "<redacted>" + s[end:]
}

// safePath validates that path resolves inside mediaRoot, returns the
// absolute form. ErrUnsafeMediaPath is returned when it escapes.
//
// Symlink resolution mirrors internal/bridge/service.loadMediaAttachment:
// on macOS the canonical dataDir resolves through /private/tmp/... while
// the user-supplied path may come in as /tmp/..., so a literal prefix
// check would falsely reject a legitimate path that the bridge itself
// staged via StoreOutboundFile. EvalSymlinks is only applied when the
// candidate exists; a probe-for-write path validates by lexical
// comparison.
func safePath(path, mediaRoot string) (string, error) {
	abs, err := absClean(path)
	if err != nil {
		return "", err
	}
	root, err := absClean(mediaRoot)
	if err != nil {
		return "", err
	}
	if eval, err := filepath.EvalSymlinks(abs); err == nil {
		abs = eval
	}
	if eval, err := filepath.EvalSymlinks(root); err == nil {
		root = eval
	}
	if !strings.HasPrefix(abs, root+string(os.PathSeparator)) && abs != root {
		return "", errUnsafeMediaPath
	}
	return abs, nil
}

var errUnsafeMediaPath = fmt.Errorf("path is not under the bridge media store")

func absClean(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func lastSegment(p string) string {
	idx := strings.LastIndex(p, string(os.PathSeparator))
	if idx < 0 {
		return p
	}
	return p[idx+1:]
}
