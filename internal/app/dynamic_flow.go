package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/fileutil"
	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/flowrun"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
)

type FlowStepSnapshot struct {
	ID            string `json:"id"`
	Agent         string `json:"agent"`
	SessionID     string `json:"sessionId,omitempty"`
	Status        string `json:"status"`
	Prompt        string `json:"prompt,omitempty"`
	Output        string `json:"output,omitempty"`
	Iteration     int    `json:"iteration"`
	RunningAgents int    `json:"runningAgents"`
}
type FlowSnapshot struct {
	RunID         string             `json:"runId,omitempty"`
	RootSessionID string             `json:"rootSessionId"`
	FlowID        string             `json:"flowId"`
	Status        string             `json:"status"`
	Dynamic       bool               `json:"dynamic"`
	Running       bool               `json:"running"`
	Steps         []FlowStepSnapshot `json:"steps"`
}
type dynamicRun struct {
	taskBaseline        map[string]bool
	sessionBaseline     map[string]bool
	lastCompletedOutput string
	mu                  sync.Mutex
	record              *flowrun.Run
	lease               flowrun.Lease
	inbox               []flowrun.Inbox
	stopStatus          string
	runtimeArgs         map[string]any
	cancel              context.CancelFunc
	done                chan struct{}
	status              string
	manifest            *flow.DynamicManifest
	steps               map[string]context.CancelFunc
	steering            []string
}

func (app *App) FlowBusy(root string) bool {
	app.dynamicMu.Lock()
	defer app.dynamicMu.Unlock()
	_, ok := app.dynamicRuns[root]
	return ok
}
func (app *App) flowLifetime() context.Context {
	if app.ctx != nil {
		return app.ctx
	}
	return context.Background()
}
func (app *App) flowNote(root, text string) {
	if app.Messages != nil {
		_, _ = app.Messages.Create(context.Background(), root, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: text}}})
	}
}
func flowKey(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:12])
}

