package agent

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/hooks"
	"github.com/opencode-ai/opencode/internal/langfuse"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/prompt"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
	"github.com/opencode-ai/opencode/internal/version"
)

// Common errors
var (
	ErrRequestCancelled = errors.New("request cancelled by user")
	ErrSessionBusy      = errors.New("session is currently processing another request")
	// ErrAgentBusy is returned by agent.Update when called while the agent
	// is mid-request. Callers (notably the API /agent/model/select handler)
	// match against this sentinel via errors.Is to surface a 409 Conflict.
	ErrAgentBusy = errors.New("cannot change model while processing requests")

	//go:embed prompts/*.md
	AgentPrompts embed.FS
)

type AgentEventType string

const (
	AgentEventTypeError     AgentEventType = "error"
	AgentEventTypeResponse  AgentEventType = "response"
	AgentEventTypeSummarize AgentEventType = "summarize"
)

const (
	AutoCompactionThreshold = 0.95
)

// effectiveCompactionThreshold applies RunOptions.CompactionThreshold as an
// override to the global AutoCompactionThreshold. A zero override means
// "use the default"; values outside (0, 1] are clamped and warned so a
// misconfigured flow step can't silently disable compaction or force it
// on every turn. Kept as a top-level helper (not a RunOptions method) so
// hot-path calls compile inline without an extra receiver load.
func effectiveCompactionThreshold(override float64) float64 {
	if override == 0 {
		return AutoCompactionThreshold
	}
	if override < 0 {
		logging.Warn("negative compaction threshold clamped to default",
			"got", override, "default", AutoCompactionThreshold)
		return AutoCompactionThreshold
	}
	if override > 1 {
		logging.Warn("compaction threshold > 1 clamped to 1",
			"got", override)
		return 1
	}
	return override
}

type AgentEvent struct {
	Type    AgentEventType
	Message message.Message
	Error   error

	// When has structured output
	StructOutput *message.ToolResult

	// When summarizing
	SessionID string
	Progress  string
	Done      bool

	// FlowStepID is set when event originates from a Flow step
	FlowStepID string
}

// RunOptions configures a single agent.Run invocation. New options should
// be added here rather than as new positional parameters on Run/RunWith so
// existing call sites stay terse.
type RunOptions struct {
	// NonInteractive marks the calling context as a one-shot (flow step,
	// headless CLI, ACP one-shot). When true, processGeneration holds the
	// turn open at the end of each agentic cycle until pending background
	// tasks (bash run_in_background, task async, monitor) for the session
	// reach terminal state, then re-enters the agentic loop so the model
	// can react to the synthetic completion(s) within the same RunWith
	// invocation. Default (false) preserves the original interactive
	// auto-resume behaviour where ResumeSession kicks a fresh agent.Run.
	NonInteractive bool

	// CompactionThreshold overrides the global AutoCompactionThreshold
	// (default 0.95) for this Run only. Set to a value in (0, 1] to trigger
	// synchronous compaction earlier — e.g. a flow step processing a lot of
	// tool output can set 0.7 to compact well before the hard limit. Zero
	// means "use the global default"; values outside (0, 1] are clamped to
	// the closest valid endpoint and a warn is logged. Only the tool-use
	// loop's pre-model-call check consults this override; unrelated paths
	// (final-turn checks, provider-side hard limits) remain unchanged.
	CompactionThreshold float64
}

type Service interface {
	pubsub.Suscriber[AgentEvent]
	AgentID() config.AgentName
	Model() models.Model
	Tools() []tools.BaseTool
	ResolvedTools() ([]tools.BaseTool, bool)
	// Run starts an agent turn for the given session. Backward-compat shim
	// that calls RunWith with the zero-value RunOptions (interactive mode).
	// maxTurnsOverride > 0 caps the agentic loop to that many turns for THIS call
	// only (e.g. a flow step's Step.MaxTurns). Pass 0 to inherit the agent /
	// global / default precedence.
	Run(ctx context.Context, sessionID string, content string, maxTurnsOverride int, attachments ...message.Attachment) (<-chan AgentEvent, error)
	// RunWith is the full-options entry point. See RunOptions for available
	// flags. Use this from non-interactive callers (flow runner, headless
	// CLI / ACP) to engage the end-of-turn wait on pending background tasks.
	RunWith(ctx context.Context, sessionID string, content string, maxTurnsOverride int, opts RunOptions, attachments ...message.Attachment) (<-chan AgentEvent, error)
	Cancel(sessionID string)
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	// TryLockSession attempts to acquire the session-busy slot used by Run().
	// Returns true if the slot was free and is now held by the caller (which
	// must release it via UnlockSession). Returns false if a Run is already in
	// flight or another caller holds the lock. Used by the cron scheduler to
	// stop an agent turn from starting while it commits its synthetic
	// tool_call/tool_result pair to the parent session.
	TryLockSession(sessionID string) bool
	UnlockSession(sessionID string)
	Update(agentName config.AgentName, modelID models.ModelID) (models.Model, error)
	Summarize(ctx context.Context, sessionID string) error
	// SummarizeSync compacts the session and blocks until the summary has been
	// written (unlike Summarize, which is event-driven and returns immediately).
	// It holds the session-busy lock for the duration so a concurrent Run can't
	// interleave, and returns ErrSessionBusy if the session is already in use.
	SummarizeSync(ctx context.Context, sessionID string) error
	GenerateRecap(ctx context.Context, sessionID string) (string, error)
}

type agent struct {
	*pubsub.Broker[AgentEvent]
	sessions session.Service
	messages message.Service

	agentID          config.AgentName
	toolsCh          <-chan tools.BaseTool
	toolsOnce        sync.Once
	tools            []tools.BaseTool
	toolsResolved    atomic.Bool
	provider         provider.Provider
	allowParallelism bool

	titleProvider     provider.Provider
	summarizeProvider provider.Provider

	// factory exposes services that are late-injected on the factory
	// after agent construction. Today we read HookRegistry off it at
	// tool-dispatch time (per claude-code-hooks-plugin-system); future
	// late-injected services (e.g. additional hook surfaces) would
	// reach the agent the same way without enlarging this struct.
	factory AgentFactory

	activeRequests sync.Map
}

func newAgent(
	ctx context.Context,
	agentInfo *agentregistry.AgentInfo,
	sessions session.Service,
	messages message.Service,
	permissions permission.Service,
	historyService history.Service,
	lspService lsp.LspService,
	reg agentregistry.Registry,
	mcpReg MCPRegistry,
	factory AgentFactory,
) (Service, error) {
	agentTools := NewToolSet(ctx, agentInfo, reg, permissions, historyService, lspService, sessions, messages, mcpReg, factory)

	agentProvider, err := createAgentProvider(agentInfo.ID, withInteractive(agentInfo.Interactive), withBoundPeers(agentInfo.BoundPeers))
	if err != nil {
		return nil, err
	}

	var titleProvider, summarizeProvider provider.Provider
	if agentInfo.Mode == config.AgentModeAgent {
		summarizeProvider, err = createAgentProvider(config.AgentSummarizer, withDisableCache())
		if err != nil {
			return nil, err
		}
		titleProvider, err = createAgentProvider(config.AgentDescriptor, withDisableCache())
		if err != nil {
			return nil, err
		}
	}

	agent := &agent{
		Broker:            pubsub.NewBroker[AgentEvent](),
		agentID:           agentInfo.ID,
		provider:          agentProvider,
		messages:          messages,
		sessions:          sessions,
		toolsCh:           agentTools,
		titleProvider:     titleProvider,
		summarizeProvider: summarizeProvider,
		activeRequests:    sync.Map{},
		allowParallelism:  agentInfo.AllowsParallelToolUse(),
		factory:           factory,
	}

	// Resolve tools in background so they're ready before first Run() call
	// and so TUI can show MCP status without blocking.
	go func() {
		defer logging.RecoverPanic("agent.resolveTools", nil)
		agent.resolveTools()
	}()

	return agent, nil
}

// hookCall is the result of a PreToolUse evaluation that the dispatch
// code branches on. It collapses "no hooks registered" / "no matching
// hooks" / "hooks ran but returned no decision" into a single zero value
// so the call sites don't repeat the nil-check three times.
type hookCall struct {
	registry *hooks.Registry
	decision hooks.PreToolDecision
}

// firePreTool evaluates PreToolUse for the given tool call. Returns the
// (possibly-mutated) input JSON string to pass to the tool, plus the
// hookCall capturing whether a block / explicit-allow / additional-context
// applies. When no registry is installed or no hooks match, the returned
// hookCall has registry=nil and the original input is returned unchanged.
//
// On JSON-parse failure (rare — input strings come from the LLM provider
// already JSON-validated) we skip the hook entirely and return the
// original input, logging a debug message. We never crash the agent loop
// over a malformed hook input.
func (a *agent) firePreTool(ctx context.Context, sessionID, toolCallName, toolCallInput string) (string, hookCall) {
	if a.factory == nil {
		return toolCallInput, hookCall{}
	}
	reg := a.factory.HookRegistry()
	if reg == nil {
		return toolCallInput, hookCall{}
	}
	// Fast-path: skip JSON parse + os.Getwd() when no PreToolUse hooks
	// are configured for any tool. This is the common case for users
	// who don't configure hooks at all — most tool calls in most
	// sessions. Without this gate the helper unmarshals every tool
	// input for nothing, costing a few µs per call across a hot loop.
	if !reg.HasEvent(hooks.EventPreToolUse) {
		return toolCallInput, hookCall{}
	}
	var inputMap map[string]any
	if toolCallInput != "" {
		if err := json.Unmarshal([]byte(toolCallInput), &inputMap); err != nil {
			logging.Debug("hook PreToolUse: tool input was not a JSON object; skipping hook",
				"tool", toolCallName, "error", err)
			return toolCallInput, hookCall{}
		}
	}
	cwd := hookCWD(reg)
	dec := reg.RunPreTool(ctx, sessionID, cwd, toolCallName, inputMap)
	if dec.UpdatedInput != nil {
		if rewritten, err := json.Marshal(dec.UpdatedInput); err == nil {
			toolCallInput = string(rewritten)
		} else {
			logging.Warn("hook PreToolUse: failed to re-serialize updatedInput; using original",
				"tool", toolCallName, "error", err)
		}
	}
	return toolCallInput, hookCall{registry: reg, decision: dec}
}

// appendHookContext glues a tool's content with a hook's additionalContext.
// Format: "<content>\n\n[hook context: <ctx>]". When ctx is empty, returns
// the content unchanged. Applied at every record(...) site so PreToolUse
// (block path and non-block path) and PostToolUse contexts all reach the
// agent on the next turn — spec requires additionalContext to be visible
// to the agent regardless of whether the hook blocked.
func appendHookContext(content, ctx string) string {
	if ctx == "" {
		return content
	}
	if content == "" {
		return "[hook context: " + ctx + "]"
	}
	return content + "\n\n[hook context: " + ctx + "]"
}

// joinHookContext combines a PreToolUse additionalContext with a
// PostToolUse additionalContext. Either may be empty; the result is
// `\n`-separated when both are present so the agent sees them as
// distinct lines.
func joinHookContext(pre, post string) string {
	switch {
	case pre == "":
		return post
	case post == "":
		return pre
	default:
		return pre + "\n" + post
	}
}

