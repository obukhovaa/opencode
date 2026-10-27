package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"mvdan.cc/sh/v3/syntax"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
)

type ToolInfo struct {
	Name        string
	Description string
	Parameters  map[string]any
	Required    []string
	// TODO: Consider to add Output parameters: https://modelcontextprotocol.io/specification/2025-06-18/server/tools#output-schema
}

type toolResponseType string

type (
	sessionIDContextKey         string
	messageIDContextKey         string
	isTaskAgentContextKey       string
	agentIDContextKey           string
	metadataTagsContextKey      string
	flowIDContextKey            string
	flowStepIDContextKey        string
	flowStepIterationContextKey string
	flowArgsContextKey          string
	nonInteractiveContextKey    string
	stepScopedContextKey        string
	requesterContextKey         string
	parentSessionIDContextKey   string
	peerContextKey              string
	jobArgsContextKey           string
)

const (
	ToolResponseTypeText  toolResponseType = "text"
	ToolResponseTypeImage toolResponseType = "image"

	SessionIDContextKey         sessionIDContextKey         = "session_id"
	MessageIDContextKey         messageIDContextKey         = "message_id"
	IsTaskAgentContextKey       isTaskAgentContextKey       = "is_task_agent"
	AgentIDContextKey           agentIDContextKey           = "agent_id"
	MetadataTagsContextKey      metadataTagsContextKey      = "metadata_tags"
	FlowIDContextKey            flowIDContextKey            = "flow_id"
	FlowStepIDContextKey        flowStepIDContextKey        = "flow_step_id"
	FlowStepIterationContextKey flowStepIterationContextKey = "flow_step_iteration"
	FlowArgsContextKey          flowArgsContextKey          = "flow_args"
	// NonInteractiveContextKey marks the tool-execution ctx of a
	// RunOptions{NonInteractive: true} agent run (flow step, headless
	// prompt, ACP one-shot). Set by agent.processGeneration alongside
	// SessionIDContextKey; runtime-only, never persisted. The bash tool
	// reads it to redirect foreground wall-clock waits to the background-
	// task wait (see openspec bash-background-mode spec).
	NonInteractiveContextKey nonInteractiveContextKey = "non_interactive"
	// StepScopedContextKey carries the step-scoped base context installed
	// by the flow runner: a context that survives any single agent turn
	// ending but is cancelled when the step's deadline elapses or the step
	// completes. Detached async subagents derive their run context from it
	// instead of context.Background() so a timed-out step cancels them
	// (see openspec flow-runtime-resume / task-async-mode specs).
	StepScopedContextKey stepScopedContextKey = "step_scoped_ctx"
	// RequesterContextKey carries the identity of the human a run is
	// working for (an email when it could be resolved, else the platform
	// user id). Set per turn by the chat bridge from the inbound message's
	// author, by the cron scheduler from the job's stored requester, and
	// on an auto-resumed turn from the completed task's Task.Requester;
	// read by telemetry to stamp the trace's `requester` metadata. Use
	// WithRequester / RequesterFromContext.
	RequesterContextKey requesterContextKey = "requester"
	// ParentSessionIDContextKey carries the PARENT of the session a tool
	// runs in — set by agent.RunWith from session.ParentSessionID when the
	// run's session is itself a subagent / task session, absent for a
	// top-level session. Registration sites stamp it onto task.Task so the
	// registry's session-and-children scope (the foreground-wait redirect,
	// tasklist, taskstop) can match a child's task from the parent. It is
	// the parent, never RootSessionID: a flow shares one root across every
	// step, including parallel siblings (openspec background-tasks).
	ParentSessionIDContextKey parentSessionIDContextKey = "parent_session_id"
	// PeerContextKey carries the chat-bridge peer a turn is serving — the
	// peer the bridge resolved the inbound message's session from, taken
	// from the authenticated inbound request and never from the model or
	// the message text. Set per turn by the chat bridge; carried onto async
	// subagents. Read by the agent (peer attribution for external peers)
	// and by the MCP client (a server's `peerHeader`). Use WithPeer /
	// PeerFromContext.
	PeerContextKey peerContextKey = "bridge_peer"
	// JobArgsContextKey carries the top-level args of the job the turn
	// belongs to, exactly as the orchestrator supplied them (POST /flow
	// `args`, or --flow-args). Unlike FlowArgsContextKey it is neither
	// filtered by telemetry.flowArgs nor stringified, and it never reaches
	// telemetry: its only reader is the MCP client, which stamps the
	// `flowArgHeaders` of a server. Set by the flow runner, and carried onto
	// subagents, bridge turns and auto-resumed turns of the job. Use
	// WithJobArgs / JobArgsFromContext.
	JobArgsContextKey jobArgsContextKey = "job_args"

	// MaxToolResponseTokens is the maximum number of tokens allowed in a tool response
	// to prevent context overflow. ~1200KB of text content.
	MaxToolResponseTokens = 300_000
)