// beginFlow serializes process-local and process-external owners. The OS releases
// the lock after a crash, permitting RecoverRunning only after ownership is ours.
func (app *App) beginFlow(root, status string) (context.Context, *dynamicRun, func(), error) {
	app.dynamicMu.Lock()
	defer app.dynamicMu.Unlock()
	if app.mcpPolicyChanging {
		return nil, nil, nil, fmt.Errorf("MCP tools are updating; retry the flow when they finish")
	}
	if app.dynamicRuns == nil {
		app.dynamicRuns = make(map[string]*dynamicRun)
	}
	if app.dynamicRuns[root] != nil {
		return nil, nil, nil, fmt.Errorf("flow already running")
	}
	dir, err := flow.DynamicDirectory()
	if err != nil {
		return nil, nil, nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, nil, err
	}
	lock, err := fileutil.Acquire(filepath.Join(dir, flowKey(root)+".lock"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("flow is owned by another process: %w", err)
	}
	ctx, cancel := context.WithCancel(app.flowLifetime())
	run := &dynamicRun{cancel: cancel, done: make(chan struct{}), status: status, steps: make(map[string]context.CancelFunc)}
	app.dynamicRuns[root] = run
	finish := func() {
		cancel()
		app.dynamicMu.Lock()
		_ = lock.Release()
		delete(app.dynamicRuns, root)
		app.dynamicMu.Unlock()
		close(run.done)
	}
	return ctx, run, finish, nil
}

func (app *App) StartDynamicFlow(ctx context.Context, root, prompt string) error {
	if app.FlowRuns != nil {
		_, err := app.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "plan", Goal: prompt})
		return err
	}
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("usage: /flow <goal>")
	}
	if _, err := exec.LookPath("openspec"); err != nil {
		return fmt.Errorf("dynamic flows require OpenSpec; install it first with npm install -g @fission-ai/openspec, then retry /flow")
	}
	name, agent := app.ResolveSessionAgent(ctx, root)
	info, ok := app.Registry.Get(string(name))
	if !ok || info.Mode != config.AgentModeAgent || info.DynamicFlow == nil || !info.DynamicFlow.Enabled {
		return fmt.Errorf("enable dynamicFlow.enabled for primary agent %s first", name)
	}
	if !app.Registry.IsToolEnabled(string(name), tools.FlowToolName) {
		return fmt.Errorf("agent %s excludes the flow tool", name)
	}
	size := info.DynamicFlow.FlowComplexity
	if size != "" {
		if _, ok := config.DynamicFlowLimits(size); !ok {
			return fmt.Errorf("invalid configured flowComplexity %q", size)
		}
	}
	if _, err := app.Sessions.Get(ctx, root); err != nil {
		return err
	}
	dir, err := flow.DynamicDirectory()
	if err != nil {
		return err
	}
	if _, err = flow.LoadDynamic(dir, root); err == nil {
		return fmt.Errorf("session already owns a flow; use /flow to inspect or resume it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	runCtx, run, finish, err := app.beginFlow(root, "planning")
	if err != nil {
		return err
	}
	var names []string
	allowed := map[string]bool{}
	for _, a := range app.Registry.ListByMode(config.AgentModeSubagent) {
		names = append(names, a.ID)
		allowed[a.ID] = true
	}
	submit := func(ctx context.Context, sessionID string, req tools.FlowRequest) error {
		if sessionID != root || req.Operation != "create" {
			return fmt.Errorf("only the planning root may create this flow")
		}
		selected := size
		if selected == "" {
			selected = req.Complexity
		}
		f, err := flow.ParseDynamic(req.Definition, "dynamic-"+flowKey(root), string(name), selected, allowed)
		if err != nil {
			return err
		}
		m := flow.DynamicManifest{RootSessionID: root, Agent: string(name), Complexity: selected, Definition: *f, Prompt: prompt}
		app.dynamicMu.Lock()
		defer app.dynamicMu.Unlock()
		if run.manifest != nil {
			return fmt.Errorf("definition already submitted")
		}
		m.Steering = append(m.Steering, run.steering...)
		if err = flow.SaveDynamic(dir, m); err != nil {
			return err
		}
		run.manifest = &m
		return nil
	}
	runCtx = tools.WithFlowSubmitter(runCtx, submit)
	go func() {
		defer finish()
		choice := size
		if choice == "" {
			choice = "auto"
		}
		events, err := agent.Run(runCtx, root, flow.BuilderPrompt(prompt, string(name), choice, names), 80)
		if err == nil {
			for event := range events {
				if event.Error != nil {
					err = event.Error
				}
			}
		}
		if err != nil {
			app.flowNote(root, "Flow planning stopped: "+err.Error())
			return
		}
		app.dynamicMu.Lock()
		m := run.manifest
		app.dynamicMu.Unlock()
		if m == nil {
			app.flowNote(root, "No flow definition was submitted. Retry /flow with the goal.")
			return
		}
		// Channel close can precede the deferred busy-slot release.
		for agent.IsSessionBusy(root) {
			select {
			case <-runCtx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
		if err = app.executeFlow(runCtx, root, run, *m, false); err != nil {
			app.flowNote(root, "Flow stopped: "+err.Error())
		}
	}()
	return nil
}

func (app *App) QueueFlowSteering(root, text string) error {
	if app.FlowRuns != nil {
		return app.QueueFlowMessages(context.Background(), root, []FlowSteeringMessage{{Text: text}})
	}
	app.dynamicMu.Lock()
	defer app.dynamicMu.Unlock()
	run := app.dynamicRuns[root]
	if run == nil {
		return fmt.Errorf("no running flow")
	}
	if run.manifest == nil {
		run.steering = append(run.steering, text)
		if app.Messages != nil {
			_, err := app.Messages.Create(context.Background(), root, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}})
			return err
		}
		return nil
	}
	dir, err := flow.DynamicDirectory()
	if err != nil {
		return err
	}
	m := *run.manifest
	m.Steering = append(append([]string(nil), m.Steering...), text)
	if err = flow.SaveDynamic(dir, m); err != nil {
		return err
	}
	run.manifest = &m
	if app.Messages != nil {
		_, err = app.Messages.Create(context.Background(), root, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}})
	}
	return err
}

