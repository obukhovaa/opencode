package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/opencode-ai/opencode/internal/logging"
)

// Registry is the agent-visible entry point. It defers to a host-supplied
// getter to fetch the current hooks map — in production this returns the
// `hooks` block from `config.Get()`, decoupling the registry from any
// specific config loader (and avoiding an internal/hooks ↔ internal/config
// import cycle).
//
// Hooks are read from the getter on every event fire. The getter is
// expected to be cheap (a map read off the in-memory config); fresh reads
// on every event are kept so a future "live config reload" change can be
// implemented purely inside the config layer without touching the
// registry's contract.
type Registry struct {
	getHooks    func() map[string][]MatcherGroup
	projectRoot string
}

// NewRegistry builds a Registry whose hooks come from the supplied
// getter. A nil getter (or a getter returning a nil map) is the explicit
// signal that hooks are disabled — RunPreTool / RunPostTool return zero
// decisions immediately.
//
// projectRoot is set as `OPENCODE_PROJECT_DIR` / `CLAUDE_PROJECT_DIR`
// for spawned hook subprocesses and used as their working directory.
func NewRegistry(getHooks func() map[string][]MatcherGroup, projectRoot string) *Registry {
	return &Registry{getHooks: getHooks, projectRoot: projectRoot}
}

// ProjectRoot returns the project root the registry uses for hook
// subprocess `cwd` + the OPENCODE_PROJECT_DIR / CLAUDE_PROJECT_DIR env
// vars. The agent loop uses this as a fallback when `os.Getwd()` fails
// (e.g. the agent's working directory was unlinked mid-session).
func (r *Registry) ProjectRoot() string {
	if r == nil {
		return ""
	}
	return r.projectRoot
}

// HasEvent reports whether any matcher groups are configured for the
// given event. Cheap (one map read) and side-effect-free; used by the
// agent's hot path to short-circuit JSON parsing and os.Getwd() work
// when no hooks would fire anyway. The common case for most users is
// "no hooks configured at all", so this saves a few µs per tool call
// in the default deployment.
func (r *Registry) HasEvent(event string) bool {
	return len(r.loadGroups(event)) > 0
}

// loadGroups invokes the getter and returns the matcher groups for the
// requested event. Returns nil if the getter is unset or the requested
// event has no entries.
//
// Lookup is case-insensitive on the event key. This matters because
// viper (the config loader powering `.opencode.json`) lowercases all
// map keys during JSON ingestion — so a user who writes the canonical
// "PreToolUse" event name in their config ends up with `"pretooluse"`
// in `Config.Hooks` after viper.Unmarshal. A canonical-case lookup
// would miss the entry entirely. We fold both sides at lookup time so
// hooks fire regardless of how the config layer canonicalized keys.
func (r *Registry) loadGroups(event string) []MatcherGroup {
	if r == nil || r.getHooks == nil {
		return nil
	}
	all := r.getHooks()
	if len(all) == 0 {
		return nil
	}
	if g, ok := all[event]; ok {
		return g
	}
	target := strings.ToLower(event)
	for k, v := range all {
		if strings.ToLower(k) == target {
			return v
		}
	}
	return nil
}

