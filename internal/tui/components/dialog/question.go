package dialog

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	questionpkg "github.com/opencode-ai/opencode/internal/question"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

// QuestionResponseMsg is sent when the user answers or dismisses a question.
type QuestionResponseMsg struct {
	RequestID string
	Answers   [][]string
	Rejected  bool
}

// QuestionDialogCmp is the interface for the question dialog component.
type QuestionDialogCmp interface {
	tea.Model
	layout.Bindings
	SetQuestion(request questionpkg.Request) tea.Cmd
	RequestID() string
}

type questionKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Enter  key.Binding
	Escape key.Binding
	Space  key.Binding
	Tab    key.Binding
	J      key.Binding
	K      key.Binding
}

var questionKeys = questionKeyMap{
	Up: key.NewBinding(
		key.WithKeys("up"),
		key.WithHelp("↑", "previous option"),
	),
	Down: key.NewBinding(
		key.WithKeys("down"),
		key.WithHelp("↓", "next option"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("⏎", "select"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "dismiss"),
	),
	Space: key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("space", "toggle"),
	),
	Tab: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch to custom input"),
	),
	J: key.NewBinding(
		key.WithKeys("j"),
		key.WithHelp("j", "next option"),
	),
	K: key.NewBinding(
		key.WithKeys("k"),
		key.WithHelp("k", "previous option"),
	),
}

const (
	questionDialogMinWidth   = 50
	questionDialogMaxVisible = 6
	questionDialogItemRows   = 2 // label + description
)

type questionDialogCmp struct {
	width      int
	height     int
	windowSize tea.WindowSizeMsg

	request     questionpkg.Request
	questionIdx int // current question index (for multi-question)
	answers     [][]string

	selectedIdx int    // cursor position in options list
	selected    []bool // toggle state per option (multi-select)

	customFocused bool // whether custom text input is focused
	customInput   textinput.Model

	// contentWidth and maxVisible are cached layout dimensions
	contentWidth int
	maxVisible   int
}

func (q *questionDialogCmp) Init() tea.Cmd {
	return textinput.Blink
}

func (q *questionDialogCmp) currentPrompt() *questionpkg.Prompt {
	if q.questionIdx < len(q.request.Questions) {
		return &q.request.Questions[q.questionIdx]
	}
	return nil
}

func (q *questionDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		q.windowSize = msg
		q.recomputeLayout()
	case tea.KeyPressMsg:
		prompt := q.currentPrompt()
		if prompt == nil {
			return q, nil
		}

		// If custom input is focused, handle text input first
		if q.customFocused {
			switch {
			case key.Matches(msg, questionKeys.Escape):
				return q, q.reject()
			case key.Matches(msg, questionKeys.Enter):
				val := strings.TrimSpace(q.customInput.Value())
				if val != "" {
					return q, q.submitAnswer([]string{val})
				}
				return q, nil
			case key.Matches(msg, questionKeys.Tab):
				q.customFocused = false
				q.customInput.Blur()
				return q, nil
			default:
				var cmd tea.Cmd
				q.customInput, cmd = q.customInput.Update(msg)
				return q, cmd
			}
		}

		// Options list navigation
		switch {
		case key.Matches(msg, questionKeys.Escape):
			return q, q.reject()
		case key.Matches(msg, questionKeys.Up):
			q.moveSelection(-1)
			return q, nil
		case key.Matches(msg, questionKeys.Down):
			q.moveSelection(1)
			return q, nil
		case key.Matches(msg, questionKeys.K):
			q.moveSelection(-1)
			return q, nil
		case key.Matches(msg, questionKeys.J):
			q.moveSelection(1)
			return q, nil
		case key.Matches(msg, questionKeys.Tab):
			if prompt.IsCustomEnabled() {
				q.customFocused = true
				q.customInput.Focus()
				return q, nil
			}
		case key.Matches(msg, questionKeys.Space):
			if prompt.Multiple && q.selectedIdx < len(prompt.Options) {
				q.selected[q.selectedIdx] = !q.selected[q.selectedIdx]
				return q, nil
			}
		case key.Matches(msg, questionKeys.Enter):
			if prompt.Multiple {
				// Submit all selected options
				var labels []string
				for i, opt := range prompt.Options {
					if i < len(q.selected) && q.selected[i] {
						labels = append(labels, opt.Label)
					}
				}
				if len(labels) == 0 {
					return q, nil // require at least one selection
				}
				return q, q.submitAnswer(labels)
			}
			// Single select — submit the highlighted option
			if q.selectedIdx < len(prompt.Options) {
				return q, q.submitAnswer([]string{prompt.Options[q.selectedIdx].Label})
			}
		}
	}
	return q, nil
}

