package chat

import (
	"encoding/json"
	"fmt"
	"image/color"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

type uiMessageType int

const (
	userMessageType uiMessageType = iota
	assistantMessageType
	toolMessageType
)

const (
	maxResultHeight          = 10
	collapsedResultLines     = 3
	expandedResultLines      = 500 // Bound rendering work for large outputs.
	collapsedNestedToolCalls = 3

	// ToggleToolDetailsKey toggles tool details throughout the transcript.
	ToggleToolDetailsKey = "ctrl+o"
)

// ToggleToolDetailsMsg toggles tool details throughout the transcript.
type ToggleToolDetailsMsg struct{}

type uiMessage struct {
	ID          string
	messageType uiMessageType
	position    int
	height      int
	content     string
}

func toMarkdown(content string, focused bool, width int) string {
	r := styles.GetMarkdownRenderer(width)
	rendered, _ := r.Render(content)
	return rendered
}

func renderMessage(msg string, isUser bool, isFocused bool, width int, info ...string) string {
	t := theme.CurrentTheme()

	style := styles.BaseStyle().
		Width(width).
		BorderLeft(true).
		Foreground(t.Text()).
		BorderForeground(t.Primary()).
		BorderBackground(t.Background()).
		BorderStyle(lipgloss.ThickBorder())

	if isUser {
		style = style.BorderForeground(t.Secondary())
	}

	// Apply markdown formatting and handle background color
	parts := []string{
		styles.ForceReplaceBackgroundWithLipgloss(toMarkdown(msg, isFocused, width-1), t.Background()),
	}

	// Remove newline at the end
	parts[0] = strings.TrimSuffix(parts[0], "\n")
	if len(info) > 0 {
		parts = append(parts, info...)
	}

	rendered := style.Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			parts...,
		),
	)

	return rendered
}

func renderUserMessage(msg message.Message, isFocused bool, width int, position int, expanded bool) uiMessage {
	textContent := collapseSkillBlocks(msg.Content().String())

	// Detect shell mode messages by content prefix
	if isShellCommandMessage(textContent) {
		return renderShellCommandMessage(msg, textContent, width, position)
	}
	if isShellOutputMessage(textContent) {
		return renderShellOutputMessage(msg, textContent, width, position, expanded)
	}

	var styledAttachments []string
	t := theme.CurrentTheme()
	attachmentStyles := styles.BaseStyle().
		MarginLeft(1).
		Background(t.TextMuted()).
		Foreground(t.Text())
	for _, attachment := range msg.BinaryContent() {
		file := filepath.Base(attachment.Path)
		var filename string
		if len(file) > 10 {
			filename = fmt.Sprintf(" %s %s...", styles.DocumentIcon, file[0:7])
		} else {
			filename = fmt.Sprintf(" %s %s", styles.DocumentIcon, file)
		}
		styledAttachments = append(styledAttachments, attachmentStyles.Render(filename))
	}
	content := ""
	if len(styledAttachments) > 0 {
		attachmentContent := styles.BaseStyle().Width(width).Render(lipgloss.JoinHorizontal(lipgloss.Left, styledAttachments...))
		content = renderMessage(textContent, true, isFocused, width, attachmentContent)
	} else {
		content = renderMessage(textContent, true, isFocused, width)
	}
	userMsg := uiMessage{
		ID:          msg.ID,
		messageType: userMessageType,
		position:    position,
		height:      lipgloss.Height(content),
		content:     content,
	}
	return userMsg
}

// skillBlockOpen matches the opening tag of an expanded skill block. The
// closing tag is located by search rather than by regex so a skill body that
// itself contains the literal text cannot terminate the match early.
var skillBlockOpen = regexp.MustCompile(`<skill_content name="([^"]*)">`)

const skillBlockClose = "</skill_content>"

// collapseSkillBlocks replaces each expanded <skill_content> region with a
// one-line summary for display. A message that stages two skills would
// otherwise print hundreds of lines of instructions the user already knows they
// asked for, burying the prose they wrote around it.
//
// This is presentation only: the stored message and the payload sent to the
// model keep the complete expanded text. An unterminated opening tag is left
// verbatim — better to show the raw text than to swallow the rest of the
// message.
func collapseSkillBlocks(text string) string {
	if !strings.Contains(text, skillBlockClose) {
		return text
	}

	var b strings.Builder
	rest := text
	for {
		loc := skillBlockOpen.FindStringSubmatchIndex(rest)
		if loc == nil {
			break
		}
		closeAt := strings.Index(rest[loc[1]:], skillBlockClose)
		if closeAt < 0 {
			break
		}

		name := rest[loc[2]:loc[3]]
		body := strings.Trim(rest[loc[1]:loc[1]+closeAt], "\n")
		lines := 0
		if body != "" {
			lines = strings.Count(body, "\n") + 1
		}

		b.WriteString(rest[:loc[0]])
		b.WriteString(fmt.Sprintf("%s skill:%s · %s", styles.SkillIcon, name, pluralLines(lines)))
		rest = rest[loc[1]+closeAt+len(skillBlockClose):]
	}
	b.WriteString(rest)
	return b.String()
}

