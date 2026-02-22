package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/history"
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
}

type agentFactory struct {
	sessions    session.Service
	messages    message.Service
	permissions permission.Service
	history     history.Service
	lspClients  map[string]*lsp.Client
	registry    agentregistry.Registry
	mcpRegistry MCPRegistry

	mu        sync.Mutex
	stepCache map[string]Service
}

func NewAgentFactory(
	sessions session.Service,
	messages message.Service,
	permissions permission.Service,
	history history.Service,
	lspClients map[string]*lsp.Client,
	registry agentregistry.Registry,
	mcpRegistry MCPRegistry,
) AgentFactory {
	return &agentFactory{
		sessions:    sessions,
		messages:    messages,
		permissions: permissions,
		history:     history,
		lspClients:  lspClients,
		registry:    registry,
		mcpRegistry: mcpRegistry,
		stepCache:   make(map[string]Service),
	}
}

func (f *agentFactory) NewAgent(ctx context.Context, agentID string, outputSchema map[string]any, stepID string) (Service, error) {
	// Step can't change in runtime, so safe to cache
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

	svc, err := newAgent(ctx, &infoCopy, f.sessions, f.messages, f.permissions, f.history, f.lspClients, f.registry, f.mcpRegistry, f)
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
