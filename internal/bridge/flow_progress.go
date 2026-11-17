package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

var ErrFlowMessageNotFound = errors.New("flow progress message no longer exists")

type FlowProgress struct {
	RunID      string             `json:"runId"`
	FlowID     string             `json:"flowId"`
	Status     string             `json:"status"`
	Error      string             `json:"error,omitempty"`
	Summary    string             `json:"summary,omitempty"`
	StartedAt  int64              `json:"startedAt,omitempty"`
	UpdatedAt  int64              `json:"updatedAt,omitempty"`
	Full       bool               `json:"full"`
	TotalSteps int                `json:"totalSteps,omitempty"`
	Steps      []FlowProgressStep `json:"steps,omitempty"`
}

type FlowProgressStep struct {
	ID             string `json:"id"`
	Agent          string `json:"agent,omitempty"`
	Status         string `json:"status"`
	Output         string `json:"output,omitempty"`
	IsStructOutput bool   `json:"isStructOutput,omitempty"`
	Iteration      int    `json:"iteration,omitempty"`
	StartedAt      int64  `json:"startedAt,omitempty"`
	FinishedAt     int64  `json:"finishedAt,omitempty"`
}

type FlowProgressPeerResolver interface {
	ResolveFlowProgressPeer(PeerRef, EditableMessageToken) string
}

type FlowProgressEditor interface {
	SendFlowProgress(context.Context, PeerRef, FlowProgress) (EditableMessageToken, error)
	EditFlowProgress(context.Context, PeerRef, EditableMessageToken, FlowProgress) error
}

// UTF-16 accounting also stays below platforms that count Unicode code points.
func LimitFlowText(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	used := 0
	for i, r := range s {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if used+n > limit {
			marker := "… [truncated]"
			if limit < FlowTextLen(marker) {
				return strings.Repeat("…", limit)
			}
			return LimitFlowTextPrefix(s[:i], limit-FlowTextLen(marker)) + marker
		}
		used += n
	}
	return s
}

func LimitFlowTextPrefix(s string, limit int) string {
	used := 0
	for i, r := range s {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if used+n > limit {
			return s[:i]
		}
		used += n
	}
	return s
}

func FlowTextLen(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

func FlowSafeText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '@' {
			return '＠'
		}
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
}

func FlowStatusIcon(status string) string {
	switch status {
	case "completed":
		return "✓"
	case "failed", "error":
		return "✗"
	case "running":
		return "▶"
	case "waiting_for_input", "postponed", "suspended", "interrupted":
		return "⏸"
	case "terminated", "cancelled", "canceled", "skipped":
		return "—"
	default:
		return "○"
	}
}

func FlowProgressTitle(p FlowProgress) string {
	name := LimitFlowText(FlowSafeText(p.FlowID), 90)
	if name == "" {
		name = "Workflow"
	}
	completed := 0
	for _, s := range p.Steps {
		if s.Status == "completed" {
			completed++
		}
	}
	title := FlowStatusIcon(p.Status) + " " + name + " · " + LimitFlowText(FlowSafeText(p.Status), 30)
	if len(p.Steps) > 0 {
		if p.TotalSteps > len(p.Steps) {
			title += fmt.Sprintf(" · %d/%d tracked steps · %d total", completed, len(p.Steps), p.TotalSteps)
		} else {
			title += fmt.Sprintf(" · %d/%d steps", completed, len(p.Steps))
		}
	}
	if d := flowDuration(p.StartedAt, p.UpdatedAt); d != "" {
		title += " · " + d
	}
	return title
}

func flowDuration(start, end int64) string {
	if start <= 0 || end < start {
		return ""
	}
	return (time.Duration((end-start)/1000) * time.Second).String()
}

func FlowStepLabel(s FlowProgressStep) string {
	return FlowStatusIcon(s.Status) + " " + LimitFlowText(FlowSafeText(s.ID), 80) + " · " + LimitFlowText(FlowSafeText(s.Status), 25)
}

func FlowStepDetails(s FlowProgressStep, now int64) string {
	parts := []string{}
	if s.Agent != "" {
		parts = append(parts, LimitFlowText(FlowSafeText(s.Agent), 80))
	}
	if s.Iteration > 1 {
		parts = append(parts, fmt.Sprintf("iteration %d", s.Iteration))
	}
	end := s.FinishedAt
	if end == 0 {
		end = now
	}
	if d := flowDuration(s.StartedAt, end); d != "" {
		parts = append(parts, d)
	}
	return strings.Join(parts, " · ")
}

func FlowStepOutput(s FlowProgressStep, limit int) (string, bool) {
	raw := strings.TrimSpace(s.Output)
	var pretty bytes.Buffer
	structured := s.IsStructOutput
	if raw != "" && (s.IsStructOutput || strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[")) && json.Indent(&pretty, []byte(raw), "", "  ") == nil {
		raw = pretty.String()
		structured = true
	}
	return LimitFlowText(FlowSafeText(raw), limit), structured
}

// Select active/error steps first, then recent completed steps, while retaining plan order.
func FlowVisibleSteps(p FlowProgress, limit int) ([]FlowProgressStep, int) {
	if len(p.Steps) <= limit {
		return p.Steps, max(0, p.TotalSteps-len(p.Steps))
	}
	chosen := map[int]bool{}
	for _, priority := range [][]string{{"running", "waiting_for_input", "postponed", "suspended"}, {"failed"}} {
		for i, s := range p.Steps {
			for _, status := range priority {
				if s.Status == status && len(chosen) < limit {
					chosen[i] = true
				}
			}
		}
	}
	for i := len(p.Steps) - 1; i >= 0 && len(chosen) < limit; i-- {
		if p.Steps[i].Status == "completed" {
			chosen[i] = true
		}
	}
	for i := range p.Steps {
		if len(chosen) >= limit {
			break
		}
		chosen[i] = true
	}
	out := make([]FlowProgressStep, 0, len(chosen))
	for i, s := range p.Steps {
		if chosen[i] {
			out = append(out, s)
		}
	}
	return out, max(p.TotalSteps, len(p.Steps)) - len(out)
}

func FlowProgressText(p FlowProgress) string {
	var b strings.Builder
	b.WriteString(FlowProgressTitle(p))
	if p.Error != "" {
		b.WriteString("\n✗ " + LimitFlowText(FlowSafeText(p.Error), 550))
	}
	if p.Summary != "" {
		b.WriteString("\n" + LimitFlowText(FlowSafeText(p.Summary), 500))
	}
	steps, omitted := FlowVisibleSteps(p, 12)
	for _, s := range steps {
		b.WriteString("\n" + FlowStepLabel(s))
		if details := FlowStepDetails(s, p.UpdatedAt); details != "" {
			b.WriteString(" · " + details)
		}
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\n… %d more steps", omitted)
	}
	if p.Status == "suspended" || p.Status == "postponed" || p.Status == "interrupted" {
		b.WriteString("\n/flow resume · /flow terminate")
	}
	return LimitFlowText(b.String(), 3000)
}
