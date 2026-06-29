package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/task"
)

const MonitorToolName = "monitor"

// MonitorParams is the input schema for the monitor tool.
type MonitorParams struct {
	Cmd           string   `json:"cmd"`
	Args          []string `json:"args,omitempty"`
	Cwd           string   `json:"cwd,omitempty"`
	Pattern       string   `json:"pattern"`
	MinIntervalMs int      `json:"min_interval_ms,omitempty"`
	MaxEvents     int      `json:"max_events,omitempty"`
	Description   string   `json:"description,omitempty"`
}

// MonitorResponseMetadata is the per-task ack metadata for the monitor tool.
type MonitorResponseMetadata struct {
	TaskID        string `json:"task_id"`
	OutputPath    string `json:"output_file"`
	Pattern       string `json:"pattern"`
	MinIntervalMs int    `json:"min_interval_ms"`
	MaxEvents     int    `json:"max_events"`
}

const (
	monitorDefaultIntervalMs = 5000
	monitorMinIntervalMs     = 100
	monitorMaxIntervalMs     = 600000
	monitorDefaultMaxEvents  = 200
	monitorMaxEventsCap      = 10000
)

type monitorTool struct {
	permissions permission.Service
	registry    agentregistry.Registry
}

// NewMonitorTool constructs the monitor tool.
func NewMonitorTool(perm permission.Service, reg agentregistry.Registry) BaseTool {
	return &monitorTool{permissions: perm, registry: reg}
}

func (m *monitorTool) Info() ToolInfo {
	return ToolInfo{
		Name: MonitorToolName,
		Description: `Spawn a long-running subprocess and stream regex-matched lines back as notifications. Returns immediately; a terminal notification fires when the subprocess exits, hits max_events, or is stopped via ` + "`taskstop`" + `.

Use for follow-mode streams (` + "`tail -F`" + `, ` + "`kubectl logs -f`" + `, ` + "`journalctl -f`" + `). For run-to-completion work (build/test/deploy) use ` + "`bash`" + ` with ` + "`run_in_background: true`" + ` instead.

No shell: for pipes/redirects use ` + "`cmd: \"bash\", args: [\"-c\", \"...\"]`" + `. Don't poll ` + "`tasklist`" + ` — events arrive automatically.
`,
		Parameters: map[string]any{
			"cmd": map[string]any{
				"type":        "string",
				"description": "The command to spawn (absolute path or PATH-resolved)",
			},
			"args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional arguments to pass to cmd",
			},
			"cwd": map[string]any{
				"type":        "string",
				"description": "Working directory; defaults to the workspace root",
			},
			"pattern": map[string]any{
				"type":        "string",
				"description": "RE2 regex applied line-by-line to merged stdout+stderr. Matched lines are coalesced and surfaced as monitor-event notifications.",
			},
			"min_interval_ms": map[string]any{
				"type":        "integer",
				"description": "Coalesce window in milliseconds (default 5000, min 100, max 600000). All matched lines that arrive within one window are batched into one notification.",
			},
			"max_events": map[string]any{
				"type":        "integer",
				"description": "Hard cap on the number of monitor-event notifications (default 200, max 10000). When reached, the subprocess is SIGTERMed and a terminal killed notification fires.",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Short description of what's being monitored; shown in the ack and in tasklist",
			},
		},
		Required: []string{"cmd", "pattern"},
	}
}

func (m *monitorTool) AllowParallelism(ToolCall, []ToolCall) bool { return true }
func (m *monitorTool) IsBaseline() bool                           { return false }

