package chat

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/slashcmd"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
	"github.com/opencode-ai/opencode/internal/tui/vim"
)

type editorMode string

const (
	modeNormal editorMode = "normal"
	modeShell  editorMode = "shell"
)

// SubmissionExpander turns submitted message text into the prompt to send,
// resolving the slash invocations it contains. It is injected by the chat page,
// which owns the command registry and the active session; the editor calls it at
// the single expansion point in send(), before the queue/dispatch fork.
type SubmissionExpander func(text string) (slashcmd.Expansion, error)

// InvocationScanner reports the slash invocations the current draft contains.
// It backs the recognition hint drawn above the input, so it runs on every
// keystroke — unlike SubmissionExpander it must not substitute arguments or run
// shell markup.
type InvocationScanner func(text string) []slashcmd.Invocation

// recognizedInvocation is one chip in the recognition hint.
type recognizedInvocation struct {
	label  string
	action bool // performs a TUI action rather than expanding into the message
	// rejects marks an invocation that resolves to a known name the message may
	// not be submitted with — a skill without `user-invocable: true`. It gets a
	// chip because the absence of one means "this line will be sent as plain
	// text", which is the opposite of what happens: Expand refuses the whole
	// message.
	rejects bool
}

type editorCmp struct {
	width           int
	height          int
	app             *app.App
	session         session.Session
	expand          SubmissionExpander
	scan            InvocationScanner
	recognized      []recognizedInvocation
	scannedDraft    string
	textarea        textarea.Model
	attachments     []message.Attachment
	deleteMode      bool
	mode            editorMode
	shellHistory    []string
	shellHistoryIdx int
	shellExecuting  bool
	// shellCancel stops the captured command currently running. Non-nil only
	// while a captured command is in flight; an interactive run owns the
	// terminal, so ctrl+c reaches the command itself and there is nothing here
	// to cancel.
	shellCancel context.CancelFunc
	// shellInteractive records which path the in-flight command took.
	shellInteractive bool
	// selection holds the display coordinates of the active vim visual
	// selection. Computed in Update whenever the draft or the selection
	// changes, never in View — deriving them interrogates a textarea's layout,
	// and doing that during render risks moving a scroll position.
	selection       []selectionSpan
	selectionLayout selectionLayout
	// textareaOuterWidth is the width handed to textarea.SetWidth. The probe
	// that resolves selection coordinates must be given the same value, not the
	// narrower one Width() reports back after the widget's own reservations.
	textareaOuterWidth int
	vimHandler         *vim.Handler // nil when vim mode is disabled
}

type EditorKeyMaps struct {
	Send       key.Binding
	OpenEditor key.Binding
}

type DeleteAttachmentKeyMaps struct {
	AttachmentDeleteMode key.Binding
	Escape               key.Binding
	DeleteAllAttachments key.Binding
}

var editorMaps = EditorKeyMaps{
	Send: key.NewBinding(
		key.WithKeys("enter", "ctrl+s"),
		key.WithHelp("enter", "send message"),
	),
	OpenEditor: key.NewBinding(
		key.WithKeys("ctrl+e"),
		key.WithHelp("ctrl+e", "open editor"),
	),
}

var DeleteKeyMaps = DeleteAttachmentKeyMaps{
	AttachmentDeleteMode: key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl+r+{i}", "delete attachment at index i"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel delete mode"),
	),
	DeleteAllAttachments: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("ctrl+r+r", "delete all attchments"),
	),
}

const (
	maxAttachments = 5
)

// promptColumnWidth returns the number of terminal columns occupied by the left-side
// prompt widget rendered in View. All current modes (normal, shell, vim-normal) vary
// the prompt's color only — not its column width — so a single derived value is valid
// for all modes. A future multi-character prompt MUST update this helper so that
// SetSize and View cannot diverge independently.
func (m *editorCmp) promptColumnWidth() int {
	style := lipgloss.NewStyle().Padding(0, 0, 0, 1).Bold(true)
	return lipgloss.Width(style.Render(">"))
}

// hasAffordanceRow reports whether the single row above the textarea is shown.
// Attachments and the slash-invocation recognition hint share that one row, so
// the reservation is 0 or 1 regardless of how many affordances are active.
func (m *editorCmp) hasAffordanceRow() bool {
	return len(m.attachments) > 0 || len(m.recognized) > 0
}

// syncTextareaHeight sets the textarea height based on affordance-row presence.
// When the row is shown, one line is reserved for it.
// This MUST be called from SetSize and from every Update branch that changes the
// affordance row (attachments or recognized invocations), never from View.
func (m *editorCmp) syncTextareaHeight() {
	if m.hasAffordanceRow() {
		m.textarea.SetHeight(m.height - 1)
	} else {
		m.textarea.SetHeight(m.height)
	}
}