func (app *App) executeFlow(ctx context.Context, root string, run *dynamicRun, m flow.DynamicManifest, recover bool) error {
	if run.record != nil {
		return app.executeStoredFlow(ctx, root, run, m, recover)
	}
	limits, _ := config.DynamicFlowLimits(m.Complexity)
	if !m.Standalone {
		ctx = agentpkg.WithPrimaryFlowPrefix(ctx, app.PrimaryAgents[config.AgentName(m.Agent)], root)
		ctx = tools.WithFlowTaskLimit(ctx, limits.MaxParallel)
		ctx = tools.WithFlowTaskTurns(ctx, limits.MaxTurns)
	}
	app.dynamicMu.Lock()
	run.status = "running"
	if run.manifest == nil {
		run.manifest = &m
	} else {
		m = *run.manifest
	}
	app.dynamicMu.Unlock()
	before := func(ctx context.Context, step *flow.Step, args map[string]any) (context.Context, error) {
		app.dynamicMu.Lock()
		defer app.dynamicMu.Unlock()
		if err := ctx.Err(); err != nil {
			return ctx, err
		}
		for _, candidate := range run.manifest.Definition.Spec.Steps {
			if candidate.ID == step.ID {
				*step = candidate
				break
			}
		}
		if len(run.manifest.Steering) > 0 {
			steering := strings.Join(run.manifest.Steering, "\n\n")
			// Persist steering in the step args as well as the prompt, so a crash
			// after consumption does not silently lose the user's direction.
			args["user_steering"] = steering
			batch, _ := json.Marshal(run.manifest.Steering)
			args["_dynamic_steering_batch"] = string(batch)
			step.Prompt += "\n\nLatest user steering (adjust remaining work accordingly):\n${args.user_steering}"

		} else if _, ok := args["user_steering"].(string); ok {
			step.Prompt += "\n\nUser steering:\n${args.user_steering}"
		}
		stepCtx, cancel := context.WithCancel(ctx)
		run.steps[step.ID] = cancel
		return stepCtx, nil
	}
	ctx = tools.WithFlowSubmitter(ctx, func(callCtx context.Context, sessionID string, req tools.FlowRequest) error {
		name, _ := callCtx.Value(tools.AgentIDContextKey).(config.AgentName)
		if string(name) != m.Agent {
			return fmt.Errorf("only the pinned primary agent may amend a flow")
		}
		return app.amendDynamicFlow(callCtx, root, run, req)
	})
	opts := flow.RunOptions{DefinitionSnapshot: func() *flow.Flow {
		app.dynamicMu.Lock()
		defer app.dynamicMu.Unlock()
		f := run.manifest.Definition
		return &f
	}, Definition: &m.Definition, RootSessionID: root, RecoverRunning: recover, BeforeStep: before, MaxParallel: 1, Attended: true}
	if m.Standalone {
		opts.MaxParallel = 0
	}
	prefix := root
	if m.SessionPrefix != "" {
		prefix = m.SessionPrefix
	}
	events, states, err := app.Flows.RunWithOptions(ctx, prefix, m.Definition.ID, map[string]any{"prompt": m.Prompt}, opts)
	if err != nil {
		return err
	}
	lastStatus := map[string]flow.FlowStatus{}
	progress := flowStepProgress{}
	for events != nil || states != nil {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case state, ok := <-states:
			if !ok {
				states = nil
				continue
			}
			if state != nil {
				app.dynamicMu.Lock()
				if state.Status == flow.FlowStatusRunning {
					app.ackFlowSteering(run, state.Args)
				}
				if state.Status == flow.FlowStatusCompleted || state.Status == flow.FlowStatusFailed || state.Status == flow.FlowStatusPostponed {
					delete(run.steps, state.StepID)
				}
				app.dynamicMu.Unlock()
				if lastStatus[state.StepID] != state.Status {
					lastStatus[state.StepID] = state.Status
					stepAgent := ""
					for _, s := range m.Definition.Spec.Steps {
						if s.ID == state.StepID {
							stepAgent = s.Agent
							break
						}
					}
					text := app.flowStepProgressNote(ctx, progress, *state, stepAgent)
					app.flowNote(root, text)
				}
			}
		}
	}
	return nil
}

