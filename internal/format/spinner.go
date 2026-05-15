package format

import (
	"context"
	"fmt"
	"os"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	cterm "github.com/charmbracelet/x/term"
)

// Spinner wraps the bubbles spinner for non-interactive mode.
// In non-TTY environments (e.g., when invoked as a subprocess), the spinner
// becomes a no-op to avoid bubbletea's "could not open TTY" errors.
type Spinner struct {
	model  spinner.Model
	done   chan struct{}
	prog   *tea.Program
	ctx    context.Context
	cancel context.CancelFunc
	noop   bool // true when no TTY is available
}

// spinnerModel is the tea.Model for the spinner
type spinnerModel struct {
	spinner  spinner.Model
	message  string
	quitting bool
}

func (m spinnerModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m spinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		m.quitting = true
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case quitMsg:
		m.quitting = true
		return m, tea.Quit
	default:
		return m, nil
	}
}

func (m spinnerModel) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	return tea.NewView(fmt.Sprintf("%s %s", m.spinner.View(), m.message))
}

// quitMsg is sent when we want to quit the spinner
type quitMsg struct{}

// NewSpinner creates a new spinner with the given message.
// If stderr is not a terminal, the spinner is a no-op.
func NewSpinner(message string) *Spinner {
	// Bubbletea requires a TTY. When running as a subprocess (CI, scripts,
	// another CLI invoking opencode -p), /dev/tty is unavailable.
	// Detect this early and return a no-op spinner.
	if !cterm.IsTerminal(os.Stderr.Fd()) {
		return &Spinner{
			done: make(chan struct{}),
			noop: true,
		}
	}

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = s.Style.Foreground(s.Style.GetForeground())

	ctx, cancel := context.WithCancel(context.Background())

	model := spinnerModel{
		spinner: s,
		message: message,
	}

	prog := tea.NewProgram(model, tea.WithOutput(os.Stderr), tea.WithoutCatchPanics())

	return &Spinner{
		model:  s,
		done:   make(chan struct{}),
		prog:   prog,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start begins the spinner animation. No-op if no TTY is available.
func (s *Spinner) Start() {
	if s.noop {
		return
	}
	go func() {
		defer close(s.done)
		go func() {
			<-s.ctx.Done()
			s.prog.Send(quitMsg{})
		}()
		_, err := s.prog.Run()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error running spinner: %v\n", err)
		}
	}()
}

// Stop ends the spinner animation. No-op if no TTY is available.
func (s *Spinner) Stop() {
	if s.noop {
		return
	}
	s.cancel()
	<-s.done
}
