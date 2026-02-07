package install

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveServers_EmptyConfig(t *testing.T) {
	cfg := &config.Config{
		LSP: make(map[string]config.LSPConfig),
	}

	servers := ResolveServers(cfg)

	// No servers configured, so nothing should be returned
	assert.Empty(t, servers)
}

func TestResolveServers_ConfiguredBuiltin(t *testing.T) {
	cfg := &config.Config{
		LSP: map[string]config.LSPConfig{
			"gopls":      {},
			"typescript": {},
		},
	}

	servers := ResolveServers(cfg)

	assert.Len(t, servers, 2)

	gopls, ok := servers["gopls"]
	require.True(t, ok)
	assert.Equal(t, []string{".go"}, gopls.Extensions)
	assert.Equal(t, []string{"gopls"}, gopls.Command)
	assert.Equal(t, StrategyGoInstall, gopls.Strategy)

	ts, ok := servers["typescript"]
	require.True(t, ok)
	assert.Contains(t, ts.Extensions, ".ts")
	assert.Contains(t, ts.Extensions, ".tsx")
	assert.Contains(t, ts.Extensions, ".js")
	assert.Equal(t, StrategyNpm, ts.Strategy)
}

func TestResolveServers_UserDisablesBuiltin(t *testing.T) {
	cfg := &config.Config{
		LSP: map[string]config.LSPConfig{
			"gopls": {Disabled: true},
		},
	}

	servers := ResolveServers(cfg)

	_, ok := servers["gopls"]
	assert.False(t, ok)
}

func TestResolveServers_UserOverridesCommand(t *testing.T) {
	cfg := &config.Config{
		LSP: map[string]config.LSPConfig{
			"gopls": {
				Command: "/custom/gopls",
				Args:    []string{"--debug"},
			},
		},
	}

	servers := ResolveServers(cfg)
	assert.Len(t, servers, 1)

	gopls := servers["gopls"]
	assert.Equal(t, []string{"/custom/gopls", "--debug"}, gopls.Command)
	assert.Equal(t, []string{".go"}, gopls.Extensions) // keeps built-in extensions
}

func TestResolveServers_UserOverridesExtensions(t *testing.T) {
	cfg := &config.Config{
		LSP: map[string]config.LSPConfig{
			"gopls": {
				Extensions: []string{".go", ".mod"},
			},
		},
	}

	servers := ResolveServers(cfg)
	assert.Len(t, servers, 1)

	gopls := servers["gopls"]
	assert.Equal(t, []string{".go", ".mod"}, gopls.Extensions)
}

func TestResolveServers_UserAddsEnv(t *testing.T) {
	cfg := &config.Config{
		LSP: map[string]config.LSPConfig{
			"gopls": {
				Env: map[string]string{"GOFLAGS": "-mod=vendor"},
			},
		},
	}

	servers := ResolveServers(cfg)
	assert.Len(t, servers, 1)

	gopls := servers["gopls"]
	assert.Equal(t, map[string]string{"GOFLAGS": "-mod=vendor"}, gopls.Env)
}

func TestResolveServers_UserAddsInitialization(t *testing.T) {
	init := map[string]any{"setting": true}
	cfg := &config.Config{
		LSP: map[string]config.LSPConfig{
			"gopls": {
				Initialization: init,
			},
		},
	}

	servers := ResolveServers(cfg)
	assert.Len(t, servers, 1)

	gopls := servers["gopls"]
	assert.Equal(t, init, gopls.Initialization)
}

func TestResolveServers_CustomServer(t *testing.T) {
	cfg := &config.Config{
		LSP: map[string]config.LSPConfig{
			"my-lsp": {
				Command:    "my-lsp-server",
				Args:       []string{"--stdio"},
				Extensions: []string{".custom"},
				Env:        map[string]string{"DEBUG": "1"},
			},
		},
	}

	servers := ResolveServers(cfg)
	assert.Len(t, servers, 1)

	custom, ok := servers["my-lsp"]
	require.True(t, ok)
	assert.Equal(t, []string{"my-lsp-server", "--stdio"}, custom.Command)
	assert.Equal(t, []string{".custom"}, custom.Extensions)
	assert.Equal(t, map[string]string{"DEBUG": "1"}, custom.Env)
	assert.Equal(t, StrategyNone, custom.Strategy) // custom servers can't auto-install
}

func TestResolveServers_DisabledCustomServer(t *testing.T) {
	cfg := &config.Config{
		LSP: map[string]config.LSPConfig{
			"my-lsp": {
				Disabled: true,
			},
		},
	}

	servers := ResolveServers(cfg)

	// Disabled custom server without matching builtin should not appear
	_, ok := servers["my-lsp"]
	assert.False(t, ok)
}

func TestResolveServers_GoplsDefaultInit(t *testing.T) {
	cfg := &config.Config{
		LSP: map[string]config.LSPConfig{
			"gopls": {},
		},
	}

	servers := ResolveServers(cfg)

	gopls := servers["gopls"]
	init, ok := gopls.Initialization.(map[string]any)
	require.True(t, ok)
	_, hasCodelenses := init["codelenses"]
	assert.True(t, hasCodelenses, "gopls should have default codelenses init options")
}

func TestBuiltinServers_NoDuplicateIDs(t *testing.T) {
	seen := make(map[string]bool)
	for _, s := range BuiltinServers {
		assert.False(t, seen[s.ID], "duplicate server ID: %s", s.ID)
		seen[s.ID] = true
	}
}

func TestBuiltinServers_AllHaveExtensions(t *testing.T) {
	for _, s := range BuiltinServers {
		assert.NotEmpty(t, s.Extensions, "server %s has no extensions", s.ID)
	}
}

func TestBuiltinServers_AllHaveCommand(t *testing.T) {
	for _, s := range BuiltinServers {
		assert.NotEmpty(t, s.Command, "server %s has no command", s.ID)
	}
}

func TestFindMatchingAsset(t *testing.T) {
	assets := []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}{
		{Name: "server-linux-amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux-amd64"},
		{Name: "server-darwin-arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin-arm64"},
		{Name: "server-darwin-amd64.tar.gz", BrowserDownloadURL: "https://example.com/darwin-amd64"},
		{Name: "server-windows-amd64.zip", BrowserDownloadURL: "https://example.com/windows-amd64"},
	}

	// This test verifies the function doesn't crash and returns something reasonable
	result := findMatchingAsset(assets, "test-server")
	assert.NotEmpty(t, result, "should find an asset for current platform")
}

func TestBinDir(t *testing.T) {
	dir := BinDir()
	assert.Contains(t, dir, ".opencode")
	assert.Contains(t, dir, "bin")
}
