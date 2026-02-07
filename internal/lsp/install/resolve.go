package install

import (
	"github.com/opencode-ai/opencode/internal/config"
)

// InstallStrategy defines how an LSP server binary is obtained.
type InstallStrategy int

const (
	StrategyNone          InstallStrategy = iota // Must be pre-installed on PATH
	StrategyNpm                                  // npm install --prefix <dir> <package>
	StrategyGoInstall                            // go install <pkg>@latest
	StrategyGitHubRelease                        // Download from GitHub releases
)

// ServerDefinition describes a built-in LSP server.
type ServerDefinition struct {
	ID             string
	Extensions     []string
	Command        []string // Default command and args
	Strategy       InstallStrategy
	InstallPackage string // npm package name or go module path
	InstallRepo    string // GitHub owner/repo for release downloads
	DefaultInit    map[string]any
}

// ResolvedServer is the final server config after merging registry + user config.
type ResolvedServer struct {
	ID             string
	Extensions     []string
	Command        []string
	Env            map[string]string
	Initialization any
	Disabled       bool
	Strategy       InstallStrategy
	InstallPackage string
	InstallRepo    string
}

// ResolveServers merges the built-in registry with user config overrides.
func ResolveServers(cfg *config.Config) map[string]ResolvedServer {
	result := make(map[string]ResolvedServer)

	// Start with built-in servers
	for _, def := range BuiltinServers {
		var initOpts any
		if def.DefaultInit != nil {
			initOpts = def.DefaultInit
		}
		result[def.ID] = ResolvedServer{
			ID:             def.ID,
			Extensions:     def.Extensions,
			Command:        def.Command,
			Strategy:       def.Strategy,
			InstallPackage: def.InstallPackage,
			InstallRepo:    def.InstallRepo,
			Initialization: initOpts,
		}
	}

	// Apply user config overrides
	for name, lspCfg := range cfg.LSP {
		existing, exists := result[name]

		if lspCfg.Disabled {
			if exists {
				existing.Disabled = true
				result[name] = existing
			}
			continue
		}

		if !exists {
			// Custom server from config
			existing = ResolvedServer{
				ID:       name,
				Strategy: StrategyNone,
			}
		}

		if lspCfg.Command != "" {
			existing.Command = append([]string{lspCfg.Command}, lspCfg.Args...)
		}
		if len(lspCfg.Extensions) > 0 {
			existing.Extensions = lspCfg.Extensions
		}
		if len(lspCfg.Env) > 0 {
			existing.Env = lspCfg.Env
		}
		if lspCfg.Initialization != nil {
			existing.Initialization = lspCfg.Initialization
		}

		result[name] = existing
	}

	return result
}