func (app *App) ResumeFlow(ctx context.Context, root string) error {
	if app.FlowRuns != nil {
		return app.resumeStoredFlow(ctx, root)
	}
	dir, err := flow.DynamicDirectory()
	if err != nil {
		return err
	}
	m, err := flow.LoadDynamic(dir, root)
	if errors.Is(err, os.ErrNotExist) {
		return app.resumeStandalone(ctx, root)
	}
	if err != nil {
		return err
	}
	if m.Terminated {
		return fmt.Errorf("flow was terminated")
	}
	snap, err := app.DynamicFlowSnapshot(ctx, root)
	if err != nil {
		return err
	}
	if snap.Status == "completed" {
		return fmt.Errorf("flow already completed")
	}
	runCtx, run, finish, err := app.beginFlow(root, "resuming")
	if err != nil {
		return err
	}
	app.dynamicMu.Lock()
	run.manifest = &m
	app.dynamicMu.Unlock()
	go func() {
		defer finish()
		if err := app.executeFlow(runCtx, root, run, m, true); err != nil {
			app.flowNote(root, "Flow resume stopped: "+err.Error())
		}
	}()
	return nil
}

func (app *App) flowRows(ctx context.Context, root string) ([]db.FlowState, error) {
	if app.flowQuery == nil {
		return nil, fmt.Errorf("flow storage unavailable")
	}
	return app.flowQuery.ListFlowStatesByRootSession(ctx, root)
}
func (app *App) DynamicFlowSnapshot(ctx context.Context, root string) (FlowSnapshot, error) {
	if app.FlowRuns != nil {
		return app.storedFlowSnapshot(ctx, root)
	}
	snap := FlowSnapshot{RootSessionID: root}
	rows, err := app.flowRows(ctx, root)
	if err != nil {
		return snap, err
	}
	dir, err := flow.DynamicDirectory()
	if err != nil {
		return snap, err
	}
	m, loadErr := flow.LoadDynamic(dir, root)
	var def *flow.Flow
	if loadErr == nil {
		snap.Dynamic = !m.Standalone
		snap.FlowID = m.Definition.ID
		def = &m.Definition
		if m.Terminated {
			snap.Status = "terminated"
		}
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return snap, loadErr
	} else if len(rows) > 0 {
		snap.FlowID = rows[0].FlowID
		def, _ = flow.Get(snap.FlowID)
	}
	app.dynamicMu.Lock()
	if run := app.dynamicRuns[root]; run != nil {
		snap.Running = true
		snap.Status = run.status
		if def == nil && run.manifest != nil {
			f := run.manifest.Definition
			def = &f
			snap.FlowID = f.ID
			snap.Dynamic = true
		}
	}
	app.dynamicMu.Unlock()
	byID := map[string]db.FlowState{}
	for _, r := range rows {
		byID[r.StepID] = r
	}
	if def != nil {
		for _, step := range def.Spec.Steps {
			item := FlowStepSnapshot{ID: step.ID, Agent: step.Agent, Prompt: step.Prompt, Status: "pending"}
			if r, ok := byID[step.ID]; ok {
				item.SessionID = r.SessionID
				item.Status = r.Status
				item.Iteration = int(r.Iteration)
				item.Output = r.Output.String
			}
			if item.Status == "running" && snap.Running {
				item.RunningAgents = 1
			}
			if item.SessionID != "" && item.Status == "running" && snap.Running && app.Messages != nil {
				msgs, err := app.Messages.List(ctx, item.SessionID)
				if err == nil {
					var out strings.Builder
					for j := max(0, len(msgs)-8); j < len(msgs); j++ {
						if msgs[j].Role == message.Assistant {
							out.WriteString(msgs[j].Content().String())
							out.WriteString("\n")
						}
					}
					text := out.String()
					if len(text) > 16000 {
						text = text[len(text)-16000:]
					}
					if text != "" {
						item.Output = text
					}
				}
				seen := map[string]bool{}
				if reg := task.GlobalRegistry(); reg != nil {
					for _, t := range reg.ListBySessionTree(item.SessionID) {
						if t.Kind == task.KindTask && t.State() == task.StateRunning {
							item.RunningAgents++
							seen[t.AgentSessionID] = true
						}
					}
				}
				if app.Sessions != nil {
					children, err := app.Sessions.ListChildren(ctx, root)
					if err == nil {
						item.RunningAgents += countSynchronousFlowAgents(item.SessionID, children, seen, agentpkg.SessionBusy)
					}
				}
			}
			snap.Steps = append(snap.Steps, item)
		}
	} else {
		for _, r := range rows {
			snap.Steps = append(snap.Steps, FlowStepSnapshot{ID: r.StepID, SessionID: r.SessionID, Status: r.Status, Output: r.Output.String, Iteration: int(r.Iteration)})
		}
	}
	if snap.FlowID == "" && !snap.Running {
		return snap, fmt.Errorf("session has no flow")
	}
	if snap.Status == "" {
		snap.Status = "completed"
		if def != nil && flow.HasPendingWork(rows, def) {
			snap.Status = "ready"
		}
		if len(rows) == 0 {
			snap.Status = "ready"
		}
		for _, row := range rows {
			if row.Status != "completed" {
				snap.Status = row.Status
				if snap.Status == "running" {
					snap.Status = "interrupted"
				}
				break
			}
		}
	}
	return snap, nil
}

