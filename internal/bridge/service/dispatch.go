package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// Per-session dispatch channel capacities, per the chat-bridge spec:
//
//   - inbound: 16, NEVER drop. Reviewers' messages MUST NOT be lost.
//     Pushers (adapter-side per-peer goroutines) block when the queue
//     fills — chat platforms have their own buffering that absorbs the
//     stall.
//   - parts:   64, drop-oldest. Part-event transitions can collapse
//     ("completed" supersedes "running" supersedes "pending"). Drops are
//     rate-limited to one warn log per session per minute.
const (
	dispatchInboundCap = 16
	dispatchPartsCap   = 64
	// partsDrainGrace is how long the parts subscription is kept open
	// after agent.Run terminates, so trailing transitions (a tool's
	// final "completed" transition that the agent emits AFTER the
	// terminal AgentEvent) can be flushed to the chat surface. Tuned
	// short — agents emit terminal-event-then-trailing-parts in the
	// same iteration of the event loop, so the queued events are
	// already in flight; we only need to give the broker time to push.
	partsDrainGrace = 100 * time.Millisecond
)

// sessionDispatch owns the single agent.Run callsite for one bound
// sessionID. It runs in its own goroutine; all inbound messages for the
// session route through its inbound channel, ensuring serialization (no
// ErrSessionBusy can escape) and ordering (parts-event demux is interleaved
// with inbound on the same select loop). Lifecycle is tied to bind/unbind:
// created on the first Bind for the session, torn down on the last Unbind
// or when the orchestrator observes the session row's session_id was
// NULL'd by FK ON DELETE SET NULL.
type sessionDispatch struct {
	svc       *Service
	sessionID string

	inbound chan bridge.Inbound
	parts   chan pubsub.Event[message.PartEvent]

	mu          sync.Mutex
	overflowLog time.Time

	stop atomic.Bool

	// ownedSessions caches whether a given session ID is "ours" —
	// either this dispatcher's session or one of its descendants
	// (subagent sessions spawned via the `task` tool, which share
	// root_session_id with the parent). Per-event store lookups
	// would be too expensive in the hot path; this map amortises it.
	// Keys: session ID; values: bool. See isOwnedSession.
	ownedSessions sync.Map
}

// newSessionDispatch constructs and launches the per-session dispatcher
// goroutines. There are TWO goroutines per dispatcher:
//
//   - run: drains d.inbound and calls handleInbound, which blocks for
//     the entire agent.Run lifetime (minutes for long tool sequences).
//   - runParts: drains d.parts and calls handlePartEvent, processing
//     tool-update events CONCURRENTLY with the in-flight inbound. The
//     spec mandates "Indicator emission MUST NOT block the inbound
//     dispatch loop"; folding parts into the same select as inbound
//     made the loop block for the entire run duration so tool icons
//     only arrived AFTER the final assistant reply. Splitting into two
//     goroutines fixes the ordering — parts now interleave with the
//     run in real time.
//
// Both exit when ctx is cancelled, the inbound channel is closed via
// close(), or Service.Stop tears the bridge down.
func (s *Service) newSessionDispatch(sessionID string) *sessionDispatch {
	d := &sessionDispatch{
		svc:       s,
		sessionID: sessionID,
		inbound:   make(chan bridge.Inbound, dispatchInboundCap),
		parts:     make(chan pubsub.Event[message.PartEvent], dispatchPartsCap),
	}
	s.launchSupervised("session-dispatch/"+sessionID, d.run)
	s.launchSupervised("session-dispatch-parts/"+sessionID, d.runParts)
	return d
}

// run is the dispatcher's inbound loop. Reads from d.inbound, calls
// handleInbound, repeats. handleInbound BLOCKS for the entire agent.Run
// lifetime — that's by design (the spec mandates one in-flight Run per
// session at a time), so this loop only processes one inbound at a
// time. Parts events are handled in parallel by runParts so they don't
// have to wait for the run to finish.
func (d *sessionDispatch) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case in, ok := <-d.inbound:
			if !ok {
				return
			}
			if d.stop.Load() {
				return
			}
			d.handleInbound(ctx, in)
		}
	}
}