func (m *monitorTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params MonitorParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("invalid parameters: %s", err)), nil
	}
	if strings.TrimSpace(params.Cmd) == "" {
		return NewTextErrorResponse("cmd is required"), nil
	}
	if strings.TrimSpace(params.Pattern) == "" {
		return NewTextErrorResponse("pattern is required"), nil
	}
	if params.MinIntervalMs == 0 {
		params.MinIntervalMs = monitorDefaultIntervalMs
	}
	if params.MinIntervalMs < monitorMinIntervalMs || params.MinIntervalMs > monitorMaxIntervalMs {
		return NewTextErrorResponse(fmt.Sprintf("min_interval_ms must be in [%d, %d]", monitorMinIntervalMs, monitorMaxIntervalMs)), nil
	}
	if params.MaxEvents == 0 {
		params.MaxEvents = monitorDefaultMaxEvents
	}
	if params.MaxEvents < 1 || params.MaxEvents > monitorMaxEventsCap {
		return NewTextErrorResponse(fmt.Sprintf("max_events must be in [1, %d]", monitorMaxEventsCap)), nil
	}

	re, err := regexp.Compile(params.Pattern)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("invalid pattern regex: %s", err)), nil
	}

	cwd := params.Cwd
	if cwd == "" {
		cwd = config.WorkingDirectory()
	}

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return NewEmptyResponse(), errors.New("session id and message id are required")
	}

	// Permission gate at spawn time. The synthetic events and terminal
	// notifications that follow do NOT trigger fresh permission checks.
	action := m.registry.EvaluatePermission(string(GetAgentID(ctx)), MonitorToolName, params.Cmd)
	switch action {
	case permission.ActionAllow:
	case permission.ActionDeny:
		return NewEmptyResponse(), permission.ErrorPermissionDenied
	default:
		ok := m.permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        cwd,
			ToolName:    MonitorToolName,
			Action:      "spawn",
			Description: fmt.Sprintf("Monitor: %s (pattern: %s)", joinCommand(params.Cmd, params.Args), params.Pattern),
			Params:      params,
		})
		if !ok {
			return NewEmptyResponse(), permission.ErrorPermissionDenied
		}
	}

	reg := task.GlobalRegistry()
	if reg == nil {
		return NewTextErrorResponse("monitor: task registry not initialized"), nil
	}
	taskID := task.NewTaskID(task.KindMonitor)
	outputPath, outputFile, err := reg.PrepareOutputFile(taskID)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("monitor: prepare output file: %w", err)
	}

	// Pipe stdout+stderr through a tee. cmd.Stdout and cmd.Stderr both
	// point to the write end of an in-process pipe so the read goroutine
	// can scan the merged stream while every byte is also written to disk.
	pr, pw := io.Pipe()
	cmd := exec.Command(params.Cmd, params.Args...)
	cmd.Dir = cwd
	cmd.Stdout = pw
	cmd.Stderr = pw
	// The monitored leaf becomes its own process-group leader so taskstop
	// / max_events-reached can SIGTERM the whole descendant tree (children
	// the monitor launched — e.g. `kubectl logs -f` spawning helpers —
	// receive the signal too). No-op on Windows.
	task.SetProcessGroupAttr(cmd)

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = outputFile.Close()
		_ = os.Remove(outputPath)
		return NewTextErrorResponse(fmt.Sprintf("failed to start command: %v", err)), nil
	}

	tk := &task.Task{
		ID:                    taskID,
		SessionID:             sessionID,
		Kind:                  task.KindMonitor,
		OutputPath:            outputPath,
		OriginatingToolCallID: call.ID,
		OriginatingToolName:   MonitorToolName,
		Description:           params.Description,
		Proc:                  cmd.Process,
	}
	if err := reg.Register(tk); err != nil {
		_ = cmd.Process.Kill()
		_ = pw.Close()
		_ = outputFile.Close()
		_ = os.Remove(outputPath)
		return NewEmptyResponse(), fmt.Errorf("monitor: register task: %w", err)
	}

	state := &monitorState{
		taskID:      taskID,
		sessionID:   sessionID,
		callID:      call.ID,
		params:      params,
		re:          re,
		outputFile:  outputFile,
		outputPath:  outputPath,
		stop:        make(chan struct{}),
		eventBudget: params.MaxEvents,
	}
	syntheticInput := buildSyntheticMonitorInput(params)

	go state.scanLoop(pr)
	go state.coalesceLoop(syntheticInput)
	go state.waitAndFinalize(cmd, pw, syntheticInput)

	ack := fmt.Sprintf(
		"Monitor started.\ntask_id: %s\noutput_file: %s\ncmd: %s\npattern: %s\nmin_interval_ms: %d\nmax_events: %d\n\nMatching lines will arrive as synthetic monitor-event notifications. A terminal notification (completed / failed / killed) fires when the subprocess exits, max_events is reached, or you call taskstop. Do NOT poll — the events arrive automatically.",
		taskID, outputPath, joinCommand(params.Cmd, params.Args), params.Pattern, params.MinIntervalMs, params.MaxEvents,
	)
	return WithResponseMetadata(NewTextResponse(ack), MonitorResponseMetadata{
		TaskID:        taskID,
		OutputPath:    outputPath,
		Pattern:       params.Pattern,
		MinIntervalMs: params.MinIntervalMs,
		MaxEvents:     params.MaxEvents,
	}), nil
}

