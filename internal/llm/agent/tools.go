package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/install"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
)

var (
	viewerToolNames = []string{
		tools.LSToolName,
		tools.GlobToolName,
		tools.GrepToolName,
		tools.ReadToolName,
		tools.ViewImageToolName,
		tools.WebFetchToolName,
		tools.SkillToolName,
		tools.SourcegraphToolName,
	}
	editorToolNames = []string{
		tools.WriteToolName,
		tools.EditToolName,
		tools.MultiEditToolName,
		tools.DeleteToolName,
		tools.PatchToolName,
		tools.BashToolName,
		// Background-task tools spawn/kill subprocesses or subagents and are
		// available to both agents and subagents (subagents may want to
		// monitor or kill their own background work too).
		tools.MonitorToolName,
		tools.TaskListToolName,
		tools.TaskStopToolName,
	}
	managerToolNames = []string{
		TaskToolName,
		tools.QuestionToolName,
		tools.CronCreateToolName,
		tools.CronDeleteToolName,
		tools.CronListToolName,
		tools.TodoWriteToolName,
		tools.RouterSendToolName,
	}
)

// NewToolSet dynamically builds the tool slice for an agent based on its
// registry info. Only tools that pass registry.IsToolEnabled are included.
// Deliberately context-free: MCP tool loading runs under the registry's own
// lifetime, so a short-lived creator (e.g. an async task tool call whose ctx
// dies with its parallel batch) cannot strand the agent without MCP tools.
func NewToolSet(
	info *agentregistry.AgentInfo,
	reg agentregistry.Registry,
	permissions permission.Service,
	historyService history.Service,
	lspService lsp.LspService,
	sessions session.Service,
	messages message.Service,
	mcpRegistry MCPRegistry,
	factory AgentFactory,
) <-chan tools.BaseTool {
	agentID := info.ID
	result := make(chan tools.BaseTool, 100)

	// Deferral is opt-in per agent. Fail-open: deferring tools without the
	// toolsearch discovery ladder would strand them (fallback providers omit
	// their schemas entirely), so a config that disables toolsearch while
	// declaring deferrals is ignored wholesale.
	deferredCfg := info.DeferredTools
	if len(deferredCfg) > 0 && !reg.IsToolEnabled(agentID, tools.ToolSearchToolName) {
		logging.Warn("deferredTools configured but toolsearch is disabled; ignoring deferral (fail-open)", "agent", agentID)
		deferredCfg = nil
	}
	// One activation-sequence counter per toolset: the fallback path appends
	// activated tools in this order, keeping previously sent positions stable.
	deferSeq := &atomic.Int64{}
	maybeDefer := func(t tools.BaseTool) tools.BaseTool {
		if t == nil || len(deferredCfg) == 0 {
			return t
		}
		if permission.IsToolDeferred(t.Info().Name, deferredCfg) {
			return tools.WrapDeferred(t, deferSeq)
		}
		return t
	}

	// Progressive context disclosure (design D9/D11): ONE state object
	// shared by every trigger-tool wrapper of this toolset — per-wrapper
	// state would break cross-tool dedup (a read fires the injection; a
	// grep on the same directory must not re-inject) and double-count the
	// session byte budget. nil when discovery is disabled, the agent or
	// step opted out, or the walk found nothing: then no wrapper is
	// installed at all — zero allocation, zero behavior change.
	disclosure := newContextDisclosureState(info, config.Get())
	// WRAP ORDER INVARIANT: disclosure is applied INSIDE deferral —
	// maybeDefer(maybeWrapDisclosure(t)) — so *tools.DeferredWrapper stays
	// the outermost type and the existing type assertions on it (provider
	// serialization, deferred-delta announcements, toolsearch activation
	// backfill, resolveTools logging) keep working for a tool that is both
	// deferred and a disclosure trigger.
	maybeWrapDisclosure := func(t tools.BaseTool) tools.BaseTool {
		if t == nil || disclosure == nil || !contextDisclosureTriggers[t.Info().Name] {
			return t
		}
		return &contextDisclosureWrapper{inner: t, state: disclosure}
	}

	// When adding a case here, also add the tool to
	// tools/description_budget_test.go so its description stays budgeted.
	createTool := func(name string) tools.BaseTool {
		switch name {
		case tools.LSToolName:
			return tools.NewLsTool(config.Get(), reg, permissions)
		case tools.GlobToolName:
			return tools.NewGlobTool(reg, permissions)
		case tools.GrepToolName:
			return tools.NewGrepTool(reg, permissions)
		case tools.ReadToolName:
			return tools.NewReadTool(lspService, reg, permissions)
		case tools.ViewImageToolName:
			return tools.NewViewImageTool()
		case tools.WebFetchToolName:
			return tools.NewFetchTool(reg, permissions)
		case tools.SkillToolName:
			return tools.NewSkillTool(permissions, reg, agentID)
		case tools.SourcegraphToolName:
			return tools.NewSourcegraphTool()
		case tools.WebSearchToolName:
			return tools.NewWebSearchTool(reg, tools.NewSearchProviderRegistry(config.Get()), permissions)
		case tools.WriteToolName:
			return tools.NewWriteTool(lspService, permissions, historyService, reg)
		case tools.EditToolName:
			return tools.NewEditTool(lspService, permissions, historyService, reg)
		case tools.MultiEditToolName:
			return tools.NewMultiEditTool(lspService, permissions, historyService, reg)
		case tools.DeleteToolName:
			return tools.NewDeleteTool(permissions, historyService, reg)
		case tools.PatchToolName:
			return tools.NewPatchTool(lspService, permissions, historyService, reg)
		case tools.BashToolName:
			return tools.NewBashTool(permissions, reg)
		case TaskToolName:
			return NewAgentTool(sessions, permissions, reg, factory)
		case tools.CronCreateToolName:
			if svc, helper := factory.CronServices(); svc != nil {
				return tools.NewCronCreateTool(svc, helper)
			}
			return nil
		case tools.CronDeleteToolName:
			if svc, _ := factory.CronServices(); svc != nil {
				return tools.NewCronDeleteTool(svc)
			}
			return nil
		case tools.CronListToolName:
			if svc, helper := factory.CronServices(); svc != nil {
				return tools.NewCronListTool(svc, helper)
			}
			return nil
		case tools.QuestionToolName:
			if qSvc := factory.QuestionService(); qSvc != nil {
				return tools.NewQuestionTool(qSvc, permissions)
			}
			return nil
		case tools.TodoWriteToolName:
			if store := factory.TodoStore(); store != nil {
				return tools.NewTodoWriteTool(store)
			}
			return nil
		case tools.MonitorToolName:
			return tools.NewMonitorTool(permissions, reg)
		case tools.TaskListToolName:
			return tools.NewTaskListTool()
		case tools.TaskStopToolName:
			return tools.NewTaskStopTool(permissions, reg)
		case tools.RouterSendToolName:
			// Conditional registration per chat-bridge-agent-tool spec:
			// (a) agent mode (enforced by managerToolNames branch's
			//     info.Mode == AgentModeAgent gate) and
			// (b) at least one configured + enabled channel.
			sender, cfg, mediaRoot := factory.BridgeSender()
			if sender == nil || !tools.ShouldRegisterRouterSend(cfg) {
				return nil
			}
			return tools.NewRouterSendTool(tools.RouterSendDeps{
				Sender:    sender,
				Cfg:       cfg,
				MediaRoot: mediaRoot,
			})
		default:
			return nil
		}
	}

	for _, name := range viewerToolNames {
		if reg.IsToolEnabled(agentID, name) {
			if t := createTool(name); t != nil {
				result <- maybeDefer(maybeWrapDisclosure(t))
			}
		}
	}

	// Only add websearch tool if providers are configured
	cfg := config.Get()
	if cfg != nil && cfg.WebSearch != nil && len(cfg.WebSearch.Providers) > 0 {
		if reg.IsToolEnabled(agentID, tools.WebSearchToolName) {
			if t := createTool(tools.WebSearchToolName); t != nil {
				result <- maybeDefer(t)
			}
		}
	}

	for _, name := range editorToolNames {
		if reg.IsToolEnabled(agentID, name) {
			if t := createTool(name); t != nil {
				result <- maybeDefer(maybeWrapDisclosure(t))
			}
		}
	}

	for _, name := range managerToolNames {
		// Cron tools are default-deny: an agent must opt in by setting the
		// tool to true in its config. Without this hivemind would inherit
		// "enabled" for any tool not explicitly listed in its Tools map.
		isCronTool := name == tools.CronCreateToolName ||
			name == tools.CronDeleteToolName ||
			name == tools.CronListToolName

		var enabled bool
		if isCronTool {
			enabled = reg.IsToolExplicitlyEnabled(agentID, name)
		} else {
			enabled = reg.IsToolEnabled(agentID, name)
		}

		if enabled {
			if info.Mode == config.AgentModeAgent {
				if t := createTool(name); t != nil {
					result <- maybeDefer(t)
				}
			} else {
				logging.Warn("Subagent can't have manager tools enabled, tool will be ignored", "agent", agentID, "tool", name)
			}
		}
	}

	// Inject struct_output tool if the agent has an output schema configured
	if info.Output != nil && info.Output.Schema != nil {
		if reg.IsToolEnabled(agentID, tools.StructOutputToolName) {
			schema := info.Output.Schema
			baseDir := ""
			if info.Location != "" {
				baseDir = filepath.Dir(info.Location)
			}
			resolved, err := format.ResolveSchemaRef(schema, baseDir)
			if err != nil {
				logging.Error("Failed to resolve output schema $ref", "agent", agentID, "error", err)
			} else {
				logging.Info("Using structured output", "agent", agentID, "schema", resolved)
				result <- tools.NewStructOutputTool(resolved)
			}
		}
	}

	if len(deferredCfg) > 0 {
		// Registered whenever deferral is in effect — regardless of model
		// (mid-session model switches must not strand deferred tools) and
		// regardless of whether any known tool currently matches (MCP-only
		// patterns match tools that arrive asynchronously below). Providers
		// decide per request whether to serialize it.
		result <- tools.NewToolSearchTool()
	}

	wg := sync.WaitGroup{}

	// MCP tools — shared instances, filter per agent
	wg.Add(1)
	go func() {
		defer logging.RecoverPanic("MCP-goroutine", nil)
		defer wg.Done()
		for mt := range mcpRegistry.LoadTools(nil) {
			if reg.IsToolEnabled(agentID, mt.Info().Name) {
				result <- maybeDefer(mt)
			}
		}
	}()

	// LSP tools – can be properly initialised only after servers up and running
	wg.Add(1)
	go func() {
		defer logging.RecoverPanic("LSP-goroutine", nil)
		defer wg.Done()
		cfg := config.Get()
		if len(install.ResolveServers(cfg)) > 0 && reg.IsToolEnabled(agentID, tools.LSPToolName) {
			result <- maybeDefer(tools.NewLspTool(lspService))
		}
	}()

	go func() {
		wg.Wait()
		close(result)
	}()

	return result
}