// refreshRecognition recomputes the recognition hint when the draft has changed.
// Called from the Update wrapper so every path that mutates the textarea is
// covered by one site; never from View, per the chat-editor-layout invariant
// that height is a function of state computed in Update.
func (m *editorCmp) refreshRecognition() {
	if m.scan == nil {
		return
	}
	draft := m.textarea.Value()
	if draft == m.scannedDraft {
		return
	}
	m.scannedDraft = draft

	var recognized []recognizedInvocation
	// An invocation must start at column 0, so a draft with no line beginning
	// in a slash cannot contain one. Checking that first keeps ordinary typing
	// off the registry-building path entirely.
	if m.mode == modeNormal && (strings.HasPrefix(draft, "/") || strings.Contains(draft, "\n/")) {
		for _, inv := range m.scan(draft) {
			switch {
			case inv.Err != nil:
				recognized = append(recognized, recognizedInvocation{label: "/" + inv.Name, rejects: true})
			case inv.Kind == slashcmd.KindPrompt:
				recognized = append(recognized, recognizedInvocation{label: "/" + inv.Name})
			case inv.Kind == slashcmd.KindAction:
				recognized = append(recognized, recognizedInvocation{label: "/" + inv.Name, action: true})
			}
		}
	}

	if len(recognized) == 0 && len(m.recognized) == 0 {
		return
	}
	m.recognized = recognized
	m.syncTextareaHeight()
}

func (m *editorCmp) openEditor() tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "nvim"
	}

	tmpfile, err := os.CreateTemp("", "msg_*.md")
	if err != nil {
		return util.ReportError(err)
	}
	// Seed the file with the current draft — including any staged invocations —
	// so $EDITOR opens what the user was writing instead of a blank buffer, and
	// so the content it returns can safely replace the input.
	if draft := m.textarea.Value(); draft != "" {
		if _, err := tmpfile.WriteString(draft); err != nil {
			tmpfile.Close()
			return util.ReportError(err)
		}
	}
	tmpfile.Close()

	// Seeding the file removes the abort the empty-file check used to provide:
	// quitting without saving now leaves the draft in the file, which would be
	// submitted as though the user had asked for it. The modification time
	// distinguishes the two — an editor that never wrote leaves it untouched —
	// so an unsaved exit cancels and the draft simply stays in the input.
	var seededAt time.Time
	if st, err := os.Stat(tmpfile.Name()); err == nil {
		seededAt = st.ModTime()
	}
	c := exec.Command(editor, tmpfile.Name()) //nolint:gosec
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return util.ReportError(err)
		}
		if st, statErr := os.Stat(tmpfile.Name()); statErr == nil &&
			!seededAt.IsZero() && st.ModTime().Equal(seededAt) {
			os.Remove(tmpfile.Name())
			return util.ReportWarn("Editor closed without saving; message left unchanged")
		}
		content, err := os.ReadFile(tmpfile.Name())
		if err != nil {
			return util.ReportError(err)
		}
		if len(content) == 0 {
			return util.ReportWarn("Message is empty")
		}
		os.Remove(tmpfile.Name())
		// Hand the content back through send() rather than emitting SendMsg
		// directly: send() is the single point where slash invocations are
		// expanded and where the queue/dispatch fork happens, and the external
		// editor's content must go through both.
		return editorContentMsg{Text: string(content)}
	})
}

// stageInvocation inserts a staged invocation at the cursor. It occupies its own
// line — a submitted invocation is only recognised at column 0 — so a newline is
// inserted first when the cursor sits mid-line. Existing text and attachments
// are untouched: staging is additive, so several invocations and the user's own
// prose accumulate into one message.
func (m *editorCmp) stageInvocation(text string) {
	lines := strings.Split(m.textarea.Value(), "\n")
	row, col := m.textarea.Line(), m.textarea.Column()

	// Text sitting to the right of the cursor has to be pushed down as well as
	// the invocation being moved to a line start: left where it is, it would
	// share the invocation's line and be parsed as its arguments, so staging
	// `/commit` mid-sentence would turn the rest of the sentence into an
	// argument instead of leaving it as prose.
	trailing := row >= 0 && row < len(lines) && col < len([]rune(lines[row]))

	if col > 0 {
		text = "\n" + text
	}
	if trailing {
		text += "\n"
	}
	m.textarea.InsertString(text)
	if trailing {
		// InsertString leaves the cursor after the newline, on the pushed-down
		// text. Put it back at the end of the invocation so typed arguments
		// land on its line. CursorUp moves one display line, and CursorEnd
		// clamps to the end of whatever logical row that lands in, so this is
		// correct whether or not the staged line soft-wraps.
		m.textarea.CursorUp()
		m.textarea.CursorEnd()
	}
}

func (m *editorCmp) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink}
	if m.vimHandler != nil {
		cmds = append(cmds, util.CmdHandler(VimModeChangedMsg{
			Mode: string(m.vimHandler.Mode()),
		}))
	}
	return tea.Batch(cmds...)
}