// runParts drains d.parts on a separate goroutine so tool-update
// emission overlaps with the in-flight agent.Run. Without this split,
// tool icons (🔧 read / 🔧 grep / etc.) only reach the chat surface
// AFTER the final assistant reply, because handleInbound blocks the
// dispatch loop's select.
//
// The d.parts channel still acts as the back-pressure boundary with
// drop-oldest semantics (drainParts forwards from the broker into
// d.parts non-blockingly); runParts consumes that buffer at chat-
// surface speed.
func (d *sessionDispatch) runParts(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-d.parts:
			if !ok {
				return
			}
			if d.stop.Load() {
				return
			}
			d.handlePartEvent(ev)
		}
	}
}

// handleInbound is one full agent.Run turn for a single inbound message.
// Steps (per the chat-bridge spec subscribe-before-Run requirement):
//
//  1. Subscribe to messages.SubscribeParts BEFORE calling agent.Run. The
//     broker has a zero-subscribers fast path that drops events emitted
//     before any subscriber attaches — calling Run first would silently
//     lose the agent's first ToolCall pending events.
//  2. Translate bridge.Attachment slice into message.Attachment for
//     agent.Run (the bridge package and message package use distinct
//     attachment types because the bridge package can't import message
//     without creating an import cycle through internal/config).
//  3. Invoke agent.Run; drain the returned channel (one terminal event).
//  4. Demux parts from the local subscription into d.parts so the
//     dispatcher's select loop can fan them to outbound, but cap the
//     queue at 64 with drop-oldest (newest wins because per-tool
//     transitions are strictly ordered: pending → running → completed).
func (d *sessionDispatch) handleInbound(ctx context.Context, in bridge.Inbound) {
	defer func() {
		if r := recover(); r != nil {
			logging.Error("bridge: handleInbound panic", "session", d.sessionID, "panic", r)
		}
	}()

	agent := d.svc.app.ActiveAgent()
	if agent == nil {
		logging.Warn("bridge: no active agent; dropping inbound", "session", d.sessionID)
		return
	}

	// Subscribe parts BEFORE Run — the broker's zero-subscriber fast path
	// will otherwise drop the agent's first ToolCall pending events.
	// On return, give the parts subscription a short grace window to
	// flush any trailing "completed" transitions that the agent emitted
	// AFTER the terminal AgentEvent. Without it, the partsCancel would
	// fire the moment handleInbound returns and drainParts could exit
	// before the tail events made it through the broker — leading to
	// stale "running" tool indicators on the chat surface.
	partsCtx, partsCancel := context.WithCancel(ctx)
	defer func() {
		select {
		case <-time.After(partsDrainGrace):
		case <-ctx.Done():
		}
		partsCancel()
	}()
	partsSub := d.svc.app.Messages.SubscribeParts(partsCtx)

	atts := translateAttachments(in.Attachments)
	runCh, err := agent.Run(ctx, d.sessionID, in.Text, 0, atts...)
	if err != nil {
		// In our single-callsite design ErrSessionBusy MUST NOT escape —
		// but other Run errors (e.g. shutting down) can. Log AND surface
		// to the chat surface so a stuck session is observable to the
		// reviewer instead of silently swallowing messages. Cap the
		// detail leaked to chat to the public-facing fields.
		logging.Warn("bridge: agent.Run failed", "session", d.sessionID, "err", err)
		surfaceMsg := "bridge: agent run failed (" + err.Error() + "). " +
			"If this keeps happening, use /reset in chat to clear the session " +
			"or POST /session/" + d.sessionID + "/abort to release the busy lock."
		d.svc.replyToPeer(ctx, in.Peer, surfaceMsg)
		return
	}

	// Fan part events into d.parts for outbound surface delivery (typing,
	// tool-update prints). Filter to this session's parts; broker is
	// process-wide and carries every session's events. The drainParts
	// goroutine runs under the supervised launcher so a panic inside
	// (e.g. a malformed PartEvent) cannot crash the orchestrator, and
	// s.wg tracks it across Service.Stop.
	d.svc.launchSupervisedCtx("dispatch-parts/"+d.sessionID, partsCtx, func(ctx context.Context) {
		d.drainParts(ctx, partsSub)
	})

	// Drain the agent's terminal event. The channel delivers exactly one
	// AgentEvent and closes (per agent.Run contract). We process its
	// outcome (text + struct-output) on the dispatcher goroutine so all
	// outbound work for this session remains serialized.
	for ev := range runCh {
		d.handleTerminalEvent(ctx, ev)
	}
}