// WithRequester returns ctx carrying requester under RequesterContextKey.
// An empty requester returns ctx unchanged, so an unresolvable author never
// masks a requester already on the context.
func WithRequester(ctx context.Context, requester string) context.Context {
	if requester == "" {
		return ctx
	}
	return context.WithValue(ctx, RequesterContextKey, requester)
}

// RequesterFromContext returns the requester carried by ctx, or "".
func RequesterFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	r, _ := ctx.Value(RequesterContextKey).(string)
	return r
}

// Peer is the chat-bridge peer a turn serves: the channel it came in on,
// the bridge identity that received it, and the peer id within that
// channel. Mirrors bridge.PeerRef without importing the bridge.
type Peer struct {
	Channel  string
	Identity string
	PeerID   string
}

// WithPeer returns ctx carrying peer under PeerContextKey. A peer with no
// id returns ctx unchanged.
func WithPeer(ctx context.Context, peer Peer) context.Context {
	if peer.PeerID == "" {
		return ctx
	}
	return context.WithValue(ctx, PeerContextKey, peer)
}

// PeerFromContext returns the peer carried by ctx, if any.
func PeerFromContext(ctx context.Context) (Peer, bool) {
	if ctx == nil {
		return Peer{}, false
	}
	p, ok := ctx.Value(PeerContextKey).(Peer)
	return p, ok
}

// WithJobArgs returns ctx carrying a copy of args under JobArgsContextKey.
// No args returns ctx unchanged.
func WithJobArgs(ctx context.Context, args map[string]any) context.Context {
	if len(args) == 0 {
		return ctx
	}
	cp := make(map[string]any, len(args))
	for k, v := range args {
		cp[k] = v
	}
	return context.WithValue(ctx, JobArgsContextKey, cp)
}

// JobArgsFromContext returns the job args carried by ctx, if any. The map is
// shared: callers must not modify it.
func JobArgsFromContext(ctx context.Context) (map[string]any, bool) {
	if ctx == nil {
		return nil, false
	}
	a, ok := ctx.Value(JobArgsContextKey).(map[string]any)
	return a, ok
}

func jobArgsOrNil(ctx context.Context) map[string]any {
	a, _ := JobArgsFromContext(ctx)
	return a
}

type processJob struct {
	id   string
	args map[string]any
}

var processJobHolder atomic.Pointer[processJob]

// SetProcessJobArgs publishes the args of the job this process is currently
// running, together with the bridge job identity they belong to, for turns
// that do not descend from the flow run's context — chat bridge turns. Nil
// or empty args clear it. The flow runner is its only writer.
func SetProcessJobArgs(jobID string, args map[string]any) {
	if len(args) == 0 {
		processJobHolder.Store(nil)
		return
	}
	cp := make(map[string]any, len(args))
	for k, v := range args {
		cp[k] = v
	}
	processJobHolder.Store(&processJob{id: jobID, args: cp})
}

