package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/opencode-ai/opencode/internal/tui/vim"
)

func TestShellInvocation(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		want   string
		wantOK bool
	}{
		{name: "simple command", text: "!ls -la", want: "ls -la", wantOK: true},
		{name: "space after bang", text: "! ls -la", want: "ls -la", wantOK: true},
		{name: "force prefix is part of the command", text: "!!sudo -v", want: "!sudo -v", wantOK: true},
		{name: "multi-line command", text: "!for f in *; do\n echo $f\ndone", want: "for f in *; do\n echo $f\ndone", wantOK: true},

		// The exclusions. Pasting a markdown doc that opens with an image, or
		// prose containing !=, must not be diverted into the shell.
		{name: "markdown image", text: "![diagram](./a.png) what is this?", wantOK: false},
		{name: "inequality", text: "!= is not equals", wantOK: false},

		{name: "leading whitespace", text: " !ls", wantOK: false},
		{name: "bang not at start", text: "run !ls", wantOK: false},
		{name: "bare bang", text: "!", wantOK: false},
		{name: "bang and whitespace only", text: "!   ", wantOK: false},
		{name: "empty", text: "", wantOK: false},
		{name: "plain prose", text: "explain the shell code", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := shellInvocation(tt.text)
			if ok != tt.wantOK {
				t.Fatalf("shellInvocation(%q) ok = %v, want %v", tt.text, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("shellInvocation(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

// TestShellEntryPathsAgree is the point of the shared predicate: the same
// characters must produce the same command however they reached the draft.
func TestShellEntryPathsAgree(t *testing.T) {
	const command = "git status --short"

	t.Run("typed", func(t *testing.T) {
		ed := newTestEditor()
		ed.Update(tea.KeyPressMsg{Code: '!', Text: "!"})
		if !ed.IsShellMode() {
			t.Fatal("typing ! did not enter shell mode")
		}
		ed.textarea.SetValue(command)
		if got := ed.textarea.Value(); got != command {
			t.Errorf("draft = %q, want %q", got, command)
		}
	})

	t.Run("pasted", func(t *testing.T) {
		ed := newTestEditor()
		ed.Update(tea.PasteMsg{Content: "!" + command})
		if !ed.IsShellMode() {
			t.Fatal("pasting !command did not enter shell mode")
		}
		if got := ed.textarea.Value(); got != command {
			t.Errorf("draft = %q, want %q", got, command)
		}
	})

	t.Run("submitted", func(t *testing.T) {
		ed := newTestEditor()
		ed.textarea.SetValue("!" + command)
		cmd := ed.send()
		if cmd == nil {
			t.Fatal("send() returned no command for a bang-prefixed draft")
		}
		if !ed.IsShellMode() {
			t.Fatal("submitting a bang-prefixed draft did not enter shell mode")
		}
		if len(ed.shellHistory) != 1 || ed.shellHistory[0] != command {
			t.Errorf("shell history = %v, want [%q]", ed.shellHistory, command)
		}
		if !ed.shellExecuting {
			t.Error("submitting a bang-prefixed draft did not start execution")
		}
	})
}

func TestPasteDoesNotEnterShellMode(t *testing.T) {
	tests := []struct {
		name    string
		seed    string
		content string
	}{
		{name: "markdown image", content: "![diagram](./a.png)\n\nWhat does this show?"},
		{name: "prose", content: "here is a long\nmulti-line note about the code"},
		{name: "command into a non-empty draft", seed: "please run ", content: "!git status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ed := newTestEditor()
			if tt.seed != "" {
				ed.textarea.SetValue(tt.seed)
			}
			ed.Update(tea.PasteMsg{Content: tt.content})
			if ed.IsShellMode() {
				t.Fatalf("paste %q wrongly entered shell mode", tt.content)
			}
			if got := ed.textarea.Value(); got != tt.seed+tt.content {
				t.Errorf("draft = %q, want %q", got, tt.seed+tt.content)
			}
		})
	}
}

func TestSubmitOrdinaryMessageIsNotShell(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ed, _ := newEditorForTest(ctx, &editorFakeAgent{})
	ed.textarea.SetValue("explain the shell mode code")

	ed.send()

	if ed.IsShellMode() {
		t.Error("ordinary message entered shell mode")
	}
	if len(ed.shellHistory) != 0 {
		t.Errorf("ordinary message reached the shell history: %v", ed.shellHistory)
	}
}

func TestBangEntersShellModeFromVimInsert(t *testing.T) {
	ed := newTestEditor()
	ed.vimHandler = vim.NewHandler() // starts in INSERT
	ed.Update(tea.KeyPressMsg{Code: '!', Text: "!"})
	if !ed.IsShellMode() {
		t.Error("! in vim INSERT did not enter shell mode")
	}
}

// --- interactive handoff and cancellation ------------------------------------

// runShellDraft puts text in the shell-mode draft and executes it, returning the
// resulting model state. The returned cmd is deliberately not run: the captured
// path would spawn a real shell and the interactive path would seize the
// terminal. What is under test is the routing decision, which is made before
// either happens.
func runShellDraft(t *testing.T, ed *editorCmp, draft string) tea.Cmd {
	t.Helper()
	ed.enterShellMode()
	ed.textarea.SetValue(draft)
	return ed.executeShell()
}

func TestExecuteShellRoutesByClassification(t *testing.T) {
	tests := []struct {
		name            string
		draft           string
		wantInteractive bool
		wantHistory     string
	}{
		{name: "ordinary command is captured", draft: "ls -la", wantInteractive: false, wantHistory: "ls -la"},
		{name: "known interactive program", draft: "sudo -v", wantInteractive: true, wantHistory: "sudo -v"},
		{name: "force prefix", draft: "!my-tool --login", wantInteractive: true, wantHistory: "!my-tool --login"},
		{name: "docker without a tty flag is captured", draft: "docker ps", wantInteractive: false, wantHistory: "docker ps"},
		{name: "docker exec -it", draft: "docker exec -it web sh", wantInteractive: true, wantHistory: "docker exec -it web sh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ed := newTestEditor()
			cmd := runShellDraft(t, ed, tt.draft)
			if cmd == nil {
				t.Fatal("executeShell returned no command")
			}
			if !ed.shellExecuting {
				t.Error("executeShell did not mark the editor as executing")
			}
			if ed.shellInteractive != tt.wantInteractive {
				t.Errorf("interactive = %v, want %v", ed.shellInteractive, tt.wantInteractive)
			}
			// Only the captured path has anything to cancel: an interactive run
			// owns the terminal, so ctrl+c reaches the command itself.
			if hasCancel := ed.shellCancel != nil; hasCancel == tt.wantInteractive {
				t.Errorf("shellCancel set = %v for interactive = %v", hasCancel, tt.wantInteractive)
			}
			if len(ed.shellHistory) != 1 || ed.shellHistory[0] != tt.wantHistory {
				t.Errorf("history = %v, want [%q]", ed.shellHistory, tt.wantHistory)
			}
		})
	}
}

// TestExecuteShellUnconfiguredProgramIsCaptured is the other half of the force
// prefix's reason to exist: an unknown program takes the captured path, where
// the "needs a terminal" hint points the user at `!!`.
func TestExecuteShellUnconfiguredProgramIsCaptured(t *testing.T) {
	ed := newTestEditor()
	if cmd := runShellDraft(t, ed, "my-tool --login"); cmd == nil {
		t.Fatal("executeShell returned no command")
	}
	if ed.shellInteractive {
		t.Fatal("an unconfigured program was routed to the interactive path")
	}
}

func TestExecuteShellEmptyDraftIsNoOp(t *testing.T) {
	for _, draft := range []string{"", "   ", "!", "!  "} {
		ed := newTestEditor()
		if cmd := runShellDraft(t, ed, draft); cmd != nil {
			t.Errorf("executeShell(%q) returned a command; want no-op", draft)
		}
		if ed.shellExecuting {
			t.Errorf("executeShell(%q) marked the editor as executing", draft)
		}
	}
}

func TestCancelKeysStopARunningCommand(t *testing.T) {
	for _, keyMsg := range []tea.KeyPressMsg{
		{Code: tea.KeyEscape},
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		t.Run(keyMsg.String(), func(t *testing.T) {
			ed := newTestEditor()
			ed.enterShellMode()
			cancelled := false
			ed.shellExecuting = true
			ed.shellCancel = func() { cancelled = true }

			ed.Update(keyMsg)

			if !cancelled {
				t.Errorf("%s while executing did not cancel the command", keyMsg.String())
			}
			if !ed.IsShellMode() {
				t.Error("cancelling left shell mode")
			}
		})
	}
}

func TestNonCancelKeyIsIgnoredWhileExecuting(t *testing.T) {
	ed := newTestEditor()
	ed.enterShellMode()
	ed.shellExecuting = true
	ed.shellCancel = func() { t.Fatal("an ordinary key cancelled the command") }

	ed.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})

	if got := ed.textarea.Value(); got != "" {
		t.Errorf("draft = %q; keys must not reach the textarea while a command runs", got)
	}
}

func TestExitShellModeCancelsARunningCommand(t *testing.T) {
	ed := newTestEditor()
	ed.enterShellMode()
	cancelled := false
	ed.shellExecuting = true
	ed.shellCancel = func() { cancelled = true }

	// A session switch is the real-world trigger: it exits shell mode without
	// the user ever pressing a cancel key.
	ed.Update(SessionClearedMsg{})

	if !cancelled {
		t.Error("leaving shell mode orphaned the running command")
	}
}

func TestShellResultClearsRunningState(t *testing.T) {
	ed := newTestEditor()
	ed.shellExecuting = true
	ed.shellCancel = func() {}

	ed.Update(ShellResultMsg{Command: "ls"})

	if ed.shellExecuting {
		t.Error("shellExecuting still set after the result arrived")
	}
	if ed.shellCancel != nil {
		t.Error("stale cancel func left behind after the result arrived")
	}
}

// TestCapturedRunHint covers the hint's construction, which lives with the
// captured path rather than with the renderer: only the executor knows the
// command text the hint has to quote back.
func TestCapturedRunHint(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		stdout   string
		wantHint bool
	}{
		{name: "sudo without a tty", stderr: "sudo: no tty present and no askpass program specified", wantHint: true},
		{name: "signature on stdout", stdout: "a terminal is required to read the password", wantHint: true},
		{name: "ordinary failure", stderr: "ls: nope: No such file or directory", wantHint: false},
		{name: "success", stdout: "a\nb\n", wantHint: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := buildCapturedResult("my-tool --login", tt.stdout, tt.stderr, 1, false, nil)
			if hasHint := msg.Hint != ""; hasHint != tt.wantHint {
				t.Errorf("hint present = %v, want %v (hint=%q)", hasHint, tt.wantHint, msg.Hint)
			}
			if tt.wantHint && !strings.Contains(msg.Hint, "!!my-tool --login") {
				t.Errorf("hint does not name the force-prefixed command: %q", msg.Hint)
			}
		})
	}
}

