package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

// CommandHandler is the contract chat-command implementations satisfy.
// Each handler is invoked synchronously from the dispatcher's inbound
// loop; the returned *CommandReply carries the chat-surface response.
// A nil reply (or one whose Text is empty AND Hint is nil) skips the
// reply. Long-running handlers MUST do work in a fresh goroutine
// rather than blocking the dispatcher.
//
// Reply.Text is the plain-text fallback; Reply.Hint is the optional
// structured render that rich-adapter-aware surfaces consume. Authors
// of new commands should ALWAYS populate Reply.Text for correctness on
// minimal adapters; Reply.Hint is opt-in for richer output. See
// bridge-command-render-native capability.
type CommandHandler func(ctx context.Context, in bridge.Inbound) *bridge.CommandReply

// replyText is a small helper for handlers that have no structured
// rendering — wraps a string as a text-only *CommandReply.
func replyText(text string) *bridge.CommandReply {
	return &bridge.CommandReply{Text: text}
}

// ChatCommands returns the chat-command surface the bridge exposes.
// Commands match the TS bridge's `bridge.ts` chat-command implementation
// minus /dir (workspace switching is not ported per the chat-bridge spec
// — one opencode process = one workspace).
//
// The returned map is keyed by command name (no leading slash). Handlers
// are stateless except for what's reachable via Service.
func (s *Service) ChatCommands() map[string]CommandHandler {
	return map[string]CommandHandler{
		"agent":    s.cmdAgent,
		"model":    s.cmdModel,
		"sessions": s.cmdSessions,
		"session":  s.cmdSession,
		"reset":    s.cmdReset,
		"abort":    s.cmdAbort,
		"pair":     s.cmdPair,
		"skip":     s.cmdSkip,
		"help":     s.cmdHelp,
		"dir":      s.cmdDir,
	}
}

// cmdAbort cancels any in-flight agent.Run on the current session and
// releases the busy lock. Use case: a subagent (`task` tool) calls an
// MCP tool that hangs forever — the parent run is wedged and the
// session won't accept new messages until aborted. Previously the
// reviewer had no way to recover without an HTTP POST to /session/<id>/abort
// or restarting opencode.
func (s *Service) cmdAbort(ctx context.Context, in bridge.Inbound) *bridge.CommandReply {
	binding, err := s.resolveBinding(ctx, in.Peer)
	if err != nil {
		return replyText("Failed to resolve binding: " + err.Error())
	}
	activeAgent := s.app.ActiveAgent()
	if activeAgent == nil {
		return replyText("No active agent — nothing to abort.")
	}
	if !activeAgent.IsSessionBusy(binding.SessionID) {
		return replyText(fmt.Sprintf("Session %s is not running anything to abort.",
			shortSessionID(binding.SessionID)))
	}
	activeAgent.Cancel(binding.SessionID)
	return replyText(fmt.Sprintf("Aborted in-flight run on session %s. Send another message to continue.",
		shortSessionID(binding.SessionID)))
}

// cmdAgent: list available agents or switch the active one.
// "/agent" (no args) → reply with the available list.
// "/agent coder" → switch active agent to "coder".
func (s *Service) cmdAgent(_ context.Context, in bridge.Inbound) *bridge.CommandReply {
	args := strings.TrimSpace(in.CommandArgs)
	if args == "" {
		names := s.app.PrimaryAgentKeys
		if len(names) == 0 {
			return replyText("No primary agents registered.")
		}
		active := s.app.ActiveAgentName()
		// Build both text fallback and structured hint.
		textLines := make([]string, len(names))
		items := make([]bridge.ListItem, len(names))
		for i, n := range names {
			marker := ""
			textMarker := ""
			if n == active {
				marker = "active"
				textMarker = " (active)"
			}
			textLines[i] = fmt.Sprintf("- %s%s", n, textMarker)
			items[i] = bridge.ListItem{Label: n, Marker: marker}
		}
		return &bridge.CommandReply{
			Text: "Available agents:\n" + strings.Join(textLines, "\n"),
			Hint: bridge.NewListHint("Available agents", items, "active"),
		}
	}
	target := config.AgentName(args)
	if err := s.app.SetActiveAgent(target); err != nil {
		return replyText(fmt.Sprintf("Failed to set agent %q: %v", args, err))
	}
	body := fmt.Sprintf("Active agent: %s", args)
	return &bridge.CommandReply{
		Text: body,
		Hint: bridge.NewStatusHint(body),
	}
}