func (a *agent) Tools() []tools.BaseTool {
	return a.resolveTools()
}

// ResolvedTools returns the current tool set without blocking.
// The bool is true once tools have finished loading.
func (a *agent) ResolvedTools() ([]tools.BaseTool, bool) {
	if a.toolsResolved.Load() {
		return a.tools, true
	}
	return nil, false
}

func (a *agent) resolveTools() []tools.BaseTool {
	a.toolsOnce.Do(func() {
		toolSet := make([]tools.BaseTool, 0, 20)
		toolNames := make([]string, 0, 20)
		for t := range a.toolsCh {
			toolSet = append(toolSet, t)
		}
		toolSet = OrderTools(toolSet)
		// Split the log by deferral state so it's clear which tools are
		// loaded into the model's context vs. which are enabled-but-deferred
		// (schema withheld until discovered via toolsearch). Deferred tools
		// remain in the resolved set — they are wrapped, not removed — so
		// this log is NOT the wire payload; the per-request convertTools is
		// where deferred tools get defer_loading (native) or are omitted
		// (fallback).
		var deferredNames []string
		for _, t := range toolSet {
			if _, ok := t.(*tools.DeferredWrapper); ok {
				deferredNames = append(deferredNames, t.Info().Name)
			} else {
				toolNames = append(toolNames, t.Info().Name)
			}
		}
		a.tools = toolSet
		a.toolsResolved.Store(true)
		// toolsearch is created before the full toolset exists (it streams
		// through the same channel); hand it the resolved slice to search.
		for _, t := range toolSet {
			if ts, ok := t.(*tools.ToolSearchTool); ok {
				ts.BindToolset(toolSet)
			}
		}
		if len(deferredNames) > 0 {
			logging.Info("Resolved tool set", "agent", a.AgentID(),
				"tools", strings.Join(toolNames, ", "),
				"deferredTools", strings.Join(deferredNames, ", "))
		} else {
			logging.Info("Resolved tool set", "agent", a.AgentID(), "tools", strings.Join(toolNames, ", "))
		}
	})
	return a.tools
}