// A cancelled run must not be blamed on a missing terminal: the user stopped it.
func TestCancelledRunGetsNoHint(t *testing.T) {
	msg := buildCapturedResult("sudo -v", "", "sudo: no tty present", 143, true, nil)
	if msg.Hint != "" {
		t.Errorf("cancelled run carried a terminal hint: %q", msg.Hint)
	}
	if !msg.Cancelled {
		t.Error("cancelled run not marked as cancelled")
	}
}

// TestBangIsVimInputInCommandModes guards the shell sigil from stealing a key
// that vim owns. `!` in NORMAL is the start of a filter command and in a visual
// mode it filters the selection; neither should open shell mode.
func TestBangIsVimInputInCommandModes(t *testing.T) {
	tests := []struct {
		name  string
		enter []tea.KeyPressMsg
	}{
		{
			name:  "NORMAL",
			enter: []tea.KeyPressMsg{{Code: tea.KeyEscape}},
		},
		{
			name:  "VISUAL",
			enter: []tea.KeyPressMsg{{Code: tea.KeyEscape}, {Code: 'v', Text: "v"}},
		},
		{
			name:  "VISUAL LINE",
			enter: []tea.KeyPressMsg{{Code: tea.KeyEscape}, {Code: 'V', Text: "V"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ed := newTestEditor()
			ed.selectionLayout = newSelectionLayout()
			ed.vimHandler = vim.NewHandler()
			ed.SetSize(40, 3)
			for _, k := range tt.enter {
				ed.Update(k)
			}

			ed.Update(tea.KeyPressMsg{Code: '!', Text: "!"})

			if ed.IsShellMode() {
				t.Errorf("! in %s entered shell mode", tt.name)
			}
		})
	}
}