// cmdModel: list available models or switch the active agent's model.
// "/model" → list supported models for the active agent's provider.
// "/model <id>" → call config.UpdateAgentModel for the active agent.
func (s *Service) cmdModel(_ context.Context, in bridge.Inbound) *bridge.CommandReply {
	args := strings.TrimSpace(in.CommandArgs)
	if args == "" {
		ids := make([]string, 0, len(models.SupportedModels))
		for id := range models.SupportedModels {
			ids = append(ids, string(id))
		}
		sort.Strings(ids)
		items := make([]bridge.ListItem, len(ids))
		for i, id := range ids {
			items[i] = bridge.ListItem{Label: id}
		}
		return &bridge.CommandReply{
			Text: "Models:\n" + strings.Join(ids, "\n"),
			Hint: bridge.NewListHint("Models", items, ""),
		}
	}
	id := models.ModelID(args)
	if _, ok := models.SupportedModels[id]; !ok {
		return replyText(fmt.Sprintf("Unknown model %q", args))
	}
	active := s.app.ActiveAgentName()
	if err := config.UpdateAgentModel(active, id); err != nil {
		return replyText(fmt.Sprintf("Failed to update model: %v", err))
	}
	return replyText(fmt.Sprintf("Active model: %s (agent %s)", args, active))
}

// cmdSessions: list recent sessions. Truncates to top-10 by recency.
// IDs are shown as 8-char prefixes so the reviewer can type a short
// suffix into `/session <prefix>` to switch. The current binding's
// session is marked with `★` so the reviewer can tell which row is
// active without running `/session`.
//
// Filtering: empty placeholder sessions are hidden from the listing.
// `bridge.Service.Bind` creates a session row up-front via
// ensureSession so the FK constraint on bridge_sessions can be
// satisfied; if no inbound ever arrives, that row stays at
// MessageCount==0 with the default "chat-bridge" title. Hiding them
// keeps the listing focused on real conversations the reviewer has
// had — operators can still find them via the HTTP API. The current
// binding's session is ALWAYS shown even if empty (you should be able
// to see what `/session` reports for the row you're on).
func (s *Service) cmdSessions(ctx context.Context, in bridge.Inbound) *bridge.CommandReply {
	sessions, err := s.app.Sessions.List(ctx)
	if err != nil {
		return replyText("Failed to list sessions: " + err.Error())
	}
	if len(sessions) == 0 {
		return replyText("No sessions yet.")
	}
	currentID := ""
	if b, err := s.resolveBinding(ctx, in.Peer); err == nil {
		currentID = b.SessionID
	}
	// Filter empty placeholder sessions (Title=="chat-bridge" + zero
	// messages) — these are pre-created stubs from Bind() that never
	// got a conversation. Always keep the current binding's session
	// visible so /session has a row to mark with ★.
	filtered := sessions[:0]
	for _, sess := range sessions {
		isEmptyPlaceholder := sess.Title == BridgePlaceholderTitle && sess.MessageCount == 0
		if isEmptyPlaceholder && sess.ID != currentID {
			continue
		}
		filtered = append(filtered, sess)
	}
	sessions = filtered
	if len(sessions) == 0 {
		return replyText("No sessions yet.")
	}
	now := time.Now().UnixMilli()
	if len(sessions) > 10 {
		sessions = sessions[:10]
	}
	lines := make([]string, 0, len(sessions))
	tableRows := make([][]string, 0, len(sessions))
	for _, sess := range sessions {
		marker := "  "
		if sess.ID == currentID {
			marker = "★ "
		}
		// Use prompt_tokens + completion_tokens (the conversation's
		// net usage as shown in the TUI), NOT the total_* fields —
		// total_* aggregate every model API call including cached
		// context replay, which inflates the number ~10x and
		// disagrees with what the user sees locally. Same goes for
		// the relative-age field: TUI says "updated Nm", which maps
		// to UpdatedAt, not CreatedAt. Cost matches the TUI's
		// status-bar formula and includes successful subagent costs
		// via agent-tool.go:205's parent.Cost += subagent.Cost
		// roll-up — see cmdSession below for full notes.
		lines = append(lines, fmt.Sprintf("%s%s · %s · %s tokens · %s · updated %s",
			marker, shortSessionID(sess.ID), sess.Title,
			formatTokens(sess.PromptTokens+sess.CompletionTokens),
			formatCost(sess.Cost),
			formatRelativeAge(sess.UpdatedAt*1000, now),
		))
		tableRows = append(tableRows, []string{
			strings.TrimSpace(marker) + shortSessionID(sess.ID),
			sess.Title,
			formatTokens(sess.PromptTokens + sess.CompletionTokens),
			formatCost(sess.Cost),
			formatRelativeAge(sess.UpdatedAt*1000, now),
		})
	}
	text := "Recent sessions (★ = current):\n" + strings.Join(lines, "\n") +
		"\n\nSwitch with `/session <id-prefix>`."
	return &bridge.CommandReply{
		Text: text,
		Hint: bridge.NewTableHint([]string{"ID", "Title", "Tokens", "Cost", "Updated"}, tableRows),
	}
}