func (q *questionDialogCmp) moveSelection(delta int) {
	prompt := q.currentPrompt()
	if prompt == nil || len(prompt.Options) == 0 {
		return
	}
	next := q.selectedIdx + delta
	if next < 0 {
		next = 0
	}
	if next >= len(prompt.Options) {
		next = len(prompt.Options) - 1
	}
	q.selectedIdx = next
}

func (q *questionDialogCmp) submitAnswer(labels []string) tea.Cmd {
	q.answers[q.questionIdx] = labels
	q.questionIdx++

	if q.questionIdx < len(q.request.Questions) {
		// Advance to next question
		q.resetForCurrentQuestion()
		return nil
	}

	// All questions answered — submit
	answers := make([][]string, len(q.answers))
	copy(answers, q.answers)
	requestID := q.request.ID
	return util.CmdHandler(QuestionResponseMsg{
		RequestID: requestID,
		Answers:   answers,
	})
}

func (q *questionDialogCmp) reject() tea.Cmd {
	requestID := q.request.ID
	return util.CmdHandler(QuestionResponseMsg{
		RequestID: requestID,
		Rejected:  true,
	})
}

func (q *questionDialogCmp) resetForCurrentQuestion() {
	prompt := q.currentPrompt()
	if prompt == nil {
		return
	}
	q.selectedIdx = 0
	q.selected = make([]bool, len(prompt.Options))
	q.customFocused = false
	q.customInput.SetValue("")
	q.customInput.Blur()
}

func (q *questionDialogCmp) recomputeLayout() {
	prompt := q.currentPrompt()
	if prompt == nil {
		return
	}

	// Compute content width based on option text lengths
	width := questionDialogMinWidth
	for _, opt := range prompt.Options {
		if w := len(opt.Label) + 8; w > width { // 8 = indicator + padding
			width = w
		}
		if w := len(opt.Description) + 10; w > width {
			width = w
		}
	}
	if w := len(prompt.Question) + 6; w > width {
		width = w
	}

	if q.windowSize.Width > 0 {
		maxW := int(float64(q.windowSize.Width) * 0.6)
		if width > maxW {
			width = maxW
		}
	}
	q.contentWidth = max(questionDialogMinWidth, width)

	// Compute visible slots
	slots := questionDialogMaxVisible
	if q.windowSize.Height > 0 {
		// Reserve space for: border(2) + padding(2) + title(1) + question(2) + help(1) + custom input(2) + spacers(3)
		chromeRows := 13
		fromHeight := max(1, (q.windowSize.Height-chromeRows)/questionDialogItemRows)
		slots = min(slots, fromHeight)
	}
	if len(prompt.Options) > 0 {
		slots = min(slots, len(prompt.Options))
	}
	q.maxVisible = max(1, slots)
}