func (app *App) ControlFlow(ctx context.Context, root, step, action string) error {
	if app.FlowRuns != nil {
		return app.controlStoredFlow(ctx, root, step, action)
	}
	if action == "resume" {
		return app.ResumeFlow(ctx, root)
	}
	if action != "cancel" && action != "skip" && action != "terminate" {
		return fmt.Errorf("unknown flow action %q", action)
	}

	if action == "skip" {
		dir, err := flow.DynamicDirectory()
		if err != nil {
			return err
		}
		m, err := flow.LoadDynamic(dir, root)
		if err != nil {
			return err
		}
		if err = validateFlowSkip(m, step); err != nil {
			return err
		}
		rows, err := app.flowRows(ctx, root)
		if err != nil {
			return err
		}
		found := false
		for _, r := range rows {
			if r.StepID == step {
				found = true
				if r.Status == "completed" {
					return fmt.Errorf("step already completed")
				}
			}
		}
		if !found {
			return fmt.Errorf("step has not started")
		}
	}
	app.dynamicMu.Lock()
	run := app.dynamicRuns[root]

	if run != nil {
		run.cancel()
	}
	app.dynamicMu.Unlock()
	if run != nil {
		select {
		case <-run.done:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
			return fmt.Errorf("flow is still stopping; retry once it settles")
		}
	}
	dir, err := flow.DynamicDirectory()
	if err != nil {
		return err
	}
	m, err := flow.LoadDynamic(dir, root)
	if action == "terminate" {
		if err == nil {
			m.Terminated = true
			return flow.SaveDynamic(dir, m)
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if action == "cancel" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("skip requires a dynamic flow: %w", err)
	}
	if err = validateFlowSkip(m, step); err != nil {
		return err
	}
	rows, err := app.flowRows(ctx, root)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.StepID == step {
			if r.Status == "completed" {
				return fmt.Errorf("step already completed while stopping")
			}
			_, err = app.flowQuery.UpdateFlowState(ctx, db.UpdateFlowStateParams{SessionID: r.SessionID, Status: "completed", Args: r.Args, Output: sql.NullString{String: "Skipped by user", Valid: true}, Iteration: r.Iteration})
			if err != nil {
				return err
			}
			if snap, err := app.DynamicFlowSnapshot(ctx, root); err == nil && snap.Status == "completed" {
				return nil
			}
			return app.ResumeFlow(ctx, root)
		}
	}
	return fmt.Errorf("step %q has not started", step)
}

func (app *App) resumeStandalone(ctx context.Context, root string) error {
	rows, err := app.flowRows(ctx, root)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("session has no saved flow")
	}
	f, err := flow.Get(rows[0].FlowID)
	if err != nil {
		return err
	}
	if !flow.HasPendingWork(rows, f) {
		return fmt.Errorf("flow already completed")
	}
	suffix := "-" + strings.ReplaceAll(f.ID, "/", "--") + "-" + f.Spec.Steps[0].ID
	if !strings.HasSuffix(root, suffix) {
		return fmt.Errorf("cannot resolve standalone flow prefix")
	}
	prefix := strings.TrimSuffix(root, suffix)
	runCtx, run, finish, err := app.beginFlow(root, "running")
	if err != nil {
		return err
	}
	m := flow.DynamicManifest{RootSessionID: root, Definition: *f, Standalone: true, SessionPrefix: prefix, Agent: f.Spec.Steps[0].Agent, Complexity: "XXL"}
	m.Definition.Spec.Session.ResumeOnFailure = true
	dir, err := flow.DynamicDirectory()
	if err != nil {
		finish()
		return err
	}
	if err = flow.SaveDynamic(dir, m); err != nil {
		finish()
		return err
	}
	app.dynamicMu.Lock()
	run.manifest = &m
	app.dynamicMu.Unlock()
	go func() {
		defer finish()
		if err := app.executeFlow(runCtx, root, run, m, true); err != nil {
			app.flowNote(root, err.Error())
		}
	}()

	return nil
}