// hookCWD resolves the working directory passed to hooks. Prefers the
// live process cwd (`os.Getwd`) so a flow that `chdir`'d sees its real
// location; falls back to the registry's project root if `os.Getwd`
// fails (rare — only when the working directory was unlinked, e.g. a
// flow stepped into a tempdir that was later cleaned).
func hookCWD(reg *hooks.Registry) string {
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return reg.ProjectRoot()
}

// firePostTool evaluates PostToolUse for a successful tool call. Returns
// the (possibly-mutated) tool output plus any additionalContext the hook
// chain accumulated — callers MUST apply the context via appendHookContext
// before recording the result so the agent sees it on its next turn.
// A nil registry / no matching hooks / no decision degrades to the
// original output and empty context.
func (a *agent) firePostTool(ctx context.Context, sessionID, toolCallName, toolCallInput, toolOutput string) (string, string) {
	if a.factory == nil {
		return toolOutput, ""
	}
	reg := a.factory.HookRegistry()
	if reg == nil {
		return toolOutput, ""
	}
	// Fast-path: same rationale as firePreTool — most tool calls don't
	// have PostToolUse hooks configured. Avoid the JSON unmarshal and
	// the os.Getwd syscall when there's no event to dispatch.
	if !reg.HasEvent(hooks.EventPostToolUse) {
		return toolOutput, ""
	}
	var inputMap map[string]any
	if toolCallInput != "" {
		_ = json.Unmarshal([]byte(toolCallInput), &inputMap) // best-effort; nil map is fine
	}
	cwd := hookCWD(reg)
	dec := reg.RunPostTool(ctx, sessionID, cwd, toolCallName, inputMap, toolOutput)
	out := toolOutput
	switch {
	case dec.BlockReason != "":
		out = dec.BlockReason
	case dec.UpdatedOutput != nil:
		out = *dec.UpdatedOutput
	}
	return out, dec.AdditionalContext
}

func (a *agent) AgentID() config.AgentName {
	return a.agentID
}

func (a *agent) Model() models.Model {
	return a.provider.Model()
}

func (a *agent) Cancel(sessionID string) {
	// Cancel regular requests. Skip the cron sentinel — user-initiated cancel
	// must not strip the lock the cron scheduler is holding around its
	// synthetic message commit.
	if val, exists := a.activeRequests.Load(sessionID); exists {
		if cancel, ok := val.(context.CancelFunc); ok {
			a.activeRequests.Delete(sessionID)
			logging.InfoPersist(fmt.Sprintf("Request cancellation initiated for session: %s", sessionID))
			cancel()
		}
	}

	// Also check for summarize requests
	if cancelFunc, exists := a.activeRequests.LoadAndDelete(sessionID + "-summarize"); exists {
		if cancel, ok := cancelFunc.(context.CancelFunc); ok {
			logging.InfoPersist(fmt.Sprintf("Summarize cancellation initiated for session: %s", sessionID))
			cancel()
		}
	}
}

func (a *agent) IsBusy() bool {
	busy := false
	a.activeRequests.Range(func(key, value any) bool {
		switch v := value.(type) {
		case context.CancelFunc:
			if v != nil {
				busy = true
				return false // Stop iterating
			}
		case cronLock:
			busy = true
			return false
		}
		return true // Continue iterating
	})
	return busy
}

func (a *agent) IsSessionBusy(sessionID string) bool {
	_, busy := a.activeRequests.Load(sessionID)
	return busy
}

// cronLock is a sentinel stored in activeRequests when the cron scheduler
// holds a session-busy slot. It is distinguishable from a real Run's
// context.CancelFunc by type, so UnlockSession never deletes a live cancel.
type cronLock struct{}

// TryLockSession attempts to acquire the session-busy slot. Returns false if
// the slot is already held (by an in-flight Run or another lock holder).
// While held, IsSessionBusy/IsBusy report true and a concurrent Run() returns
// ErrSessionBusy — preventing the agent from starting a turn that would
// interleave with the cron scheduler's synthetic tool_call/tool_result pair.
func (a *agent) TryLockSession(sessionID string) bool {
	_, loaded := a.activeRequests.LoadOrStore(sessionID, cronLock{})
	return !loaded
}

// UnlockSession releases a slot acquired via TryLockSession. It is a no-op if
// the slot is not held by the cron sentinel — never deletes a live Run's
// cancel func.
func (a *agent) UnlockSession(sessionID string) {
	if val, ok := a.activeRequests.Load(sessionID); ok {
		if _, isLock := val.(cronLock); isLock {
			a.activeRequests.Delete(sessionID)
		}
	}
}

func (a *agent) generateTitle(ctx context.Context, sessionID string, content string) error {
	if content == "" {
		return nil
	}
	if a.titleProvider == nil {
		return nil
	}
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	// A user-set title is authoritative — skip generation entirely so we don't
	// waste a descriptor call. The write below is also guarded at the DB level
	// (SetGeneratedTitle) to close the race where a rename lands after this read.
	if sess.UserSetTitle {
		return nil
	}
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, sessionID)
	ctx = context.WithValue(ctx, tools.AgentIDContextKey, config.AgentName("descriptor"))
	ctx = a.createLangfuseTrace(ctx, sess)
	defer langfuse.EndTrace(ctx)
	parts := []message.ContentPart{message.TextContent{Text: content}}
	response, err := a.titleProvider.SendMessages(
		ctx,
		[]message.Message{
			{
				Role:  message.User,
				Parts: parts,
			},
		},
		make([]tools.BaseTool, 0),
	)
	if err != nil {
		return err
	}

	title := strings.TrimSpace(strings.ReplaceAll(response.Content, "\n", " "))
	if title == "" {
		return nil
	}

	// Guarded write: only lands if the session is still not user-titled, so a
	// rename that commits while this descriptor call was in flight wins.
	_, err = a.sessions.SetGeneratedTitle(ctx, sessionID, title)
	return err
}

const recapMessageWindow = 30

// Minimum thresholds for generating a recap. Sessions below either threshold
// are too short to warrant the cost of a summarization call.
const (
	recapMinMessages    = 5
	recapMinTotalTokens = 8000
)

func (a *agent) GenerateRecap(ctx context.Context, sessionID string) (string, error) {
	if a.summarizeProvider == nil {
		return "", fmt.Errorf("summarize provider not available")
	}

	recent, err := a.messages.ListLatest(ctx, sessionID, recapMessageWindow)
	if err != nil {
		return "", fmt.Errorf("failed to list messages: %w", err)
	}
	if len(recent) == 0 {
		return "", nil
	}

	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get session: %w", err)
	}

	totalTokens := sess.TotalPromptTokens + sess.TotalCompletionTokens
	if len(recent) < recapMinMessages {
		logging.Debug("recap: session too short, skipping",
			"session_id", sessionID,
			"messages", len(recent),
			"tokens", totalTokens,
		)
		return "", nil
	}
	if totalTokens > 0 && totalTokens < recapMinTotalTokens {
		logging.Debug("recap: session too few tokens, skipping",
			"session_id", sessionID,
			"messages", len(recent),
			"tokens", totalTokens,
		)
		return "", nil
	}

	ctx = context.WithValue(ctx, tools.SessionIDContextKey, sessionID)
	ctx = context.WithValue(ctx, tools.AgentIDContextKey, config.AgentName("summarizer"))
	ctx = a.createLangfuseTrace(ctx, sess)
	defer langfuse.EndTrace(ctx)

	recapPrompt, err := AgentPrompts.ReadFile("prompts/recap.md")
	if err != nil {
		return "", fmt.Errorf("failed to load recap prompt: %w", err)
	}

	promptMsg := message.Message{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: string(recapPrompt)}},
	}

	msgsWithPrompt := append(recent, promptMsg)
	events := a.summarizeProvider.StreamResponse(
		ctx,
		msgsWithPrompt,
		make([]tools.BaseTool, 0),
	)
	response, err := provider.StreamToResponse(events)
	if err != nil {
		return "", fmt.Errorf("failed to generate recap: %w", err)
	}

	return strings.TrimSpace(response.Content), nil
}

func (a *agent) err(err error) AgentEvent {
	return AgentEvent{
		Type:  AgentEventTypeError,
		Error: err,
	}
}

// Run is the backward-compat shim — see Service.Run. Delegates to RunWith
// with zero-value RunOptions (interactive mode, no end-of-turn wait).
func (a *agent) Run(ctx context.Context, sessionID string, content string, maxTurnsOverride int, attachments ...message.Attachment) (<-chan AgentEvent, error) {
	return a.RunWith(ctx, sessionID, content, maxTurnsOverride, RunOptions{}, attachments...)
}

// RunWith is the full-options entry point. See RunOptions for available flags.
func (a *agent) RunWith(ctx context.Context, sessionID string, content string, maxTurnsOverride int, opts RunOptions, attachments ...message.Attachment) (<-chan AgentEvent, error) {
	if !a.provider.Model().SupportsAttachments && attachments != nil {
		attachments = nil
	}
	// Events channel is buffered (cap 1) so the recover handler — and
	// the normal-path send below — can never block on a consumer that
	// has gone away (ctx cancellation, caller stopped ranging). agent.Run
	// emits exactly one terminal event, so cap 1 is sufficient. A blocked
	// send in the panic path would prevent the deferred cleanup
	// (activeRequests.Delete + cancel) from running and leak the
	// session's busy lock — that's the regression scenario the deferred
	// cleanup was added to prevent in the first place.
	events := make(chan AgentEvent, 1)
	if a.IsSessionBusy(sessionID) {
		return nil, ErrSessionBusy
	}

	genCtx, cancel := context.WithCancel(ctx)

	a.activeRequests.Store(sessionID, cancel)
	go func() {
		logging.Info("Agent started", "sessionID", sessionID, "agent", a.AgentID(), "nonInteractive", opts.NonInteractive)
		now := time.Now()
		// Cleanup MUST run regardless of how the goroutine exits.
		// Defer LIFO order: RecoverPanic registered LAST runs FIRST
		// on panic. With the events channel now buffered, the recover
		// handler's send + close never block, so the subsequent
		// cancel + Delete defers always get to run and the session's
		// busy lock is released.
		defer a.activeRequests.Delete(sessionID)
		defer cancel()
		defer logging.RecoverPanic("agent.Run", func() {
			// Send is non-blocking due to events channel buffer.
			// Close after so consumers observing the channel-close
			// signal don't deadlock either.
			events <- a.err(fmt.Errorf("panic while running the agent"))
			close(events)
		})
		var attachmentParts []message.ContentPart
		for _, attachment := range attachments {
			attachmentParts = append(attachmentParts, message.BinaryContent{Path: attachment.FilePath, MIMEType: attachment.MimeType, Data: attachment.Content})
		}

		result := a.processGeneration(genCtx, sessionID, content, maxTurnsOverride, attachmentParts, opts)
		gauge := time.Since(now).Milliseconds()
		if result.Error != nil {
			if errors.Is(result.Error, ErrRequestCancelled) || errors.Is(result.Error, context.Canceled) {
				logging.Warn("Agent processing cancelled", "sessionID", sessionID, "agent", a.AgentID(), "gauge", gauge)
			} else {
				logging.Error("Agent processing failed", "sessionID", sessionID, "agent", a.AgentID(), "gauge", gauge)
				logging.ErrorPersist(result.Error.Error())
			}
		} else {
			logging.Info("Agent completed", "sessionID", sessionID, "agent", a.AgentID(), "gauge", gauge)
		}

		a.Publish(pubsub.CreatedEvent, result)
		events <- result
		close(events)
	}()
	return events, nil
}