func pluralLines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

func isShellCommandMessage(text string) bool {
	return strings.HasPrefix(text, "$ ")
}

func isShellOutputMessage(text string) bool {
	return strings.HasPrefix(text, "```\n") && strings.HasSuffix(text, "\n```")
}

func renderShellCommandMessage(msg message.Message, text string, width int, position int) uiMessage {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	style := baseStyle.
		Width(width).
		BorderLeft(true).
		BorderStyle(lipgloss.ThickBorder()).
		PaddingLeft(1).
		BorderForeground(t.TextMuted()).
		BorderBackground(t.Background())

	commandText := strings.TrimPrefix(text, "$ ")
	shellName := filepath.Base(shell.GetShellPath())
	label := baseStyle.Foreground(t.TextMuted()).Render(fmt.Sprintf("%s: ", shellName))
	cmdRendered := baseStyle.
		Width(width - 2 - lipgloss.Width(label)).
		Foreground(t.Text()).
		Render(commandText)

	content := style.Render(lipgloss.JoinHorizontal(lipgloss.Left, label, cmdRendered))
	return uiMessage{
		ID:          msg.ID,
		messageType: userMessageType,
		position:    position,
		height:      lipgloss.Height(content),
		content:     content,
	}
}

func renderShellOutputMessage(msg message.Message, text string, width int, position int, expanded bool) uiMessage {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	style := baseStyle.
		Width(width).
		BorderLeft(true).
		BorderStyle(lipgloss.ThickBorder()).
		PaddingLeft(1).
		BorderForeground(t.TextMuted()).
		BorderBackground(t.Background())

	// Extract content between ``` fences
	inner := strings.TrimPrefix(text, "```\n")
	inner = strings.TrimSuffix(inner, "\n```")

	// Check for exit code and render badge
	var exitBadge string
	lines := strings.Split(inner, "\n")
	lastLine := lines[len(lines)-1]
	if strings.HasPrefix(lastLine, "[exit code: ") && strings.HasSuffix(lastLine, "]") {
		exitCodeStr := strings.TrimPrefix(lastLine, "[exit code: ")
		exitCodeStr = strings.TrimSuffix(exitCodeStr, "]")
		exitBadge = baseStyle.
			Width(width - 2).
			Foreground(t.Error()).
			Background(t.Background()).
			Bold(true).
			Render(fmt.Sprintf("exit %s", exitCodeStr))
		inner = strings.Join(lines[:len(lines)-1], "\n")
		inner = strings.TrimRight(inner, "\n")
	}

	rendered := renderCodeBlock("bash", inner, resultLineLimit(expanded), expanded, width-2)

	parts := []string{rendered}
	if exitBadge != "" {
		parts = append(parts, exitBadge)
	}

	content := style.Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
	return uiMessage{
		ID:          msg.ID,
		messageType: userMessageType,
		position:    position,
		height:      lipgloss.Height(content),
		content:     content,
	}
}

// Returns multiple uiMessages because of the tool calls
func renderAssistantMessage(
	msg message.Message,
	msgIndex int,
	allMessages []message.Message,
	taskMessages map[string][]message.Message,
	focusedUIMessageId string,
	isSummary bool,
	width int,
	position int,
	expanded bool,
) []uiMessage {
	messages := []uiMessage{}
	content := msg.Content().String()
	thinking := msg.IsThinking()
	thinkingContent := msg.ReasoningContent().Thinking
	finished := msg.IsFinished()
	finishData := msg.FinishPart()
	info := []string{}

	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	// Add finish info if available
	if finished {
		switch finishData.Reason {
		case message.FinishReasonEndTurn:
			took := formatTimestampDiff(msg.CreatedAt, finishData.Time)
			info = append(info, baseStyle.
				Width(width-1).
				Foreground(t.TextMuted()).
				Render(fmt.Sprintf(" %s (%s)", models.SupportedModels[msg.Model].Name, took)),
			)
		case message.FinishReasonCanceled:
			info = append(info, baseStyle.
				Width(width-1).
				Foreground(t.TextMuted()).
				Render(fmt.Sprintf(" %s (%s)", models.SupportedModels[msg.Model].Name, "canceled")),
			)
		case message.FinishReasonError:
			info = append(info, baseStyle.
				Width(width-1).
				Foreground(t.TextMuted()).
				Render(fmt.Sprintf(" %s (%s)", models.SupportedModels[msg.Model].Name, "error")),
			)
		case message.FinishReasonPermissionDenied:
			info = append(info, baseStyle.
				Width(width-1).
				Foreground(t.TextMuted()).
				Render(fmt.Sprintf(" %s (%s)", models.SupportedModels[msg.Model].Name, "permission denied")),
			)
		}
	}
	contentRendered := false
	if strings.TrimSpace(content) != "" || (finished && finishData.Reason == message.FinishReasonEndTurn) {
		if strings.TrimSpace(content) == "" {
			content = "*Finished without output*"
		}
		if isSummary {
			info = append(info, baseStyle.Width(width-1).Foreground(t.TextMuted()).Render(" (summary)"))
		}

		content = renderMessage(content, false, true, width, info...)
		contentRendered = true
	} else if thinking && thinkingContent != "" {
		logging.Debug("Thinking content rendered", "size", len(thinkingContent))
		content = renderMessage(thinkingContent, false, msg.ID == focusedUIMessageId, width)
		contentRendered = true
	}
	if contentRendered {
		messages = append(messages, uiMessage{
			ID:          msg.ID,
			messageType: assistantMessageType,
			position:    position,
			height:      lipgloss.Height(content),
			content:     content,
		})
		position += messages[0].height
		position++ // for the space
	}

	for _, toolCall := range msg.ToolCalls() {
		toolCallContent := renderToolMessage(
			toolCall,
			allMessages,
			taskMessages,
			focusedUIMessageId,
			false,
			width,
			position,
			expanded,
		)
		messages = append(messages, toolCallContent)
		position += toolCallContent.height
		position++ // for the space
	}
	return messages
}