func (m *editorCmp) send() tea.Cmd {
	// Always check for empty input first — an empty submit is a no-op
	// regardless of queue or busy state (task 3.3).
	value := m.textarea.Value()
	if value == "" {
		return nil
	}

	// The backstop for text that reached the draft by a route that did not
	// switch modes. It runs BEFORE expansion so a `!` draft is never expanded
	// into a prompt or queued as a message.
	if command, ok := shellInvocation(value); ok && m.canEnterShellMode() {
		m.textarea.SetValue(command)
		m.enterShellMode()
		return tea.Batch(
			util.CmdHandler(ShellModeChangedMsg{ShellMode: true}),
			m.executeShell(),
		)
	}

	// Expand slash invocations before anything else. On failure the editor is
	// left exactly as the user typed it: a rejected submission must never eat
	// the message.
	expansion, err := m.expandSubmission(value)
	if err != nil {
		return util.ReportWarn(err.Error())
	}
	if expansion.Action != nil {
		// The whole message was one action command: it performs a TUI action
		// instead of being sent, so the input is consumed but no message is
		// created. Attachments are deliberately left in place — there is no
		// message to carry them, so they stay staged for the next one.
		m.textarea.Reset()
		return util.CmdHandler(RunActionMsg{Command: expansion.Action, Args: expansion.ActionArgs})
	}
	value = expansion.Prompt

	attachments := m.attachments

	// FIFO routing (Decision 8, task 3.1): enqueue whenever the queue is
	// non-empty OR the session is busy. Direct dispatch is only permitted
	// when BOTH are false — queue empty AND session observably idle. Without
	// the queue-non-empty branch, a submission arriving in the idle window
	// between two drain deliveries would bypass already-queued messages.
	if m.app.QueueLen(m.session.ID) > 0 || m.app.ActiveAgent().IsSessionBusy(m.session.ID) {
		m.app.EnqueueMessage(m.session.ID, app.QueuedMessage{
			Text:        value,
			Attachments: attachments,
		})
		m.textarea.Reset()
		m.attachments = nil
		// syncTextareaHeight MUST follow every mutation of m.attachments and
		// never run from View (chat-editor-layout spec).
		m.syncTextareaHeight()
		return nil
	}

	// Queue empty AND session idle: fall through to the direct dispatch path
	// (today's behavior, unchanged).
	m.textarea.Reset()
	m.attachments = nil
	m.syncTextareaHeight()
	return tea.Batch(
		util.CmdHandler(SendMsg{
			Text:        value,
			Attachments: attachments,
		}),
	)
}

// expandSubmission applies the injected expander. A nil expander (a bare editor
// in a test or a non-chat host) passes the text through untouched.
func (m *editorCmp) expandSubmission(text string) (slashcmd.Expansion, error) {
	if m.expand == nil {
		return slashcmd.Expansion{Prompt: text}, nil
	}
	return m.expand(text)
}

// shellInvocation reports whether text is a shell command the editor should run
// rather than send, and returns the command with the leading `!` stripped.
//
// This is the ONLY place the `!` rule lives. It is consulted from all three
// paths text can reach the draft by — the `!` keypress, a bracketed paste, and
// submit — because three parallel implementations is exactly how the paste path
// came to disagree with the typed one in the first place.
//
// The `[` and `=` exclusions keep pasted markdown (`![alt](url)`) and prose
// containing `!=` out of the shell. They are the minimum needed: a longer
// denylist would trade a rare false positive for a rule nobody can predict, and
// anything the predicate refuses is still one keystroke from working.
func shellInvocation(text string) (string, bool) {
	if !strings.HasPrefix(text, "!") {
		return "", false
	}
	rest := text[1:]
	if rest == "" {
		return "", false
	}
	switch rest[0] {
	case '[', '=':
		return "", false
	}
	command := strings.TrimSpace(rest)
	if command == "" {
		return "", false
	}
	return command, true
}

// canEnterShellMode reports whether the `!` sigil should switch the editor into
// shell mode. It must not fire while a vim command mode owns the keyboard:
// in NORMAL, VISUAL and VISUAL LINE, `!` is vim input.
func (m *editorCmp) canEnterShellMode() bool {
	if m.mode != modeNormal {
		return false
	}
	return m.vimHandler == nil || m.vimHandler.Mode() == vim.ModeInsert
}

func (m *editorCmp) enterShellMode() {
	m.mode = modeShell
	m.shellHistoryIdx = len(m.shellHistory)
	m.textarea.Placeholder = "Enter shell command..."
}

func (m *editorCmp) exitShellMode() {
	// Leaving shell mode with a command still running — a session switch, for
	// instance — would orphan it: nothing would be left holding the cancel func
	// and its output would land in a session the user has moved away from.
	m.cancelShell()
	m.mode = modeNormal
	m.textarea.Placeholder = ""
	m.textarea.Reset()
}

// executeShell runs the shell-mode draft. It picks between the two execution
// paths and is the only place that decision is made.
func (m *editorCmp) executeShell() tea.Cmd {
	typed := strings.TrimSpace(m.textarea.Value())
	if typed == "" {
		return nil
	}

	// A leading `!` inside shell mode forces the terminal handoff — `!!command`
	// as typed from the normal editor. It is a text prefix rather than a key
	// chord because every convenient ctrl+ chord is already bound (ctrl+o is
	// model selection), and because a prefix is visible in the draft before the
	// user commits to it.
	//
	// Resolved before anything is consumed: a draft that turns out to be no
	// command at all (a lone `!`) must leave the history and the draft exactly
	// as they were, so the user can correct the typo instead of losing it.
	command, forced := strings.CutPrefix(typed, "!")
	if forced {
		command = strings.TrimSpace(command)
		if command == "" {
			return nil
		}
	}

	// History keeps what the user typed, force prefix and all, so pressing up
	// re-runs the command the same way it ran the first time.
	m.shellHistory = append(m.shellHistory, typed)
	m.shellHistoryIdx = len(m.shellHistory)
	m.textarea.Reset()
	m.shellExecuting = true

	var extra []string
	if cfg := config.Get(); cfg != nil {
		extra = cfg.Shell.Interactive
	}
	var run tea.Cmd
	m.shellInteractive = forced || shell.ClassifyInteractive(command, extra)
	if m.shellInteractive {
		run = m.executeShellInteractive(command)
	} else {
		run = m.executeShellCaptured(command)
	}
	return tea.Batch(util.CmdHandler(ShellExecutingMsg{Executing: true}), run)
}

