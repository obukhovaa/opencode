package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/hooks"
	"github.com/opencode-ai/opencode/internal/langfuse"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/question"
	"github.com/opencode-ai/opencode/internal/session"
)

// AgentFactory creates agent instances with optional output schema overrides.
// Agents are cached by stepID for flow step reuse. Primary agents (created
// without a stepID) are tracked for reuse when no schema override is needed.
type AgentFactory interface {
	// NewAgent constructs an agent. `interactive` should be true when
	// the requested agent is for an `interactive: true` flow step —
	// it propagates to AgentInfo.Interactive and adjusts the system
	// prompt so the agent prefers multi-turn dialogue via the chat
	// bridge over an immediate struct_output emission. Callers that
	// don't know (subagent task tool, primary agent init) pass false.
	//
	// `boundPeers` is the resolved chat-bridge peers the interactive
	// step is bound to (from resolveInteractionTarget on the flow's
	// args). The system prompt grows a "## Reviewer details" section
	// listing them so the agent sees mention handles + channels
	// without flow authors having to template ${args.reviewer.*}
	// (the flow resolver has no nested-path support anyway). Pass nil
	// for non-interactive callers or when the binding isn't known yet.
	//
	// `stepCtx` is the flow step's `context` override (nil outside
	// flows / for steps without one) and `flowVars` carries the
	// ${flow.id} / ${flow.step} template token values. Both must ride
	// the signature: NewAgent is context-free (its ctx is discarded),
	// so the FlowIDContextKey/FlowStepIDContextKey telemetry ctx values
	// — set later, on the Run context — are invisible at prompt-build
	// time. The ${agent} token is filled in here from agentID.
	NewAgent(ctx context.Context, agentID string, outputSchema map[string]any, stepID string, interactive bool, boundPeers []bridge.PeerRef, stepCtx *contextfile.StepContext, flowVars contextfile.TemplateVars) (Service, error)
	InitPrimaryAgents(ctx context.Context, outputSchema map[string]any) ([]Service, error)
	// ResetStepCache drops the per-step agent memoisation. The cache is
	// keyed on the flow YAML's step ID, which recurs across runs, so a
	// process that serves many flow runs MUST clear it between them or
	// run 2 reuses run 1's agents — and their once-resolved toolsets.
	ResetStepCache()
	SetCronServices(cronToolSvc tools.CronToolService, schedHelper tools.CronScheduleHelper)
	CronServices() (tools.CronToolService, tools.CronScheduleHelper)
	SetTodoStore(store tools.TodoStore)
	TodoStore() tools.TodoStore
	SetQuestionService(svc question.Service)
	QuestionService() question.Service
	// SetBridgeSender installs the chat-bridge handle the router_send
	// tool calls into. cmd/serve.go invokes this after the bridge
	// orchestrator starts. nil sender disables the router_send tool.
	SetBridgeSender(sender tools.BridgeSender, cfg *bridge.Config, mediaRoot string)
	// BridgeSender returns the registered handle (or nil) plus the
	// cfg.Router snapshot captured at SetBridgeSender time and the
	// media-store root. NewToolSet reads this to decide router_send
	// registration.
	BridgeSender() (tools.BridgeSender, *bridge.Config, string)

	// SetHookRegistry installs the hook runtime that fires PreToolUse /
	// PostToolUse subprocess hooks around tool dispatch. nil disables
	// hooks entirely (the agent loop behaves as if hooks were absent).
	// Mirrors the SetBridgeSender pattern: late-injected after agent
	// construction so the hooks package depends only on logging and not
	// on the agent.
	SetHookRegistry(reg *hooks.Registry)
	// HookRegistry returns the registered hook runtime, or nil if none
	// has been installed.
	HookRegistry() *hooks.Registry
}

type agentFactory struct {
	sessions    session.Service
	messages    message.Service
	permissions permission.Service
	history     history.Service
	lspService  lsp.LspService
	registry    agentregistry.Registry
	mcpRegistry MCPRegistry

	cronToolService    tools.CronToolService
	cronScheduleHelper tools.CronScheduleHelper
	todoStore          tools.TodoStore
	questionService    question.Service

	bridgeSender    tools.BridgeSender
	bridgeCfg       *bridge.Config
	bridgeMediaRoot string

	hookRegistry *hooks.Registry

	mu        sync.Mutex
	stepCache map[string]Service
}

// SetHookRegistry installs the hook runtime. nil disables hooks. Mirrors
// SetBridgeSender's late-injection pattern.
func (f *agentFactory) SetHookRegistry(reg *hooks.Registry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hookRegistry = reg
}

// HookRegistry returns the installed runtime (or nil). Read-locked so
// concurrent agent dispatch can fetch it without contention.
func (f *agentFactory) HookRegistry() *hooks.Registry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hookRegistry
}