func findToolResponse(toolCallID string, futureMessages []message.Message) *message.ToolResult {
	for _, msg := range futureMessages {
		for _, result := range msg.ToolResults() {
			if result.ToolCallID == toolCallID {
				return &result
			}
		}
	}
	return nil
}

func toolName(name string) string {
	switch name {
	case agent.TaskToolName:
		return "Task"
	case tools.BashToolName:
		return "Bash"
	case tools.EditToolName:
		return "Edit"
	case tools.MultiEditToolName:
		return "MultiEdit"
	case tools.WebFetchToolName:
		return "Web Fetch"
	case tools.GlobToolName:
		return "Glob"
	case tools.GrepToolName:
		return "Grep"
	case tools.LSToolName:
		return "List"
	case tools.SourcegraphToolName:
		return "Sourcegraph"
	case tools.ReadToolName:
		return "Read"
	case tools.ViewImageToolName:
		return "View Image"
	case tools.WriteToolName:
		return "Write"
	case tools.PatchToolName:
		return "Patch"
	case tools.DeleteToolName:
		return "Delete"
	case tools.LSPToolName:
		return "Code Intelligence"
	case tools.StructOutputToolName:
		return "Structured Output"
	case tools.WebSearchToolName:
		return "Web Search"
	case tools.CronCreateToolName:
		return "Schedule Task"
	case tools.CronDeleteToolName:
		return "Cancel Task"
	case tools.CronListToolName:
		return "List Scheduled"
	}
	return name
}

func getToolAction(name string) string {
	switch name {
	case agent.TaskToolName:
		return "Preparing prompt..."
	case tools.BashToolName:
		return "Building command..."
	case tools.EditToolName:
		return "Preparing edit..."
	case tools.MultiEditToolName:
		return "Preparing edits..."
	case tools.WebFetchToolName:
		return "Fetching URL..."
	case tools.GlobToolName:
		return "Finding files..."
	case tools.GrepToolName:
		return "Searching content..."
	case tools.LSToolName:
		return "Listing directory..."
	case tools.SourcegraphToolName:
		return "Searching code..."
	case tools.ReadToolName:
		return "Reading file..."
	case tools.ViewImageToolName:
		return "Loading image..."
	case tools.WriteToolName:
		return "Preparing write..."
	case tools.PatchToolName:
		return "Preparing patch..."
	case tools.DeleteToolName:
		return "Deleting..."
	case tools.LSPToolName:
		return "Doing code intelligence..."
	case tools.StructOutputToolName:
		return "Formatting output..."
	case tools.WebSearchToolName:
		return "Searching the web..."
	case tools.CronCreateToolName:
		return "Scheduling task..."
	case tools.CronDeleteToolName:
		return "Cancelling task..."
	case tools.CronListToolName:
		return "Listing tasks..."
	}
	return "Working..."
}

// renders params, params[0] (params[1]=params[2] ....)
func renderParams(paramsWidth int, params ...string) string {
	if len(params) == 0 || paramsWidth <= 0 {
		return ""
	}
	mainParam := params[0]
	if len(mainParam) > paramsWidth {
		if paramsWidth <= 3 {
			mainParam = mainParam[:paramsWidth]
		} else {
			mainParam = mainParam[:paramsWidth-3] + "..."
		}
	}

	if len(params) == 1 {
		return mainParam
	}
	otherParams := params[1:]
	// create pairs of key/value
	// if odd number of params, the last one is a key without value
	if len(otherParams)%2 != 0 {
		otherParams = append(otherParams, "")
	}
	parts := make([]string, 0, len(otherParams)/2)
	for i := 0; i < len(otherParams); i += 2 {
		key := otherParams[i]
		value := otherParams[i+1]
		if value == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", key, value))
	}

	partsRendered := strings.Join(parts, ", ")
	remainingWidth := paramsWidth - lipgloss.Width(partsRendered) - 5 // for the space
	if remainingWidth < 30 {
		// No space for the params, just show the main
		return mainParam
	}

	if len(parts) > 0 {
		mainParam = fmt.Sprintf("%s (%s)", mainParam, strings.Join(parts, ", "))
	}

	return ansi.Truncate(mainParam, paramsWidth, "...")
}