// ProcessJobArgs returns the args published by SetProcessJobArgs, or nil.
// Whoever attaches them to a turn must first prove the turn belongs to the
// job — see ProcessJobArgsFor.
func ProcessJobArgs() map[string]any {
	if p := processJobHolder.Load(); p != nil {
		return p.args
	}
	return nil
}

// ProcessJobArgsFor returns the published args only when they were
// published for jobID. An empty jobID, or a holder published without an
// identity, never matches: a turn that cannot be tied to the job gets none.
func ProcessJobArgsFor(jobID string) map[string]any {
	if jobID == "" {
		return nil
	}
	if p := processJobHolder.Load(); p != nil && p.id == jobID {
		return p.args
	}
	return nil
}

// ParentSessionIDFromContext returns the parent of the current session
// (see ParentSessionIDContextKey), or "" for a top-level session.
func ParentSessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	p, _ := ctx.Value(ParentSessionIDContextKey).(string)
	return p
}

// TurnRequester returns who the turn on ctx works for, in the order its
// trace metadata resolves it: a non-blank `requester` flow arg (on a flow
// step's ctx when telemetry.flowArgs extracts it), else the per-turn
// requester. Use it to carry attribution onto work that outlives the turn
// (title generation, a cron job) and so loses the flow args. Does not
// apply the telemetry.requester fallback; the trace does that itself.
func TurnRequester(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if args, ok := ctx.Value(FlowArgsContextKey).(map[string]string); ok && strings.TrimSpace(args["requester"]) != "" {
		return args["requester"]
	}
	return RequesterFromContext(ctx)
}

type toolResponse struct {
	Type     toolResponseType `json:"type"`
	Content  string           `json:"content"`
	Metadata string           `json:"metadata,omitempty"`
	IsError  bool             `json:"is_error"`
}

// ToolResponse is the public interface for tool responses
type ToolResponse = toolResponse

// validateAndTruncate validates the tool response size and truncates if necessary.
// Truncation is line-aligned to avoid cutting mid-line or mid-UTF-8 character.
func validateAndTruncate(response toolResponse) toolResponse {
	// Rough estimation: ~4 characters per token
	estimatedTokens := len(response.Content) / 4

	if estimatedTokens > MaxToolResponseTokens {
		maxChars := MaxToolResponseTokens * 4
		truncated := truncateToMaxChars(response.Content, maxChars)
		response.Content = truncated + "\n\n[Output truncated due to size limit. Consider using more specific search parameters or viewing smaller sections.]"
	}

	return response
}

func NewTextResponse(content string) toolResponse {
	return validateAndTruncate(toolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
	})
}

func NewImageResponse(content string) toolResponse {
	return toolResponse{
		Type:    ToolResponseTypeImage,
		Content: content,
	}
}

func NewEmptyResponse() toolResponse {
	return toolResponse{
		Type:    ToolResponseTypeText,
		Content: "",
	}
}

func WithResponseMetadata(response toolResponse, metadata any) toolResponse {
	if metadata != nil {
		metadataBytes, err := json.Marshal(metadata)
		if err != nil {
			return response
		}
		response.Metadata = string(metadataBytes)
	}
	return response
}

func NewTextErrorResponse(content string) toolResponse {
	return validateAndTruncate(toolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
		IsError: true,
	})
}

type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

type BaseTool interface {
	Info() ToolInfo
	Run(ctx context.Context, params ToolCall) (ToolResponse, error)
	AllowParallelism(call ToolCall, allCalls []ToolCall) bool
	// IsBaseline reports whether this tool is a built-in tool. External tools
	// (e.g. MCP server tools) return false and are placed after baseline tools
	// in the tool list for cache prefix stability.
	IsBaseline() bool
}

var mutatingToolNames = map[string]bool{
	EditToolName:      true,
	WriteToolName:     true,
	MultiEditToolName: true,
	DeleteToolName:    true,
	PatchToolName:     true,
}

func IsMutatingTool(name string) bool {
	return mutatingToolNames[name]
}