// OrderTools partitions tools into baseline (preserving original order) followed
// by external/MCP tools (sorted by name). This guarantees a deterministic tool
// list for stable LLM cache prefixes.
func OrderTools(toolSet []tools.BaseTool) []tools.BaseTool {
	var baseline, external []tools.BaseTool
	for _, t := range toolSet {
		if t.IsBaseline() {
			baseline = append(baseline, t)
		} else {
			external = append(external, t)
		}
	}
	sort.Slice(external, func(i, j int) bool {
		return external[i].Info().Name < external[j].Info().Name
	})
	return append(baseline, external...)
}

// contextDisclosureTriggers is the tool set whose successful calls can
// activate nested context injection (design D8). bash is deliberately
// absent — scanning command strings for path tokens produces too many
// false positives and negatives. delete is deliberately absent — removing
// files is not working-within-a-subtree that benefits from its
// instructions.
var contextDisclosureTriggers = map[string]bool{
	tools.ReadToolName:      true,
	tools.WriteToolName:     true,
	tools.EditToolName:      true,
	tools.MultiEditToolName: true,
	tools.PatchToolName:     true,
	tools.GrepToolName:      true,
	tools.GlobToolName:      true,
	tools.LSToolName:        true,
}

// contextDisclosureState is the per-toolset activation state shared by
// every contextDisclosureWrapper of one agent — mirroring how all
// DeferredWrappers of a toolset share one deferSeq counter. Per-session
// keying isolates sessions from each other; subagent sessions carry their
// own session ID (taskSession.ID), so they get their own clean activation
// set and never inherit the parent's (design D11).
type contextDisclosureState struct {
	workDir         string
	discovered      []string // absolute paths, discovery (lexical) order
	maxFileBytes    int
	maxSessionBytes int

	mu              sync.Mutex
	injected        map[string]map[string]bool // sessionID -> abs path -> injected
	injectedBytes   map[string]int             // sessionID -> total body bytes injected
	budgetExhausted map[string]bool            // sessionID -> INFO already logged, stop injecting
}