// executeShellCaptured runs a command through the shared persistent shell and
// collects its output for the chat — the path every command took before the
// interactive handoff existed.
func (m *editorCmp) executeShellCaptured(command string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.shellCancel = cancel

	return func() tea.Msg {
		defer cancel()

		sh := shell.GetPersistentShell(config.WorkingDirectory())
		if sh == nil {
			return ShellResultMsg{
				Command:  command,
				ExitCode: 1,
				Err:      fmt.Errorf("failed to create shell instance"),
			}
		}

		stdout, stderr, exitCode, interrupted, err := sh.Exec(ctx, command, tools.DefaultTimeout)
		return buildCapturedResult(command, stdout, stderr, exitCode, interrupted && ctx.Err() != nil, err)
	}
}

// buildCapturedResult assembles the result of a captured run, including the
// hint that turns a fast terminal-related failure into an actionable one.
//
// Before terminal isolation, a command like `sudo -v` prompted on opencode's
// own terminal and hung; now it fails immediately with sudo's own diagnostic.
// Without the hint the user just sees a cryptic refusal and no way forward.
func buildCapturedResult(command, stdout, stderr string, exitCode int, cancelled bool, err error) ShellResultMsg {
	msg := ShellResultMsg{
		Command:   command,
		Stdout:    stdout,
		Stderr:    stderr,
		ExitCode:  exitCode,
		Err:       err,
		Cancelled: cancelled,
	}
	// A cancelled run must not be blamed on a missing terminal: the user
	// stopped it, and any half-written diagnostic is incidental.
	if !cancelled && shell.NeedsTerminal(stderr+"\n"+stdout) {
		msg.Hint = "This command needs a terminal. Re-run it as `!!" + command +
			"` to hand it the terminal, or add its program to `shell.interactive` in .opencode.json."
	}
	return msg
}

// executeShellInteractive hands opencode's terminal to the command for the
// duration of its run, using the same mechanism ctrl+e uses for $EDITOR.
//
// The command is a FRESH shell, not the persistent one: the persistent shell's
// stdin is a pipe and cannot be handed a terminal. Note that this spawn
// deliberately does NOT go through detachFromTerminal — owning the terminal is
// the entire point here, which is the exact opposite of the captured path's
// policy. That opposition is the design, not an oversight.
//
// Everything is built inside the returned command rather than around it, so
// constructing the command spawns no shell and touches no filesystem.
func (m *editorCmp) executeShellInteractive(command string) tea.Cmd {
	return func() tea.Msg {
		fail := func(err error) tea.Msg {
			return ShellResultMsg{
				Command:     command,
				Interactive: true,
				ExitCode:    1,
				Err:         err,
			}
		}

		sh := shell.GetPersistentShell(config.WorkingDirectory())
		if sh == nil {
			return fail(fmt.Errorf("failed to create shell instance"))
		}

		// Run where the next captured command would run, so `cd` history
		// applies to both paths. If that directory has since disappeared, fall
		// back rather than failing the run with a chdir error.
		workdir := sh.Cwd()
		if st, err := os.Stat(workdir); workdir == "" || err != nil || !st.IsDir() {
			workdir = config.WorkingDirectory()
		}

		statusFile, err := os.CreateTemp("", "opencode-interactive-status-*")
		if err != nil {
			return fail(err)
		}
		statusFile.Close()
		cwdFile, err := os.CreateTemp("", "opencode-interactive-cwd-*")
		if err != nil {
			os.Remove(statusFile.Name())
			return fail(err)
		}
		cwdFile.Close()

		// The command's own status has to be captured before the wrapper's
		// trailing bookkeeping overwrites $?; the wrapper's exit status would
		// otherwise mask it entirely.
		wrapper := fmt.Sprintf("%s\nprintf %%s $? > %s\npwd > %s\n",
			command, shellQuoteArg(statusFile.Name()), shellQuoteArg(cwdFile.Name()))

		c := exec.Command(shell.GetShellPath(), shell.CommandArgs(wrapper)...) //nolint:gosec
		c.Dir = workdir
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr

		return tea.ExecProcess(c, func(runErr error) tea.Msg {
			defer os.Remove(statusFile.Name())
			defer os.Remove(cwdFile.Name())

			msg := ShellResultMsg{Command: command, Interactive: true}

			if raw, readErr := os.ReadFile(statusFile.Name()); readErr == nil {
				if code, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil {
					msg.ExitCode = code
				}
			} else if runErr != nil {
				msg.ExitCode = 1
				msg.Err = runErr
			}

			// Apply whatever directory the command left behind back to the
			// persistent shell, so `cd` behaves the same on both paths.
			if raw, readErr := os.ReadFile(cwdFile.Name()); readErr == nil {
				if newCwd := strings.TrimSpace(string(raw)); newCwd != "" && newCwd != workdir {
					// Off the event loop: the resync goes through the persistent
					// shell's command queue, which may be occupied by a long
					// agent bash call, and blocking here would freeze the TUI
					// for its duration. The queue is FIFO, so a command the user
					// types next still lands after this one.
					go func() {
						_, _, _, _, _ = sh.Exec(context.Background(), "cd "+shellQuoteArg(newCwd), tools.DefaultTimeout)
					}()
				}
			}

			return msg
		})()
	}
}