// monitorState carries the bookkeeping for one running monitor task. All
// fields are owned by the three goroutines (scan/coalesce/wait) — no
// foreign goroutine touches state except via cmd.Process.Signal (from
// registry.Kill, which only sends SIGTERM and never reads state directly).
//
// The mu lock guards both `buffer` (drained by coalesce, appended by scan)
// and `emitted` (incremented by coalesce + final-drain in waitAndFinalize,
// read by both to compare against eventBudget). After scanLoop closes
// s.stop, coalesceLoop's pending tick and waitAndFinalize's final drain
// can race on these — the lock keeps the counter monotonic and the buffer
// non-corrupting.
type monitorState struct {
	taskID, sessionID, callID string
	params                    MonitorParams
	re                        *regexp.Regexp
	outputFile                *os.File
	outputPath                string

	mu      sync.Mutex
	buffer  []string
	emitted int

	stop        chan struct{} // closed when scan EOFs
	stopOnce    sync.Once
	eventBudget int
	finalized   sync.Once
}

func (s *monitorState) scanLoop(pr io.Reader) {
	defer logging.RecoverPanic("monitor.scanLoop", nil)
	defer s.stopOnce.Do(func() { close(s.stop) })
	scanner := bufio.NewScanner(pr)
	// Bump line size cap for chatty logs.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		// Write the FULL line to the output file unconditionally.
		_, _ = s.outputFile.WriteString(line + "\n")
		if s.re.MatchString(line) {
			s.mu.Lock()
			s.buffer = append(s.buffer, line)
			s.mu.Unlock()
		}
	}
	_ = s.outputFile.Sync()
}

func (s *monitorState) coalesceLoop(syntheticInput string) {
	defer logging.RecoverPanic("monitor.coalesceLoop", nil)
	t := time.NewTicker(time.Duration(s.params.MinIntervalMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.drainAndEmit(syntheticInput)
			if s.emittedCount() >= s.eventBudget {
				// Cap reached — terminate the subprocess. waitAndFinalize
				// will fire the StatusKilled "max_events reached" notification.
				s.killForBudget()
				return
			}
		}
	}
}

// drainAndEmit atomically swaps out the buffer and the emit counter under
// s.mu before calling the (lock-free) EnqueueTaskCompletion. Holding mu
// across the reservation ensures concurrent drains from coalesceLoop and
// waitAndFinalize never both incr past eventBudget for the same window.
func (s *monitorState) drainAndEmit(syntheticInput string) {
	s.mu.Lock()
	if len(s.buffer) == 0 || s.emitted >= s.eventBudget {
		s.mu.Unlock()
		return
	}
	lines := s.buffer
	s.buffer = nil
	s.emitted++
	s.mu.Unlock()
	s.emitEvent(lines, syntheticInput)
}

func (s *monitorState) emittedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emitted
}