// cmdSession: show details for the current session OR switch to a
// different one by ID prefix. Switching:
//   - resolves the prefix against the full session list (substring of
//     the UUID's leading characters); ambiguous matches reply with the
//     candidates and don't pick.
//   - updates this peer's binding row to point at the target session.
//     Other peers in a multi-reviewer fan-out (separate binding rows)
//     are NOT affected.
//   - ensures a dispatcher exists for the new session before returning,
//     so the next inbound from this peer routes immediately.
//   - refuses if EITHER the current or target session has a run in
//     flight, since switching mid-run would split agent output across
//     two dispatchers.
func (s *Service) cmdSession(ctx context.Context, in bridge.Inbound) *bridge.CommandReply {
	args := strings.TrimSpace(in.CommandArgs)
	binding, err := s.resolveBinding(ctx, in.Peer)
	if err != nil {
		return replyText("Failed to resolve binding: " + err.Error())
	}

	if args == "" {
		sess, err := s.app.Sessions.Get(ctx, binding.SessionID)
		if err != nil {
			return replyText("Failed to get session: " + err.Error())
		}
		// Tokens: PromptTokens+CompletionTokens is the conversation's
		// net usage matching the local TUI; TotalPromptTokens et al
		// aggregate every API call including cached context replay
		// and inflate the number by ~10x.
		//
		// Cost is the running USD total accumulated by TrackUsage
		// across every model call in this session — same value the
		// TUI status bar displays. Subagent (`task` tool) costs are
		// rolled INTO the parent via agent-tool.go:205
		// (parentSession.Cost += updatedSession.Cost) once the task
		// completes successfully, so the number here already covers
		// the whole conversation tree. Note: subagents that fail or
		// are canceled mid-run (e.g. a hung MCP call) do NOT credit
		// back to the parent — that's an opencode-wide accounting
		// blind spot, not bridge-specific.
		return replyText(fmt.Sprintf("★ Session %s\nTitle: %s\nTokens: %s in / %s out\nCost: %s\nMessages: %d\n\nUse `/sessions` to list, `/session <id-prefix>` to switch.",
			shortSessionID(sess.ID), sess.Title,
			formatTokens(sess.PromptTokens),
			formatTokens(sess.CompletionTokens),
			formatCost(sess.Cost),
			sess.MessageCount,
		))
	}

	// Switch path. Resolve the prefix.
	sessions, err := s.app.Sessions.List(ctx)
	if err != nil {
		return replyText("Failed to list sessions: " + err.Error())
	}
	var matches []sessionMatch
	for _, sess := range sessions {
		if strings.HasPrefix(sess.ID, args) {
			matches = append(matches, sessionMatch{ID: sess.ID, Title: sess.Title})
		}
	}
	switch len(matches) {
	case 0:
		return replyText(fmt.Sprintf("No session matches prefix %q. Use `/sessions` to see recent IDs.", args))
	case 1:
		// proceed
	default:
		// Cap the listing so a one-character prefix on a large
		// workspace doesn't flood chat with hundreds of lines.
		const maxAmbiguousList = 20
		display := matches
		more := 0
		if len(display) > maxAmbiguousList {
			more = len(display) - maxAmbiguousList
			display = display[:maxAmbiguousList]
		}
		lines := make([]string, len(display))
		for i, m := range display {
			lines[i] = fmt.Sprintf("- %s · %s", shortSessionID(m.ID), m.Title)
		}
		footer := ""
		if more > 0 {
			footer = fmt.Sprintf("\n…and %d more. Use a longer prefix to disambiguate.", more)
		}
		return replyText(fmt.Sprintf("Prefix %q is ambiguous — %d matches:\n%s%s",
			args, len(matches), strings.Join(lines, "\n"), footer))
	}
	target := matches[0]
	if target.ID == binding.SessionID {
		return replyText(fmt.Sprintf("Already on session %s · %s.", shortSessionID(target.ID), target.Title))
	}

	// Refuse mid-run on either side. Tells the user explicitly rather
	// than silently splitting traffic.
	activeAgent := s.app.ActiveAgent()
	if activeAgent != nil {
		if activeAgent.IsSessionBusy(binding.SessionID) {
			return replyText(fmt.Sprintf("Current session %s is mid-run — wait for it to finish or `/reset` first.",
				shortSessionID(binding.SessionID)))
		}
		if activeAgent.IsSessionBusy(target.ID) {
			return replyText(fmt.Sprintf("Target session %s is mid-run — try again in a moment.",
				shortSessionID(target.ID)))
		}
	}

	if err := s.store.UpdateBindingSessionID(ctx, s.projectID,
		in.Peer.Channel, in.Peer.Identity, in.Peer.PeerID, target.ID); err != nil {
		return replyText("Failed to switch session: " + err.Error())
	}
	// Ensure the dispatcher for the new session is up so the next
	// inbound routes without a cold-start delay. The old session's
	// dispatcher is left alone — closeDispatcherIfEmpty would tear it
	// down later if no other peers point at it.
	_ = s.dispatcherFor(target.ID)

	return replyText(fmt.Sprintf("Switched to session %s · %s. Next message routes there.",
		shortSessionID(target.ID), target.Title))
}