// RunPreTool fires the PreToolUse event for the given tool call and
// returns the aggregated decision.
//
// Precedence (per design.md::D6):
//   - Hooks run in declaration order (user-scope first, project after,
//     local after, plus each group's hooks list in array order).
//   - `updatedInput` chains: each hook receives the previous hook's
//     updatedInput as its `tool_input`. The final updated value is what
//     the runtime applies to the tool call.
//   - `permissionDecision: "deny"` (or exit 2) sets Block=true with the
//     FIRST hook's reason. Later hooks still run (so their additional
//     context / updated input is captured for logging), but Block wins.
//   - `permissionDecision: "allow"` sets ExplicitAllow=true UNLESS a
//     subsequent hook denies — deny always wins.
//   - Non-blocking errors (spawn failure, timeout, JSON parse failure,
//     other non-zero exit) are logged at WARN and the chain proceeds.
func (r *Registry) RunPreTool(ctx context.Context, sessionID, cwd, toolName string, toolInput map[string]any) PreToolDecision {
	groups := r.loadGroups(EventPreToolUse)
	if len(groups) == 0 {
		return PreToolDecision{}
	}
	matched := matchEntries(groups, toolName)
	if len(matched) == 0 {
		return PreToolDecision{}
	}

	// chainInput is the rolling tool_input that gets handed to each hook.
	// It MAY mutate across iterations as hooks return `updatedInput`.
	chainInput := cloneMap(toolInput)
	decision := PreToolDecision{}
	var ctxBuf []string

	for _, entry := range matched {
		payload := PreToolUseInput{
			SessionID:     sessionID,
			CWD:           cwd,
			HookEventName: EventPreToolUse,
			ToolName:      toolName,
			ToolInput:     chainInput,
		}
		out, ok := r.dispatch(ctx, entry, payload, EventPreToolUse, toolName)
		if !ok {
			continue
		}
		// PreToolUse: apply this hook's decision before passing to the next.
		if out != nil && out.HookSpecificOutput != nil {
			hso := out.HookSpecificOutput
			if hso.UpdatedInput != nil {
				chainInput = hso.UpdatedInput
				decision.UpdatedInput = hso.UpdatedInput
			}
			switch strings.ToLower(hso.PermissionDecision) {
			case "deny":
				if !decision.Block {
					decision.Block = true
					decision.BlockReason = firstNonEmpty(hso.PermissionDecisionReason, "Tool call denied by PreToolUse hook")
					decision.ExplicitAllow = false
				}
			case "allow":
				if !decision.Block {
					decision.ExplicitAllow = true
				}
			}
			if hso.AdditionalContext != "" {
				ctxBuf = append(ctxBuf, hso.AdditionalContext)
			}
		}
	}

	// Exit-2 blocks are surfaced separately via dispatch's return path —
	// see the inline branch there. Decision was already updated; nothing
	// to do here beyond joining the context buffer.
	if len(ctxBuf) > 0 {
		decision.AdditionalContext = strings.Join(ctxBuf, "\n")
	}
	return decision
}

// RunPostTool fires the PostToolUse event after a tool's Run returned
// successfully (toolErr == nil). On error tools, callers MUST NOT invoke
// this — the spec is explicit that PostToolUse only fires on success.
//
// PostToolUse has no allow/deny — only output replacement and context.
// Exit-2 "block" semantics for PostToolUse mean "replace output with stderr".
func (r *Registry) RunPostTool(ctx context.Context, sessionID, cwd, toolName string, toolInput map[string]any, toolOutput string) PostToolDecision {
	groups := r.loadGroups(EventPostToolUse)
	if len(groups) == 0 {
		return PostToolDecision{}
	}
	matched := matchEntries(groups, toolName)
	if len(matched) == 0 {
		return PostToolDecision{}
	}

	chainOutput := toolOutput
	decision := PostToolDecision{}
	var ctxBuf []string

	for _, entry := range matched {
		payload := PostToolUseInput{
			SessionID:     sessionID,
			CWD:           cwd,
			HookEventName: EventPostToolUse,
			ToolName:      toolName,
			ToolInput:     toolInput,
			ToolOutput:    chainOutput,
		}
		out, ok := r.dispatchPost(ctx, entry, payload, toolName, &decision)
		if !ok {
			continue
		}
		if out != nil && out.HookSpecificOutput != nil {
			hso := out.HookSpecificOutput
			// nil means "field absent" (no change); a non-nil pointer
			// even to "" is an explicit replacement — lets hooks fully
			// suppress noisy output by emitting `"updatedToolOutput": ""`.
			if hso.UpdatedToolOutput != nil {
				chainOutput = *hso.UpdatedToolOutput
				updated := *hso.UpdatedToolOutput
				decision.UpdatedOutput = &updated
			}
			if hso.AdditionalContext != "" {
				ctxBuf = append(ctxBuf, hso.AdditionalContext)
			}
		}
	}
	if len(ctxBuf) > 0 {
		decision.AdditionalContext = strings.Join(ctxBuf, "\n")
	}
	return decision
}