func (a *agent) processGeneration(ctx context.Context, sessionID, content string, maxTurnsOverride int, attachmentParts []message.ContentPart, opts RunOptions) AgentEvent {
	cfg := config.Get()
	// List existing messages; if none, start title generation asynchronously.
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return a.err(fmt.Errorf("failed to list messages: %w", err))
	}
	if len(msgs) == 0 {
		titleContent := content
		go func() {
			defer logging.RecoverPanic("agent.Run", func() {
				logging.ErrorPersist("panic while generating title")
			})
			titleErr := a.generateTitle(context.Background(), sessionID, titleContent)
			if titleErr != nil {
				logging.ErrorPersist(fmt.Sprintf("failed to generate title: %v", titleErr))
			}
		}()
	}
	session, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return a.err(fmt.Errorf("failed to get session: %w", err))
	}
	if session.SummaryMessageID != "" {
		msgs = a.filterMessagesFromSummary(msgs, session.SummaryMessageID)
	}
	// Auto-recover sessions previously corrupted by an empty user turn
	// (older builds called createUserMessage unconditionally on auto-resume
	// — see the comment block before the createUserMessage call below).
	// The persisted `user(text="")` makes every subsequent agent.Run on
	// that session fail with HTTP 400 `messages: text content blocks must
	// be non-empty`. Dropping these messages from the history we send
	// upstream lets the model continue without manual intervention.
	msgs = filterEmptyUserMessages(msgs)
	if session.ParentSessionID != "" {
		ctx = context.WithValue(ctx, tools.IsTaskAgentContextKey, true)
	}
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, sessionID)
	ctx = context.WithValue(ctx, tools.AgentIDContextKey, a.AgentID())
	// Propagate the non-interactive marker onto the tool-execution ctx so
	// tools (bash) can make non-interactive-aware decisions — e.g. redirect
	// a foreground `sleep` to the background-task wait instead of burning
	// wall-clock while tasks are pending. Runtime-only, never persisted.
	ctx = context.WithValue(ctx, tools.NonInteractiveContextKey, opts.NonInteractive)
	ctx = tools.AddTag(ctx, "agent", a.AgentID())

	ctx = a.createLangfuseTrace(ctx, session)
	defer langfuse.EndTrace(ctx)

	effectiveMaxTurns := resolveMaxTurns(maxTurnsOverride, a.agentID)

	// When the caller supplied no content and no attachments, this is an
	// auto-resume turn — task.EnqueueTaskCompletion has already written
	// the synthetic Assistant(ToolCall) + Tool(ToolResult) pair, and that
	// Tool message IS the input the model needs to react to. Creating
	// an additional user message with an empty TextContent block here
	// would send `[..., assistant(tool_use), user(tool_result), user("")]`
	// upstream, and the Anthropic/Vertex/Bedrock API rejects empty text
	// blocks with `messages: text content blocks must be non-empty` (HTTP
	// 400). Skip the synthetic-user-turn and drive the model off the
	// existing history.
	var userMsg message.Message
	hasUserTurn := content != "" || len(attachmentParts) > 0
	msgHistory := msgs
	if hasUserTurn {
		if hint := proactiveMaxTurnsHint(effectiveMaxTurns); hint != "" {
			content += hint
		}
		var err error
		userMsg, err = a.createUserMessage(ctx, sessionID, content, attachmentParts)
		if err != nil {
			return a.err(fmt.Errorf("failed to create user message: %w", err))
		}
		msgHistory = append(msgs, userMsg)
	}
	var agentMessage message.Message
	var toolResults *message.Message
	var structOutput *message.ToolResult
	structOutputIsErr := true
	cycles := 0
	preserveTail := false

	// Susped to get lazy tools
	toolSet := a.resolveTools()

	tracker := newCallTracker()

	// finalResult holds the natural-completion event that the inner loop
	// produced. Errors return directly from processGeneration; only the
	// success paths flow through finalResult so the outer non-interactive
	// wait can decide whether to re-cycle.
	var finalResult AgentEvent
	// outerCycles caps the number of "wait for background tasks then re-
	// enter the inner agentic loop" iterations. Bounded by effectiveMaxTurns
	// so a runaway "spawn background, terminal turn, wait, spawn more"
	// pattern cannot exceed the per-Run budget.
	outerCycles := 0