func (q *questionDialogCmp) View() tea.View {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	prompt := q.currentPrompt()
	if prompt == nil {
		return tea.NewView("")
	}

	w := q.contentWidth
	if w == 0 {
		w = questionDialogMinWidth
	}
	slots := q.maxVisible
	if slots == 0 {
		slots = min(questionDialogMaxVisible, max(1, len(prompt.Options)))
	}

	// Title
	titleLabel := baseStyle.
		Foreground(t.Primary()).
		Bold(true).
		Render("Question")
	var title string
	if len(q.request.Questions) > 1 {
		counter := baseStyle.
			Foreground(t.TextMuted()).
			Render(" (" + strconv.Itoa(q.questionIdx+1) + "/" + strconv.Itoa(len(q.request.Questions)) + ")")
		title = baseStyle.Width(w).Padding(0, 1).Render(titleLabel + counter)
	} else {
		title = baseStyle.Width(w).Padding(0, 1).Render(titleLabel)
	}

	// Question text (may wrap)
	questionText := baseStyle.
		Foreground(t.Text()).
		Width(w-2).
		Padding(0, 1).
		Render(prompt.Question)

	// Build option items
	blankRow := baseStyle.Width(w).Render("")

	startIdx := 0
	if len(prompt.Options) > slots {
		halfVisible := slots / 2
		if q.selectedIdx >= halfVisible && q.selectedIdx < len(prompt.Options)-halfVisible {
			startIdx = q.selectedIdx - halfVisible
		} else if q.selectedIdx >= len(prompt.Options)-halfVisible {
			startIdx = len(prompt.Options) - slots
		}
	}
	endIdx := min(startIdx+slots, len(prompt.Options))

	optionItems := make([]string, 0, slots)
	for i := startIdx; i < endIdx; i++ {
		opt := prompt.Options[i]
		isHighlighted := i == q.selectedIdx && !q.customFocused
		showDesc := opt.Description != "" && opt.Description != opt.Label

		// Build indicator
		var indicator string
		if prompt.Multiple {
			if i < len(q.selected) && q.selected[i] {
				indicator = "[x] "
			} else {
				indicator = "[ ] "
			}
		} else {
			if isHighlighted {
				indicator = "  > "
			} else {
				indicator = "    "
			}
		}

		labelStyle := baseStyle.Width(w).Padding(0, 1)

		if isHighlighted {
			labelStyle = labelStyle.
				Foreground(t.Accent()).
				Bold(true)
		}

		labelLine := labelStyle.Render(indicator + opt.Label)

		if showDesc {
			descStyle := baseStyle.Width(w).Padding(0, 1).Foreground(t.TextMuted())
			descLine := descStyle.Render("    " + opt.Description)
			optionItems = append(optionItems, lipgloss.JoinVertical(lipgloss.Left, labelLine, descLine))
		} else {
			optionItems = append(optionItems, labelLine)
		}
	}
	for len(optionItems) < slots {
		optionItems = append(optionItems, blankRow)
	}

	optionsBlock := baseStyle.Width(w).Render(lipgloss.JoinVertical(lipgloss.Left, optionItems...))

	// Build parts
	parts := []string{
		title,
		blankRow,
		questionText,
		blankRow,
		optionsBlock,
	}

	// Custom input
	if prompt.IsCustomEnabled() {
		q.customInput.SetWidth(w - 4)
		inputStyle := baseStyle.Width(w).Padding(0, 1)
		if q.customFocused {
			inputStyle = inputStyle.
				Border(lipgloss.RoundedBorder(), false, false, false, true).
				BorderForeground(t.Primary())
		}
		parts = append(parts, blankRow, inputStyle.Render(q.customInput.View()))
	}

	// Help text
	var helpParts []string
	helpParts = append(helpParts, "↑↓ navigate")
	if prompt.Multiple {
		helpParts = append(helpParts, "space toggle")
	}
	helpParts = append(helpParts, "⏎ select")
	if prompt.IsCustomEnabled() {
		helpParts = append(helpParts, "tab custom")
	}
	helpParts = append(helpParts, "esc dismiss")

	help := baseStyle.
		Width(w).
		Padding(0, 1).
		Foreground(t.TextMuted()).
		Render(strings.Join(helpParts, "  "))

	parts = append(parts, blankRow, help)

	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	rendered := baseStyle.Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderBackground(t.Background()).
		BorderForeground(t.TextMuted()).
		Width(lipgloss.Width(content) + 6).
		Render(content)

	rendered = styles.ForceReplaceBackgroundWithLipgloss(rendered, t.Background())

	return tea.NewView(rendered)
}

func (q *questionDialogCmp) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(questionKeys)
}

func (q *questionDialogCmp) SetQuestion(request questionpkg.Request) tea.Cmd {
	q.request = request
	q.questionIdx = 0
	q.answers = make([][]string, len(request.Questions))
	q.resetForCurrentQuestion()
	q.recomputeLayout()
	return nil
}

func (q *questionDialogCmp) RequestID() string {
	return q.request.ID
}

func NewQuestionDialogCmp() QuestionDialogCmp {
	t := theme.CurrentTheme()
	ti := textinput.New()
	ti.Placeholder = "Or type your own answer..."
	ti.Prompt = ""
	ti.SetWidth(46)
	st := ti.Styles()
	st.Focused.Placeholder = st.Focused.Placeholder.Background(t.Background())
	st.Blurred.Placeholder = st.Blurred.Placeholder.Background(t.Background())
	st.Focused.Text = st.Focused.Text.Background(t.Background())
	st.Blurred.Text = st.Blurred.Text.Background(t.Background())
	ti.SetStyles(st)

	return &questionDialogCmp{
		customInput: ti,
	}
}
