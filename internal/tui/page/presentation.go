package page

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

type pageGeometry struct{ width, bodyHeight, x, y int }

func pageDimensions(w, h int) pageGeometry {
	x, y := 2, 1
	if w < 40 {
		x = 1
	}
	if h < 12 {
		y = 0
	}
	return pageGeometry{max(1, w-2*x), max(1, h-2*y-5), x, y}
}
func pagePlain(text string) string           { return ansi.Strip(text) }
func pageLabel(text string) string           { return strings.Join(strings.Fields(pagePlain(text)), " ") }
func pageText(text string, width int) string { return ansi.Truncate(text, max(0, width), "…") }
func pageMuted(text string) string {
	return styles.BaseStyle().Foreground(theme.CurrentTheme().TextMuted()).Render(text)
}
func pageStatus(status string) string {
	status = pageLabel(status)
	t := theme.CurrentTheme()
	icon, col := "○", t.TextMuted()
	switch {
	case strings.HasPrefix(status, "connected"), strings.HasPrefix(status, "active"), status == "completed":
		icon, col = "●", t.Success()
	case status == "running", status == "planning", status == "resuming":
		icon, col = "●", t.Primary()
	case strings.Contains(status, "error"), status == "failed":
		icon, col = "●", t.Error()
	case status == "waiting_for_input", status == "postponed", status == "interrupted":
		icon, col = "◐", t.Warning()
	}
	return styles.BaseStyle().Foreground(col).Render(icon + " " + status)
}
func pageRow(name, status string, selected bool, width int) string {
	name = pageLabel(name)
	t := theme.CurrentTheme()
	marker := "  "
	style := styles.BaseStyle().Foreground(t.Text())
	if selected {
		marker = "▸ "
		style = style.Foreground(t.Primary()).Bold(true)
	}
	return style.Render(pageText(marker+name, width)) + "\n" + pageText("  "+pageStatus(status), width)
}

// Bound both dimensions before composing panels; long output cannot move the footer.
func pageLines(content string, width, height int) string {
	rows := strings.Split(content, "\n")
	out := make([]string, max(0, height))
	for i := range out {
		if i < len(rows) {
			out[i] = pageText(rows[i], width)
		}
		out[i] += strings.Repeat(" ", max(0, width-ansi.StringWidth(out[i])))
	}
	return strings.Join(out, "\n")
}
func pagePanel(title, content string, w, h int, focused bool) string {
	if w < 6 || h < 3 {
		return pageLines(content, w, h)
	}
	t := theme.CurrentTheme()
	col := t.BorderDim()
	if focused {
		col = t.Primary()
	}
	border := styles.BaseStyle().Foreground(col)
	label := pageText(" "+pageLabel(title)+" ", w-4)
	top := border.Render("╭─" + label + strings.Repeat("─", max(0, w-3-ansi.StringWidth(label))) + "╮")
	rows := strings.Split(pageLines(content, w-4, h-2), "\n")
	for i, row := range rows {
		rows[i] = border.Render("│") + " " + row + " " + border.Render("│")
	}
	return top + "\n" + strings.Join(rows, "\n") + "\n" + border.Render("╰"+strings.Repeat("─", w-2)+"╯")
}
func pageFrame(w, h int, title, subtitle, summary, body, footer string) string {
	g := pageDimensions(w, h)
	t := theme.CurrentTheme()
	heading := styles.BaseStyle().Foreground(t.Primary()).Bold(true).Render(title)
	if summary != "" {
		heading += "  " + pageMuted(summary)
	}
	content := pageText(heading, g.width) + "\n" + pageText(pageMuted(pageLabel(subtitle)), g.width) + "\n\n" + pageLines(body, g.width, g.bodyHeight) + "\n\n" + pageMuted(pageFooter(footer, g.width))
	// Keep borders/selection visible with foreground colors; fill every ANSI cell
	// with the theme background, as required by the TUI's dialog rendering contract.
	rendered := lipgloss.NewStyle().Padding(g.y, g.x).Width(max(1, w)).Height(max(1, h)).MaxWidth(max(1, w)).MaxHeight(max(1, h)).Foreground(t.Text()).Background(t.Background()).Render(content)
	return styles.ForceReplaceBackgroundWithLipgloss(rendered, t.Background())
}
func pageSplit(w, h int) (listW, listH, detailW, detailH int, wide bool) {
	if w >= 86 {
		listW = max(28, min(42, w*2/5))
		return listW, h, w - listW - 2, h, true
	}
	listH = max(3, min(9, h/2))
	return w, listH, w, max(1, h-listH-1), false
}
func pageJoin(list, detail string, wide bool) string {
	if wide {
		return lipgloss.JoinHorizontal(lipgloss.Top, list, "  ", detail)
	}
	return list + "\n" + detail
}
func pageEmpty(title, help string) string {
	return styles.BaseStyle().Bold(true).Render(title) + "\n\n" + pageMuted(help)
}
func pageCount(selected, total int) string {
	if total == 0 {
		return "0 items"
	}
	return fmt.Sprintf("%d / %d", selected+1, total)
}

func pageFooter(text string, width int) string {
	if ansi.StringWidth(text) <= width {
		return text
	}
	if i := strings.LastIndex(text, " · Esc "); i >= 0 {
		escape := text[i+3:]
		if width <= ansi.StringWidth(escape)+3 {
			return pageText(escape, width)
		}
		return pageText(text[:i], width-ansi.StringWidth(escape)-3) + " · " + escape
	}
	return pageText(text, width)
}