// shellQuoteArg single-quotes a string for safe interpolation into a shell
// command. Paths come from os.CreateTemp and the shell's own pwd, but they are
// still interpolated into a command string, so they are quoted rather than
// trusted.
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cancelShell stops the running captured command. The persistent shell's
// context watcher turns the cancellation into a descendant-tree terminate, so
// nothing is left behind.
//
// An interactive run is not cancellable from here by design: it owns the
// terminal, so ctrl+c goes to the command itself, which is what the user means.
func (m *editorCmp) cancelShell() tea.Cmd {
	if m.shellCancel == nil {
		return nil
	}
	m.shellCancel()
	m.shellCancel = nil
	return nil
}

func (m *editorCmp) shellHistoryUp() {
	if len(m.shellHistory) == 0 {
		return
	}
	if m.shellHistoryIdx > 0 {
		m.shellHistoryIdx--
		m.textarea.SetValue(m.shellHistory[m.shellHistoryIdx])
	}
}

func (m *editorCmp) shellHistoryDown() {
	if len(m.shellHistory) == 0 {
		return
	}
	if m.shellHistoryIdx < len(m.shellHistory)-1 {
		m.shellHistoryIdx++
		m.textarea.SetValue(m.shellHistory[m.shellHistoryIdx])
	} else {
		m.shellHistoryIdx = len(m.shellHistory)
		m.textarea.Reset()
	}
}

func (m *editorCmp) IsShellMode() bool {
	return m.mode == modeShell
}

func (m *editorCmp) ConsumesCtrlC() bool {
	if m.vimHandler != nil {
		return m.vimHandler.ConsumesCtrlC()
	}
	return false
}

func (m *editorCmp) VimMode() string {
	if m.vimHandler != nil {
		return string(m.vimHandler.Mode())
	}
	return ""
}

// Update wraps update so the recognition hint is refreshed after every message,
// whichever branch mutated the textarea. Each branch of update returns m itself,
// so the wrapper can act on the same instance before returning it.
func (m *editorCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.update(msg)
	m.refreshRecognition()
	m.refreshSelection()
	return model, cmd
}

// refreshSelection recomputes the visual selection's display coordinates. Like
// refreshRecognition it runs once per message from the Update wrapper, so every
// path that moves the cursor or edits the draft is covered by one call site —
// and, per the chat-editor-layout invariant, never from View.
func (m *editorCmp) refreshSelection() {
	if m.vimHandler == nil || !m.vimHandler.Mode().IsVisual() {
		m.selection = nil
		return
	}

	from, to, linewise, active := m.vimHandler.Selection(&m.textarea)
	if !active {
		m.selection = nil
		return
	}

	m.selectionLayout.sync(m.textarea.Value(), m.textareaOuterWidth, m.textarea.Height())
	m.selection = m.selectionLayout.spans(
		from, to,
		m.textarea.ScrollYOffset(),
		textareaPromptWidth,
		m.textarea.Height(),
		linewise,
	)
}