func removeWorkingDirPrefix(path string) string {
	wd := config.WorkingDirectory()
	path = strings.TrimPrefix(path, wd)
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimPrefix(path, "./")
	path = strings.TrimPrefix(path, "../")
	return path
}

func expandedParams(value string, limit int) string {
	value = strings.TrimRight(value, "\n")
	body, hidden := truncateLines(value, limit)
	if hidden > 0 {
		body += fmt.Sprintf("\n… +%s not shown", pluralLines(hidden))
	}
	return body
}

func renderToolParams(paramWidth int, toolCall message.ToolCall, expanded bool) string {
	params := ""
	switch toolCall.Name {
	case agent.TaskToolName:
		var params agent.TaskParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		if expanded {
			return expandedParams(params.Prompt, expandedResultLines)
		}
		prompt := strings.ReplaceAll(params.Prompt, "\n", " ")
		toolParams := []string{prompt}
		if params.SubagentType != "" {
			toolParams = append(toolParams, "agent", params.SubagentType)
		}
		if params.TaskID != "" {
			toolParams = append(toolParams, "resumed", "true")
		}
		return renderParams(paramWidth, toolParams...)
	case tools.BashToolName:
		var params tools.BashParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		command := strings.ReplaceAll(params.Command, "\n", " ")
		if expanded {
			command = expandedParams(params.Command, expandedResultLines)
		}
		if params.Description != "" {
			desc := params.Description
			if len(desc) > 80 && !expanded {
				desc = desc[:77] + "..."
			}
			t := theme.CurrentTheme()
			descLine := styles.BaseStyle().
				Foreground(t.TextMuted()).
				Italic(true).
				Render(desc)
			cmdLine := command
			if !expanded {
				cmdLine = renderParams(paramWidth, command)
			}
			return descLine + "\n" + cmdLine
		}
		if expanded {
			return command
		}
		return renderParams(paramWidth, command)
	case tools.EditToolName:
		var params tools.EditParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		filePath := removeWorkingDirPrefix(params.FilePath)
		return renderParams(paramWidth, filePath)
	case tools.MultiEditToolName:
		var params tools.MultiEditParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		filePath := removeWorkingDirPrefix(params.FilePath)
		return renderParams(paramWidth, filePath, "edits", fmt.Sprintf("%d", len(params.Edits)))
	case tools.WebFetchToolName:
		var params tools.FetchParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		url := params.URL
		toolParams := []string{
			url,
		}
		if params.Format != "" {
			toolParams = append(toolParams, "format", params.Format)
		}
		if params.Timeout != 0 {
			toolParams = append(toolParams, "timeout", (time.Duration(params.Timeout) * time.Second).String())
		}
		return renderParams(paramWidth, toolParams...)
	case tools.GlobToolName:
		var params tools.GlobParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		pattern := params.Pattern
		toolParams := []string{
			pattern,
		}
		if params.Path != "" {
			toolParams = append(toolParams, "path", params.Path)
		}
		return renderParams(paramWidth, toolParams...)
	case tools.GrepToolName:
		var params tools.GrepParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		pattern := params.Pattern
		toolParams := []string{
			pattern,
		}
		if params.Path != "" {
			toolParams = append(toolParams, "path", params.Path)
		}
		if params.Include != "" {
			toolParams = append(toolParams, "include", params.Include)
		}
		if params.LiteralText {
			toolParams = append(toolParams, "literal", "true")
		}
		return renderParams(paramWidth, toolParams...)
	case tools.LSToolName:
		var params tools.LSParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		path := params.Path
		if path == "" {
			path = "."
		}
		return renderParams(paramWidth, path)
	case tools.SourcegraphToolName:
		var params tools.SourcegraphParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		return renderParams(paramWidth, params.Query)
	case tools.WebSearchToolName:
		var params tools.WebSearchParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		toolParams := []string{params.Query}
		if params.Provider != "" {
			toolParams = append(toolParams, "provider", params.Provider)
		}
		if params.MaxResults > 0 {
			toolParams = append(toolParams, "max_results", fmt.Sprintf("%d", params.MaxResults))
		}
		return renderParams(paramWidth, toolParams...)
	case tools.LSPToolName:
		var params tools.LSParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		return renderParams(paramWidth, params.Path, fmt.Sprintf("ignore: %s", strings.Join(params.Ignore, ", ")))
	case tools.ReadToolName:
		var params tools.ViewParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		filePath := removeWorkingDirPrefix(params.FilePath)
		toolParams := []string{
			filePath,
		}
		if params.Limit != 0 {
			toolParams = append(toolParams, "limit", fmt.Sprintf("%d", params.Limit))
		}
		if params.Offset != 0 {
			toolParams = append(toolParams, "offset", fmt.Sprintf("%d", params.Offset))
		}
		return renderParams(paramWidth, toolParams...)
	case tools.ViewImageToolName:
		var params tools.ViewImageParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		filePath := removeWorkingDirPrefix(params.FilePath)
		return renderParams(paramWidth, filePath)
	case tools.WriteToolName:
		var params tools.WriteParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		filePath := removeWorkingDirPrefix(params.FilePath)
		return renderParams(paramWidth, filePath)
	case tools.DeleteToolName:
		var params tools.DeleteParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		filePath := removeWorkingDirPrefix(params.Path)
		return renderParams(paramWidth, filePath)
	default:
		input := strings.ReplaceAll(toolCall.Input, "\n", " ")
		params = renderParams(paramWidth, input)
	}
	return params
}