// drainParts forwards parts for this session AND any of its descendant
// (subagent) sessions from the broker subscription to d.parts. Returns
// when partsCtx is cancelled (set by handleInbound after agent.Run
// completes).
//
// Subagent visibility: when the parent agent calls the `task` tool,
// opencode spawns a subagent on a NEW session whose root_session_id
// points back at the parent. Subagent tool activity (which can
// dominate the run — e.g. 15 minutes of Atlassian MCP calls inside one
// task) emits part events on the SUBAGENT's session, not the parent's.
// Without the descendant filter, the reviewer would see "🔧 task ·
// ..." at the start and then silence for the entire subagent run.
// Including descendant events makes the chat surface reflect what the
// run is actually doing, so a hung MCP call is visible instead of
// looking like the bridge itself is stuck.
//
// Drop-oldest semantics are preserved — the consumer (runParts) drains
// d.parts in parallel with handleInbound, so backlog is rare; when it
// does happen, the oldest event is dropped first.
func (d *sessionDispatch) drainParts(partsCtx context.Context, sub <-chan pubsub.Event[message.PartEvent]) {
	for {
		select {
		case <-partsCtx.Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return
			}
			if !d.isOwnedSession(partsCtx, ev.Payload.SessionID) {
				continue
			}
			select {
			case d.parts <- ev:
			default:
				d.logOverflow()
				// Drop oldest: try once more (non-blocking). The
				// previously-oldest event was on the front of the
				// buffer; pop it by reading and discarding, then
				// retry the send.
				select {
				case <-d.parts:
				default:
				}
				select {
				case d.parts <- ev:
				default:
					// Still full — the consumer is wedged; surrender.
				}
			}
		}
	}
}

// isOwnedSession reports whether a part event's session_id is either
// this dispatcher's own session, or a descendant subagent session
// (root_session_id == d.sessionID). Results are cached in
// d.ownedSessions to amortise the per-event store lookup — a busy
// subagent can emit hundreds of events per minute, all from the same
// session ID, so we only want to look up once per discovered session.
//
// The cache is per-dispatcher and tied to its lifetime; no GC needed
// because a dispatcher is torn down when the binding is unbound.
func (d *sessionDispatch) isOwnedSession(ctx context.Context, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	if sessionID == d.sessionID {
		return true
	}
	if v, ok := d.ownedSessions.Load(sessionID); ok {
		return v.(bool)
	}
	// Cache miss — resolve via the session service. Unknown sessions
	// (deleted, not yet flushed) cache as false so we don't repeat
	// the lookup for every event in a flood.
	owned := false
	if d.svc.app != nil && d.svc.app.Sessions != nil {
		sess, err := d.svc.app.Sessions.Get(ctx, sessionID)
		if err == nil && sess.RootSessionID == d.sessionID {
			owned = true
		}
	}
	d.ownedSessions.Store(sessionID, owned)
	return owned
}

// logOverflow emits a rate-limited warn log when the parts buffer
// overflows, per the chat-bridge spec ("once per session per minute").
func (d *sessionDispatch) logOverflow() {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if now.Sub(d.overflowLog) < time.Minute {
		return
	}
	d.overflowLog = now
	logging.Warn("bridge: part-queue overflow",
		"session", d.sessionID,
		"cap", dispatchPartsCap,
	)
}