func ExtractPathsFromCall(call ToolCall) []string {
	switch call.Name {
	case PatchToolName:
		var params struct {
			PatchText string `json:"patch_text"`
		}
		if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
			return nil
		}
		paths := diff.IdentifyFilesNeeded(params.PatchText)
		paths = append(paths, diff.IdentifyFilesAdded(params.PatchText)...)
		return paths
	case GlobToolName:
		// The search path AND the pattern's literal directory prefix: a
		// glob for services/auth/**/*.go declares intent to work under
		// services/auth even when no path argument narrows the search.
		// The pattern is matched relative to the search path, so the
		// prefix is joined onto it.
		var params struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
			return nil
		}
		var paths []string
		if params.Path != "" {
			paths = append(paths, params.Path)
		}
		if prefix := globLiteralDirPrefix(params.Pattern); prefix != "" {
			paths = append(paths, filepath.Join(params.Path, prefix))
		}
		return paths
	default:
		// A grep call with no path argument returns nothing here — it
		// searches the whole workspace and declares no directory intent
		// (the progressive-disclosure activation rule relies on this).
		var common struct {
			FilePath string `json:"file_path"`
			Path     string `json:"path"`
		}
		if err := json.Unmarshal([]byte(call.Input), &common); err != nil {
			return nil
		}
		var paths []string
		if common.FilePath != "" {
			paths = append(paths, common.FilePath)
		}
		if common.Path != "" {
			paths = append(paths, common.Path)
		}
		return paths
	}
}

// globLiteralDirPrefix returns the directory portion of a glob pattern
// that precedes its first metacharacter: "services/auth/**/*.go" yields
// "services/auth", "*.go" yields "". A pattern with no metacharacters is
// a literal file path, so its dirname is the prefix.
func globLiteralDirPrefix(pattern string) string {
	literal := pattern
	if i := strings.IndexAny(pattern, "*?[{"); i >= 0 {
		literal = pattern[:i]
	}
	if j := strings.LastIndex(literal, "/"); j > 0 {
		return literal[:j]
	}
	return ""
}

// ExtractTargetDirsFromCall resolves a trigger tool's call to the absolute
// directories it works within, per the progressive-context-disclosure
// activation table (design D8): file-taking tools map file_path to its
// parent, ls takes its path as-is, and grep/glob take the path itself when
// it is a directory or its parent when it names an existing file. Relative
// paths are joined onto workDir. Non-trigger tools return nil.
func ExtractTargetDirsFromCall(call ToolCall, workDir string) []string {
	paths := ExtractPathsFromCall(call)
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	dirs := make([]string, 0, len(paths))
	add := func(d string) {
		if _, dup := seen[d]; dup {
			return
		}
		seen[d] = struct{}{}
		dirs = append(dirs, d)
	}
	for _, p := range paths {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(workDir, abs)
		}
		abs = filepath.Clean(abs)
		switch call.Name {
		case ReadToolName, WriteToolName, EditToolName, MultiEditToolName, PatchToolName:
			add(filepath.Dir(abs))
		case LSToolName:
			add(abs)
		case GrepToolName, GlobToolName:
			if info, err := os.Stat(abs); err == nil && !info.IsDir() {
				add(filepath.Dir(abs))
			} else {
				add(abs)
			}
		default:
			return nil
		}
	}
	return dirs
}

func hasFileConflict(call ToolCall, myPaths []string, allCalls []ToolCall) bool {
	for _, other := range allCalls {
		if other.ID == call.ID {
			continue
		}
		if !IsMutatingTool(other.Name) {
			continue
		}
		otherPaths := ExtractPathsFromCall(other)
		for _, mp := range myPaths {
			if slices.Contains(otherPaths, mp) {
				return true
			}
		}
	}
	return false
}