// newContextDisclosureState builds the shared state for one toolset, or
// nil when progressive disclosure is off for this agent: discovery
// disabled, agent or step opted out via context.nested: false, or the
// (cached) walk found no nested context files. nil means NewToolSet
// installs zero wrappers.
func newContextDisclosureState(info *agentregistry.AgentInfo, cfg *config.Config) *contextDisclosureState {
	if cfg == nil {
		return nil
	}
	discovery := cfg.EffectiveContextDiscovery()
	if !discovery.Enabled {
		return nil
	}
	if info.Context != nil && info.Context.Nested != nil && !*info.Context.Nested {
		return nil
	}
	if info.StepContext != nil && info.StepContext.Nested != nil && !*info.StepContext.Nested {
		return nil
	}
	discovery = discovery.WithDefaults()
	result := contextfile.Discover(cfg.WorkingDir, cfg.ContextPaths, discovery)
	// Subtract the files this agent's/step's scoped context layers already
	// inline into the system prompt — injecting them again on first touch
	// would deliver the same body twice and contradict the manifest, which
	// applies the SAME filter with the SAME vars in nestedContextManifest.
	vars := info.ContextVars
	vars.Agent = info.ID
	files := contextfile.FilterDiscovered(result.Files, info.Context, info.StepContext, cfg.WorkingDir, vars)
	if len(files) == 0 {
		return nil
	}
	return &contextDisclosureState{
		workDir:         cfg.WorkingDir,
		discovered:      files,
		maxFileBytes:    discovery.MaxFileBytes,
		maxSessionBytes: discovery.MaxSessionBytes,
		injected:        make(map[string]map[string]bool),
		injectedBytes:   make(map[string]int),
		budgetExhausted: make(map[string]bool),
	}
}

