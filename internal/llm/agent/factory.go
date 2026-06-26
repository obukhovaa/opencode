package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/hooks"
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
	NewAgent(ctx context.Context, agentID string, outputSchema map[string]any, stepID string, interactive bool, boundPeers []bridge.PeerRef) (Service, error)
	InitPrimaryAgents(ctx context.Context, outputSchema map[string]any) ([]Service, error)
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

func (f *agentFactory) NewAgent(ctx context.Context, agentID string, outputSchema map[string]any, stepID string, interactive bool, boundPeers []bridge.PeerRef) (Service, error) {
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

	svc, err := newAgent(ctx, &infoCopy, f.sessions, f.messages, f.permissions, f.history, f.lspService, f.registry, f.mcpRegistry, f)
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

func (f *agentFactory) InitPrimaryAgents(ctx context.Context, outputSchema map[string]any) ([]Service, error) {
	primaryAgents := f.registry.ListByMode(config.AgentModeAgent)
	if len(primaryAgents) == 0 {
		return []Service{}, errors.New("no primary agents found in registry")
	}
	res := make([]Service, 0, len(primaryAgents))
	for _, agentInfo := range primaryAgents {
		primaryAgent, err := f.NewAgent(ctx, string(agentInfo.ID), outputSchema, "", false, nil)
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