// SetBridgeSender installs the chat-bridge handle the router_send tool
// uses. cmd/serve.go calls this after the bridge orchestrator starts.
// nil sender disables the router_send tool entirely.
func (f *agentFactory) SetBridgeSender(sender tools.BridgeSender, cfg *bridge.Config, mediaRoot string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bridgeSender = sender
	f.bridgeCfg = cfg
	f.bridgeMediaRoot = mediaRoot
}

// BridgeSender returns the registered handle (or nil) plus the
// associated cfg.Router snapshot and media-root path.
func (f *agentFactory) BridgeSender() (tools.BridgeSender, *bridge.Config, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bridgeSender, f.bridgeCfg, f.bridgeMediaRoot
}

func NewAgentFactory(
	sessions session.Service,
	messages message.Service,
	permissions permission.Service,
	history history.Service,
	lspService lsp.LspService,
	registry agentregistry.Registry,
	mcpRegistry MCPRegistry,
) AgentFactory {
	return &agentFactory{
		sessions:    sessions,
		messages:    messages,
		permissions: permissions,
		history:     history,
		lspService:  lspService,
		registry:    registry,
		mcpRegistry: mcpRegistry,
		stepCache:   make(map[string]Service),
	}
}

// SetCronServices injects cron tool dependencies after factory creation
// (to break the initialization cycle between cron and agent packages).
func (f *agentFactory) SetCronServices(cronToolSvc tools.CronToolService, schedHelper tools.CronScheduleHelper) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cronToolService = cronToolSvc
	f.cronScheduleHelper = schedHelper
}

// CronServices returns the injected cron tool dependencies under lock.
func (f *agentFactory) CronServices() (tools.CronToolService, tools.CronScheduleHelper) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cronToolService, f.cronScheduleHelper
}

// SetTodoStore injects the in-memory todo store.
func (f *agentFactory) SetTodoStore(store tools.TodoStore) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.todoStore = store
}

// TodoStore returns the injected todo store.
func (f *agentFactory) TodoStore() tools.TodoStore {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.todoStore
}

// SetQuestionService injects the question service after factory creation
// (only in interactive mode).
func (f *agentFactory) SetQuestionService(svc question.Service) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.questionService = svc
}

// QuestionService returns the injected question service (nil in non-interactive mode).
func (f *agentFactory) QuestionService() question.Service {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.questionService
}

// NewAgent builds an agent service. ctx is kept for interface stability but
// agent construction is context-free: the toolset (incl. MCP loading) is
// resolved under registry-owned lifetimes, so a caller's request-scoped ctx
// cannot cancel it (see NewToolSet / mcpRegistry.getTools).
func (f *agentFactory) NewAgent(ctx context.Context, agentID string, outputSchema map[string]any, stepID string, interactive bool, boundPeers []bridge.PeerRef, stepCtx *contextfile.StepContext, flowVars contextfile.TemplateVars) (Service, error) {
	_ = ctx
	if stepID != "" {
		f.mu.Lock()
		if svc, ok := f.stepCache[stepID]; ok {
			f.mu.Unlock()
			return svc, nil
		}
		f.mu.Unlock()
	}

	info, ok := f.registry.Get(agentID)
	if !ok {
		return nil, fmt.Errorf("agent %q not found in registry", agentID)
	}
	if info.Disabled {
		return nil, fmt.Errorf("agent %q is disabled", agentID)
	}

	infoCopy := info
	// Resolve a Langfuse-managed system prompt here rather than at registry
	// load, so an edit made in the Langfuse UI reaches the next agent built
	// from this definition — bounded by the client's cache TTL — instead of
	// waiting for a process restart. That holds for subagents (built per
	// task spawn) and flow-step agents; a PRIMARY agent is built once by
	// InitPrimaryAgents and held for the process lifetime, so for those the
	// resolved text is pinned until restart. See docs/telemetry.md.
	resolvedPrompt, promptErr := resolveManagedPrompt(agentID, infoCopy)
	if promptErr != nil {
		return nil, promptErr
	}
	infoCopy.Prompt = resolvedPrompt
	if outputSchema != nil {
		infoCopy.Output = &agentregistry.Output{Schema: outputSchema}
	}
	// Interactive lives on the in-memory AgentInfo copy only. It
	// flows downstream into GetAgentPrompt for prompt-shape selection.
	infoCopy.Interactive = interactive
	// BoundPeers is the resolved chat-bridge peer list for this
	// step — passed through AgentInfo so newAgent → createAgentProvider
	// → GetAgentPromptWithOptions sees it and the prompt grows the
	// "## Reviewer details" section. Empty / nil for non-interactive.
	infoCopy.BoundPeers = boundPeers
	// The step context override and the template token values follow the
	// same per-call path: the prompt builder re-fetches the ORIGINAL
	// registry entry, which cannot carry per-step state.
	infoCopy.StepContext = stepCtx
	flowVars.Agent = agentID
	infoCopy.ContextVars = flowVars

	svc, err := newAgent(&infoCopy, f.sessions, f.messages, f.permissions, f.history, f.lspService, f.registry, f.mcpRegistry, f)
	if err != nil {
		return nil, fmt.Errorf("creating agent %q: %w", agentID, err)
	}

	if stepID != "" {
		f.mu.Lock()
		defer f.mu.Unlock()
		if existing, ok := f.stepCache[stepID]; ok {
			return existing, nil
		}
		f.stepCache[stepID] = svc
		logging.Debug("Cached agent for flow step", "agent", agentID, "step", stepID)
	}
	return svc, nil
}