func (app *App) AnnotateFlowSessions(ctx context.Context, sessions []session.Session) []session.Session {
	for i := range sessions {
		if sessions[i].RootSessionID != "" && sessions[i].RootSessionID != sessions[i].ID {
			continue
		}
		if snap, err := app.DynamicFlowSnapshot(ctx, sessions[i].ID); err == nil {
			sessions[i].FlowID = snap.FlowID
			sessions[i].DynamicFlow = snap.Dynamic
		}
	}
	return sessions
}

// submitOrdinaryFlow lets an enabled primary initiate planning without /flow.
// Registration is tied to this App, never to whichever agent is selected later.
func (app *App) submitOrdinaryFlow(ctx context.Context, root string, req tools.FlowRequest) error {
	if app.FlowRuns != nil {
		result, err := app.SubmitFlow(ctx, root, req)
		if err == nil {
			tools.SetFlowResult(ctx, result)
		}
		return err
	}
	if req.Operation != "create" {
		return fmt.Errorf("updates are only available within the owning flow")
	}
	// A standalone flow's first step is also its session-tree root, so
	// ancestry alone cannot distinguish it from an ordinary chat turn.
	// Reject the execution scope before touching storage or taking ownership.
	if tools.FlowAgentScope(ctx) != "" {
		return fmt.Errorf("recursive dynamic flows are forbidden")
	}
	if _, err := exec.LookPath("openspec"); err != nil {
		return fmt.Errorf("install OpenSpec first: npm install -g @fission-ai/openspec")
	}
	sess, err := app.Sessions.Get(ctx, root)
	if err != nil {
		return err
	}
	if sess.ParentSessionID != "" || sess.RootSessionID != "" && sess.RootSessionID != root {
		return fmt.Errorf("recursive dynamic flows are forbidden")
	}
	name, _ := ctx.Value(tools.AgentIDContextKey).(config.AgentName)
	if name == "" {
		return fmt.Errorf("agent identity required")
	}
	info, ok := app.Registry.Get(string(name))
	if !ok || info.Mode != config.AgentModeAgent || info.DynamicFlow == nil || !info.DynamicFlow.Enabled {
		return fmt.Errorf("dynamic flow not enabled")
	}
	size := info.DynamicFlow.FlowComplexity
	if size == "" {
		size = req.Complexity
	}
	allowed := map[string]bool{}
	for _, a := range app.Registry.ListByMode(config.AgentModeSubagent) {
		allowed[a.ID] = true
	}
	f, err := flow.ParseDynamic(req.Definition, "dynamic-"+flowKey(root), string(name), size, allowed)
	if err != nil {
		return err
	}
	dir, err := flow.DynamicDirectory()
	if err != nil {
		return err
	}
	if _, err = flow.LoadDynamic(dir, root); err == nil {
		return fmt.Errorf("session already owns a flow")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	runCtx, run, finish, err := app.beginFlow(root, "planning")
	if err != nil {
		return err
	}
	goal := ""
	if app.Messages != nil {
		if msgs, err := app.Messages.List(ctx, root); err == nil {
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == message.User {
					goal = msgs[i].Content().String()
					break
				}
			}
		}
	}
	m := flow.DynamicManifest{RootSessionID: root, Agent: string(name), Complexity: size, Definition: *f, Prompt: goal}
	app.dynamicMu.Lock()
	m.Steering = append(m.Steering, run.steering...)
	err = flow.SaveDynamic(dir, m)
	if err == nil {
		run.manifest = &m
	}
	app.dynamicMu.Unlock()
	if err != nil {
		finish()
		return err
	}
	ag := app.PrimaryAgents[name]
	if ag == nil {
		finish()
		return fmt.Errorf("primary agent unavailable")
	}
	go func() {
		defer finish()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for ag.IsSessionBusy(root) {
			select {
			case <-runCtx.Done():
				return
			case <-tick.C:
			}
		}
		if err := app.executeFlow(runCtx, root, run, m, false); err != nil {
			app.flowNote(root, "Flow stopped: "+err.Error())
		}
	}()
	return nil
}