func truncateLines(content string, limit int) (string, int) {
	if limit <= 0 {
		return content, 0
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= limit {
		return content, 0
	}
	return strings.Join(lines[:limit], "\n"), len(lines) - limit
}

// Truncate after rendering to keep markdown constructs intact, excluding
// renderer padding from the line budget.
func truncateRendered(rendered string, limit int) (string, int) {
	lines := strings.Split(rendered, "\n")
	isBlank := func(l string) bool { return strings.TrimSpace(ansi.Strip(l)) == "" }
	for len(lines) > 0 && isBlank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	if limit <= 0 || len(lines) <= limit {
		return strings.Join(lines, "\n"), 0
	}
	for len(lines) > limit && isBlank(lines[0]) {
		lines = lines[1:]
	}
	if len(lines) <= limit {
		return strings.Join(lines, "\n"), 0
	}
	return strings.Join(lines[:limit], "\n"), len(lines) - limit
}

func resultLineLimit(expanded bool) int {
	if expanded {
		return expandedResultLines
	}
	return collapsedResultLines
}

func hiddenLinesHint(hidden int, expanded bool, width int) string {
	if hidden <= 0 {
		return ""
	}
	t := theme.CurrentTheme()
	text := fmt.Sprintf("… +%s (%s to expand)", pluralLines(hidden), ToggleToolDetailsKey)
	if expanded {
		text = fmt.Sprintf("… +%s not shown", pluralLines(hidden))
	}
	return styles.BaseStyle().
		Width(width).
		Foreground(t.TextMuted()).
		Italic(true).
		Render(text)
}

func appendHint(rendered string, hidden int, expanded bool, width int) string {
	hint := hiddenLinesHint(hidden, expanded, width)
	rendered = strings.TrimRight(rendered, "\n")
	if hint == "" {
		return rendered
	}
	if rendered == "" {
		return hint
	}
	return rendered + "\n" + hint
}

func renderCodeBlock(lang, content string, limit int, expanded bool, width int) string {
	body, hidden := truncateLines(content, limit)
	rendered := styles.ForceReplaceBackgroundWithLipgloss(
		toMarkdown(fmt.Sprintf("```%s\n%s\n```", lang, body), true, width),
		theme.CurrentTheme().Background(),
	)
	return appendHint(rendered, hidden, expanded, width)
}

func renderMutedText(content string, limit int, expanded bool, width int) string {
	body, hidden := truncateLines(content, limit)
	t := theme.CurrentTheme()
	rendered := styles.BaseStyle().Width(width).Foreground(t.TextMuted()).Render(body)
	return appendHint(rendered, hidden, expanded, width)
}

func renderCollapsedMarkdown(content string, limit int, expanded bool, width int) string {
	rendered := styles.ForceReplaceBackgroundWithLipgloss(
		toMarkdown(content, false, width),
		theme.CurrentTheme().Background(),
	)
	rendered, hidden := truncateRendered(rendered, limit)
	return appendHint(rendered, hidden, expanded, width)
}

// Render the full diff before truncating so file and hunk headers do not
// consume the visible line budget.
func renderCollapsedDiff(unified string, limit int, expanded bool, width int) string {
	formatted, _ := diff.FormatDiff(unified, diff.WithTotalWidth(width))
	formatted, hidden := truncateRendered(formatted, limit)
	return appendHint(formatted, hidden, expanded, width)
}

func renderToolResponse(toolCall message.ToolCall, response message.ToolResult, width int, expanded bool) string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()
	limit := resultLineLimit(expanded)

	if response.IsError {
		if expanded {
			errContent, hidden := truncateLines(fmt.Sprintf("Error: %s", response.Content), limit)
			rendered := baseStyle.Width(width).Foreground(t.Error()).Render(errContent)
			return appendHint(rendered, hidden, expanded, width)
		}
		errContent := fmt.Sprintf("Error: %s", strings.ReplaceAll(response.Content, "\n", " "))
		errContent = ansi.Truncate(errContent, width-1, "...")
		return baseStyle.
			Width(width).
			Foreground(t.Error()).
			Render(errContent)
	}

	switch toolCall.Name {
	case agent.TaskToolName:
		metadata := agent.TaskResponseMetadata{}
		json.Unmarshal([]byte(response.Metadata), &metadata)
		if metadata.IsStructOutput {
			taskIDLine := ""
			if metadata.TaskID != "" {
				taskIDLine = "\n" + baseStyle.Width(width).Foreground(t.TextMuted()).
					Render(fmt.Sprintf("task_id: %s (reuse to resume)", metadata.TaskID))
			}
			return baseStyle.Width(width).Foreground(t.TextMuted()).Render("↓") + taskIDLine
		}
		// Strip the task_id / resume trailers from the content that's shown
		// to the user — the tags are an LLM-facing hint, not something the
		// user needs to see inline. We re-surface the task_id via a dedicated
		// muted footer line.
		displayContent := taskTrailerRegex.ReplaceAllString(response.Content, "")
		displayContent = strings.TrimRight(displayContent, "\n")
		rendered := renderCollapsedMarkdown(displayContent, limit, expanded, width)
		if metadata.TaskID != "" {
			footer := baseStyle.Width(width).Foreground(t.TextMuted()).
				Render(fmt.Sprintf("task_id: %s (pass back to task tool to resume)", metadata.TaskID))
			rendered = strings.TrimRight(rendered, "\n") + "\n" + footer
		}
		return rendered
	case tools.BashToolName:
		return renderCodeBlock("bash", response.Content, limit, expanded, width)
	case tools.EditToolName:
		metadata := tools.EditResponseMetadata{}
		json.Unmarshal([]byte(response.Metadata), &metadata)
		return renderCollapsedDiff(metadata.Diff, limit, expanded, width)
	case tools.MultiEditToolName:
		metadata := tools.MultiEditResponseMetadata{}
		json.Unmarshal([]byte(response.Metadata), &metadata)
		return renderCollapsedDiff(metadata.Diff, limit, expanded, width)
	case tools.WebFetchToolName:
		var params tools.FetchParams
		json.Unmarshal([]byte(toolCall.Input), &params)
		mdFormat := "markdown"
		switch params.Format {
		case "text":
			mdFormat = "text"
		case "html":
			mdFormat = "html"
		}
		return renderCodeBlock(mdFormat, response.Content, limit, expanded, width)
	case tools.GlobToolName, tools.GrepToolName, tools.LSToolName, tools.SourcegraphToolName:
		return renderMutedText(response.Content, limit, expanded, width)
	case tools.WebSearchToolName:
		return renderCollapsedMarkdown(response.Content, limit, expanded, width)
	case tools.LSPToolName:
		metadata := tools.LSPToolMetadata{}
		json.Unmarshal([]byte(response.Metadata), &metadata)
		return baseStyle.Width(width).Foreground(t.TextMuted()).Render(metadata.Title)
	case tools.ReadToolName:
		metadata := tools.ViewResponseMetadata{}
		json.Unmarshal([]byte(response.Metadata), &metadata)
		ext := filepath.Ext(metadata.FilePath)
		if ext != "" {
			ext = strings.ToLower(ext[1:])
		}
		return renderCodeBlock(ext, metadata.Content, limit, expanded, width)
	case tools.ViewImageToolName:
		metadata := tools.ViewImageResponseMetadata{}
		json.Unmarshal([]byte(response.Metadata), &metadata)
		// For image tools, we show a simple message indicating the image was loaded
		// The actual base64 content is not displayed in the UI as it would be too long
		imageInfo := fmt.Sprintf("📷 Image loaded: %s (%s)",
			filepath.Base(metadata.FilePath),
			metadata.MimeType)
		return baseStyle.Width(width).Foreground(t.TextMuted()).Render(imageInfo)
	case tools.WriteToolName:
		params := tools.WriteParams{}
		json.Unmarshal([]byte(toolCall.Input), &params)
		ext := filepath.Ext(params.FilePath)
		if ext != "" {
			ext = strings.ToLower(ext[1:])
		}
		return renderCodeBlock(ext, params.Content, limit, expanded, width)
	case tools.DeleteToolName:
		metadata := tools.DeleteResponseMetadata{}
		json.Unmarshal([]byte(response.Metadata), &metadata)
		return renderCollapsedDiff(metadata.Diff, limit, expanded, width)
	case tools.StructOutputToolName:
		return renderCodeBlock("json", response.Content, limit, expanded, width)
	case tools.TodoWriteToolName:
		content, _ := truncateLines(response.Content, maxResultHeight)
		return renderTodoChecklist(response.Metadata, content, width)
	default:
		return renderCodeBlock("text", response.Content, limit, expanded, width)
	}
}