func (m *editorCmp) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case ToggleVimModeMsg:
		if m.vimHandler != nil {
			// Disable vim mode
			m.vimHandler = nil
			return m, util.CmdHandler(VimModeChangedMsg{Mode: ""})
		}
		// Enable vim mode — starts in INSERT
		m.vimHandler = vim.NewHandler()
		return m, util.CmdHandler(VimModeChangedMsg{Mode: string(m.vimHandler.Mode())})
	case dialog.ThemeChangedMsg:
		// CreateTextArea seeds the replacement with SetValue, which drops the
		// cursor at the end of the buffer. Left alone that collapses an active
		// visual selection to a single character, because the anchor survives on
		// the vim handler while the cursor jumps.
		line, col := m.textarea.Line(), m.textarea.Column()
		m.textarea = CreateTextArea(&m.textarea)
		// CreateTextArea seeds the replacement by feeding the outgoing
		// textarea's *inner* width back into SetWidth, which subtracts the
		// prompt reservation a second time — so every theme change narrowed the
		// editor by one column, permanently. Re-deriving the size from the
		// container is the fix, and it also keeps the selection probe's width
		// in sync, since both come from m.width here.
		if m.width > 0 {
			m.SetSize(m.width, m.height)
		}
		restoreCursor(&m.textarea, line, col)
	case dialog.CompletionSelectedMsg:
		existingValue := m.textarea.Value()
		modifiedValue := strings.Replace(existingValue, msg.SearchString, msg.CompletionValue, 1)
		m.textarea.SetValue(modifiedValue)
		return m, nil
	case dialog.CompletionRemoveTextMsg:
		existingValue := m.textarea.Value()
		modifiedValue := strings.Replace(existingValue, msg.SearchString, "", 1)
		m.textarea.SetValue(modifiedValue)
		return m, nil
	case dialog.StageInvocationMsg:
		m.stageInvocation(msg.Text)
		return m, nil
	case editorContentMsg:
		// Content from the external $EDITOR: replace the input and submit it
		// through the normal path so it is expanded and routed like any other
		// submission.
		m.textarea.SetValue(msg.Text)
		return m, m.send()
	case SessionClearedMsg:
		m.session = session.Session{}
		if m.mode == modeShell {
			m.exitShellMode()
			return m, util.CmdHandler(ShellModeChangedMsg{ShellMode: false})
		}
		return m, nil
	case SessionSelectedMsg:
		if msg.ID != m.session.ID {
			m.session = msg
			if m.mode == modeShell {
				m.exitShellMode()
				return m, util.CmdHandler(ShellModeChangedMsg{ShellMode: false})
			}
		}
		return m, nil
	case ShellResultMsg:
		m.shellExecuting = false
		m.shellCancel = nil
		return m, nil
	case dialog.AttachmentAddedMsg:
		if len(m.attachments) >= maxAttachments {
			logging.ErrorPersist(fmt.Sprintf("cannot add more than %d images", maxAttachments))
			return m, cmd
		}
		m.attachments = append(m.attachments, msg.Attachment)
		m.syncTextareaHeight()
	case tea.PasteMsg:
		// Bracketed paste is not a key press, so without this branch pasted text
		// never reaches the `!` check and the same characters behave
		// differently depending on how they arrived.
		if command, ok := shellInvocation(msg.Content); ok &&
			m.canEnterShellMode() && m.textarea.Value() == "" {
			m.enterShellMode()
			m.textarea.SetValue(command)
			return m, util.CmdHandler(ShellModeChangedMsg{ShellMode: true})
		}
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if m.shellExecuting {
			// Everything else is swallowed while a command runs, but the cancel
			// keys must not be: without them a slow command locks the editor
			// until the two-minute tool timeout expires.
			if key.Matches(msg, DeleteKeyMaps.Escape) || msg.String() == "ctrl+c" {
				return m, m.cancelShell()
			}
			return m, nil
		}

		if key.Matches(msg, DeleteKeyMaps.AttachmentDeleteMode) {
			m.deleteMode = true
			return m, nil
		}
		if key.Matches(msg, DeleteKeyMaps.DeleteAllAttachments) && m.deleteMode {
			m.deleteMode = false
			m.attachments = nil
			m.syncTextareaHeight()
			return m, nil
		}
		if m.deleteMode && len(msg.Text) > 0 && unicode.IsDigit(rune(msg.Text[0])) {
			num := int(rune(msg.Text[0]) - '0')
			m.deleteMode = false
			if num < 10 && len(m.attachments) > num {
				if num == 0 {
					m.attachments = m.attachments[num+1:]
				} else {
					m.attachments = slices.Delete(m.attachments, num, num+1)
				}
				m.syncTextareaHeight()
				return m, nil
			}
		}
		if key.Matches(msg, messageKeys.PageUp) || key.Matches(msg, messageKeys.PageDown) ||
			key.Matches(msg, messageKeys.HalfPageUp) || key.Matches(msg, messageKeys.HalfPageDown) {
			return m, nil
		}

		// Shell mode: "!" typed at position 0 on empty input. This path handles
		// the bare sigil rather than going through shellInvocation, because at
		// this point there is no command yet — the user is about to type one.
		// shellInvocation governs the paths where the whole text already exists
		// (paste and submit).
		if m.canEnterShellMode() && msg.Text == "!" && m.textarea.Value() == "" {
			m.enterShellMode()
			return m, util.CmdHandler(ShellModeChangedMsg{ShellMode: true})
		}

		// Shell mode key handling
		if m.mode == modeShell {
			// Escape or Ctrl+C exits shell mode
			if key.Matches(msg, DeleteKeyMaps.Escape) || msg.String() == "ctrl+c" {
				m.exitShellMode()
				return m, util.CmdHandler(ShellModeChangedMsg{ShellMode: false})
			}
			// Backspace on empty exits shell mode
			if msg.String() == "backspace" && m.textarea.Value() == "" {
				m.exitShellMode()
				return m, util.CmdHandler(ShellModeChangedMsg{ShellMode: false})
			}
			// Up/Down navigate shell history
			if msg.String() == "up" {
				m.shellHistoryUp()
				return m, nil
			}
			if msg.String() == "down" {
				m.shellHistoryDown()
				return m, nil
			}
			// Enter executes shell command
			if m.textarea.Focused() && key.Matches(msg, editorMaps.Send) {
				return m, m.executeShell()
			}
			// Let other keys pass through to textarea
			m.textarea, cmd = m.textarea.Update(msg)
			return m, cmd
		}

		// Vim mode key handling
		if m.vimHandler != nil {
			handled, vimCmd, modeChanged := m.vimHandler.HandleKey(msg, &m.textarea)
			if modeChanged {
				return m, tea.Batch(vimCmd, util.CmdHandler(VimModeChangedMsg{
					Mode: string(m.vimHandler.Mode()),
				}))
			}
			if handled {
				return m, vimCmd
			}
			// Not handled by vim — fall through to normal handling
		}

		if key.Matches(msg, editorMaps.OpenEditor) {
			// Queuing Ctrl+E / external $EDITOR sessions is a future decision;
			// keep the busy-reject guard unchanged (task 6.1). The queue-length
			// arm is what preserves FIFO: openEditor's result is delivered via
			// SendMsg → chatPage.sendMessage, which dispatches directly and
			// would jump ahead of already-queued messages.
			if m.app.ActiveAgent().IsSessionBusy(m.session.ID) {
				return m, util.ReportWarn("Agent is working, please wait...")
			}
			if m.app.QueueLen(m.session.ID) > 0 {
				// Distinct message: the session is idle here (e.g. the drain
				// worker halted on an error), so "Agent is working" would be
				// factually wrong and leave the user with no idea why ctrl+e
				// is locked.
				return m, util.ReportWarn("Messages are queued — wait for them to send, press ctrl+g to view or ctrl+x to discard")
			}
			return m, m.openEditor()
		}
		if key.Matches(msg, DeleteKeyMaps.Escape) {
			m.deleteMode = false
			return m, nil
		}
		// Handle Enter key
		if m.textarea.Focused() && key.Matches(msg, editorMaps.Send) {
			value := m.textarea.Value()
			if len(value) > 0 && value[len(value)-1] == '\\' {
				// If the last character is a backslash, remove it and add a newline
				m.textarea.SetValue(value[:len(value)-1] + "\n")
				return m, nil
			} else {
				// Otherwise, send the message
				return m, m.send()
			}
		}

	}
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