// IsSafeReadOnlyCommand reports whether command may skip the bash
// permission gate. The exemption covers only a SIMPLE command led by a
// listed word: anything with a top-level control operator, redirect,
// command substitution or subshell is evaluated like every other command,
// because the prefix match alone would exempt `echo go; ./gradlew build &`
// or `ls && rm -rf build` on the strength of their first word (openspec
// bash-background-mode: "The permission-exempt safe list covers only
// simple commands"). Nor may it run another command (CD-5487): no listed
// entry is a wrapper, and an expansion bash executes (`${x@P}`, arithmetic
// over a variable, see permission.BashHidesExecution) or an option that
// runs a program (safeCommandRunsProgram) sends the call through the gate.
func IsSafeReadOnlyCommand(command string) bool {
	if hasTopLevelCompound(command) {
		return false
	}
	args, ok := simpleCommandWords(command)
	if !ok {
		return false
	}
	cmdLower := strings.ToLower(command)
	for _, safe := range safeReadOnlyCommands {
		if strings.HasPrefix(cmdLower, strings.ToLower(safe)) {
			if len(cmdLower) == len(safe) || cmdLower[len(safe)] == ' ' || cmdLower[len(safe)] == '-' {
				return !safeCommandRunsProgram(safe, args)
			}
		}
	}
	return false
}

// simpleCommandWords parses command as one simple Bash command without
// substitutions or hidden execution and returns its words with quotes
// removed. A word that is not plain literal text (a parameter expansion)
// is returned as "$", which no refused option matches but which keeps the
// argument count honest.
func simpleCommandWords(command string) ([]string, bool) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return nil, false
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(stmt.Redirs) > 0 || stmt.Background || stmt.Coprocess || stmt.Negated {
		return nil, false
	}
	substitutes := false
	syntax.Walk(stmt, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			substitutes = true
		}
		return !substitutes
	})
	if substitutes || permission.BashHidesExecution(stmt) {
		return nil, false
	}
	words := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		lit := word.Lit()
		if lit == "" {
			lit = unquotedWord(word)
		}
		words = append(words, lit)
	}
	return words, true
}

func unquotedWord(word *syntax.Word) string {
	var b strings.Builder
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "$"
				}
				b.WriteString(lit.Value)
			}
		default:
			return "$"
		}
	}
	return b.String()
}

func GetContextValues(ctx context.Context) (string, string) {
	sessionID := ctx.Value(SessionIDContextKey)
	messageID := ctx.Value(MessageIDContextKey)
	if sessionID == nil {
		return "", ""
	}
	if messageID == nil {
		return sessionID.(string), ""
	}
	return sessionID.(string), messageID.(string)
}

// IsNonInteractive reports whether the tool-execution context belongs to a
// non-interactive agent run (RunOptions{NonInteractive: true}). Absent
// marker (interactive runs, tests that don't set it) returns false.
func IsNonInteractive(ctx context.Context) bool {
	v := ctx.Value(NonInteractiveContextKey)
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// StepScopedContext returns the step-scoped base context installed by the
// flow runner (see StepScopedContextKey), or nil when the caller did not
// install one (interactive runs, headless callers without a step scope).
func StepScopedContext(ctx context.Context) context.Context {
	v := ctx.Value(StepScopedContextKey)
	if v == nil {
		return nil
	}
	if c, ok := v.(context.Context); ok {
		return c
	}
	return nil
}

// IsTaskAgent returns true if the context indicates this is a task agent
func IsTaskAgent(ctx context.Context) bool {
	isTaskAgent := ctx.Value(IsTaskAgentContextKey)
	if isTaskAgent == nil {
		return false
	}
	if val, ok := isTaskAgent.(bool); ok {
		return val
	}
	return false
}

// GetAgentID returns the agent name from context, or empty string if not set
func GetAgentID(ctx context.Context) config.AgentName {
	agentName := ctx.Value(AgentIDContextKey)
	if agentName == nil {
		return ""
	}
	if val, ok := agentName.(config.AgentName); ok {
		return val
	}
	return ""
}

func AddTag(ctx context.Context, k, v string) context.Context {
	if k == "" || v == "" {
		logging.Warn(fmt.Sprintf("Bad tag provided %s:%s", k, v))
		return ctx
	}
	cfg := config.Get()
	if cfg != nil && cfg.Telemetry != nil && len(cfg.Telemetry.DefaultTags) > 0 {
		found := false
		for _, dt := range cfg.Telemetry.DefaultTags {
			if dt == k {
				found = true
				break
			}
		}
		if !found {
			return ctx
		}
	} else {
		return ctx
	}
	tag := fmt.Sprintf("%s:%s", k, v)
	if existing, ok := ctx.Value(MetadataTagsContextKey).([]string); ok {
		tags := make([]string, len(existing)+1)
		copy(tags, existing)
		tags[len(existing)] = tag
		return context.WithValue(ctx, MetadataTagsContextKey, tags)
	}
	return context.WithValue(ctx, MetadataTagsContextKey, []string{tag})
}

// canonicalReadPath makes path absolute against the working directory,
// cleans it (collapsing "//", "/./" and "/../") and resolves its symlinks.
// clean is the lexical result; resolved is the file the path really names, or
// err when it does not resolve.
func canonicalReadPath(path string) (clean, resolved string, err error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(config.WorkingDirectory(), path)
	}
	clean = filepath.Clean(path)
	resolved, err = filepath.EvalSymlinks(clean)
	return clean, resolved, err
}

