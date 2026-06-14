package watcher

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/protocol"
)

// Test that isPathWatched, in the no-registrations fallback, scopes events to
// the LSP's declared extensions. Without this, every LSP receives every file
// event — a polyglot workspace floods e.g. bash-language-server with .go file
// notifications until its child process crashes.
func TestIsPathWatched_ExtensionScopedFallback(t *testing.T) {
	tests := []struct {
		name       string
		extensions []string
		path       string
		want       bool
	}{
		{
			name:       "gopls watches .go",
			extensions: []string{".go", ".mod", ".sum"},
			path:       "/workspace/c2-agent/internal/events/types.go",
			want:       true,
		},
		{
			name:       "gopls ignores .sh",
			extensions: []string{".go", ".mod", ".sum"},
			path:       "/workspace/c2-agent/scripts/agent.sh",
			want:       false,
		},
		{
			name:       "bashls ignores .go",
			extensions: []string{".sh", ".bash", ".zsh", ".ksh"},
			path:       "/workspace/c2-agent/internal/events/types.go",
			want:       false,
		},
		{
			name:       "bashls watches .sh",
			extensions: []string{".sh", ".bash", ".zsh", ".ksh"},
			path:       "/workspace/c2-agent/scripts/agent.sh",
			want:       true,
		},
		{
			name:       "extension match is case-insensitive on input path",
			extensions: []string{".go"},
			path:       "/workspace/main.GO",
			want:       true,
		},
		{
			name:       "no extensions declared falls back to watch-everything",
			extensions: nil,
			path:       "/workspace/anything.xyz",
			want:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &lsp.Client{}
			client.SetExtensions(tc.extensions)

			w := NewWorkspaceWatcher(client)

			watched, _ := w.isPathWatched(tc.path)
			if watched != tc.want {
				t.Fatalf("isPathWatched(%q) with extensions=%v = %v; want %v",
					tc.path, tc.extensions, watched, tc.want)
			}
		})
	}
}

// Test that explicit dynamic registrations still take precedence over the
// extension fallback (existing behavior preserved).
func TestIsPathWatched_RegistrationsTakePrecedence(t *testing.T) {
	client := &lsp.Client{}
	client.SetExtensions([]string{".go"}) // would otherwise reject .yaml

	w := NewWorkspaceWatcher(client)

	// Direct field assignment instead of AddRegistrations: that method also
	// kicks off file preloading via the (zero-value) client, which would
	// segfault. Single-goroutine test, so skipping the mutex is safe here.
	pattern := protocol.GlobPattern{Value: "**/*.yaml"}
	w.registrations = []protocol.FileSystemWatcher{{GlobPattern: pattern}}

	watched, _ := w.isPathWatched("/workspace/config.yaml")
	if !watched {
		t.Fatalf("explicit registration should win over extension fallback")
	}

	watched, _ = w.isPathWatched("/workspace/main.go")
	if watched {
		t.Fatalf("path outside registration should not be watched when registrations exist")
	}
}