func renderToolMessage(
	toolCall message.ToolCall,
	allMessages []message.Message,
	taskMessages map[string][]message.Message,
	focusedUIMessageId string,
	nested bool,
	width int,
	position int,
	expanded bool,
) uiMessage {
	if nested {
		width = width - 3
	}

	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	style := baseStyle.
		Width(width).
		BorderLeft(true).
		BorderStyle(lipgloss.ThickBorder()).
		PaddingLeft(1).
		BorderForeground(t.TextMuted()).
		BorderBackground(t.Background())

	response := findToolResponse(toolCall.ID, allMessages)
	toolNameText := baseStyle.Foreground(t.TextMuted()).
		Render(fmt.Sprintf("%s: ", toolName(toolCall.Name)))

	// Show subagent badge for task tool calls
	if toolCall.Name == agent.TaskToolName {
		var taskParams agent.TaskParams
		json.Unmarshal([]byte(toolCall.Input), &taskParams)
		badge := subagentBadge(taskParams.SubagentType, taskParams.TaskTitle, taskParams.TaskID != "")
		toolNameText = badge
	}

	if !toolCall.Finished {
		// Get a brief description of what the tool is doing
		toolAction := getToolAction(toolCall.Name)

		progressWidth := width - 2 - lipgloss.Width(toolNameText)
		if progressWidth < 0 {
			progressWidth = 0
		}
		progressText := baseStyle.
			Width(progressWidth).
			Foreground(t.TextMuted()).
			Render(fmt.Sprintf(" %s", toolAction))

		content := style.Render(lipgloss.JoinHorizontal(lipgloss.Left, toolNameText, progressText))
		toolMsg := uiMessage{
			ID:          toolCall.ID,
			messageType: toolMessageType,
			position:    position,
			height:      lipgloss.Height(content),
			content:     content,
		}
		return toolMsg
	}

	paramWidth := width - 2 - lipgloss.Width(toolNameText)
	if paramWidth < 0 {
		paramWidth = 0
	}
	params := renderToolParams(paramWidth, toolCall, expanded)
	responseContent := ""
	if response != nil {
		responseContent = renderToolResponse(toolCall, *response, width-2, expanded)
		responseContent = strings.TrimSuffix(responseContent, "\n")

		// Extract and render diagnostics summary from response content
		diagSummary := renderDiagnosticsSummary(response.Content, width-2)
		if diagSummary != "" {
			responseContent = responseContent + "\n" + diagSummary
		}
	} else {
		responseContent = baseStyle.
			Italic(true).
			Width(width - 2).
			Foreground(t.TextMuted()).
			Render("Waiting for response...")
	}

	parts := []string{}
	if !nested {
		formattedParams := baseStyle.
			Width(width - 2 - lipgloss.Width(toolNameText)).
			Foreground(t.TextMuted()).
			Render(params)

		parts = append(parts, lipgloss.JoinHorizontal(lipgloss.Left, toolNameText, formattedParams))
	} else {
		prefix := baseStyle.
			Foreground(t.TextMuted()).
			Render(" └ ")
		formattedParams := baseStyle.
			Width(width - 2 - lipgloss.Width(toolNameText)).
			Foreground(t.TextMuted()).
			Render(params)
		parts = append(parts, lipgloss.JoinHorizontal(lipgloss.Left, prefix, toolNameText, formattedParams))
	}

	if toolCall.Name == agent.TaskToolName {
		if msgs, ok := taskMessages[toolCall.ID]; ok {
			toolCalls := []message.ToolCall{}
			for _, v := range msgs {
				toolCalls = append(toolCalls, v.ToolCalls()...)
			}
			toolCalls, skipped := visibleNestedCalls(toolCalls, expanded)
			if skipped > 0 {
				parts = append(parts, baseStyle.
					Width(width-2).
					Foreground(t.TextMuted()).
					Italic(true).
					Render(nestedCallsHint(skipped)))
			}
			for _, call := range toolCalls {
				rendered := renderToolMessage(call, []message.Message{}, taskMessages, focusedUIMessageId, true, width, 0, expanded)
				parts = append(parts, rendered.content)
			}
		}
	}
	if responseContent != "" && !nested {
		parts = append(parts, responseContent)
	}

	content := style.Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			parts...,
		),
	)
	if nested {
		content = lipgloss.JoinVertical(
			lipgloss.Left,
			parts...,
		)
	}
	toolMsg := uiMessage{
		ID:          toolCall.ID,
		messageType: toolMessageType,
		position:    position,
		height:      lipgloss.Height(content),
		content:     content,
	}
	return toolMsg
}