OuterLoop:
	for {
		outerCycles++
		if outerCycles > effectiveMaxTurns {
			// Outer-cycle budget exhausted. Bare drain re-waits do not
			// consume this budget (they live inside a single iteration);
			// only cycles that re-invoke the model count. If tasks are
			// still pending, surface why the run stopped observing them
			// instead of returning silently.
			if opts.NonInteractive {
				if reg := task.GlobalRegistry(); reg != nil {
					if stillPending := reg.PendingForSession(sessionID, nil); len(stillPending) > 0 {
						a.injectWaitTimeoutNote(ctx, sessionID, stillPending,
							fmt.Errorf("outer-cycle budget exhausted after %d turns", effectiveMaxTurns))
					}
				}
			}
			break OuterLoop
		}
		for {
			cycles += 1
			// Check for cancellation before each iteration
			select {
			case <-ctx.Done():
				return a.err(ctx.Err())
			default:
				// Continue processing
			}

			etaTokens, shouldTriggerAutoCompaction := a.provider.CountTokens(ctx, effectiveCompactionThreshold(opts.CompactionThreshold), msgHistory, toolSet)
			// Check if auto-compaction should be triggered before each model call
			// This is crucial for long tool use loops that can exceed context limits
			// NOTE: since tool may provide output exceeding context limit when combined with existing history,
			// we have to do summary, which would "lossy compress" it, providing less context to the following LLM call,
			// but alternative is to fail with context limit, so we do it anyway.
			if cfg.AutoCompact && cycles != 1 && shouldTriggerAutoCompaction {
				logging.Info(
					"Auto-compaction triggered during tool use loop",
					"session_id", sessionID,
					"history_length", len(msgHistory),
					"token_count", etaTokens,
					"cycle", cycles,
				)

				// Perform synchronous compaction to shrink context
				if errSync := a.performSynchronousCompaction(ctx, sessionID); errSync != nil {
					logging.Warn("Failed to perform auto-compaction during tool use", "error", errSync)
					// Continue anyway - better to risk context overflow than stop completely
				} else {
					// After successful compaction, reload messages and rebuild msgHistory
					msgs, errMsg := a.messages.List(ctx, sessionID)
					if err != nil {
						return a.err(fmt.Errorf("failed to reload messages after compaction: %w", errMsg))
					}

					session, errMsg := a.sessions.Get(ctx, sessionID)
					if errMsg != nil {
						return a.err(fmt.Errorf("failed to get session after compaction: %w", errMsg))
					}
					msgs = a.filterMessagesFromSummary(msgs, session.SummaryMessageID)

					// Carry task budget across compaction: tell provider how much budget remains
					if agentCfg, hasCfg := config.Get().Agents[a.agentID]; hasCfg && agentCfg.TaskBudget > 0 {
						spent := session.TotalCompletionTokens
						remaining := agentCfg.TaskBudget - spent
						if remaining < 0 {
							remaining = 0
						}
						ctx = provider.TaskBudgetRemainingContext(ctx, remaining)
					}

					// Preserve original problem and result from the last tool iteration to ensure no dead-loop
					if preserveTail {
						preserveTail = false
						msgHistory = append(msgs, agentMessage, *toolResults)
					} else if hasUserTurn {
						msgHistory = append(msgs, userMsg)
					} else {
						// Auto-resume turn — no user message to re-append.
						msgHistory = msgs
					}

					// Re-count against the same effective threshold that triggered
					// this compaction so the log reflects the step's configured
					// gate, not the global default.
					etaTokens, shouldTriggerAutoCompaction = a.provider.CountTokens(ctx, effectiveCompactionThreshold(opts.CompactionThreshold), msgHistory, toolSet)
					if shouldTriggerAutoCompaction {
						logging.Warn(
							"Context compacted, but still exceed context threshold",
							"session_id", sessionID,
							"history_length", len(msgHistory),
							"token_count", etaTokens,
							"cycle", cycles,
						)
					} else {
						logging.Info(
							"Context compacted, continuing with reduced history",
							"session_id", sessionID,
							"history_length", len(msgHistory),
							"token_count", etaTokens,
							"cycle", cycles,
						)
					}
				}
			}

			// Ensure we don't run into API limitation (max_token to be generated + current tokens count)
			a.provider.AdjustMaxTokens(etaTokens)

			// Check max turns — give the model one final turn to wrap up
			if cycles > effectiveMaxTurns {
				logging.Warn("Max turns reached, requesting final response", "turns", cycles-1, "max", effectiveMaxTurns, "session_id", sessionID)
				maxTurnsPrompt, promptErr := AgentPrompts.ReadFile("prompts/max_turns.md")
				if promptErr != nil {
					logging.Warn("Failed to load max_turns prompt", "error", promptErr)
					return AgentEvent{
						Type:         AgentEventTypeResponse,
						Message:      agentMessage,
						StructOutput: structOutput,
						Done:         true,
					}
				}
				wrapUpMsg, wrapUpErr := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
					Role:  message.User,
					Parts: []message.ContentPart{message.TextContent{Text: string(maxTurnsPrompt)}},
				})
				if wrapUpErr != nil {
					logging.Warn("Failed to create wrap-up message", "error", wrapUpErr)
					return AgentEvent{
						Type:         AgentEventTypeResponse,
						Message:      agentMessage,
						StructOutput: structOutput,
						Done:         true,
					}
				}
				msgHistory = append(msgHistory, wrapUpMsg)
				// Pass full toolSet to preserve the cache prefix, but discard any tool calls the model makes
				finalMsg, _, finalErr := a.streamAndHandleEvents(ctx, sessionID, msgHistory, toolSet, tracker)
				if finalErr != nil {
					logging.Warn("Failed to get final response after max turns", "error", finalErr)
					return AgentEvent{
						Type:         AgentEventTypeResponse,
						Message:      agentMessage,
						StructOutput: structOutput,
						Done:         true,
					}
				}
				// If the model ignored the instruction and made tool calls, discard them —
				// we only want the text content as the final response
				if finalMsg.FinishReason() == message.FinishReasonToolUse {
					logging.Warn("Model made tool calls after max turns wrap-up, discarding them", "session_id", sessionID)
					a.createErrorToolResults(finalMsg)
					a.finishMessage(ctx, &finalMsg, message.FinishReasonEndTurn)
				}
				finalResult = AgentEvent{
					Type:         AgentEventTypeResponse,
					Message:      finalMsg,
					StructOutput: structOutput,
					Done:         true,
				}
				break OuterLoop
			}

			agentMessage, toolResults, err = a.streamAndHandleEvents(ctx, sessionID, msgHistory, toolSet, tracker)
			if err != nil {
				a.createErrorToolResults(agentMessage)
				if errors.Is(err, context.Canceled) {
					a.finishMessage(ctx, &agentMessage, message.FinishReasonCanceled)
					return a.err(ErrRequestCancelled)
				}
				a.finishMessage(ctx, &agentMessage, message.FinishReasonError)
				return a.err(fmt.Errorf("failed to process events: %w", err))
			}
			if cfg.Debug {
				seqID := (len(msgHistory) + 1) / 2
				toolResultFilepath := logging.WriteToolResultsJson(sessionID, seqID, toolResults)
				logging.Info("Provider stream completed", "reason", agentMessage.FinishReason(), "filepath", toolResultFilepath, "cycle", cycles)
			} else {
				logging.Info("Provider stream completed", "reason", agentMessage.FinishReason(), "cycle", cycles)
			}
			if agentMessage.FinishReason() == message.FinishReasonToolUse {
				if toolResults == nil {
					// Tool results are nil (tool execution failed or returned empty)
					// Create an empty tool results message to allow the LLM to provide a final response
					logging.Warn("Tool results are nil, creating empty tool results message to allow final response", "session_id", sessionID)
					emptyToolMsg, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
						Role:  message.Tool,
						Parts: []message.ContentPart{message.TextContent{Text: "Tool execution completed with no results."}},
					})
					if err != nil {
						logging.Warn("Failed to create empty tool results message", "error", err)
						// If we can't create the message, just return what we have
						return AgentEvent{
							Type:    AgentEventTypeResponse,
							Message: agentMessage,
							Done:    true,
						}
					}
					toolResults = &emptyToolMsg
				} else {
					structOutput, structOutputIsErr = captureStructOutput(toolResults, structOutput, structOutputIsErr)
				}

				msgHistory = append(msgHistory, agentMessage, *toolResults)

				// struct_output is contractually the model's terminal tool call:
				// the accepted result IS the run's output and nothing downstream
				// consumes a post-struct_output assistant turn. Skip the wrap-up
				// round-trip — on long reasoning sessions it is the largest,
				// most throttle-prone request of the whole run, and a provider
				// retry storm there has stranded fully-completed steps until the
				// job deadline killed them.
				//
				// With background tasks pending we cannot finish here (their
				// completions would be enqueued onto a finished session), so we
				// fall through to the pre-existing path: give the model its
				// wrap-up turn; when that turn ends the outer loop BLOCKS in
				// drainSessionTasks (WaitForActiveTasks — no busy-spin) and then
				// re-enters this loop so the model reacts to the completions. A
				// struct_output re-emitted on that later pass finishes the run
				// through this same branch. Bounded by effectiveMaxTurns like
				// any other cycle.
				if structOutput != nil && !structOutputIsErr {
					pendingTasks := 0
					if opts.NonInteractive {
						if reg := task.GlobalRegistry(); reg != nil {
							pendingTasks = len(reg.PendingForSession(sessionID, nil))
						}
					}
					if pendingTasks == 0 {
						logging.Info("struct_output accepted — finishing run without wrap-up turn", "session_id", sessionID, "cycle", cycles)
						finalResult = AgentEvent{
							Type:         AgentEventTypeResponse,
							Message:      agentMessage,
							StructOutput: structOutput,
							Done:         true,
						}
						break OuterLoop
					}
					logging.Info("struct_output accepted but background tasks pending — continuing to the wait cycle", "session_id", sessionID, "pending_count", pendingTasks)
				}

				preserveTail = true
				continue
			}
			finalResult = AgentEvent{
				Type:         AgentEventTypeResponse,
				Message:      agentMessage,
				StructOutput: structOutput,
				Done:         true,
			}
			break
		}
		// Inner agentic loop has produced a natural terminal turn.
		// In interactive mode we return directly. In non-interactive mode,
		// check for pending background tasks the model spawned this turn
		// and wait for them (bounded by ctx) before re-entering the inner
		// loop so the model can react to the synthetic completion(s)
		// within this same RunWith invocation.
		if !opts.NonInteractive {
			break OuterLoop
		}
		reg := task.GlobalRegistry()
		if reg == nil {
			break OuterLoop
		}
		pending := reg.PendingForSession(sessionID, nil)
		if len(pending) == 0 {
			break OuterLoop
		}
		logging.Info(
			"Non-interactive turn complete with pending background tasks; waiting before re-cycling",
			"session_id", sessionID,
			"pending_count", len(pending),
			"outer_cycle", outerCycles,
		)
		if err := drainSessionTasks(ctx, reg, sessionID); err != nil {
			// ctx cancelled (timeout or upstream cancel). Inject the
			// synthetic Assistant timeout note enumerating still-
			// pending tasks so any subsequent agent.Run on this
			// session sees the reason and avoids re-spawning the
			// same dead-end work.
			stillPending := reg.PendingForSession(sessionID, nil)
			a.injectWaitTimeoutNote(ctx, sessionID, stillPending, err)
			logging.Warn(
				"Non-interactive wait did not complete cleanly",
				"session_id", sessionID,
				"err", err,
				"still_pending", len(stillPending),
			)
			break OuterLoop
		}
		// Wait completed — synthetic completions are in the message log.
		// Reload, filter the empty-user-turn corruption, and let the
		// inner loop run another cycle so the model can react.
		freshMsgs, listErr := a.messages.List(ctx, sessionID)
		if listErr != nil {
			logging.Warn(
				"Failed to reload messages after non-interactive wait; returning pre-wait result",
				"session_id", sessionID,
				"err", listErr,
			)
			break OuterLoop
		}
		if session.SummaryMessageID != "" {
			freshMsgs = a.filterMessagesFromSummary(freshMsgs, session.SummaryMessageID)
		}
		freshMsgs = filterEmptyUserMessages(freshMsgs)
		msgs = freshMsgs
		msgHistory = msgs
		// Reset inner-loop bookkeeping for the next cycle. hasUserTurn
		// MUST flip to false: the original userMsg is already part of
		// the reloaded `msgs` (persisted before the first outer cycle).
		// Leaving hasUserTurn=true would make the auto-compaction reload
		// path inside the inner loop re-append userMsg to msgs, creating
		// a duplicate user turn on the second-and-later outer iterations
		// whenever compaction triggers.
		cycles = 0
		preserveTail = false
		hasUserTurn = false
	}
	return finalResult
}

// drainSessionTasks blocks until the session has NO pending background
// tasks, or ctx is cancelled. WaitForActiveTasks keeps its snapshot-at-
// start semantics (tasks registered after a wait begins are not observed
// by that wait), so after each clean return the pending set is re-read
// and the wait repeats — covering tasks that appeared during the previous
// wait window. Returns nil once drained, ctx.Err() on cancellation. The
// loop cannot spin hot: a non-empty re-read means at least one running
// task, and the next wait blocks on it.
func drainSessionTasks(ctx context.Context, reg task.Registry, sessionID string) error {
	for {
		if err := reg.WaitForActiveTasks(ctx, sessionID, task.WaitOptions{IncludeMonitor: true}); err != nil {
			return err
		}
		remaining := reg.PendingForSession(sessionID, nil)
		if len(remaining) == 0 {
			return nil
		}
		logging.Info(
			"Non-interactive drain: tasks registered after the wait snapshot; re-waiting",
			"session_id", sessionID,
			"pending_count", len(remaining),
		)
	}
}

// injectWaitTimeoutNote writes a synthetic Assistant text message into
// the session enumerating still-pending background tasks at the moment
// the non-interactive wait was cancelled. Marked Synthetic:true so the
// bridge filter skips it for outbound chat indicators; transcript / SSE
// / model-on-re-invocation consumers still observe it as ambient context.
func (a *agent) injectWaitTimeoutNote(ctx context.Context, sessionID string, pending []*task.Task, waitErr error) {
	var b strings.Builder
	fmt.Fprintf(&b, "[wait-timeout] %d background task(s) did not complete before the non-interactive wait ended (%v).\n", len(pending), waitErr)
	for _, t := range pending {
		fmt.Fprintf(&b,
			" - task_id=%s kind=%s started=%s output_file=%s",
			t.ID, string(t.Kind), t.StartedAt.UTC().Format(time.RFC3339), t.OutputPath,
		)
		if t.Description != "" {
			fmt.Fprintf(&b, " desc=%q", t.Description)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nThe step's terminal turn above was produced WITHOUT observing these tasks' completions. ")
	b.WriteString("On any subsequent agent.Run on this session, inspect the per-task output_file before re-spawning equivalent work, ")
	b.WriteString("or call `tasklist` to confirm whether the tasks are still running.")
	// Write with a background context so a cancelled parent ctx doesn't
	// silently drop the note (the whole point is observability after the
	// caller's deadline already elapsed).
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx // reserved for future enrichment (langfuse trace ID, etc.)
	if _, err := a.messages.Create(writeCtx, sessionID, message.CreateMessageParams{
		Role:      message.Assistant,
		Parts:     []message.ContentPart{message.TextContent{Text: b.String()}},
		Synthetic: true,
	}); err != nil {
		logging.Warn("Failed to inject wait-timeout note", "session_id", sessionID, "err", err)
	}
}

func (a *agent) createUserMessage(ctx context.Context, sessionID, content string, attachmentParts []message.ContentPart) (message.Message, error) {
	parts := []message.ContentPart{message.TextContent{Text: content}}
	parts = append(parts, attachmentParts...)
	return a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: parts,
	})
}

