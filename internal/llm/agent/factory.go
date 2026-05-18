package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
)

// AgentFactory creates agent instances with optional output schema overrides.
// Agents are cached by stepID for flow step reuse. Primary agents (created
// without a stepID) are tracked for reuse when no schema override is needed.
type AgentFactory interface {
	NewAgent(ctx context.Context, agentID string, outputSchema map[string]any, stepID string) (Service, error)
	InitPrimaryAgents(ctx context.Context, outputSchema map[string]any) ([]Service, error)
	SetCronServices(cronToolSvc tools.CronToolService, schedHelper tools.CronScheduleHelper)
	CronServices() (tools.CronToolService, tools.CronScheduleHelper)
	SetTodoStore(store tools.TodoStore)
	TodoStore() tools.TodoStore
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

	mu        sync.Mutex
	stepCache map[string]Service
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

func (f *agentFactory) NewAgent(ctx context.Context, agentID string, outputSchema map[string]any, stepID string) (Service, error) {
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
		primaryAgent, err := f.NewAgent(ctx, string(agentInfo.ID), outputSchema, "")
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
