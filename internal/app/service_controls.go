package app

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/install"
)

// SetMCPPolicy changes only the selected agent. Shared transports stay alive for
// other agents. Allowlists become equivalent deny-by-default tool maps.
func (a *App) SetMCPPolicy(id config.AgentName, server, action string) error {
	return agent.WithIdleToolPolicy(func() error {
		info, ok := a.Registry.Get(string(id))
		if !ok {
			return fmt.Errorf("unknown agent %q", id)
		}
		if _, ok := config.ResolveMCPServers()[server]; !ok {
			return fmt.Errorf("unknown server %q", server)
		}
		enabled, deferred := mcpPolicyMaps(info)
		if enabled == nil {
			enabled = map[string]bool{}
		}
		if deferred == nil {
			deferred = map[string]bool{}
		}
		pattern := strings.ToLower(server) + "_*"
		switch action {
		case "enable", "disable":
			value := action == "enable"
			// Exact entries beat globs, so update every known tool too.
			enabled[pattern] = value
			for _, name := range a.MCPRegistry.ServerTools(server) {
				enabled[strings.ToLower(server+"_"+name)] = value
			}
		case "defer":
			value := !deferred[pattern]
			deferred[pattern] = value
			for _, name := range a.MCPRegistry.ServerTools(server) {
				deferred[strings.ToLower(server+"_"+name)] = value
			}
		case "reconnect":
			if err := agent.ReconnectMCP(a.MCPRegistry, server); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown action %q", action)
		}
		if action != "reconnect" {
			if err := config.UpdateCfgFile(func(c *config.Config) {
				if c.Agents == nil {
					c.Agents = map[config.AgentName]config.Agent{}
				}
				entry := c.Agents[id]
				// A tools-only JSON entry otherwise overrides markdown model defaults.
				if entry.Model == "" {
					entry.Model = models.ModelID(info.Model)
					entry.MaxTokens = info.MaxTokens
				}
				entry.AllowTools = nil
				entry.Tools = enabled
				entry.DeferredTools = deferred
				c.Agents[id] = entry
			}); err != nil {
				return err
			}
			agentregistry.SetRuntimeTools(a.Registry, string(id), enabled, deferred)
		}
		info, _ = a.Registry.Get(string(id))
		svc := a.PrimaryAgents[id]
		if svc == nil {
			svc = a.ActiveAgent()
		}
		stream := agent.NewToolSet(&info, a.Registry, a.Permissions, a.History, a.LspService, a.Sessions, a.Messages, a.MCPRegistry, a.AgentFactory, "")
		return agent.ReplaceToolset(svc, stream)
	})
}

func (a *App) ActivateLSP(name string) error {
	s, ok := a.LspService.(*lspService)
	if !ok {
		return fmt.Errorf("LSP activation unavailable")
	}
	s.activationMu.Lock()
	defer s.activationMu.Unlock()
	if c := s.Clients()[name]; c != nil {
		if c.GetServerState() == lsp.StateReady {
			return nil
		}
		return fmt.Errorf("LSP server %q is in an error state", name)
	}
	cfg := *config.Get()
	cfg.LSP = maps.Clone(cfg.LSP)
	if cfg.LSP == nil {
		cfg.LSP = map[string]config.LSPConfig{}
	}
	entry := cfg.LSP[name]
	entry.Disabled = false
	cfg.LSP[name] = entry
	server, ok := install.ResolveServers(&cfg)[name]
	if !ok || len(server.Command) == 0 {
		return fmt.Errorf("unknown LSP server %q", name)
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	command, args, err := install.ResolveCommand(ctx, server, false)
	if err != nil {
		return err
	}
	s.createAndStartLSPClient(ctx, name, server, command, args...)
	if c := s.Clients()[name]; c == nil || c.GetServerState() != lsp.StateReady {
		return fmt.Errorf("LSP server %q failed to initialize", name)
	}
	return config.UpdateCfgFile(func(c *config.Config) {
		if c.LSP == nil {
			c.LSP = map[string]config.LSPConfig{}
		}
		c.LSP[name] = entry
	})
}

func mcpPolicyMaps(info agentregistry.AgentInfo) (map[string]bool, map[string]bool) {
	enabled := maps.Clone(info.Tools)
	if len(info.AllowTools) > 0 {
		enabled = map[string]bool{"*": false}
		for _, pattern := range info.AllowTools {
			enabled[pattern] = true
		}
	}
	return enabled, maps.Clone(info.DeferredTools)
}

// DeactivateLSP stops one client and watcher without restarting the application.
func (a *App) DeactivateLSP(name string) error {
	s, ok := a.LspService.(*lspService)
	if !ok {
		return fmt.Errorf("LSP controls unavailable")
	}
	s.activationMu.Lock()
	defer s.activationMu.Unlock()
	if err := config.UpdateCfgFile(func(c *config.Config) {
		if c.LSP == nil {
			c.LSP = map[string]config.LSPConfig{}
		}
		entry := c.LSP[name]
		entry.Disabled = true
		c.LSP[name] = entry
	}); err != nil {
		return err
	}
	s.cancelMu.Lock()
	if cancel := s.watcherCancelByName[name]; cancel != nil {
		cancel()
		delete(s.watcherCancelByName, name)
	}
	s.cancelMu.Unlock()
	s.mu.Lock()
	client := s.clients[name]
	delete(s.clients, name)
	s.mu.Unlock()
	if client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.Shutdown(ctx)
		_ = client.Exit(ctx)
		client.Close()
	}
	return nil
}