// emitEvent sends one coalesced notification. The caller is responsible
// for having already reserved an emit slot via the s.emitted increment
// inside drainAndEmit — emitEvent itself does not touch the counter.
func (s *monitorState) emitEvent(lines []string, syntheticInput string) {
	if len(lines) == 0 {
		return
	}
	body := fmt.Sprintf("%d match(es) in window:\n%s", len(lines), strings.Join(lines, "\n"))
	if err := task.EnqueueTaskCompletion(context.Background(), task.CompletionInput{
		SessionID:             s.sessionID,
		OriginatingToolCallID: s.callID,
		OriginatingToolName:   MonitorToolName,
		TaskID:                s.taskID,
		Kind:                  task.KindMonitor,
		Status:                task.StatusMonitorEvent,
		Input:                 syntheticInput,
		Content:               body,
	}); err != nil {
		logging.Warn("monitor: enqueue monitor-event failed", "task_id", s.taskID, "err", err)
	}
}

func (s *monitorState) killForBudget() {
	// Best-effort terminate of the whole process group (POSIX) or leaf
	// (Windows). We intentionally do NOT flip the registry state here —
	// waitAndFinalize keys its summary on `emitted >= eventBudget` to
	// emit "max_events reached"; a registry-marked Killed would shadow
	// that into a generic "Monitor stopped by taskstop" message.
	if reg := task.GlobalRegistry(); reg != nil {
		if tk, ok := reg.Get(s.taskID); ok && tk.Proc != nil {
			task.SignalProcessGroup(tk.Proc, task.TerminateSignal())
		}
	}
}

func (s *monitorState) waitAndFinalize(cmd *exec.Cmd, pw *io.PipeWriter, syntheticInput string) {
	defer logging.RecoverPanic("monitor.waitAndFinalize", nil)
	waitErr := cmd.Wait()
	// Closing the write end signals the scanLoop to return; the buffered
	// reader drains any remaining bytes first.
	_ = pw.Close()
	<-s.stop
	// Final drain (any lines accumulated between the last ticker tick and now).
	s.drainAndEmit(syntheticInput)
	_ = s.outputFile.Sync()
	_ = s.outputFile.Close()

	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	// Determine terminal status. Priority: registry-marked Killed (from
	// taskstop) → cap-reached (`emitted >= budget` AND status != killed) →
	// exit-code-driven.
	status := task.StatusCompleted
	var summary string
	if reg := task.GlobalRegistry(); reg != nil {
		if existing, ok := reg.Get(s.taskID); ok && existing.State() == task.StateKilled {
			status = task.StatusKilled
			summary = "Monitor stopped by taskstop"
		}
	}
	emitted := s.emittedCount()
	if status != task.StatusKilled && emitted >= s.eventBudget {
		status = task.StatusKilled
		summary = fmt.Sprintf("Monitor stopped: max_events reached (%d)", s.eventBudget)
	}
	if status == task.StatusCompleted && exitCode != 0 {
		status = task.StatusFailed
		summary = fmt.Sprintf("Monitor script failed (exit %d)", exitCode)
	}
	if summary == "" && status == task.StatusCompleted {
		summary = "Monitor stream ended"
	}

	s.finalized.Do(func() {
		ec := exitCode
		if err := task.EnqueueTaskCompletion(context.Background(), task.CompletionInput{
			SessionID:             s.sessionID,
			OriginatingToolCallID: s.callID,
			OriginatingToolName:   MonitorToolName,
			TaskID:                s.taskID,
			Kind:                  task.KindMonitor,
			Status:                status,
			ExitCode:              &ec,
			Input:                 syntheticInput,
			Content:               summary,
			SuppressIfNotified:    true,
		}); err != nil {
			logging.Warn("monitor: enqueue terminal completion failed", "task_id", s.taskID, "err", err)
		}
	})
}

func buildSyntheticMonitorInput(p MonitorParams) string {
	b, _ := json.Marshal(p)
	return string(b)
}

func joinCommand(cmd string, args []string) string {
	if len(args) == 0 {
		return cmd
	}
	return cmd + " " + strings.Join(args, " ")
}