// dispatch is the PreToolUse subprocess wrapper. Returns the parsed
// HookOutput (may be nil if no JSON was emitted but exit 0), and a bool
// that's false when the hook fired but its result was non-actionable
// (errored, timed out, etc — already logged). The caller passes the
// returned HookOutput through its precedence rules.
func (r *Registry) dispatch(ctx context.Context, entry HookEntry, payload PreToolUseInput, event, toolName string) (*HookOutput, bool) {
	if entry.Type != "" && entry.Type != "command" {
		logging.Warn("hook type not supported in v1; skipping",
			"event", event, "tool", toolName, "type", entry.Type)
		return nil, false
	}
	data, err := json.Marshal(payload)
	if err != nil {
		logging.Warn("hook payload marshal failed", "event", event, "tool", toolName, "error", err)
		return nil, false
	}
	res := runHook(ctx, entry, data, r.projectRoot)
	return r.applyExit(event, toolName, entry, res, nil)
}

// dispatchPost runs the PostToolUse subprocess and applies exit-2 block
// semantics directly to the decision (PostToolUse block = replace output
// with stderr, distinct from PreToolUse block = refuse to run tool).
func (r *Registry) dispatchPost(ctx context.Context, entry HookEntry, payload PostToolUseInput, toolName string, decision *PostToolDecision) (*HookOutput, bool) {
	if entry.Type != "" && entry.Type != "command" {
		logging.Warn("hook type not supported in v1; skipping",
			"event", EventPostToolUse, "tool", toolName, "type", entry.Type)
		return nil, false
	}
	data, err := json.Marshal(payload)
	if err != nil {
		logging.Warn("hook payload marshal failed", "event", EventPostToolUse, "tool", toolName, "error", err)
		return nil, false
	}
	res := runHook(ctx, entry, data, r.projectRoot)
	return r.applyExit(EventPostToolUse, toolName, entry, res, decision)
}

