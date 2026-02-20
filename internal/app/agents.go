package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/flow"
	agent "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
)

var _ flow.AgentProvider = (*agentProvider)(nil)

type agentProvider struct {
	app   *App
	ctx   context.Context
	mu    sync.Mutex
	cache map[string]agent.Service
}

func newAgentProvider(ctx context.Context, app *App) *agentProvider {
	return &agentProvider{
		app:   app,
		ctx:   ctx,
		cache: make(map[string]agent.Service),
	}
}

func (p *agentProvider) Get(agentID string) (agent.Service, error) {
	if svc, ok := p.app.PrimaryAgents[config.AgentName(agentID)]; ok {
		return svc, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if svc, ok := p.cache[agentID]; ok {
		return svc, nil
	}

	info, ok := p.app.Registry.Get(agentID)
	if !ok {
		return nil, fmt.Errorf("agent %q not found in registry", agentID)
	}

	if info.Disabled {
		return nil, fmt.Errorf("agent %q is disabled", agentID)
	}

	infoCopy := info
	svc, err := agent.NewAgent(
		p.ctx,
		&infoCopy,
		p.app.Sessions,
		p.app.Messages,
		p.app.Permissions,
		p.app.History,
		p.app.LSPClients,
		p.app.Registry,
		p.app.MCPRegistry,
	)
	if err != nil {
		return nil, fmt.Errorf("creating agent %q: %w", agentID, err)
	}

	logging.Debug("Lazily created agent for flow", "agent", agentID)
	p.cache[agentID] = svc
	return svc, nil
}