// sessionMatch is the trimmed view of session.List() used by the switch
// path so we don't keep the full Session struct around.
type sessionMatch struct {
	ID    string
	Title string
}

// shortSessionID returns the first 8 characters of a UUID-style session
// ID. Used by `/sessions` and `/session` to keep chat lines compact
// while still being unique enough to prefix-match on switch.
func shortSessionID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// cmdReset: forget the current binding so the next inbound creates a
// fresh session. Equivalent to /router/unbind for the inbound's
// (channel, identity, peerKey) tuple. Also tears down the per-session
// dispatcher when no other peer references the session, so a deployment
// where users `/reset` frequently doesn't accumulate idle dispatcher
// goroutines.
func (s *Service) cmdReset(ctx context.Context, in bridge.Inbound) *bridge.CommandReply {
	// Look up the existing binding directly (no resolveBinding — that
	// would auto-allocate a fresh session for a missing row).
	existing, _ := s.store.GetBinding(ctx, s.projectID,
		in.Peer.Channel, in.Peer.Identity, in.Peer.PeerID)
	if err := s.store.DeleteBindingByPeer(
		ctx, s.projectID, in.Peer.Channel, in.Peer.Identity, in.Peer.PeerID,
	); err != nil {
		return replyText("Failed to reset binding: " + err.Error())
	}
	if existing.SessionID != "" {
		s.closeDispatcherIfEmpty(ctx, existing.SessionID)
	}
	return replyText("Session reset; next message starts fresh.")
}