// ResetStepCache drops every per-step agent memoised by NewAgent.
//
// The cache exists so the iterations of ONE step share an agent. It is
// keyed on the flow YAML's step ID, which recurs across runs, so on a
// long-lived pod (one that serves many flow runs from a single process)
// run 2 of a flow would otherwise reuse run 1's agent objects — and with
// them run 1's resolved toolset, frozen by sync.Once. That makes any
// per-run state stale by construction, and turns a single failed MCP
// discovery into a permanent loss: the empty toolset is baked into the
// cached agent and every later run on that pod gets it.
//
// The flow runner calls this at the start of each run.
func (f *agentFactory) ResetStepCache() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stepCache) == 0 {
		return
	}
	logging.Debug("Clearing per-step agent cache for a new flow run", "cached", len(f.stepCache))
	f.stepCache = make(map[string]Service)
}

func (f *agentFactory) InitPrimaryAgents(ctx context.Context, outputSchema map[string]any) ([]Service, error) {
	primaryAgents := f.registry.ListByMode(config.AgentModeAgent)
	if len(primaryAgents) == 0 {
		return []Service{}, errors.New("no primary agents found in registry")
	}
	res := make([]Service, 0, len(primaryAgents))
	for _, agentInfo := range primaryAgents {
		primaryAgent, err := f.NewAgent(ctx, string(agentInfo.ID), outputSchema, "", false, nil, nil, contextfile.TemplateVars{})
		if err != nil {
			logging.Error("Failed to create agent", "agent", agentInfo.ID, "error", err)
			continue
		}
		res = append(res, primaryAgent)
	}
	if len(res) == 0 {
		return res, errors.New("no primary agents has been created")
	}
	return res, nil
}

// resolveManagedPrompt returns the system prompt text a registry entry
// should be built with: its inline Prompt, or the Langfuse-managed prompt
// named by LangfusePromptPath.
//
// Deliberately NOT on a caller's ctx: agent construction is context-free by
// design, so a request-scoped cancellation cannot half-build an agent. The
// fetch is bounded by the prompt client's own timeout.
//
// The error is returned rather than absorbed into a fallback. A referencing
// agent whose prompt could not be fetched must not run on the built-in
// prompt for its name: that failure reads as "Langfuse is being ignored",
// which is far harder to diagnose than a construction error naming the path.
func resolveManagedPrompt(agentID string, info agentregistry.AgentInfo) (string, error) {
	path := strings.TrimSpace(info.LangfusePromptPath)
	if path == "" {
		return info.Prompt, nil
	}
	resolved, err := langfuse.GetPrompts().Resolve(context.Background(), path, info.LangfusePromptLabel)
	if err != nil {
		return "", fmt.Errorf("agent %q: resolving langfusePromptPath %q: %w", agentID, path, err)
	}
	logging.Debug("Resolved agent prompt from Langfuse",
		"agent", agentID, "path", resolved.Path, "label", resolved.Label, "version", resolved.Version)
	return resolved.Text, nil
}

// resolveRegisteredPrompt is resolveManagedPrompt for an agent addressed by
// name. It exists for the two built-in helper agents — summarizer and
// descriptor — whose providers newAgent constructs directly instead of going
// through NewAgent, and which would otherwise silently ignore a
// langfusePromptPath while an inline `prompt` on the same agent worked.
//
// A name with no registry entry yields an empty prompt, which the prompt
// builder reads as "use the registered/built-in prompt" — the pre-existing
// behaviour for an unregistered helper agent.
func resolveRegisteredPrompt(reg agentregistry.Registry, name config.AgentName) (string, error) {
	info, ok := reg.Get(string(name))
	if !ok {
		return "", nil
	}
	return resolveManagedPrompt(string(name), info)
}