func (a *agent) streamAndHandleEvents(ctx context.Context, sessionID string, msgHistory []message.Message, toolSet []tools.BaseTool, tracker *callTracker) (message.Message, *message.Message, error) {
	eventChan := a.provider.StreamResponse(ctx, msgHistory, toolSet)

	assistantMsg, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{},
		Model: a.provider.Model().ID,
	})
	if err != nil {
		return assistantMsg, nil, fmt.Errorf("failed to create assistant message: %w", err)
	}

	ctx = context.WithValue(ctx, tools.MessageIDContextKey, assistantMsg.ID)

	// Process provider response first
	for event := range eventChan {
		if processErr := a.processEvent(ctx, sessionID, &assistantMsg, event); processErr != nil {
			return assistantMsg, nil, processErr
		}
		if ctx.Err() != nil {
			return assistantMsg, nil, ctx.Err()
		}
	}

	// Process tool calls
	toolResults := make([]message.ToolResult, len(assistantMsg.ToolCalls()))
	toolCalls := assistantMsg.ToolCalls()

	// record writes a tool result into the shared toolResults slice and
	// emits a per-part SSE event for the same tool. Each call must own a
	// unique index — the parallel-group goroutines below preserve this
	// invariant by passing entry.index, which is assigned during phase 1.
	// Concurrent invocation from those goroutines is safe: the broker's
	// Publish takes RLock, and per-index ownership prevents slice races.
	record := func(index int, tr message.ToolResult) {
		toolResults[index] = tr
		a.messages.PublishPart(sessionID, assistantMsg.ID, tr)
	}

	// Phase 1: Pre-processing (synchronous) — resolve tools, loop detection, classify parallelism
	type toolEntry struct {
		index    int
		tool     tools.BaseTool
		toolCall message.ToolCall
		parallel bool
	}
	var parallelGroup []toolEntry
	var sequentialGroup []toolEntry

	allToolCalls := make([]tools.ToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		allToolCalls[i] = tools.ToolCall{ID: tc.ID, Name: tc.Name, Input: tc.Input}
	}

	for i, toolCall := range toolCalls {
		var tool tools.BaseTool
		for _, availableTool := range toolSet {
			if availableTool.Info().Name == toolCall.Name {
				tool = availableTool
				break
			}
		}
		if tool == nil {
			record(i, message.ToolResult{
				ToolCallID: toolCall.ID,
				Name:       toolCall.Name,
				Content:    fmt.Sprintf("Tool not found: %s", toolCall.Name),
				IsError:    true,
			})
			continue
		}

		if tracker.Track(toolCall.Name, toolCall.Input) {
			streak := tracker.streakCount[toolCall.Name]
			logging.Warn("Tool call loop detected",
				"tool", toolCall.Name,
				"streak", streak,
				"session_id", sessionID,
			)
			record(i, message.ToolResult{
				ToolCallID: toolCall.ID,
				Name:       toolCall.Name,
				Content:    loopDetectedMessage(toolCall.Name, streak),
				IsError:    true,
			})
			continue
		}

		entry := toolEntry{index: i, tool: tool, toolCall: toolCall}
		if a.allowParallelism && tool.AllowParallelism(allToolCalls[i], allToolCalls) {
			entry.parallel = true
			parallelGroup = append(parallelGroup, entry)
		} else {
			sequentialGroup = append(sequentialGroup, entry)
		}
	}

	logging.Debug("Tool execution groups",
		"parallel", len(parallelGroup),
		"sequential", len(sequentialGroup),
		"session_id", sessionID,
	)

	// Langfuse client for tool call tracing (declared here to avoid goto-over-decl)
	lfClient := langfuse.Get()

	// Phase 2: Execute parallel group concurrently
	permissionDenied := false
	if len(parallelGroup) > 0 {
		permCtx, permCancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		for _, entry := range parallelGroup {
			wg.Add(1)
			go func(e toolEntry) {
				defer wg.Done()
				now := time.Now()

				// Start Langfuse tool span
				var toolSpan *langfuse.Span
				if lfClient != nil && lfClient.Enabled() {
					var toolInput any
					if shouldLogToolInput(e.toolCall.Name) {
						toolInput = e.toolCall.Input
					}
					toolSpan = lfClient.ToolStart(ctx, langfuse.ToolParams{
						Name:  e.toolCall.Name,
						Input: toolInput,
					})
					defer toolSpan.End()
				}

				// PreToolUse: hooks may mutate Input or block this call.
				// Mutation chain runs synchronously before the tool dispatch
				// goroutine starts so the tool sees the final input.
				mutatedInput, hc := a.firePreTool(ctx, sessionID, e.toolCall.Name, e.toolCall.Input)
				if hc.decision.Block {
					reason := hc.decision.BlockReason
					if reason == "" {
						reason = "Tool call blocked by PreToolUse hook"
					}
					logging.Info("Tool call blocked by hook", "tool", e.toolCall.Name, "ID", e.toolCall.ID, "reason", reason)
					if toolSpan != nil {
						toolSpan.SetError(fmt.Errorf("blocked by hook: %s", reason))
					}
					record(e.index, message.ToolResult{
						ToolCallID: e.toolCall.ID,
						Name:       e.toolCall.Name,
						Content:    appendHookContext(reason, hc.decision.AdditionalContext),
						IsError:    true,
					})
					return
				}

				type runResult struct {
					resp tools.ToolResponse
					err  error
				}
				ch := make(chan runResult, 1)
				// ExplicitAllow from a PreToolUse hook bypasses the
				// in-tool permission check for this call only (D8).
				toolCtx := permCtx
				if hc.decision.ExplicitAllow {
					toolCtx = context.WithValue(permCtx, permission.HookAllowKey, true)
				}
				go func() {
					r, errTool := e.tool.Run(toolCtx, tools.ToolCall{
						ID:    e.toolCall.ID,
						Name:  e.toolCall.Name,
						Input: mutatedInput,
					})
					ch <- runResult{r, errTool}
				}()
				var toolResult tools.ToolResponse
				var toolErr error
				select {
				case <-permCtx.Done():
					if toolSpan != nil {
						toolSpan.SetError(permCtx.Err())
					}
					return
				case res := <-ch:
					toolResult, toolErr = res.resp, res.err
				}

				gauge := time.Since(now).Milliseconds()
				if toolErr != nil {
					if toolSpan != nil {
						toolSpan.SetError(toolErr)
					}
					if errors.Is(toolErr, permission.ErrorPermissionDenied) {
						logging.Warn("Tool call denied", "tool", e.toolCall.Name,
							"ID", e.toolCall.ID,
							"input", e.toolCall.Input,
							"gauge", gauge,
						)
						record(e.index, message.ToolResult{
							ToolCallID: e.toolCall.ID,
							Name:       e.toolCall.Name,
							Content:    "Permission denied",
							IsError:    true,
						})
						permCancel()
						return
					}
					logging.Error("Tool call failed", "tool", e.toolCall.Name,
						"ID", e.toolCall.ID,
						"input", e.toolCall.Input,
						"error", toolErr.Error(),
						"gauge", gauge,
					)
					record(e.index, message.ToolResult{
						ToolCallID: e.toolCall.ID,
						Name:       e.toolCall.Name,
						Content:    fmt.Sprintf("Tool returned error: %s", toolErr.Error()),
						IsError:    true,
					})
					return
				}
				if toolSpan != nil {
					if toolResult.IsError {
						toolSpan.SetError(fmt.Errorf("%s", toolResult.Content))
					} else if shouldLogToolOutput(e.toolCall.Name) {
						toolSpan.SetOutput(toolResult.Content)
					}
				}
				logging.Debug("Tool call completed", "tool", e.toolCall.Name,
					"ID", e.toolCall.ID,
					"input", e.toolCall.Input,
					"successful", !toolResult.IsError,
					"gauge", gauge,
				)
				// PostToolUse: hooks may mutate the result content before
				// it reaches the conversation history. Only fires when
				// the tool succeeded (IsError=false) per spec contract.
				resultContent := toolResult.Content
				var postCtx string
				if !toolResult.IsError {
					resultContent, postCtx = a.firePostTool(ctx, sessionID, e.toolCall.Name, mutatedInput, toolResult.Content)
				}
				// additionalContext from BOTH PreToolUse and PostToolUse
				// must reach the agent; spec requires it to be visible
				// on the next turn whether or not the hook blocked.
				resultContent = appendHookContext(resultContent, joinHookContext(hc.decision.AdditionalContext, postCtx))
				record(e.index, message.ToolResult{
					Type:       message.ToolResultType(toolResult.Type),
					Name:       e.toolCall.Name,
					ToolCallID: e.toolCall.ID,
					Content:    resultContent,
					Metadata:   toolResult.Metadata,
					IsError:    toolResult.IsError,
				})
			}(entry)
		}
		waitDone := make(chan struct{})
		go func() {
			wg.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
			// all parallel tools completed
		case <-ctx.Done():
			<-waitDone // wait for goroutines to drain after cancellation
		}
		permCancel()

		// Check if any parallel tool returned permission denied
		for _, entry := range parallelGroup {
			r := &toolResults[entry.index]
			if r.IsError && r.Content == "Permission denied" {
				permissionDenied = true
				break
			}
		}
	}

	// Check for user cancellation after parallel group
	if ctx.Err() != nil {
		a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled)
		for i, tc := range toolCalls {
			if toolResults[i].ToolCallID == "" {
				record(i, message.ToolResult{
					ToolCallID: tc.ID,
					Name:       tc.Name,
					Content:    "Tool execution canceled by user",
					IsError:    true,
				})
			}
		}
		goto out
	}

	// If permission denied in parallel group, skip sequential and fill remaining
	if permissionDenied {
		for _, entry := range sequentialGroup {
			record(entry.index, message.ToolResult{
				ToolCallID: entry.toolCall.ID,
				Name:       entry.toolCall.Name,
				Content:    "Tool execution canceled by user",
				IsError:    true,
			})
		}
		// Fill any unset parallel results (cancelled mid-flight)
		for _, entry := range parallelGroup {
			if toolResults[entry.index].ToolCallID == "" {
				record(entry.index, message.ToolResult{
					ToolCallID: entry.toolCall.ID,
					Name:       entry.toolCall.Name,
					Content:    "Tool execution canceled by user",
					IsError:    true,
				})
			}
		}
		a.finishMessage(ctx, &assistantMsg, message.FinishReasonPermissionDenied)
		goto out
	}

	// Phase 3: Execute sequential group
	for _, entry := range sequentialGroup {
		var seqToolSpan *langfuse.Span

		select {
		case <-ctx.Done():
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled)
			record(entry.index, message.ToolResult{
				ToolCallID: entry.toolCall.ID,
				Name:       entry.toolCall.Name,
				Content:    "Tool execution canceled by user",
				IsError:    true,
			})
			// Fill remaining sequential entries
			for _, remaining := range sequentialGroup {
				if toolResults[remaining.index].ToolCallID == "" {
					record(remaining.index, message.ToolResult{
						ToolCallID: remaining.toolCall.ID,
						Name:       remaining.toolCall.Name,
						Content:    "Tool execution canceled by user",
						IsError:    true,
					})
				}
			}
			goto out
		default:
		}

		// Start Langfuse tool span
		if lfClient != nil && lfClient.Enabled() {
			var seqToolInput any
			if shouldLogToolInput(entry.toolCall.Name) {
				seqToolInput = entry.toolCall.Input
			}
			seqToolSpan = lfClient.ToolStart(ctx, langfuse.ToolParams{
				Name:  entry.toolCall.Name,
				Input: seqToolInput,
			})
		}

		// PreToolUse: same contract as the parallel path. Mutated input
		// feeds the tool; block short-circuits with the hook's reason.
		seqMutatedInput, seqHC := a.firePreTool(ctx, sessionID, entry.toolCall.Name, entry.toolCall.Input)
		if seqHC.decision.Block {
			reason := seqHC.decision.BlockReason
			if reason == "" {
				reason = "Tool call blocked by PreToolUse hook"
			}
			logging.Info("Tool call blocked by hook", "tool", entry.toolCall.Name, "ID", entry.toolCall.ID, "reason", reason)
			if seqToolSpan != nil {
				seqToolSpan.SetError(fmt.Errorf("blocked by hook: %s", reason))
				seqToolSpan.End()
			}
			record(entry.index, message.ToolResult{
				ToolCallID: entry.toolCall.ID,
				Name:       entry.toolCall.Name,
				Content:    appendHookContext(reason, seqHC.decision.AdditionalContext),
				IsError:    true,
			})
			continue
		}

		now := time.Now()
		seqToolCtx := ctx
		if seqHC.decision.ExplicitAllow {
			seqToolCtx = context.WithValue(ctx, permission.HookAllowKey, true)
		}
		toolResult, toolErr := entry.tool.Run(seqToolCtx, tools.ToolCall{
			ID:    entry.toolCall.ID,
			Name:  entry.toolCall.Name,
			Input: seqMutatedInput,
		})
		gauge := time.Since(now).Milliseconds()
		if toolErr != nil {
			if seqToolSpan != nil {
				seqToolSpan.SetError(toolErr)
				seqToolSpan.End()
			}
			if errors.Is(toolErr, permission.ErrorPermissionDenied) {
				logging.Warn("Tool call denied", "tool", entry.toolCall.Name,
					"ID", entry.toolCall.ID,
					"input", entry.toolCall.Input,
					"gauge", gauge,
				)
				record(entry.index, message.ToolResult{
					ToolCallID: entry.toolCall.ID,
					Name:       entry.toolCall.Name,
					Content:    "Permission denied",
					IsError:    true,
				})
				for _, remaining := range sequentialGroup {
					if toolResults[remaining.index].ToolCallID == "" {
						record(remaining.index, message.ToolResult{
							ToolCallID: remaining.toolCall.ID,
							Name:       remaining.toolCall.Name,
							Content:    "Tool execution canceled by user",
							IsError:    true,
						})
					}
				}
				a.finishMessage(ctx, &assistantMsg, message.FinishReasonPermissionDenied)
				break
			}
			logging.Error("Tool call failed", "tool", entry.toolCall.Name,
				"ID", entry.toolCall.ID,
				"input", entry.toolCall.Input,
				"error", toolErr.Error(),
				"gauge", gauge,
			)
			record(entry.index, message.ToolResult{
				ToolCallID: entry.toolCall.ID,
				Name:       entry.toolCall.Name,
				Content:    fmt.Sprintf("Tool returned error: %s", toolErr.Error()),
				IsError:    true,
			})
			continue
		}
		if seqToolSpan != nil {
			if toolResult.IsError {
				seqToolSpan.SetError(fmt.Errorf("%s", toolResult.Content))
			} else if shouldLogToolOutput(entry.toolCall.Name) {
				seqToolSpan.SetOutput(toolResult.Content)
			}
			seqToolSpan.End()
		}
		logging.Debug("Tool call completed", "tool", entry.toolCall.Name,
			"ID", entry.toolCall.ID,
			"input", entry.toolCall.Input,
			"successful", !toolResult.IsError,
			"gauge", gauge,
		)
		seqResultContent := toolResult.Content
		var seqPostCtx string
		if !toolResult.IsError {
			seqResultContent, seqPostCtx = a.firePostTool(ctx, sessionID, entry.toolCall.Name, seqMutatedInput, toolResult.Content)
		}
		seqResultContent = appendHookContext(seqResultContent, joinHookContext(seqHC.decision.AdditionalContext, seqPostCtx))
		record(entry.index, message.ToolResult{
			Type:       message.ToolResultType(toolResult.Type),
			Name:       entry.toolCall.Name,
			ToolCallID: entry.toolCall.ID,
			Content:    seqResultContent,
			Metadata:   toolResult.Metadata,
			IsError:    toolResult.IsError,
		})
	}