// claimAndRead injects the not-yet-injected owner files for a session,
// returning the rendered <system-reminder> blocks to append (or ""). One
// mutex covers the dedup check, the file read, and the byte accounting so
// two parallel tool calls cannot double-inject a file or overrun the
// budget. Unreadable, oversized, non-regular, or escaping files are
// skipped WITHOUT being marked injected (WARN; a later touch may retry).
// A body that would exceed the session budget flips the exhausted flag:
// INFO once, then no further bodies for that session (design D10).
func (s *contextDisclosureState) claimAndRead(sessionID string, owners []string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.budgetExhausted[sessionID] {
		return ""
	}
	injected := s.injected[sessionID]
	if injected == nil {
		injected = make(map[string]bool)
		s.injected[sessionID] = injected
	}
	var sb strings.Builder
	for _, path := range owners {
		if injected[path] {
			continue
		}
		body, ok := s.readNestedContextFile(path)
		if !ok {
			continue
		}
		// Budget accounting runs on the POST-sanitization length — that is
		// what actually lands in the tool result (defused tags grow the
		// body by one byte each).
		text := sanitizeReminderBody(string(body))
		if s.injectedBytes[sessionID]+len(text) > s.maxSessionBytes {
			s.budgetExhausted[sessionID] = true
			logging.Info("Nested context session byte budget exhausted; no further bodies will be injected", "session", sessionID, "budget", s.maxSessionBytes)
			break
		}
		injected[path] = true
		s.injectedBytes[sessionID] += len(text)
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		fmt.Fprintf(&sb, "\n\n<system-reminder>\n# From:%s\n%s</system-reminder>", path, text)
	}
	return sb.String()
}

// readNestedContextFile is the ONLY read path for nested context bodies,
// and it never trusts the process-lifetime discovery cache: the
// filesystem can change between discovery and activation (design D6/D10).
// The read is bounded and race-free — Lstat first (a cheap, precisely
// WARNed rejection of FIFOs/symlinks/devices without opening anything),
// then a kernel-enforced beneath-only open (contextfile.OpenBeneath /
// os.Root): the lookup cannot escape workDir even if a component is
// swapped for a symlink AFTER the Lstat — closing the check-then-open
// TOCTOU a userspace containment re-check cannot close. Then Stat on the
// OPENED fd (no TOCTOU window on size/type), then io.LimitReader at
// maxFileBytes+1 so even an under-reporting Stat can never smuggle an
// unbounded read past the cap.
func (s *contextDisclosureState) readNestedContextFile(path string) ([]byte, bool) {
	relPath, err := filepath.Rel(s.workDir, path)
	if err != nil || filepath.IsAbs(relPath) || relPath == ".." ||
		strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) {
		logging.Warn("Nested context injection skipped: path escapes the working directory", "file", path, "workDir", s.workDir, "error", err)
		return nil, false
	}
	lst, err := os.Lstat(path)
	if err != nil {
		logging.Warn("Nested context injection skipped: file unreadable", "file", path, "error", err)
		return nil, false
	}
	if !lst.Mode().IsRegular() {
		logging.Warn("Nested context injection skipped: not a regular file", "file", path, "mode", lst.Mode().String())
		return nil, false
	}
	f, err := contextfile.OpenBeneath(s.workDir, relPath)
	if err != nil {
		logging.Warn("Nested context injection skipped: beneath-only open failed", "file", path, "error", err)
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		logging.Warn("Nested context injection skipped: not a regular file", "file", path, "error", err)
		return nil, false
	}
	if fi.Size() > int64(s.maxFileBytes) {
		logging.Warn("Nested context injection skipped: file exceeds maxFileBytes", "file", path, "size", fi.Size(), "cap", s.maxFileBytes)
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(f, int64(s.maxFileBytes)+1))
	if err != nil {
		logging.Warn("Nested context injection skipped: file unreadable", "file", path, "error", err)
		return nil, false
	}
	if len(body) > s.maxFileBytes {
		logging.Warn("Nested context injection skipped: file exceeds maxFileBytes", "file", path, "size", len(body), "cap", s.maxFileBytes)
		return nil, false
	}
	return body, true
}

