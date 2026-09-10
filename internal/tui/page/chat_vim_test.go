package page

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/tui/vim"
)

// TestVimConsumesEscape is the regression guard for the routing gap the visual
// modes exposed: the checks here used to compare against the literal "INSERT",
// so VISUAL fell into the branch meant for NORMAL and pressing esc cancelled
// the running agent instead of ending the selection.
func TestVimConsumesEscape(t *testing.T) {
	tests := []struct {
		mode string
		want bool
	}{
		{mode: "", want: false},
		{mode: string(vim.ModeInsert), want: true},
		{mode: string(vim.ModeNormal), want: false},
		{mode: string(vim.ModeVisual), want: true},
		{mode: string(vim.ModeVisualLine), want: true},
	}

	for _, tt := range tests {
		name := tt.mode
		if name == "" {
			name = "vim disabled"
		}
		t.Run(name, func(t *testing.T) {
			p := &chatPage{vimMode: tt.mode}
			if got := p.vimConsumesEscape(); got != tt.want {
				t.Errorf("vimConsumesEscape() = %v, want %v", got, tt.want)
			}
			// ctrl+c follows esc: wherever the editor owns one it owns the
			// other, so a visual mode must never raise the quit dialog.
			if got := p.ConsumesCtrlC(); got != tt.want {
				t.Errorf("ConsumesCtrlC() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestEscInVisualDoesNotCancelTheAgent walks the actual key-handling branch
// order rather than the helper alone: the esc case checks shell mode, then the
// completion dialogs, then vim, and only then cancels a running agent.
func TestEscInVisualDoesNotCancelTheAgent(t *testing.T) {
	p := &chatPage{vimMode: string(vim.ModeVisual)}
	if !p.vimConsumesEscape() {
		t.Fatal("VISUAL must consume esc before the agent-cancel branch is reached")
	}

	p.vimMode = string(vim.ModeNormal)
	if p.vimConsumesEscape() {
		t.Error("NORMAL must let esc through so it can cancel a running agent")
	}
}