func (m *editorCmp) View() tea.View {
	t := theme.CurrentTheme()

	promptChar := ">"
	promptColor := t.Primary()
	if m.mode == modeShell {
		promptChar = "$"
		promptColor = t.Warning()
	} else if m.vimHandler != nil && m.vimHandler.Mode() == vim.ModeNormal {
		promptColor = t.Secondary()
	}

	style := lipgloss.NewStyle().
		Padding(0, 0, 0, 1).
		Bold(true).
		Foreground(promptColor)

	if m.shellExecuting {
		spinnerText := lipgloss.NewStyle().
			Padding(0, 0, 0, 1).
			Foreground(t.Warning()).
			Bold(true).
			Render("$ running...")
		return tea.NewView(spinnerText)
	}

	if !m.hasAffordanceRow() {
		return tea.NewView(lipgloss.JoinHorizontal(lipgloss.Top, style.Render(promptChar), m.textareaView()))
	}
	return tea.NewView(lipgloss.JoinVertical(lipgloss.Top,
		m.affordanceRow(),
		lipgloss.JoinHorizontal(lipgloss.Top, style.Render(promptChar),
			m.textareaView()),
	))
}

// textareaView renders the input, repairing the background of the rows below the
// placeholder.
//
// The textarea has two render paths, and only one of them pads a row out to the
// full width with its own styles. On the placeholder path — empty value with a
// placeholder set, which is shell mode — the rows past the placeholder are the
// end-of-buffer character alone, so the padding that fills them comes from the
// textarea's viewport instead, in unstyled cells that land after the style's
// reset. They draw with the terminal's default background: the black rectangle
// under the `$` prompt (the background-gap pitfall in CLAUDE.md).
//
// Every background the textarea itself sets is already the theme background, so
// forcing that background over the rendered view repaints only those unstyled
// cells.
func (m *editorCmp) textareaView() string {
	view := m.textarea.View()
	if m.textarea.Placeholder == "" || m.textarea.Value() != "" {
		// applySelection returns the view untouched when there is no selection,
		// so a non-visual render is byte-identical to what it was before
		// selection rendering existed.
		return applySelection(view, m.selection)
	}
	return styles.ForceReplaceBackgroundWithLipgloss(view, theme.CurrentTheme().Background())
}

// restoreCursor puts the cursor back on a rebuilt textarea. The widget exposes
// no absolute row setter, so the row is reached by stepping — which is exactly
// what the vim handler already does to place the cursor.
func restoreCursor(ta *textarea.Model, line, col int) {
	ta.MoveToBegin()
	for range line {
		ta.CursorDown()
	}
	ta.SetCursorColumn(col)
}

// textareaPromptWidth is the column the textarea's own prompt occupies on every
// rendered row. CreateTextArea sets Prompt to a single space; a change there
// MUST update this constant, or every selection highlight shifts sideways.
const textareaPromptWidth = 1

// affordanceRow renders the single row above the input: attachment chips first,
// then one chip per recognized slash invocation, within the container width.
func (m *editorCmp) affordanceRow() string {
	attachments := m.attachmentsContent()
	hint := m.recognitionContent(m.rowWidth() - lipgloss.Width(attachments))
	switch {
	case hint == "":
		return m.padRow(attachments)
	case attachments == "":
		return m.padRow(hint)
	}
	return m.padRow(lipgloss.JoinHorizontal(lipgloss.Top, attachments, hint))
}

// rowWidth is the rendered width of the input row this one sits above: the
// prompt column plus the textarea, which SetSize gives one column less than the
// container so the cursor never lands in the terminal's deferred-wrap column.
// The affordance row must match it exactly — a wider row makes JoinVertical pad
// every input line with an unstyled cell, which is the black gap this alignment
// exists to avoid.
func (m *editorCmp) rowWidth() int {
	return max(0, m.width-1)
}

// padRow fills the affordance row out to the input row's width with the theme
// background. JoinVertical pads shorter lines with unstyled cells, which render
// as a black gap beside the themed editor (see the background-gap pitfall in
// CLAUDE.md); padding the row itself avoids that without forcing a background
// over the textarea's own cursor rendering.
func (m *editorCmp) padRow(row string) string {
	width := m.rowWidth()
	if width <= 0 {
		return row
	}
	t := theme.CurrentTheme()
	return styles.BaseStyle().
		Width(width).
		Background(t.Background()).
		Render(row)
}