// handleTerminalEvent is invoked once per agent.Run with the terminal
// AgentEvent. For text turns it fans agent output across all bound peers
// via Service.SendBySessionID; for error events it logs but still emits
// any text the agent had produced; struct-output events skip fan-out
// (the flow engine drains the structured result separately).
//
// Implementation note: fan-out to bound peers happens through
// Service.SendBySessionID which queries the store + dispatches to adapters
// in a bounded worker pool — this dispatcher does NOT do outbound IO
// directly so a slow chat platform does not stall the next agent turn.
func (d *sessionDispatch) handleTerminalEvent(ctx context.Context, ev agent.AgentEvent) {
	switch ev.Type {
	case agent.AgentEventTypeError:
		logging.Warn("bridge: agent run terminal error",
			"session", d.sessionID, "err", ev.Error)
		// Fall through to fan-out so any partial text the agent
		// emitted before erroring still reaches the chat surface.
	case agent.AgentEventTypeSummarize:
		// Summarization is internal — no chat-surface delivery.
		return
	}

	text := agentMessageText(ev.Message)
	mediaRoot, _ := d.svc.MediaDir()
	clean, atts, unsafe := ParseFileTokens(text, mediaRoot)
	if len(unsafe) > 0 {
		logging.Warn("bridge: dropped unsafe FILE: paths from agent output",
			"session", d.sessionID, "paths", unsafe)
	}
	if clean == "" && len(atts) == 0 {
		return
	}

	out := bridge.Outbound{Text: clean, Attachments: atts}
	results, err := d.svc.SendBySessionID(ctx, d.sessionID, out)
	if err != nil {
		logging.Warn("bridge: terminal-event fan-out failed",
			"session", d.sessionID, "err", err)
		return
	}
	for _, r := range results {
		if !r.Delivered && r.Err != nil {
			logging.Info("bridge: per-peer delivery failed",
				"session", d.sessionID, "peer", r.Binding.PeerID, "err", r.Err)
		}
	}
}

// agentMessageText concatenates every TextContent part in the agent's
// terminal message. ReasoningContent (the model's internal chain of
// thought) and ToolCall/ToolResult parts are skipped — they're not
// user-facing chat surface output. The TS bridge does the same.
func agentMessageText(m message.Message) string {
	var b strings.Builder
	first := true
	for _, p := range m.Parts {
		if tc, ok := p.(message.TextContent); ok {
			if !first {
				b.WriteString("\n")
			}
			b.WriteString(tc.Text)
			first = false
		}
	}
	return b.String()
}

// handlePartEvent forwards a single part transition to the outbound
// surface. When cfg.Router.ToolUpdatesEnabled is true, tool-call lifecycle
// transitions are summarized as short chat messages so the reviewer
// sees what the agent is doing in real time. Failures (ToolResult with
// IsError) are ALWAYS surfaced regardless of the flag — silent tool
// failures are too easy to miss otherwise.
//
// Emission rules (kept terse to avoid spamming chat):
//   - ToolCall with Finished=true → one message "🔧 <name>#<id> · <params>"
//   - ToolResult with IsError=true → "✗ <name>#<id> · <error preview>"
//   - Successful completions emit "✓ <name>#<id> · <preview>"
//
// The #<id> suffix is a short stable hash of the tool_call_id so a
// reviewer watching parallel tool calls can pair each ✓/✗ result back
// to the originating 🔧 call. Without it, two concurrent `bash` calls
// would render as indistinguishable "🔧 bash" / "✓ bash" pairs.
//
// Per the chat-bridge spec the dispatcher MUST consume from d.parts
// even when the outbound is suppressed — otherwise drainParts back-
// pressures the broker subscription and stalls every other session.
func (d *sessionDispatch) handlePartEvent(ev pubsub.Event[message.PartEvent]) {
	if d.svc.cfg == nil {
		return
	}
	tu := d.svc.cfg.ToolUpdatesEnabled
	switch part := ev.Payload.Part.(type) {
	case message.ToolCall:
		// Streaming providers (Anthropic) publish each ToolCall up to
		// THREE times:
		//   1. EventToolUseStart — Finished=false, Input empty
		//   2. EventToolUseStop  — Finished=true,  Input STILL empty
		//      (the delta-accumulation path is commented out in
		//      agent.go, so Input isn't merged at this point)
		//   3. EventComplete     — Finished=true,  Input MERGED with
		//      the assembled args (via mergeToolCalls)
		// Non-streaming providers (OpenAI / Gemini) only fire #3.
		//
		// We want exactly one line per tool call, with the full args.
		// Filter on `Finished && Input != ""`:
		//   - #1 fails Finished                  → skip
		//   - #2 has empty Input                 → skip
		//   - #3 (the only useful one)           → emit
		// A genuinely-no-args tool (e.g. get_all_projects → "{}")
		// still passes because its Input is the literal "{}", not "".
		if !tu || !part.Finished || part.Input == "" {
			return
		}
		label := part.Name + callIDSuffix(part.ID)
		params := formatToolParams(part.Name, part.Input)
		if params != "" {
			d.emitToolUpdate(fmt.Sprintf("🔧 %s · %s", label, params))
		} else {
			d.emitToolUpdate(fmt.Sprintf("🔧 %s", label))
		}
	case message.ToolResult:
		label := part.Name + callIDSuffix(part.ToolCallID)
		if part.IsError {
			// Failures always surface, even with ToolUpdatesEnabled=false,
			// so silent breakage is visible to the reviewer. Truncate by
			// rune (codepoint) so a multi-byte glyph at the boundary is
			// not split into invalid UTF-8.
			preview := truncateRunes(oneLine(part.Content), 200)
			d.emitToolUpdate(fmt.Sprintf("✗ %s · %s", label, preview))
			return
		}
		// Successful tool result: emit a compact preview so the
		// reviewer can see what the tool actually returned, not just
		// that it ran. Gated by ToolUpdatesEnabled (errors above are
		// not gated; successes are).
		if !tu {
			return
		}
		preview := truncateRunes(oneLine(part.Content), 200)
		if preview == "" {
			// Empty success — represent it explicitly so the user
			// doesn't think the tool transition was dropped.
			d.emitToolUpdate(fmt.Sprintf("✓ %s", label))
		} else {
			d.emitToolUpdate(fmt.Sprintf("✓ %s · %s", label, preview))
		}
	}
}