out:
	if len(toolResults) == 0 {
		return assistantMsg, nil, nil
	}
	parts := make([]message.ContentPart, 0)
	for _, tr := range toolResults {
		parts = append(parts, tr)
	}
	msg, err := a.messages.Create(context.Background(), assistantMsg.SessionID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: parts,
	})
	if err != nil {
		return assistantMsg, nil, fmt.Errorf("failed to create cancelled tool message: %w", err)
	}

	return assistantMsg, &msg, nil
}

func (a *agent) finishMessage(ctx context.Context, msg *message.Message, finishReson message.FinishReason) {
	msg.AddFinish(finishReson)
	// When the caller's ctx is already cancelled (graceful shutdown, step
	// timeout, ErrRequestCancelled cleanup), a.messages.Update would fail
	// the SQL call immediately and the assistant message would persist with
	// parts=[] / finished_at=null — indistinguishable from "stream still
	// in flight" on subsequent inspection or resume. Fall back to a fresh
	// background context with a short deadline so the finish marker lands
	// regardless of how the agent loop is unwinding.
	writeCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		writeCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	_ = a.messages.Update(writeCtx, *msg)
}

// createErrorToolResults creates a tool results message with error results for all tool calls
// in the given assistant message. This ensures that every tool_use block in the DB
// has a corresponding tool_result, preventing API errors when the session is resumed.
func (a *agent) createErrorToolResults(assistantMsg message.Message) *message.Message {
	toolCalls := assistantMsg.ToolCalls()
	if len(toolCalls) == 0 {
		return nil
	}
	parts := make([]message.ContentPart, len(toolCalls))
	for i, tc := range toolCalls {
		parts[i] = message.ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    "Tool execution was interrupted",
			IsError:    true,
		}
	}
	msg, err := a.messages.Create(context.Background(), assistantMsg.SessionID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: parts,
	})
	if err != nil {
		logging.Warn("Failed to create error tool results message", "error", err)
		return nil
	}
	return &msg
}

// mergeToolCalls updates tool call Input from the accumulated response without replacing IDs.
// During streaming, tool calls are registered with IDs from ContentBlockStartEvent.
// The accumulated SDK response may carry different IDs (e.g. through LiteLLM/Vertex proxies).
// Tool results already reference the streaming IDs, so we must preserve them.
func (a *agent) mergeToolCalls(assistantMsg *message.Message, accumulated []message.ToolCall) {
	existing := assistantMsg.ToolCalls()
	if len(existing) == 0 {
		// No streaming tool calls — fall back to using the accumulated ones directly
		assistantMsg.SetToolCalls(accumulated)
		return
	}

	// Match by position: streaming events and accumulated blocks arrive in the same order
	for i, tc := range existing {
		if i < len(accumulated) {
			tc.Input = accumulated[i].Input
			tc.Finished = true
			assistantMsg.UpdateToolCall(tc)
		}
	}
}

func (a *agent) processEvent(ctx context.Context, sessionID string, assistantMsg *message.Message, event provider.ProviderEvent) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Continue processing.
	}

	switch event.Type {
	case provider.EventThinkingDelta:
		// Thinking deltas ride the Thinking field (Content is empty on
		// these events) — this feeds the live preview part; the
		// authoritative signed blocks replace it at EventComplete.
		assistantMsg.AppendReasoningContent(event.Thinking)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventContentDelta:
		assistantMsg.AppendContent(event.Content)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventToolUseStart:
		assistantMsg.AddToolCall(*event.ToolCall)
		// Emit per-part event so SSE consumers see the tool transition to
		// "pending" mid-stream. Snapshot from the message (post-AddToolCall)
		// so we publish the same shape that lands in the message store.
		if tc, ok := assistantMsg.FindToolCall(event.ToolCall.ID); ok {
			a.messages.PublishPart(sessionID, assistantMsg.ID, tc)
		}
		return a.messages.Update(ctx, *assistantMsg)
	// TODO: see how to handle this
	// case provider.EventToolUseDelta:
	// 	tm := time.Unix(assistantMsg.UpdatedAt, 0)
	// 	assistantMsg.AppendToolCallInput(event.ToolCall.ID, event.ToolCall.Input)
	// 	if time.Since(tm) > 1000*time.Millisecond {
	// 		err := a.messages.Update(ctx, *assistantMsg)
	// 		assistantMsg.UpdatedAt = time.Now().Unix()
	// 		return err
	// 	}
	case provider.EventToolUseStop:
		assistantMsg.FinishToolCall(event.ToolCall.ID)
		// "running" — provider finished assembling the call, tool execution
		// is about to begin. Snapshot reflects Finished=true.
		if tc, ok := assistantMsg.FindToolCall(event.ToolCall.ID); ok {
			a.messages.PublishPart(sessionID, assistantMsg.ID, tc)
		}
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventError:
		if errors.Is(event.Error, context.Canceled) {
			logging.InfoPersist(fmt.Sprintf("Event processing canceled for session: %s", sessionID))
			return context.Canceled
		}
		logging.ErrorPersist(event.Error.Error())
		return event.Error
	case provider.EventComplete:
		// HACK: validate if we really need it
		// Merge tool call data from the accumulated response without replacing IDs.
		// During streaming, tool calls are added via EventToolUseStart with their IDs,
		// and tool results reference those IDs. The accumulated response may carry
		// different IDs (e.g. through proxies), so we must preserve the streaming IDs
		// and only update the Input field which is accumulated by the SDK.
		a.mergeToolCalls(assistantMsg, event.Response.ToolCalls)
		// Replace streamed reasoning preview parts with the finalized
		// per-block list (text + signature verbatim) so the blocks can be
		// replayed on subsequent requests. When the provider reports no
		// reasoning, any preview parts stay as display-only (unsigned)
		// parts — same as canceled turns.
		if len(event.Response.Reasoning) > 0 {
			assistantMsg.SetReasoningParts(event.Response.Reasoning)
		}
		assistantMsg.AddFinish(event.Response.FinishReason)
		if err := a.messages.Update(ctx, *assistantMsg); err != nil {
			return fmt.Errorf("failed to update message: %w", err)
		}
		// Re-publish each ToolCall with its finalized Input. The
		// streaming path's EventToolUseStart / EventToolUseStop only
		// publishes the call shape (ID + Name), and non-streaming
		// providers (OpenAI / Gemini) never emit the per-call events
		// at all — they assemble everything in the EventComplete
		// payload. Without this republish, PartEvent subscribers
		// (chat bridge tool-update indicators, SSE consumers) see
		// the tool name without any context, and parallel calls
		// of the same tool can't be told apart from their results.
		// Publishing post-merge gives subscribers the canonical
		// shape that lands in the message store.
		for _, tc := range assistantMsg.ToolCalls() {
			a.messages.PublishPart(sessionID, assistantMsg.ID, tc)
		}
		return a.TrackUsage(ctx, sessionID, a.provider.Model(), event.Response.Usage)
	}

	return nil
}