// applyExit interprets the runner Result against Claude Code's documented
// exit-code semantics. Returns the parsed HookOutput (nil if no actionable
// JSON), and a bool that's false when the call was a non-blocking error
// (caller skips applying any decision but proceeds to next hook).
//
// For PostToolUse, postDecision is non-nil and exit 2 directly sets
// BlockReason on it (since PreToolUse's "block tool" semantic doesn't
// apply — instead we replace output with stderr).
func (r *Registry) applyExit(event, toolName string, entry HookEntry, res Result, postDecision *PostToolDecision) (*HookOutput, bool) {
	cmd := entry.Command
	switch {
	case res.Err != nil && !errors.Is(res.Err, errTimeout):
		logging.Warn("hook spawn or wait error", "event", event, "tool", toolName, "command", cmd,
			"error", res.Err, "stderr", truncForLog(res.Stderr))
		return nil, false
	case res.Timeout:
		logging.Warn("hook timed out", "event", event, "tool", toolName, "command", cmd,
			"duration_ms", res.Duration.Milliseconds())
		return nil, false
	case res.ExitCode == 2:
		stderrText := strings.TrimSpace(string(res.Stderr))
		if stderrText == "" {
			stderrText = "Tool call blocked by hook (exit 2 without stderr)"
		}
		logging.Info("hook blocked tool call", "event", event, "tool", toolName, "command", cmd,
			"stderr", truncForLog(res.Stderr))
		// For PreToolUse the caller needs to set Block via a synthesized
		// HookOutput. For PostToolUse we set BlockReason on the decision
		// directly. Guard the assignment so the FIRST exit-2 hook wins
		// (matching PreToolUse's first-deny-wins precedence in
		// RunPreTool); without the guard a later exit-2 hook would
		// silently overwrite the earlier block reason.
		if postDecision != nil {
			if postDecision.BlockReason == "" {
				postDecision.BlockReason = stderrText
			}
			return nil, false // no JSON to parse; decision already applied
		}
		return &HookOutput{
			HookSpecificOutput: &HookSpecificOutput{
				HookEventName:            event,
				PermissionDecision:       "deny",
				PermissionDecisionReason: stderrText,
			},
		}, true
	case res.ExitCode != 0:
		logging.Warn("hook non-zero exit (non-blocking)", "event", event, "tool", toolName,
			"command", cmd, "exit", res.ExitCode, "stderr", truncForLog(res.Stderr))
		return nil, false
	}
	// Exit 0: parse stdout if present. Empty stdout = no decision; not an error.
	if res.TruncatedStdout {
		logging.Warn("hook stdout exceeded cap; result may be incomplete",
			"event", event, "tool", toolName, "command", cmd)
	}
	if len(bytesTrim(res.Stdout)) == 0 {
		logging.Debug("hook completed with no decision", "event", event, "tool", toolName, "command", cmd,
			"duration_ms", res.Duration.Milliseconds())
		return nil, true
	}
	// UseNumber so a hook returning a numeric tool_input field (IDs,
	// timestamps) doesn't get downcast to float64 — which would silently
	// mangle integers past 2^53 when re-serialized into the next hook's
	// stdin or written through to the tool's actual input.
	var parsed HookOutput
	dec := json.NewDecoder(bytes.NewReader(res.Stdout))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		logging.Warn("hook stdout not valid JSON; ignoring",
			"event", event, "tool", toolName, "command", cmd, "error", err)
		return nil, false
	}
	logging.Debug("hook completed", "event", event, "tool", toolName, "command", cmd,
		"duration_ms", res.Duration.Milliseconds())
	return &parsed, true
}

// matchEntries walks every matcher group, compiles each matcher, and
// returns the flat slice of HookEntry that should fire for `toolName`.
// Bad matchers are skipped with a WARN — one malformed pattern shouldn't
// disable the rest.
func matchEntries(groups []MatcherGroup, toolName string) []HookEntry {
	var out []HookEntry
	for _, g := range groups {
		m, err := CompileMatcher(g.Matcher)
		if err != nil {
			logging.Warn("hook matcher invalid; skipping group",
				"matcher", g.Matcher, "error", err.Error())
			continue
		}
		if !m(toolName) {
			continue
		}
		out = append(out, g.Hooks...)
	}
	return out
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	// Round-trip through JSON to deep-clone — hook payloads are JSON anyway,
	// and a deep clone here prevents accidental mutation if a tool's caller
	// holds the input map by reference past the dispatch.
	//
	// Decoder.UseNumber preserves integer precision past 2^53 (the float64
	// safe-integer limit). Without it, a 64-bit ID or millisecond timestamp
	// passing through a no-op hook gets silently mangled by JSON's default
	// number → float64 coercion. The downstream re-serialization
	// (`json.Marshal(map)`) emits json.Number values back as their original
	// numeric literals, so the round-trip is lossless.
	data, err := json.Marshal(m)
	if err != nil {
		return m // fall back; not worth crashing the agent for a clone failure
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return m
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// bytesTrim trims leading/trailing whitespace from a byte slice without
// allocating a new string — used to detect "exit 0 + empty stdout".
func bytesTrim(b []byte) []byte {
	start := 0
	for start < len(b) && isASCIIWhitespace(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isASCIIWhitespace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isASCIIWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func truncForLog(b []byte) string {
	const maxLen = 256
	if len(b) <= maxLen {
		return string(b)
	}
	return fmt.Sprintf("%s…(+%d bytes)", b[:maxLen], len(b)-maxLen)
}