// strictestAction returns the more restrictive of two actions: deny, then ask,
// then allow.
func strictestAction(a, b permission.Action) permission.Action {
	rank := func(x permission.Action) int {
		switch x {
		case permission.ActionDeny:
			return 2
		case permission.ActionAsk:
			return 1
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// authorizeRead gates a read-category tool on the canonical form of path and
// returns the path the tool must then operate on, so what was checked is what
// is read. The path is cleaned and its symlinks resolved first; a path that
// does not resolve is denied. The permission patterns are evaluated against
// both the cleaned and the resolved path and the stricter verdict wins, so
// neither a "..", a double slash nor a symlink out of an allowed directory
// reaches a file the patterns do not allow, and a rule written against a
// symlink's own name still applies.
//
// With no registry or permission service nothing is checked and path is
// returned unchanged.
//
// The check and the later open are two steps: a path component swapped for a
// symlink in between would be followed by the open. read and view_image close
// that window by opening through openForRead, which refuses a file whose real
// path is no longer the authorized one. ls, glob and grep only ever enumerate
// a directory (ripgrep and the walk open the files themselves), and
// grep/glob re-check every match's resolved path afterwards, but that is a
// second check-then-use of the same kind: a swap racing the enumeration is
// not detected there.
func authorizeRead(ctx context.Context, reg agentregistry.Registry, perms permission.Service, toolName, path string) (string, error) {
	if reg == nil || perms == nil {
		return path, nil
	}
	clean, resolved, err := canonicalReadPath(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A missing file is not a permission matter: canonicalization
			// only fails because there is nothing to resolve. Authorize the
			// lexical path and let the caller's existence check answer with
			// "file not found" (+ suggestions) instead of a misleading
			// permission denial. Read-only tools cannot leak through a
			// nonexistent target, so skipping the resolved-path check here
			// weakens nothing.
			if err := checkReadPermission(ctx, reg, perms, toolName, clean); err != nil {
				return "", err
			}
			return clean, nil
		}
		return "", permission.ErrorPermissionDenied
	}
	if err := checkReadPermission(ctx, reg, perms, toolName, clean, resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// checkReadPermission evaluates read-category permission for a tool and the
// given paths (the strictest verdict wins; an ask prompts once, for the last
// path). Returns nil if allowed, a permission.RuleDeniedError if a configured
// rule denied it (the agent loop reports that to the model and keeps the run
// going), and permission.ErrorPermissionDenied if the prompt was rejected. If
// registry is nil, permission check is skipped (always allowed). Callers holding a user-supplied path should go through
// authorizeRead, which canonicalizes it first.
func checkReadPermission(ctx context.Context, reg agentregistry.Registry, perms permission.Service, toolName string, paths ...string) error {
	if reg == nil || perms == nil || len(paths) == 0 {
		return nil
	}
	path := paths[len(paths)-1]
	action := permission.ActionAllow
	for _, p := range paths {
		action = strictestAction(action, reg.EvaluateReadPermission(string(GetAgentID(ctx)), toolName, p))
	}
	switch action {
	case permission.ActionAllow:
		return nil
	case permission.ActionDeny:
		// Name the path as the caller gave it, not its resolved target.
		return permission.DeniedByRule(toolName, paths[0])
	default: // ask
		sessionID, _ := GetContextValues(ctx)
		if sessionID == "" {
			return permission.ErrorPermissionDenied
		}
		if !perms.Request(ctx, permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        path,
			ToolName:    toolName,
			Action:      "read",
			Description: fmt.Sprintf("Read access: %s %s", toolName, path),
		}) {
			return permission.ErrorPermissionDenied
		}
		return nil
	}
}

// readableMatch returns a predicate that reports whether a search result under
// root may be shown: its symlinks must resolve, and the permission patterns
// must not deny the resolved path (nor the lexical one). An "ask" verdict
// cannot be put to the user once per match, so it stands for what was already
// approved for root: the match is kept only while it stays inside the resolved
// root. Nil when nothing is checked.
func readableMatch(ctx context.Context, reg agentregistry.Registry, perms permission.Service, toolName, root string) func(path string) bool {
	if reg == nil || perms == nil {
		return nil
	}
	agentID := string(GetAgentID(ctx))
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		rootResolved = root
	}
	prefix := strings.TrimSuffix(rootResolved, string(filepath.Separator)) + string(filepath.Separator)
	return func(path string) bool {
		resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
		if err != nil {
			return false
		}
		action := strictestAction(
			reg.EvaluateReadPermission(agentID, toolName, filepath.Clean(path)),
			reg.EvaluateReadPermission(agentID, toolName, resolved),
		)
		switch action {
		case permission.ActionAllow:
			return true
		case permission.ActionDeny:
			return false
		default:
			return resolved == rootResolved || strings.HasPrefix(resolved, prefix)
		}
	}
}

var errReadPathChanged = errors.New("path changed between the permission check and the open")

// openForRead opens path, which authorizeRead returned as the canonical form
// that was permission-checked. With verify set it then makes sure the opened
// file really is that path, so a symlink swapped in after the check is refused
// instead of followed. verify is false when authorizeRead checked nothing and
// path is whatever the caller supplied.
func openForRead(path string, verify bool) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if verify {
		if err := verifyOpenedPath(f, path); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

// readFileForRead is os.ReadFile through openForRead.
func readFileForRead(path string, verify bool) ([]byte, error) {
	f, err := openForRead(path, verify)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// verifyOpenedPath reports errReadPathChanged unless the open file f is the
// file at the canonical path want. Where the kernel can name the real path of
// a descriptor (/proc/self/fd/N) that is compared with want, which also covers
// a swapped parent directory. Elsewhere want must still resolve to itself and
// to the very file that was opened.
func verifyOpenedPath(f *os.File, want string) error {
	if got, ok := descriptorPath(f); ok {
		if got != want {
			return errReadPathChanged
		}
		return nil
	}
	return verifyOpenedPathByStat(f, want)
}

func descriptorPath(f *os.File) (string, bool) {
	rc, err := f.SyscallConn()
	if err != nil {
		return "", false
	}
	var got string
	var rerr error
	if err := rc.Control(func(fd uintptr) {
		got, rerr = os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	}); err != nil || rerr != nil {
		return "", false
	}
	return got, true
}

func verifyOpenedPathByStat(f *os.File, want string) error {
	if resolved, err := filepath.EvalSymlinks(want); err != nil || resolved != want {
		return errReadPathChanged
	}
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(want)
	if err != nil || !os.SameFile(opened, current) {
		return errReadPathChanged
	}
	return nil
}