// sanitizeReminderBody neutralizes literal <system-reminder> framing tags
// inside a nested context body before it is wrapped: a repo file
// containing the closing tag would otherwise terminate the reminder block
// early and let the remainder masquerade as genuine tool output — or open
// a forged reminder with a fabricated "# From:" header. The tags are
// defused by inserting a backslash after '<' (content preserved, framing
// broken); the closing form is replaced first so the second pass cannot
// touch its output.
func sanitizeReminderBody(text string) string {
	text = strings.ReplaceAll(text, "</system-reminder>", `<\/system-reminder>`)
	return strings.ReplaceAll(text, "<system-reminder>", `<\system-reminder>`)
}

// contextDisclosureWrapper appends nested context file bodies to the
// result of the first successful tool call that touches their owning
// directory — the system prompt is never mutated (design D9; injection
// precedent: toolsearch's <system-reminder> tool results). It delegates
// every BaseTool method to the inner tool, so providers never see it; it
// is applied INSIDE deferral, keeping *tools.DeferredWrapper outermost
// for the existing type-assertion sites.
type contextDisclosureWrapper struct {
	inner tools.BaseTool
	state *contextDisclosureState
}

func (w *contextDisclosureWrapper) Info() tools.ToolInfo { return w.inner.Info() }

func (w *contextDisclosureWrapper) AllowParallelism(call tools.ToolCall, allCalls []tools.ToolCall) bool {
	return w.inner.AllowParallelism(call, allCalls)
}

func (w *contextDisclosureWrapper) IsBaseline() bool { return w.inner.IsBaseline() }

func (w *contextDisclosureWrapper) Run(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
	resp, err := w.inner.Run(ctx, call)
	// Inject only on a SUCCESSFUL text result: directory context after a
	// failed call (e.g. read of a nonexistent file) is noise. Disclosure
	// failure never propagates — the worst case is returning resp as-is.
	if err != nil || resp.IsError || resp.Type != tools.ToolResponseTypeText {
		return resp, err
	}
	sessionID, _ := tools.GetContextValues(ctx)
	if sessionID == "" {
		return resp, err
	}
	dirs := tools.ExtractTargetDirsFromCall(call, w.state.workDir)
	if len(dirs) == 0 {
		return resp, err
	}
	// Union of owners across target dirs (a patch may touch several),
	// outermost-first within each dir, deduped across dirs.
	var owners []string
	seen := make(map[string]struct{})
	for _, dir := range dirs {
		for _, f := range contextfile.OwnersForPath(dir, w.state.discovered, w.state.workDir) {
			if _, dup := seen[f]; dup {
				continue
			}
			seen[f] = struct{}{}
			owners = append(owners, f)
		}
	}
	if len(owners) == 0 {
		return resp, err
	}
	if blocks := w.state.claimAndRead(sessionID, owners); blocks != "" {
		resp.Content += blocks
	}
	return resp, err
}
