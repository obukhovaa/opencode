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
// loop; the returned text is the chat-surface reply (empty string skips
// the reply). Long-running handlers MUST do work in a fresh goroutine
// rather than blocking the dispatcher.
type CommandHandler func(ctx context.Context, in bridge.Inbound) (reply string)

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
func (s *Service) cmdAbort(ctx context.Context, in bridge.Inbound) string {
	binding, err := s.resolveBinding(ctx, in.Peer)
	if err != nil {
		return "Failed to resolve binding: " + err.Error()
	}
	activeAgent := s.app.ActiveAgent()
	if activeAgent == nil {
		return "No active agent — nothing to abort."
	}
	if !activeAgent.IsSessionBusy(binding.SessionID) {
		return fmt.Sprintf("Session %s is not running anything to abort.",
			shortSessionID(binding.SessionID))
	}
	activeAgent.Cancel(binding.SessionID)
	return fmt.Sprintf("Aborted in-flight run on session %s. Send another message to continue.",
		shortSessionID(binding.SessionID))
}

// cmdAgent: list available agents or switch the active one.
// "/agent" (no args) → reply with the available list.
// "/agent coder" → switch active agent to "coder".
func (s *Service) cmdAgent(_ context.Context, in bridge.Inbound) string {
	args := strings.TrimSpace(in.CommandArgs)
	if args == "" {
		// List primary agents.
		names := s.app.PrimaryAgentKeys
		if len(names) == 0 {
			return "No primary agents registered."
		}
		out := make([]string, len(names))
		for i, n := range names {
			marker := ""
			if n == s.app.ActiveAgentName() {
				marker = " (active)"
			}
			out[i] = fmt.Sprintf("- %s%s", n, marker)
		}
		return "Available agents:\n" + strings.Join(out, "\n")
	}
	target := config.AgentName(args)
	if err := s.app.SetActiveAgent(target); err != nil {
		return fmt.Sprintf("Failed to set agent %q: %v", args, err)
	}
	return fmt.Sprintf("Active agent: %s", args)
}