// recognitionContent renders one chip per invocation the draft will actually
// expand or run. An unrecognized `/token` produces no chip, which is the whole
// point: the absence of a chip is how the user learns that what they typed will
// be sent as plain text rather than resolved.
//
// Chips are dropped rather than wrapped once they exceed budget columns — the
// row is one line, and the editor's no-overflow contract binds it. A dropped
// chip is accounted for by a trailing count so the hint never under-reports.
func (m *editorCmp) recognitionContent(budget int) string {
	if len(m.recognized) == 0 || budget <= 0 {
		return ""
	}

	t := theme.CurrentTheme()
	base := styles.BaseStyle().MarginLeft(1).Background(t.Background())
	expands := base.Foreground(t.Success())
	acts := base.Foreground(t.Info())
	rejects := base.Foreground(t.Error())
	overflow := base.Foreground(t.TextMuted())

	var (
		chips []string
		used  int
	)
	for i, inv := range m.recognized {
		style := expands
		switch {
		case inv.rejects:
			style = rejects
		case inv.action:
			style = acts
		}
		chip := style.Render(fmt.Sprintf("%s %s", styles.SkillIcon, inv.label))
		width := lipgloss.Width(chip)

		// Reserve room for the "+N" marker whenever chips remain after this one.
		remaining := len(m.recognized) - i - 1
		reserve := 0
		if remaining > 0 {
			reserve = lipgloss.Width(overflow.Render(fmt.Sprintf("+%d", remaining)))
		}
		if used+width+reserve > budget {
			break
		}
		chips = append(chips, chip)
		used += width
	}

	if dropped := len(m.recognized) - len(chips); dropped > 0 {
		marker := overflow.Render(fmt.Sprintf("+%d", dropped))
		if used+lipgloss.Width(marker) <= budget {
			chips = append(chips, marker)
		}
	}
	if len(chips) == 0 {
		return ""
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, chips...)
}

func (m *editorCmp) SetSize(width, height int) tea.Cmd {
	m.width = width
	m.height = height
	// Reserve promptColumnWidth() columns for the left-side prompt widget plus one
	// column as a right-margin guard — the cursor never reaches the terminal's final
	// deferred-wrap column, avoiding inconsistent glyph rendering across emulators.
	m.textareaOuterWidth = max(0, width-m.promptColumnWidth()-1)
	m.textarea.SetWidth(m.textareaOuterWidth)
	m.syncTextareaHeight()
	return nil
}

func (m *editorCmp) GetSize() (int, int) {
	return m.textarea.Width(), m.textarea.Height()
}

func (m *editorCmp) attachmentsContent() string {
	var styledAttachments []string
	t := theme.CurrentTheme()
	attachmentStyles := styles.BaseStyle().
		MarginLeft(1).
		Background(t.TextMuted()).
		Foreground(t.Text())
	for i, attachment := range m.attachments {
		var filename string
		if len(attachment.FileName) > 10 {
			filename = fmt.Sprintf(" %s %s...", styles.DocumentIcon, attachment.FileName[0:7])
		} else {
			filename = fmt.Sprintf(" %s %s", styles.DocumentIcon, attachment.FileName)
		}
		if m.deleteMode {
			filename = fmt.Sprintf("%d%s", i, filename)
		}
		styledAttachments = append(styledAttachments, attachmentStyles.Render(filename))
	}
	content := lipgloss.JoinHorizontal(lipgloss.Left, styledAttachments...)
	return content
}

func (m *editorCmp) BindingKeys() []key.Binding {
	bindings := []key.Binding{}
	bindings = append(bindings, layout.KeyMapToSlice(editorMaps)...)
	bindings = append(bindings, layout.KeyMapToSlice(DeleteKeyMaps)...)
	return bindings
}

func CreateTextArea(existing *textarea.Model) textarea.Model {
	t := theme.CurrentTheme()
	bgColor := t.Background()
	textColor := t.Text()
	textMutedColor := t.TextMuted()

	ta := textarea.New()
	s := ta.Styles()
	s.Blurred.Base = styles.BaseStyle().Background(bgColor).Foreground(textColor)
	s.Blurred.CursorLine = styles.BaseStyle().Background(bgColor)
	s.Blurred.Placeholder = styles.BaseStyle().Background(bgColor).Foreground(textMutedColor)
	s.Blurred.Text = styles.BaseStyle().Background(bgColor).Foreground(textColor)
	s.Focused.Base = styles.BaseStyle().Background(bgColor).Foreground(textColor)
	s.Focused.CursorLine = styles.BaseStyle().Background(bgColor)
	s.Focused.Placeholder = styles.BaseStyle().Background(bgColor).Foreground(textMutedColor)
	s.Focused.Text = styles.BaseStyle().Background(bgColor).Foreground(textColor)
	ta.SetStyles(s)

	ta.Prompt = " "
	ta.ShowLineNumbers = false
	ta.CharLimit = -1

	if existing != nil {
		ta.SetValue(existing.Value())
		ta.SetWidth(existing.Width())
		ta.SetHeight(existing.Height())
	}

	ta.Focus()
	return ta
}

func NewEditorCmp(app *app.App, expand SubmissionExpander, scan InvocationScanner) tea.Model {
	ta := CreateTextArea(nil)
	var vimH *vim.Handler
	if config.Get().TUI.VimMode {
		vimH = vim.NewHandler()
	}
	return &editorCmp{
		app:             app,
		expand:          expand,
		scan:            scan,
		textarea:        ta,
		mode:            modeNormal,
		vimHandler:      vimH,
		selectionLayout: newSelectionLayout(),
	}
}