// callIDSuffix renders a short stable suffix derived from the tool
// call ID so reviewers can pair "🔧 bash#abcd" with its "✓ bash#abcd"
// (or "✗ bash#abcd") result. Empty input yields an empty suffix.
//
// The ID is truncated to the trailing 6 chars — provider-issued IDs
// are typically opaque strings like "toolu_01ABC..." or "call_xyz...".
// The trailing portion is more entropic than the prefix (which is
// often a fixed scheme prefix) and short enough to keep chat lines
// compact.
func callIDSuffix(id string) string {
	if id == "" {
		return ""
	}
	const n = 6
	if len(id) > n {
		id = id[len(id)-n:]
	}
	return "#" + id
}

// truncateRunes returns s capped to maxRunes codepoints. Slicing a
// UTF-8 string at a byte index can land mid-codepoint and produce
// invalid UTF-8; counting runes guarantees the cut is at a codepoint
// boundary. Appends an ellipsis when truncation occurred.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i] + "…"
		}
		count++
	}
	return s
}

// formatToolParams extracts a short informative summary from a
// ToolCall.Input (a JSON string). Common params for known tools
// (bash command, file path, grep pattern, etc.) are surfaced
// verbatim; unknown tools fall back to a single-line dump of the
// raw input. Result is rune-truncated to 200 chars so a long bash
// command or file path can't dominate a chat line.
//
// Implementation note: this duplicates a subset of the TUI's
// renderToolParams logic but uses anonymous JSON decoding instead
// of importing each tool's typed param struct. That keeps the
// bridge package free of a dependency on internal/llm/tools.
func formatToolParams(name, input string) string {
	if input == "" {
		return ""
	}
	// Guard against a runaway agent or malformed tool surface that
	// could emit a multi-megabyte JSON input. Per-event allocation
	// is a hot path here (handlePartEvent runs once per tool
	// transition), and json.Unmarshal allocates proportionally to
	// the input size. Realistic tool inputs are < 4 KB; the cap
	// gives generous headroom while bounding worst case.
	const maxParamInputBytes = 64 * 1024
	if len(input) > maxParamInputBytes {
		return truncateRunes(strings.ReplaceAll(input, "\n", " "), 200)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(input), &raw); err != nil {
		// Fall back to the raw input minus newlines so the line stays
		// single-line.
		return truncateRunes(strings.ReplaceAll(input, "\n", " "), 200)
	}
	// Priority key per known tool. The first key in the list that's
	// present and non-empty wins. The "params" tail appends any
	// secondary keys for context (e.g. read offset/limit).
	type keySpec struct {
		primary   string
		secondary []string
	}
	keys := map[string]keySpec{
		"bash":        {primary: "command"},
		"read":        {primary: "file_path", secondary: []string{"limit", "offset"}},
		"write":       {primary: "file_path"},
		"edit":        {primary: "file_path"},
		"multiedit":   {primary: "file_path", secondary: []string{"edits"}},
		"delete":      {primary: "path"},
		"ls":          {primary: "path"},
		"grep":        {primary: "pattern", secondary: []string{"path", "include"}},
		"glob":        {primary: "pattern", secondary: []string{"path"}},
		"view_image":  {primary: "file_path"},
		"webfetch":    {primary: "url"},
		"websearch":   {primary: "query", secondary: []string{"max_results"}},
		"sourcegraph": {primary: "query"},
		"task":        {primary: "prompt", secondary: []string{"subagent_type"}},
		"router_send": {primary: "peerId", secondary: []string{"channel", "identity"}},
	}
	spec, known := keys[name]
	var parts []string
	if known {
		if v := stringField(raw, spec.primary); v != "" {
			parts = append(parts, oneLine(v))
		}
		for _, k := range spec.secondary {
			if v := stringField(raw, k); v != "" {
				parts = append(parts, k+"="+oneLine(v))
			}
		}
	}
	if len(parts) == 0 {
		// Unknown tool or known-tool with no recognised primary key —
		// fall back to a compact representation of the whole input.
		return truncateRunes(strings.ReplaceAll(input, "\n", " "), 200)
	}
	return truncateRunes(strings.Join(parts, " "), 200)
}