// cmdModel: list available models or switch the active agent's model.
// "/model" → list supported models for the active agent's provider.
// "/model <id>" → call config.UpdateAgentModel for the active agent.
func (s *Service) cmdModel(_ context.Context, in bridge.Inbound) string {
	args := strings.TrimSpace(in.CommandArgs)
	if args == "" {
		// Compact listing of supported model IDs.
		ids := make([]string, 0, len(models.SupportedModels))
		for id := range models.SupportedModels {
			ids = append(ids, string(id))
		}
		sort.Strings(ids)
		return "Models:\n" + strings.Join(ids, "\n")
	}
	id := models.ModelID(args)
	if _, ok := models.SupportedModels[id]; !ok {
		return fmt.Sprintf("Unknown model %q", args)
	}
	active := s.app.ActiveAgentName()
	if err := config.UpdateAgentModel(active, id); err != nil {
		return fmt.Sprintf("Failed to update model: %v", err)
	}
	return fmt.Sprintf("Active model: %s (agent %s)", args, active)
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
func (s *Service) cmdSessions(ctx context.Context, in bridge.Inbound) string {
	sessions, err := s.app.Sessions.List(ctx)
	if err != nil {
		return "Failed to list sessions: " + err.Error()
	}
	if len(sessions) == 0 {
		return "No sessions yet."
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
		return "No sessions yet."
	}
	now := time.Now().UnixMilli()
	if len(sessions) > 10 {
		sessions = sessions[:10]
	}
	lines := make([]string, 0, len(sessions))
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
		// to UpdatedAt, not CreatedAt.
		lines = append(lines, fmt.Sprintf("%s%s · %s · %s tokens · updated %s",
			marker, shortSessionID(sess.ID), sess.Title,
			formatTokens(sess.PromptTokens+sess.CompletionTokens),
			formatRelativeAge(sess.UpdatedAt*1000, now),
		))
	}
	return "Recent sessions (★ = current):\n" + strings.Join(lines, "\n") +
		"\n\nSwitch with `/session <id-prefix>`."
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
func (s *Service) cmdSession(ctx context.Context, in bridge.Inbound) string {
	args := strings.TrimSpace(in.CommandArgs)
	binding, err := s.resolveBinding(ctx, in.Peer)
	if err != nil {
		return "Failed to resolve binding: " + err.Error()
	}

	if args == "" {
		sess, err := s.app.Sessions.Get(ctx, binding.SessionID)
		if err != nil {
			return "Failed to get session: " + err.Error()
		}
		// Tokens: PromptTokens+CompletionTokens is the conversation's
		// net usage matching the local TUI; TotalPromptTokens et al
		// aggregate every API call including cached context replay
		// and inflate the number by ~10x.
		return fmt.Sprintf("★ Session %s\nTitle: %s\nTokens: %s in / %s out\nMessages: %d\n\nUse `/sessions` to list, `/session <id-prefix>` to switch.",
			shortSessionID(sess.ID), sess.Title,
			formatTokens(sess.PromptTokens),
			formatTokens(sess.CompletionTokens),
			sess.MessageCount,
		)
	}

	// Switch path. Resolve the prefix.
	sessions, err := s.app.Sessions.List(ctx)
	if err != nil {
		return "Failed to list sessions: " + err.Error()
	}
	var matches []sessionMatch
	for _, sess := range sessions {
		if strings.HasPrefix(sess.ID, args) {
			matches = append(matches, sessionMatch{ID: sess.ID, Title: sess.Title})
		}
	}
	switch len(matches) {
	case 0:
		return fmt.Sprintf("No session matches prefix %q. Use `/sessions` to see recent IDs.", args)
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
		return fmt.Sprintf("Prefix %q is ambiguous — %d matches:\n%s%s",
			args, len(matches), strings.Join(lines, "\n"), footer)
	}
	target := matches[0]
	if target.ID == binding.SessionID {
		return fmt.Sprintf("Already on session %s · %s.", shortSessionID(target.ID), target.Title)
	}

	// Refuse mid-run on either side. Tells the user explicitly rather
	// than silently splitting traffic.
	activeAgent := s.app.ActiveAgent()
	if activeAgent != nil {
		if activeAgent.IsSessionBusy(binding.SessionID) {
			return fmt.Sprintf("Current session %s is mid-run — wait for it to finish or `/reset` first.",
				shortSessionID(binding.SessionID))
		}
		if activeAgent.IsSessionBusy(target.ID) {
			return fmt.Sprintf("Target session %s is mid-run — try again in a moment.",
				shortSessionID(target.ID))
		}
	}

	if err := s.store.UpdateBindingSessionID(ctx, s.projectID,
		in.Peer.Channel, in.Peer.Identity, in.Peer.PeerID, target.ID); err != nil {
		return "Failed to switch session: " + err.Error()
	}
	// Ensure the dispatcher for the new session is up so the next
	// inbound routes without a cold-start delay. The old session's
	// dispatcher is left alone — closeDispatcherIfEmpty would tear it
	// down later if no other peers point at it.
	_ = s.dispatcherFor(target.ID)

	return fmt.Sprintf("Switched to session %s · %s. Next message routes there.",
		shortSessionID(target.ID), target.Title)
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
func (s *Service) cmdReset(ctx context.Context, in bridge.Inbound) string {
	// Look up the existing binding directly (no resolveBinding — that
	// would auto-allocate a fresh session for a missing row).
	existing, _ := s.store.GetBinding(ctx, s.projectID,
		in.Peer.Channel, in.Peer.Identity, in.Peer.PeerID)
	if err := s.store.DeleteBindingByPeer(
		ctx, s.projectID, in.Peer.Channel, in.Peer.Identity, in.Peer.PeerID,
	); err != nil {
		return "Failed to reset binding: " + err.Error()
	}
	if existing.SessionID != "" {
		s.closeDispatcherIfEmpty(ctx, existing.SessionID)
	}
	return "Session reset; next message starts fresh."
}

// cmdPair: surface the pairing-code allowlist mechanism for the
// inbound's peer. Phase 4 (Telegram adapter) provides the
// pairing-code-hash check; this command is a no-op stub until then.
func (s *Service) cmdPair(_ context.Context, _ bridge.Inbound) string {
	return "Pairing is configured via .opencode.json router.channels.telegram.bots[].pairingCodeHash"
}

// cmdSkip: skip the current pending agent question (if any). Phase 3.7
// (question protocol) wires this; for now the stub explains.
func (s *Service) cmdSkip(_ context.Context, _ bridge.Inbound) string {
	return "No pending question to skip."
}

// cmdDir: workspace switching is NOT supported in the Go bridge — one
// opencode process = one workspace, pinned to config.WorkingDirectory().
// This command is intercepted only to give the user a clear "unsupported"
// reply rather than letting it fall through to the agent prompt.
func (s *Service) cmdDir(_ context.Context, _ bridge.Inbound) string {
	return "Workspace switching is not supported in this deployment. Restart opencode in the desired directory."
}

// cmdHelp: list every available command.
func (s *Service) cmdHelp(_ context.Context, _ bridge.Inbound) string {
	cmds := []string{
		"/agent [name]      list or switch the active agent",
		"/model [id]        list or switch the model",
		"/sessions             list recent sessions (★ = current)",
		"/session              show details for the current session",
		"/session <id-prefix>  switch to another session by ID prefix",
		"/reset             forget this binding; next message starts fresh",
		"/abort             cancel an in-flight run on the current session (use when stuck)",
		"/pair              pairing-code information",
		"/skip              skip the current pending question",
		"/help              this listing",
		"/dir               (not supported — opencode is pinned to one workspace)",
	}
	return strings.Join(cmds, "\n")
}