func visibleNestedCalls(calls []message.ToolCall, expanded bool) ([]message.ToolCall, int) {
	if expanded || len(calls) <= collapsedNestedToolCalls {
		return calls, 0
	}
	skipped := len(calls) - collapsedNestedToolCalls
	return calls[skipped:], skipped
}

func nestedCallsHint(skipped int) string {
	noun := "tool uses"
	if skipped == 1 {
		noun = "tool use"
	}
	return fmt.Sprintf(" … +%d earlier %s (%s to expand)", skipped, noun, ToggleToolDetailsKey)
}

func subagentBadge(agentType string, title string, isResumed bool) string {
	t := theme.CurrentTheme()

	// Detect cron-fired tasks by ⏲ prefix in title
	isCron := strings.HasPrefix(title, "⏲")

	icon := "●"
	if isCron {
		icon = "⏲"
	}
	var color color.Color
	name := agentType

	reg := agentregistry.GetRegistry()
	if info, ok := reg.Get(agentType); ok {
		if info.Name != "" {
			name = info.Name
		}
		switch info.Color {
		case "primary":
			color = t.Primary()
		case "secondary":
			color = t.Secondary()
		case "warning":
			color = t.Warning()
		case "error":
			color = t.Error()
		case "info":
			color = t.Info()
		case "success":
			color = t.Success()
		default:
			color = nil
		}
	}

	if color == nil {
		switch agentType {
		case "explorer":
			color = t.Info()
		case "workhorse":
			color = t.Warning()
		default:
			color = t.Secondary()
		}
	}

	status := "new task"
	if isCron {
		status = "cron"
	} else if isResumed {
		status = "resumed"
	}

	badge := styles.BaseStyle().
		Foreground(color).
		Render(fmt.Sprintf("%s %s ", icon, name))

	var taskTitle string
	if title != "" {
		taskTitle = styles.BaseStyle().
			Foreground(t.TextEmphasized()).
			Blink(true).
			Render(fmt.Sprintf("(%s > %s): ", status, title))
	} else {
		taskTitle = styles.BaseStyle().
			Foreground(t.TextEmphasized()).
			Blink(true).
			Render(fmt.Sprintf("(%s):", status))
	}

	return fmt.Sprintf("%s%s", badge, taskTitle)
}