func (a *agent) TrackUsage(ctx context.Context, sessionID string, model models.Model, usage provider.TokenUsage) error {
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	inputCost, outputCost := provider.CalculateCost(model, usage)
	cost := inputCost + outputCost

	sess.Cost += cost
	sess.CompletionTokens = usage.OutputTokens + usage.CacheReadTokens
	sess.PromptTokens = usage.InputTokens + usage.CacheCreationTokens
	sess.TotalCompletionTokens += usage.OutputTokens
	sess.TotalPromptTokens += usage.InputTokens + usage.CacheCreationTokens + usage.CacheReadTokens

	logging.Info("Track usage",
		"token_out_total", sess.CompletionTokens,
		"token_in_total", sess.PromptTokens,
		"token_in", usage.InputTokens,
		"token_out", usage.OutputTokens,
		"cache_created", usage.CacheCreationTokens,
		"cache_read", usage.CacheReadTokens,
		"cost", cost,
	)

	_, err = a.sessions.Save(ctx, sess)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func (a *agent) Update(agentName config.AgentName, modelID models.ModelID) (models.Model, error) {
	if a.IsBusy() {
		return models.Model{}, ErrAgentBusy
	}

	if err := config.UpdateAgentModel(agentName, modelID); err != nil {
		return models.Model{}, fmt.Errorf("failed to update config: %w", err)
	}

	provider, err := createAgentProvider(agentName)
	if err != nil {
		return models.Model{}, fmt.Errorf("failed to create provider for model %s: %w", modelID, err)
	}

	a.provider = provider

	return a.provider.Model(), nil
}

// shouldTriggerAutoCompaction checks if the session should trigger auto-compaction
// based on token usage approaching the context window limit
// filterMessagesFromSummary filters messages to start from the summary message if one exists.
// filterEmptyUserMessages drops User messages that carry only empty
// TextContent and no other parts (no attachments, no tool results). Those
// are the corruption pattern left behind by older builds where
// `createUserMessage(ctx, sid, "", nil)` ran on auto-resume — the persisted
// `user(text="")` makes the Anthropic/Vertex/Bedrock API reject every
// subsequent agent.Run on the session with `messages: text content blocks
// must be non-empty` (HTTP 400). Filtering at read time auto-recovers
// these sessions without needing a DB migration or manual cleanup.
// captureStructOutput merges the struct_output tool result (if any) from a
// tool-results message into the running (structOutput, isErr) pair. A
// non-error result always wins — including over an earlier success, so a
// model that re-emits struct_output after reacting to background-task
// completions (the deferred-finish path) gets its update through. An error
// (schema-rejected) result is kept only as a fallback while no result
// exists at all, so the final AgentEvent can still surface it; it never
// downgrades a captured success.
func captureStructOutput(toolResults *message.Message, structOutput *message.ToolResult, isErr bool) (*message.ToolResult, bool) {
	if s, ok := toolResults.StructOutput(); ok || structOutput == nil {
		return s, !ok
	}
	return structOutput, isErr
}

func filterEmptyUserMessages(msgs []message.Message) []message.Message {
	out := msgs[:0]
	for _, m := range msgs {
		if isEmptyUserTextMessage(m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func isEmptyUserTextMessage(m message.Message) bool {
	if m.Role != message.User || len(m.Parts) == 0 {
		return false
	}
	sawEmptyText := false
	for _, p := range m.Parts {
		switch part := p.(type) {
		case message.TextContent:
			// Whitespace-only counts as empty; any real text makes the
			// message legitimate.
			if strings.TrimSpace(part.Text) != "" {
				return false
			}
			sawEmptyText = true
		case message.Finish:
			// message.Service.Create unconditionally appends a Finish
			// marker to every non-assistant message. It's metadata, not
			// content — ignore when assessing emptiness.
			continue
		default:
			// Any other content type (BinaryContent attachment,
			// ImageURLContent, ToolResult, etc.) carries real payload —
			// the message is not "empty" for filter purposes.
			return false
		}
	}
	return sawEmptyText
}

// This reduces context size by excluding messages before the summary.
// It ensures that tool_use/tool_result pairs are not split by the filter boundary.
func (a *agent) filterMessagesFromSummary(msgs []message.Message, summaryMessageID string) []message.Message {
	if summaryMessageID == "" {
		return msgs
	}

	summaryMsgIndex := -1
	for i, msg := range msgs {
		if msg.ID == summaryMessageID {
			summaryMsgIndex = i
			break
		}
	}

	if summaryMsgIndex == -1 {
		return msgs
	}

	filteredMsgs := msgs[summaryMsgIndex:]
	// Convert the summary message role to User so it can be used in conversation
	filteredMsgs[0].Role = message.User

	// Ensure the filtered messages don't start with orphaned tool results
	// (Tool messages whose corresponding Assistant message was before the summary).
	// Skip any Tool messages that immediately follow the summary message.
	result := filteredMsgs[:1] // Always keep the summary message
	skippingOrphanedTools := true
	for _, msg := range filteredMsgs[1:] {
		if skippingOrphanedTools {
			if msg.Role == message.Tool {
				logging.Warn("Skipping orphaned tool result message after summary filter", "message_id", msg.ID)
				continue
			}
			skippingOrphanedTools = false
		}
		result = append(result, msg)
	}
	return result
}

// performSynchronousCompaction performs summarization synchronously and waits for completion
// This is used for auto-compaction in non-interactive mode to shrink context before continuing
func (a *agent) performSynchronousCompaction(ctx context.Context, sessionID string) error {
	if a.summarizeProvider == nil {
		return fmt.Errorf("summarize provider not available")
	}

	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to list messages: %w", err)
	}

	// NOTE: We don't check IsSessionBusy here because this is called from within
	logging.Info("Starting synchronous compaction", "session_id", sessionID, "message_count", len(msgs))

	if len(msgs) == 0 {
		return fmt.Errorf("no messages to summarize")
	}

	summarizeCtx := context.WithValue(ctx, tools.SessionIDContextKey, sessionID)
	summarizeCtx = context.WithValue(summarizeCtx, tools.AgentIDContextKey, config.AgentName("summarizer"))
	if lf := langfuse.Get(); lf != nil && lf.Enabled() {
		sess, sessErr := a.sessions.Get(ctx, sessionID)
		if sessErr == nil {
			summarizeCtx = a.createLangfuseTrace(summarizeCtx, sess)
		}
	}
	defer langfuse.EndTrace(summarizeCtx)
	summarizePrompt, err := AgentPrompts.ReadFile("prompts/compaction.md")
	if err != nil {
		return fmt.Errorf("failed to load summary prompt: %w", err)
	}

	promptMsg := message.Message{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: string(summarizePrompt)}},
	}

	msgsWithPrompt := append(msgs, promptMsg)
	events := a.summarizeProvider.StreamResponse(
		summarizeCtx,
		msgsWithPrompt,
		make([]tools.BaseTool, 0),
	)
	response, err := provider.StreamToResponse(events)
	if err != nil {
		return fmt.Errorf("failed to summarize: %w", err)
	}

	summary := strings.TrimSpace(response.Content)
	if summary == "" {
		return fmt.Errorf("empty summary returned")
	}

	// Get the session to update
	oldSession, err := a.sessions.Get(summarizeCtx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	// Create a new message with the summary
	msg, err := a.messages.Create(summarizeCtx, oldSession.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: summary}},
		Model: a.summarizeProvider.Model().ID,
	})
	if err != nil {
		return fmt.Errorf("failed to create summary message: %w", err)
	}

	oldSession.SummaryMessageID = msg.ID
	oldSession.CompletionTokens = response.Usage.OutputTokens
	oldSession.PromptTokens = 0
	oldSession.TotalCompletionTokens += response.Usage.OutputTokens
	oldSession.TotalPromptTokens += response.Usage.InputTokens + response.Usage.CacheCreationTokens + response.Usage.CacheReadTokens
	inCost, outCost := provider.CalculateCost(a.summarizeProvider.Model(), response.Usage)
	oldSession.Cost += inCost + outCost

	_, err = a.sessions.Save(summarizeCtx, oldSession)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	logging.Info("Synchronous compaction completed successfully", "session_id", sessionID)
	return nil
}

func (a *agent) Summarize(ctx context.Context, sessionID string) error {
	if a.summarizeProvider == nil {
		return fmt.Errorf("summarize provider not available")
	}

	// Check if session is busy
	if a.IsSessionBusy(sessionID) {
		return ErrSessionBusy
	}

	// Create a new context with cancellation
	summarizeCtx, cancel := context.WithCancel(ctx)

	// Store the cancel function in activeRequests to allow cancellation
	a.activeRequests.Store(sessionID+"-summarize", cancel)

	go func() {
		defer a.activeRequests.Delete(sessionID + "-summarize")
		defer cancel()
		event := AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Starting summarization...",
		}

		a.Publish(pubsub.CreatedEvent, event)
		// Get all messages from the session
		msgs, err := a.messages.List(summarizeCtx, sessionID)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to list messages: %w", err),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		summarizeCtx = context.WithValue(summarizeCtx, tools.SessionIDContextKey, sessionID)
		summarizeCtx = context.WithValue(summarizeCtx, tools.AgentIDContextKey, config.AgentName("summarizer"))
		if lf := langfuse.Get(); lf != nil && lf.Enabled() {
			sess, sessErr := a.sessions.Get(summarizeCtx, sessionID)
			if sessErr == nil {
				summarizeCtx = a.createLangfuseTrace(summarizeCtx, sess)
			}
		}
		defer langfuse.EndTrace(summarizeCtx)

		if len(msgs) == 0 {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("no messages to summarize"),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}

		event = AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Analyzing conversation...",
		}
		a.Publish(pubsub.CreatedEvent, event)

		summarizePrompt, err := AgentPrompts.ReadFile("prompts/compaction.md")
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to load summary prompt: %w", err),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}

		// Create a new message with the summarize prompt
		promptMsg := message.Message{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: string(summarizePrompt)}},
		}

		// Append the prompt to the messages
		msgsWithPrompt := append(msgs, promptMsg)

		event = AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Generating summary...",
		}

		a.Publish(pubsub.CreatedEvent, event)

		// Send the messages to the summarize provider via streaming
		// to avoid Anthropic's non-streaming timeout restriction
		events := a.summarizeProvider.StreamResponse(
			summarizeCtx,
			msgsWithPrompt,
			make([]tools.BaseTool, 0),
		)
		response, err := provider.StreamToResponse(events)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to summarize: %w", err),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}

		summary := strings.TrimSpace(response.Content)
		if summary == "" {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("empty summary returned"),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		event = AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Creating new session...",
		}

		a.Publish(pubsub.CreatedEvent, event)
		oldSession, err := a.sessions.Get(summarizeCtx, sessionID)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to get session: %w", err),
				Done:  true,
			}

			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		// Create a message in the new session with the summary
		msg, err := a.messages.Create(summarizeCtx, oldSession.ID, message.CreateMessageParams{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: summary},
				message.Finish{
					Reason: message.FinishReasonEndTurn,
					Time:   time.Now().Unix(),
				},
			},
			Model: a.summarizeProvider.Model().ID,
		})
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to create summary message: %w", err),
				Done:  true,
			}

			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		oldSession.SummaryMessageID = msg.ID
		oldSession.CompletionTokens = response.Usage.OutputTokens
		oldSession.PromptTokens = 0
		oldSession.TotalCompletionTokens += response.Usage.OutputTokens
		oldSession.TotalPromptTokens += response.Usage.InputTokens + response.Usage.CacheCreationTokens + response.Usage.CacheReadTokens
		inCost, outCost := provider.CalculateCost(a.summarizeProvider.Model(), response.Usage)
		oldSession.Cost += inCost + outCost
		_, err = a.sessions.Save(summarizeCtx, oldSession)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to save session: %w", err),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
		}

		event = AgentEvent{
			Type:      AgentEventTypeSummarize,
			SessionID: oldSession.ID,
			Progress:  "Summary complete",
			Done:      true,
		}
		a.Publish(pubsub.CreatedEvent, event)
		// Send final success event with the new session ID
	}()

	return nil
}