// cmdPair: surface the pairing-code allowlist mechanism for the
// inbound's peer. Phase 4 (Telegram adapter) provides the
// pairing-code-hash check; this command is a no-op stub until then.
func (s *Service) cmdPair(_ context.Context, _ bridge.Inbound) *bridge.CommandReply {
	return replyText("Pairing is configured via .opencode.json router.channels.telegram.bots[].pairingCodeHash")
}

// cmdSkip: skip the current pending agent question (if any). Phase 3.7
// (question protocol) wires this; for now the stub explains.
func (s *Service) cmdSkip(_ context.Context, _ bridge.Inbound) *bridge.CommandReply {
	return replyText("No pending question to skip.")
}

// cmdDir: workspace switching is NOT supported in the Go bridge — one
// opencode process = one workspace, pinned to config.WorkingDirectory().
// This command is intercepted only to give the user a clear "unsupported"
// reply rather than letting it fall through to the agent prompt.
func (s *Service) cmdDir(_ context.Context, _ bridge.Inbound) *bridge.CommandReply {
	return replyText("Workspace switching is not supported in this deployment. Restart opencode in the desired directory.")
}

// helpEntry pairs a command's display form with its description for
// rich rendering. The list is iterated for both the text fallback and
// the structured hint to keep them in sync.
type helpEntry struct {
	Cmd  string
	Desc string
}

// helpEntriesForChannel returns the slash-command help entries that
// apply to the given channel. /pair only renders on Telegram (per
// bridge-adapter-scoped-commands). Pass channel="" for an all-channels
// view (introspection / operator debug — not used by the chat surface).
func (s *Service) helpEntriesForChannel(channel string) []helpEntry {
	all := []helpEntry{
		{Cmd: "/agent [name]", Desc: "list or switch the active agent"},
		{Cmd: "/model [id]", Desc: "list or switch the model"},
		{Cmd: "/sessions", Desc: "list recent sessions (★ = current)"},
		{Cmd: "/session [id-prefix]", Desc: "show details or switch by ID prefix"},
		{Cmd: "/reset", Desc: "forget this binding; next message starts fresh"},
		{Cmd: "/abort", Desc: "cancel an in-flight run on the current session"},
		{Cmd: "/pair", Desc: "pairing-code information"},
		{Cmd: "/skip", Desc: "skip the current pending question"},
		{Cmd: "/help", Desc: "this listing"},
		{Cmd: "/dir", Desc: "(not supported — opencode is pinned to one workspace)"},
	}
	if channel == "" || channel == "telegram" {
		return all
	}
	// Non-Telegram channels: drop /pair.
	out := make([]helpEntry, 0, len(all)-1)
	for _, e := range all {
		if e.Cmd == "/pair" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// cmdHelp: list every command available on the current channel.
func (s *Service) cmdHelp(_ context.Context, in bridge.Inbound) *bridge.CommandReply {
	entries := s.helpEntriesForChannel(in.Peer.Channel)
	textLines := make([]string, len(entries))
	items := make([]bridge.ListItem, len(entries))
	for i, e := range entries {
		textLines[i] = fmt.Sprintf("%-22s %s", e.Cmd, e.Desc)
		items[i] = bridge.ListItem{Label: e.Cmd, Sublabel: e.Desc}
	}
	return &bridge.CommandReply{
		Text: strings.Join(textLines, "\n"),
		Hint: bridge.NewListHint("Commands", items, ""),
	}
}