func (app *App) amendDynamicFlow(ctx context.Context, root string, run *dynamicRun, req tools.FlowRequest) error {

	if req.Operation != "update" {
		return fmt.Errorf("a flow cannot recursively create another flow")
	}
	app.dynamicMu.Lock()
	m := *run.manifest
	app.dynamicMu.Unlock()
	if m.Standalone {
		return fmt.Errorf("standalone flow definitions are managed in their source YAML")
	}
	allowed := map[string]bool{}
	for _, a := range app.Registry.ListByMode(config.AgentModeSubagent) {
		allowed[a.ID] = true
	}
	next, err := flow.ParseDynamic(req.Definition, m.Definition.ID, m.Agent, m.Complexity, allowed)
	if err != nil {
		return err
	}
	rows, err := app.flowRows(ctx, root)
	if err != nil {
		return err
	}
	entered := map[string]bool{}
	for _, r := range rows {
		entered[r.StepID] = true
	}
	if err = flow.ValidateDynamicAmendment(&m.Definition, next, entered); err != nil {
		return err
	}
	app.dynamicMu.Lock()
	defer app.dynamicMu.Unlock()
	m = *run.manifest
	m.Definition = *next
	dir, err := flow.DynamicDirectory()
	if err != nil {
		return err
	}
	if err = flow.SaveDynamic(dir, m); err != nil {
		return err
	}
	run.manifest = &m
	return nil
}

// ackFlowSteering runs under dynamicMu, only after the runtime has durably
// written the step's args. A crash before this acknowledgement causes replay,
// not loss. New messages appended after the delivered batch remain queued.
func (app *App) ackFlowSteering(run *dynamicRun, args map[string]any) {
	encoded, _ := args["_dynamic_steering_batch"].(string)
	var batch []string
	if json.Unmarshal([]byte(encoded), &batch) != nil || len(batch) == 0 || run.manifest == nil || len(run.manifest.Steering) < len(batch) {
		return
	}
	for i, s := range batch {
		if run.manifest.Steering[i] != s {
			return
		}
	}
	m := *run.manifest
	m.Steering = append([]string(nil), m.Steering[len(batch):]...)
	dir, err := flow.DynamicDirectory()
	if err == nil && flow.SaveDynamic(dir, m) == nil {
		run.manifest = &m
	}
}

// ShutdownFlows cancels runners before the message store and database disappear.
func (app *App) ShutdownFlows(budget time.Duration) {
	app.dynamicMu.Lock()
	runs := make([]*dynamicRun, 0, len(app.dynamicRuns))
	for _, run := range app.dynamicRuns {
		runs = append(runs, run)
		run.cancel()
	}
	app.dynamicMu.Unlock()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for _, run := range runs {
		select {
		case <-run.done:
		case <-timer.C:
			return
		}
	}
}

func validateFlowSkip(m flow.DynamicManifest, step string) error {
	for _, s := range m.Definition.Spec.Steps {
		if s.ID != step {
			continue
		}
		if s.Output != nil && s.Output.Schema != nil {
			return fmt.Errorf("cannot skip a step requiring structured output; resume it to supply valid output")
		}
		if len(s.Rules) > 1 {
			return fmt.Errorf("cannot skip branching step; resume it to choose a route")
		}
		for _, r := range s.Rules {
			if r.If != "" || r.Cycle || r.Postpone {
				return fmt.Errorf("cannot skip conditional or looping step; resume it")
			}
		}
		return nil
	}
	return fmt.Errorf("step %q does not exist", step)
}

// Count busy descendants absent from the background-task registry, including
// synchronous nested task calls, without counting another flow step's agents.
func countSynchronousFlowAgents(step string, children []session.Session, seen map[string]bool, busy func(string) bool) int {
	parents := make(map[string]string, len(children))
	for _, child := range children {
		parents[child.ID] = child.ParentSessionID
	}
	count := 0
	for _, child := range children {
		if seen[child.ID] || !busy(child.ID) {
			continue
		}
		ancestor := child.ParentSessionID
		for depth := 0; ancestor != "" && depth <= len(children); depth++ {
			if ancestor == step {
				count++
				break
			}
			ancestor = parents[ancestor]
		}
	}
	return count
}