// SummarizeSync compacts the session synchronously, returning only once the
// summary message has been written and the session's token counts updated.
// Callers that need a deterministic completion signal (e.g. the chat bridge's
// /compact command, which posts a "done" reply with the new context size) use
// this instead of the async, event-driven Summarize. It takes the same
// session-busy lock a Run/cron uses, so it will not interleave with an
// in-flight turn and returns ErrSessionBusy when the session is occupied.
func (a *agent) SummarizeSync(ctx context.Context, sessionID string) error {
	if a.summarizeProvider == nil {
		return fmt.Errorf("summarize provider not available")
	}
	if !a.TryLockSession(sessionID) {
		return ErrSessionBusy
	}
	defer a.UnlockSession(sessionID)
	return a.performSynchronousCompaction(ctx, sessionID)
}

type providerOptions struct {
	disableCache bool
	// interactive is propagated to prompt.GetAgentPromptWithOptions
	// so the interactive-step variant of the structured-output prompt
	// is selected when this agent is running an `interactive: true`
	// flow step.
	interactive bool
	// boundPeers is the resolved chat-bridge peer list (from
	// resolveInteractionTarget) for this interactive step. Passed
	// through to prompt.AgentPromptOptions so the "## Reviewer
	// details" section knows the mention handle + channel + peerId.
	boundPeers []bridge.PeerRef
}

type providerOption func(*providerOptions)

func withDisableCache() providerOption {
	return func(o *providerOptions) {
		o.disableCache = true
	}
}

func withInteractive(b bool) providerOption {
	return func(o *providerOptions) {
		o.interactive = b
	}
}

// withBoundPeers carries the resolved chat-bridge peers through to
// prompt.AgentPromptOptions. Empty / nil for non-interactive callers
// — the prompt builder gates the reviewer-details section on the
// interactive branch anyway, so a nil here is a no-op.
func withBoundPeers(peers []bridge.PeerRef) providerOption {
	return func(o *providerOptions) {
		o.boundPeers = peers
	}
}

func createAgentProvider(agentName config.AgentName, providerOpts ...providerOption) (agentProvider provider.Provider, err error) {
	var popts providerOptions
	for _, o := range providerOpts {
		o(&popts)
	}
	defer func() {
		if err == nil {
			logging.Info("Agent provider created", "agent", agentName, "model", agentProvider.Model())
		}
	}()
	cfg := config.Get()
	agentConfig, ok := cfg.Agents[agentName]
	if !ok {
		// Try registry for custom (markdown-defined) agents
		reg := agentregistry.GetRegistry()
		if info, found := reg.Get(agentName); found && info.Model != "" {
			agentConfig = config.Agent{
				Model:           models.ModelID(info.Model),
				MaxTokens:       info.MaxTokens,
				ReasoningEffort: info.ReasoningEffort,
			}
		} else if found {
			// Inherit coder's model if no model specified
			coderCfg, coderOk := cfg.Agents[config.AgentCoder]
			if !coderOk {
				return nil, fmt.Errorf("agent %s has no model and coder agent not configured", agentName)
			}
			agentConfig = config.Agent{
				Model:           coderCfg.Model,
				MaxTokens:       coderCfg.MaxTokens,
				ReasoningEffort: coderCfg.ReasoningEffort,
			}
		} else {
			return nil, fmt.Errorf("agent %s not found", agentName)
		}
	}
	model, ok := models.SupportedModels[agentConfig.Model]
	if !ok {
		return nil, fmt.Errorf("model %s not supported", agentConfig.Model)
	}

	providerCfg, ok := cfg.Providers[model.Provider]
	if !ok {
		return nil, fmt.Errorf("provider %s not supported", model.Provider)
	}
	if providerCfg.Disabled {
		return nil, fmt.Errorf("provider %s is not enabled", model.Provider)
	}
	maxTokens := model.DefaultMaxTokens
	if agentConfig.MaxTokens > 0 {
		maxTokens = agentConfig.MaxTokens
	}

	opts := []provider.ProviderClientOption{
		provider.WithAPIKey(providerCfg.APIKey),
		provider.WithModel(model),
		provider.WithSystemMessage(prompt.GetAgentPromptWithOptions(agentName, model.Provider, prompt.AgentPromptOptions{
			Interactive: popts.interactive,
			BoundPeers:  popts.boundPeers,
		})),
		provider.WithMaxTokens(maxTokens),
	}
	if providerCfg.BaseURL != "" {
		opts = append(opts, provider.WithBaseURL(providerCfg.BaseURL))
	}
	if len(providerCfg.Headers) != 0 {
		opts = append(opts, provider.WithHeaders(providerCfg.Headers))
	}
	if providerCfg.Metadata != nil {
		opts = append(opts, provider.WithMetadata(providerCfg.Metadata))
	}
	if lf := langfuse.Get(); lf != nil && lf.Enabled() {
		opts = append(opts, provider.WithLangfuse(lf))
	}

	if model.Provider == models.ProviderOpenAI || model.Provider == models.ProviderYandexCloud || model.Provider == models.ProviderLocal && model.CanReason {
		openaiOpts := []provider.OpenAIOption{
			provider.WithReasoningEffort(agentConfig.ReasoningEffort),
		}
		if model.UseLegacyMaxTokens {
			openaiOpts = append(openaiOpts, provider.WithLegacyMaxTokens())
		}
		if popts.disableCache {
			openaiOpts = append(openaiOpts, provider.WithOpenAIDisableCache())
		}
		opts = append(
			opts,
			provider.WithOpenAIOptions(openaiOpts...),
		)
	} else if model.Provider == models.ProviderAnthropic || model.Provider == models.ProviderVertexAI || model.Provider == models.ProviderBedrock || model.Provider == models.ProviderKimi {
		var anthropicOpts []provider.AnthropicOption
		if model.CanReason {
			anthropicOpts = append(anthropicOpts, provider.WithAnthropicShouldThinkFn(provider.DefaultShouldThinkFn))
			if model.SupportsAdaptiveThinking {
				anthropicOpts = append(anthropicOpts, provider.WithAnthropicReasoningEffort(agentConfig.ReasoningEffort))
			}
			if agentConfig.TaskBudget > 0 && model.SupportsTaskBudget {
				anthropicOpts = append(anthropicOpts, provider.WithAnthropicTaskBudget(agentConfig.TaskBudget))
			}
		}
		if popts.disableCache {
			anthropicOpts = append(anthropicOpts, provider.WithAnthropicDisableCache())
		}
		if len(anthropicOpts) > 0 {
			opts = append(opts, provider.WithAnthropicOptions(anthropicOpts...))
		}
	} else if model.Provider == models.ProviderGemini && popts.disableCache {
		opts = append(opts, provider.WithGeminiOptions(provider.WithGeminiDisableCache()))
	}

	agentProvider, err = provider.NewProvider(
		model.Provider,
		opts...,
	)
	if err != nil {
		return nil, fmt.Errorf("could not create provider: %v", err)
	}

	return agentProvider, nil
}

// createLangfuseTrace creates a Langfuse trace for the current agent generation
// and returns a context enriched with the root trace span.
// If Langfuse is not initialized, the context is returned unchanged.
// When running inside a flow, the trace name uses the format "agentID/flowID/stepID"
// and flow-specific metadata (flowID, stepID, extracted flow args) is included.
func (a *agent) createLangfuseTrace(ctx context.Context, sess session.Session) context.Context {
	lf := langfuse.Get()
	if lf == nil || !lf.Enabled() {
		return ctx
	}

	rootSessionID := sess.RootSessionID
	if rootSessionID == "" {
		rootSessionID = sess.ID
	}

	tags := provider.ResolveTags(ctx)

	metadata := map[string]any{
		"agent_id":   string(a.AgentID()),
		"session_id": sess.ID,
	}
	if sess.ParentSessionID != "" {
		metadata["parent_session_id"] = sess.ParentSessionID
	}

	// Build trace name — enrich with flow context when available
	traceName := string(a.AgentID())
	flowID, _ := ctx.Value(tools.FlowIDContextKey).(string)
	flowStepID, _ := ctx.Value(tools.FlowStepIDContextKey).(string)
	flowStepIteration, _ := ctx.Value(tools.FlowStepIterationContextKey).(int)

	if flowID != "" {
		metadata["flow_id"] = flowID
		if flowStepID != "" {
			metadata["flow_step_id"] = flowStepID
			// Compose the full name including the optional `#N` iteration
			// suffix in one go, then truncate once. Truncating in two passes
			// can strip the suffix when the agent/flow/step concatenation
			// already hits maxTraceNameLen.
			fullName := fmt.Sprintf("%s/%s/%s", a.AgentID(), flowID, flowStepID)
			if flowStepIteration > 1 {
				metadata["flow_step_iteration"] = fmt.Sprintf("%d", flowStepIteration)
				fullName = fmt.Sprintf("%s#%d", fullName, flowStepIteration)
			}
			traceName = truncateStr(fullName, maxTraceNameLen)
		} else {
			traceName = truncateStr(fmt.Sprintf("%s/%s", a.AgentID(), flowID), maxTraceNameLen)
		}

		// Include extracted flow args as dedicated metadata fields.
		// No prefix — the metadata namespace already ensures uniqueness.
		if flowArgs, ok := ctx.Value(tools.FlowArgsContextKey).(map[string]string); ok {
			for k, v := range flowArgs {
				metadata[k] = truncateStr(v, maxMetadataValueLen)
			}
		}
	}

	// Apply metadata namespace prefix when configured so custom keys are
	// grouped under a common prefix in Langfuse (e.g. "app.flow_id").
	if cfg := config.Get(); cfg.Telemetry != nil && cfg.Telemetry.MetadataNamespace != "" {
		metadata = langfuse.NamespaceMetadata(metadata, cfg.Telemetry.MetadataNamespace)
	}

	return lf.TraceStart(ctx, langfuse.TraceParams{
		Name:      traceName,
		SessionID: rootSessionID,
		UserID:    provider.GetUserID(),
		Tags:      tags,
		Release:   version.Version,
		Metadata:  metadata,
		IsChild:   sess.ParentSessionID != "",
	})
}

const (
	maxTraceNameLen     = 200
	maxMetadataValueLen = 200
)

// shouldLogToolInput returns true if the tool's input should be logged to telemetry.
func shouldLogToolInput(toolName string) bool {
	cfg := config.Get()
	if cfg.Telemetry == nil || cfg.Telemetry.Tools == nil || !cfg.Telemetry.Tools.Enabled {
		return false
	}
	return matchAnyPattern(cfg.Telemetry.Tools.LogInput, toolName)
}

// shouldLogToolOutput returns true if the tool's output should be logged to telemetry.
func shouldLogToolOutput(toolName string) bool {
	cfg := config.Get()
	if cfg.Telemetry == nil || cfg.Telemetry.Tools == nil || !cfg.Telemetry.Tools.Enabled {
		return false
	}
	return matchAnyPattern(cfg.Telemetry.Tools.LogOutput, toolName)
}

// matchAnyPattern returns true if the name matches any of the wildcard patterns.
func matchAnyPattern(patterns []string, name string) bool {
	for _, p := range patterns {
		if permission.MatchWildcard(p, name) {
			return true
		}
	}
	return false
}

// truncateStr truncates a string to at most max bytes, appending "..." if truncated.
func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
