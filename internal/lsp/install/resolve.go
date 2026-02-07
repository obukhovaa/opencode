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
	Strategy       InstallStrategy
	InstallPackage string
	InstallRepo    string
}

// builtinByID returns a lookup map from server ID to its built-in definition.
func builtinByID() map[string]ServerDefinition {
	m := make(map[string]ServerDefinition, len(BuiltinServers))
	for _, def := range BuiltinServers {
		m[def.ID] = def
	}
	return m
}

// ResolveServers returns only LSP servers explicitly configured by the user.
// If a configured server matches a built-in, its defaults are merged.
// Disabled servers are excluded from the result.
func ResolveServers(cfg *config.Config) map[string]ResolvedServer {
	result := make(map[string]ResolvedServer)
	builtins := builtinByID()

	for name, lspCfg := range cfg.LSP {
		if lspCfg.Disabled {
			continue
		}

		var server ResolvedServer

		if def, ok := builtins[name]; ok {
			var initOpts any
			if def.DefaultInit != nil {
				initOpts = def.DefaultInit
			}
			server = ResolvedServer{
				ID:             def.ID,
				Extensions:     def.Extensions,
				Command:        def.Command,
				Strategy:       def.Strategy,
				InstallPackage: def.InstallPackage,
				InstallRepo:    def.InstallRepo,
				Initialization: initOpts,
			}
		} else {
			server = ResolvedServer{
				ID:       name,
				Strategy: StrategyNone,
			}
		}

		if lspCfg.Command != "" {
			server.Command = append([]string{lspCfg.Command}, lspCfg.Args...)
		}
		if len(lspCfg.Extensions) > 0 {
			server.Extensions = lspCfg.Extensions
		}
		if len(lspCfg.Env) > 0 {
			server.Env = lspCfg.Env
		}
		if lspCfg.Initialization != nil {
			server.Initialization = lspCfg.Initialization
		}

		result[name] = server
	}

	return result
}