var diagSummaryRegex = regexp.MustCompile(`<diagnostic_summary>\s*Current file: (\d+) errors, (\d+) warnings\s*Project: (\d+) errors, (\d+) warnings\s*</diagnostic_summary>`)

// taskTrailerRegex matches the task_id / task_resume_hint trailers that the
// agent-tool appends to Task tool responses for the LLM. The TUI strips these
// tags from the rendered output and re-surfaces the task_id via a dedicated
// footer line.
var taskTrailerRegex = regexp.MustCompile(`(?s)\s*<task_id>[^<]*</task_id>\s*<task_resume_hint>.*?</task_resume_hint>\s*`)

// renderDiagnosticsSummary extracts the diagnostic summary from tool output
// and renders it as a subtle one-line indicator with error/warning icons.
func renderDiagnosticsSummary(content string, width int) string {
	matches := diagSummaryRegex.FindStringSubmatch(content)
	if matches == nil {
		return ""
	}

	fileErrors, _ := strconv.Atoi(matches[1])
	fileWarnings, _ := strconv.Atoi(matches[2])
	projectErrors, _ := strconv.Atoi(matches[3])
	projectWarnings, _ := strconv.Atoi(matches[4])

	totalErrors := fileErrors + projectErrors
	totalWarnings := fileWarnings + projectWarnings

	if totalErrors == 0 && totalWarnings == 0 {
		return ""
	}

	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	var parts []string
	if totalErrors > 0 {
		errText := baseStyle.
			Foreground(t.Error()).
			Render(fmt.Sprintf("%s %d", styles.ErrorIcon, totalErrors))
		parts = append(parts, errText)
	}
	if totalWarnings > 0 {
		warnText := baseStyle.
			Foreground(t.Warning()).
			Render(fmt.Sprintf("%s %d", styles.WarningIcon, totalWarnings))
		parts = append(parts, warnText)
	}

	summary := strings.Join(parts, baseStyle.Foreground(t.TextMuted()).Render("  "))
	return baseStyle.Width(width).Render(summary)
}

// renderTodoChecklist renders todowrite metadata as a visual checklist.
func renderTodoChecklist(metadata string, fallback string, width int) string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	var meta struct {
		Todos []struct {
			Content  string `json:"content"`
			Status   string `json:"status"`
			Priority string `json:"priority"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil || len(meta.Todos) == 0 {
		return baseStyle.Width(width).Foreground(t.TextMuted()).Render(fallback)
	}

	bg := t.Background()
	var sb strings.Builder
	for _, item := range meta.Todos {
		var icon string
		var style lipgloss.Style
		switch item.Status {
		case "completed":
			icon = "✓"
			style = baseStyle.Foreground(t.Success()).Background(bg).Strikethrough(true)
		case "in_progress":
			icon = "→"
			style = baseStyle.Foreground(t.Primary()).Background(bg)
		case "cancelled":
			icon = "~"
			style = baseStyle.Foreground(t.TextMuted()).Background(bg).Strikethrough(true)
		default: // pending
			icon = "○"
			style = baseStyle.Foreground(t.Text()).Background(bg)
		}
		iconStyle := baseStyle.Foreground(style.GetForeground()).Background(bg)
		pad := baseStyle.Background(bg).Render("  ")
		gap := baseStyle.Background(bg).Render(" ")
		sb.WriteString(fmt.Sprintf("%s%s%s%s\n", pad, iconStyle.Render(icon), gap, style.Render(item.Content)))
	}

	rendered := baseStyle.Width(width).Render(strings.TrimRight(sb.String(), "\n"))
	return styles.ForceReplaceBackgroundWithLipgloss(rendered, bg)
}

// Helper function to format the time difference between two Unix timestamps (seconds)
func formatTimestampDiff(start, end int64) string {
	diffSeconds := end - start
	if diffSeconds < 1 {
		return "<1s"
	}
	if diffSeconds < 60 {
		return fmt.Sprintf("%ds", diffSeconds)
	}
	minutes := diffSeconds / 60
	seconds := diffSeconds % 60
	if seconds == 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dm%ds", minutes, seconds)
}