// stringField returns map[k] coerced to a single-line string. Numbers
// and booleans are formatted via %v; arrays/objects show their length
// (e.g. "edits=3") because dumping their full content rarely improves
// readability on a chat line.
func stringField(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// JSON numbers always decode to float64; render as integer
		// when possible so "offset=10" doesn't read "offset=10.000000".
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case bool:
		return fmt.Sprintf("%v", t)
	case []any:
		return fmt.Sprintf("%d", len(t))
	case map[string]any:
		return fmt.Sprintf("(%d keys)", len(t))
	default:
		return fmt.Sprintf("%v", v)
	}
}

// oneLine strips newlines so a multi-line bash command or prompt
// doesn't break chat formatting.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// emitToolUpdate sends a short status line to every peer currently
// bound to this dispatcher's session. Runs in a separate goroutine so
// platform-call latency cannot stall the per-session dispatcher loop
// (per the chat-bridge spec "Indicator emission MUST NOT block the
// inbound dispatch loop").
func (d *sessionDispatch) emitToolUpdate(text string) {
	ctx := d.svc.ctx
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Warn("bridge: emitToolUpdate panic", "session", d.sessionID, "panic", r)
			}
		}()
		_, err := d.svc.SendBySessionID(ctx, d.sessionID, bridge.Outbound{Text: text})
		if err != nil {
			logging.Warn("bridge: tool-update fan-out failed", "session", d.sessionID, "err", err)
		}
	}()
}

// translateAttachments converts the bridge-domain Attachment slice into
// the message-domain Attachment slice agent.Run expects. The two types
// are field-compatible; the indirection exists solely because
// internal/bridge cannot import internal/message (transitive cycle through
// internal/config — see bridge.go's package docstring).
func translateAttachments(in []bridge.Attachment) []message.Attachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]message.Attachment, len(in))
	for i, a := range in {
		out[i] = message.Attachment{
			FilePath: a.FilePath,
			FileName: a.FileName,
			MimeType: a.MimeType,
			Content:  a.Content,
		}
	}
	return out
}

// closeOnce marks the dispatcher as stopped and drains its channels.
// Caller MUST hold s.dispatchMu.
func (d *sessionDispatch) close() {
	if !d.stop.CompareAndSwap(false, true) {
		return
	}
	close(d.inbound)
}

// pushInbound enqueues an inbound message onto the dispatcher's inbound
// channel. Blocks when the channel is full (the spec's "back-pressure
// adapter instead of drop" semantics). Returns ctx.Err() if ctx is
// cancelled while waiting for capacity.
func (d *sessionDispatch) pushInbound(ctx context.Context, in bridge.Inbound) error {
	select {
	case d.inbound <- in:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
