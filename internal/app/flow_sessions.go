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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/flowrun"
	"github.com/opencode-ai/opencode/internal/langfuse"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
)

var ErrFlowPermission = errors.New("flow operation not authorized")
var ErrFlowInput = errors.New("invalid flow request")

const flowLeaseTTL = 45 * time.Second

type FlowSteeringMessage struct {
	NoQuestionRetarget bool            `json:"noQuestionRetarget,omitempty"`
	RootMessageID      string          `json:"rootMessageId,omitempty"`
	ID                 string          `json:"id"`
	Text               string          `json:"text"`
	Origin             json.RawMessage `json:"origin,omitempty"`
	TargetSessionID    string          `json:"targetSessionId,omitempty"`
	TargetStepID       string          `json:"targetStepId,omitempty"`
	TargetQuestionID   string          `json:"targetQuestionId,omitempty"`
}
type FlowRunEvent struct {
	ID             string                `json:"id"`
	Type           string                `json:"type"`
	RunID          string                `json:"runId"`
	ConversationID string                `json:"conversationId"`
	Status         string                `json:"status"`
	StepID         string                `json:"stepId,omitempty"`
	SessionID      string                `json:"sessionId,omitempty"`
	Kind           string                `json:"kind"`
	Output         string                `json:"output,omitempty"`
	Revision       int64                 `json:"revision"`
	Fence          int64                 `json:"fence"`
	Sequence       int64                 `json:"sequence"`
	MessageIDs     []string              `json:"messageIds,omitempty"`
	Messages       []FlowSteeringMessage `json:"messages,omitempty"`
	Origin         json.RawMessage       `json:"origin,omitempty"`
}
type flowOrigin struct {
	AuthorizedPeers []tools.Peer   `json:"authorizedPeers,omitempty"`
	Requester       string         `json:"requester,omitempty"`
	Peer            tools.Peer     `json:"peer,omitempty"`
	JobArgs         map[string]any `json:"jobArgs,omitempty"`
}
type storedFlowDefinition struct {
	RequestHash string                      `json:"requestHash,omitempty"`
	Manifest    flow.DynamicManifest        `json:"manifest"`
	Args        map[string]any              `json:"args,omitempty"`
	Planning    bool                        `json:"planning,omitempty"`
	SourceHash  string                      `json:"sourceHash,omitempty"`
	Prompts     map[string]storedFlowPrompt `json:"prompts,omitempty"`
}
type storedFlowPrompt struct {
	Text    string `json:"text"`
	Path    string `json:"path"`
	Version int    `json:"version"`
}

func (app *App) SessionFlowAccess(ctx context.Context, conversation string) error {
	if app.Sessions == nil {
		return fmt.Errorf("%w: session service unavailable", ErrFlowInput)
	}
	s, err := app.Sessions.Get(ctx, conversation)
	if err != nil {
		return err
	}
	if s.ProjectID != app.sessionFlowProjectID() {
		return ErrFlowPermission
	}
	if s.ParentSessionID != "" || s.RootSessionID != "" && s.RootSessionID != s.ID {
		return fmt.Errorf("%w: flow starts require an ordinary root conversation", ErrFlowPermission)
	}
	return nil
}
func (app *App) FlowCapabilities() map[string]any {
	distributed := false
	if app.FlowRuns != nil {
		if source, ok := any(app.FlowRuns).(interface{ Dialect() string }); ok {
			distributed = source.Dialect() == "mysql"
		}
	}
	return map[string]any{"protocolVersion": 1, "available": app.FlowRuns != nil, "durable": app.FlowRuns != nil, "nested": false, "operations": []string{"plan", "create", "update", "run", "list", "status", "control"}, "distributedRecovery": distributed}
}
func (app *App) SubscribeFlowEvents(ctx context.Context) <-chan pubsub.Event[FlowRunEvent] {
	app.dynamicMu.Lock()
	if app.flowEvents == nil {
		app.flowEvents = pubsub.NewBroker[FlowRunEvent]()
	}
	b := app.flowEvents
	app.dynamicMu.Unlock()
	return b.Subscribe(ctx)
}
func (app *App) publishFlowEvent(e FlowRunEvent) {
	app.dynamicMu.Lock()
	b := app.flowEvents
	app.dynamicMu.Unlock()
	if b != nil {
		b.Publish(pubsub.UpdatedEvent, e)
	}
}
func (app *App) CurrentFlow(ctx context.Context, conversation string) (flowrun.Run, error) {
	if err := app.SessionFlowAccess(ctx, conversation); err != nil {
		return flowrun.Run{}, err
	}
	if app.FlowRuns == nil {
		return flowrun.Run{}, flowrun.ErrNotFound
	}
	r, err := app.FlowRuns.Current(ctx, conversation)
	if err == nil || !errors.Is(err, flowrun.ErrNotFound) {
		return r, err
	}
	return app.FlowRuns.Latest(ctx, conversation)
}
func (app *App) ReplayFlowEvents(ctx context.Context, conversation, after string, limit int) ([]FlowRunEvent, error) {
	r, err := app.CurrentFlow(ctx, conversation)
	if err != nil {
		return nil, err
	}
	var sequence int64
	if after != "" {
		split := strings.LastIndex(after, ":")
		if split < 0 || after[:split] != r.ID {
			return nil, fmt.Errorf("%w: cursor does not belong to current run", ErrFlowInput)
		}
		sequence, err = strconv.ParseInt(after[split+1:], 10, 64)
		if err != nil || sequence < 0 {
			return nil, fmt.Errorf("%w: invalid event cursor", ErrFlowInput)
		}
	}
	records, err := app.FlowRuns.Events(ctx, r.ID, sequence, min(max(limit, 1), 500))
	if err != nil {
		return nil, err
	}
	out := make([]FlowRunEvent, 0, len(records))
	for _, record := range records {
		var e FlowRunEvent
		if err = json.Unmarshal(record.Payload, &e); err != nil {
			return nil, err
		}
		e.ID = record.ID
		e.Sequence = record.Sequence
		out = append(out, e)
	}
	return out, nil
}
func (app *App) FlowLease(runID string) (flowrun.Lease, bool) {
	app.dynamicMu.Lock()
	runs := make([]*dynamicRun, 0, len(app.dynamicRuns))
	for _, r := range app.dynamicRuns {
		runs = append(runs, r)
	}
	app.dynamicMu.Unlock()
	for _, r := range runs {
		r.mu.Lock()
		if r.record != nil && r.record.ID == runID && r.lease.Token != 0 {
			lease := r.lease
			r.mu.Unlock()
			return lease, true
		}
		r.mu.Unlock()
	}
	return flowrun.Lease{}, false
}

func (app *App) AckFlowEvent(ctx context.Context, runID, eventID, recipient string) error {
	if app.FlowRuns == nil {
		return flowrun.ErrNotFound
	}
	lease, owned := app.FlowLease(runID)
	if !owned {
		var err error
		lease, err = app.FlowRuns.ClaimDelivery(ctx, runID, "delivery-"+uuid.NewString(), flowLeaseTTL)
		if err != nil {
			return err
		}
		defer app.FlowRuns.Release(context.Background(), lease, "")
	}
	return app.FlowRuns.AckEvent(ctx, lease, eventID, recipient)
}
func (app *App) SessionFlowConversation(ctx context.Context, sid string) (string, bool, error) {
	if app.FlowRuns == nil || app.Sessions == nil {
		return "", false, nil
	}
	original, err := app.Sessions.Get(ctx, sid)
	if err != nil {
		return "", false, err
	}
	if original.ProjectID != app.sessionFlowProjectID() {
		return "", false, ErrFlowPermission
	}
	current := original
	for depth := 0; depth < 64; depth++ {
		state, err := app.flowQuery.GetFlowState(ctx, current.ID)
		if err == nil {
			r, err := app.FlowRuns.Get(ctx, state.RootSessionID)
			if err != nil {
				if errors.Is(err, flowrun.ErrNotFound) {
					return "", false, nil
				}
				return "", false, err
			}
			if original.RootSessionID != r.ConversationID && original.ID != r.ConversationID {
				return "", false, ErrFlowPermission
			}
			return r.ConversationID, true, nil
		}
		if !errors.Is(err, flowrun.ErrNotFound) {
			return "", false, err
		}
		if current.ParentSessionID == "" {
			break
		}
		current, err = app.Sessions.Get(ctx, current.ParentSessionID)
		if err != nil {
			return "", false, err
		}
	}
	return "", false, nil
}
func flowArgsForStorage(args map[string]any) map[string]any {
	out := map[string]any{}
	redacted := config.RedactedFlowArgs()
	for k, v := range args {
		if _, secret := redacted[k]; !secret {
			out[k] = v
		}
	}
	return out
}
func flowResult(r flowrun.Run, op string) tools.FlowResult {
	var d storedFlowDefinition
	_ = json.Unmarshal(r.Snapshot, &d)
	return tools.FlowResult{Operation: op, RunID: r.ID, ConversationID: r.ConversationID, Status: r.Status, Kind: r.Kind, FlowID: d.Manifest.Definition.ID}
}

func (app *App) SubmitFlow(ctx context.Context, conversation string, req tools.FlowRequest) (tools.FlowResult, error) {
	if err := tools.NormalizeFlowRequest(&req); err != nil {
		return tools.FlowResult{}, fmt.Errorf("%w: %v", ErrFlowInput, err)
	}
	if app.FlowRuns == nil {
		return tools.FlowResult{}, fmt.Errorf("%w: durable flow storage unavailable", ErrFlowInput)
	}
	if tools.FlowAgentScope(ctx) != "" {
		return tools.FlowResult{}, fmt.Errorf("%w: nested flow operations are forbidden", ErrFlowPermission)
	}
	if err := app.SessionFlowAccess(ctx, conversation); err != nil {
		return tools.FlowResult{}, err
	}
	name, _ := ctx.Value(tools.AgentIDContextKey).(config.AgentName)
	if name == "" {
		name, _ = app.ResolveSessionAgent(ctx, conversation)
	}
	info, ok := app.Registry.Get(string(name))
	if !ok || info.Disabled || info.Mode != config.AgentModeAgent || info.DynamicFlow == nil || !info.DynamicFlow.Enabled || !app.Registry.IsToolEnabled(string(name), tools.FlowToolName) {
		return tools.FlowResult{}, ErrFlowPermission
	}
	switch req.Operation {
	case "list":
		result := tools.FlowResult{Operation: "list", ConversationID: conversation}
		for _, f := range flow.All() {
			if f.Disabled {
				continue
			}
			allowed := true
			for _, s := range f.Spec.Steps {
				if strings.Contains(s.Agent, "${") {
					continue
				}
				id := s.Agent
				if id == "" {
					id = "coder"
				}
				if app.Registry.EvaluatePermission(string(name), "task", id) == permission.ActionDeny {
					allowed = false
					break
				}
			}
			if allowed {
				result.Flows = append(result.Flows, tools.FlowListing{ID: f.ID, Name: f.Name, Description: f.Description, Args: f.Spec.Args})
			}
		}
		sort.Slice(result.Flows, func(i, j int) bool { return result.Flows[i].ID < result.Flows[j].ID })
		return result, nil
	case "status":
		r, err := app.CurrentFlow(ctx, conversation)
		if err != nil {
			return tools.FlowResult{}, err
		}
		result := flowResult(r, "status")
		result.Snapshot, err = app.storedFlowSnapshot(ctx, conversation)
		return result, err
	case "control":
		if err := app.controlStoredFlow(ctx, conversation, req.StepID, req.Action); err != nil {
			return tools.FlowResult{}, err
		}
		r, err := app.CurrentFlow(ctx, conversation)
		return flowResult(r, "control"), err
	case "update":
		return tools.FlowResult{}, fmt.Errorf("%w: updates must come from the owning generated flow step", ErrFlowInput)
	case "plan", "create", "run":
	default:
		return tools.FlowResult{}, fmt.Errorf("%w: unknown operation", ErrFlowInput)
	}
	if req.InvocationID == "" {
		req.InvocationID = uuid.NewString()
	}
	hashRequest := req
	hashRequest.Args = flowArgsForStorage(req.Args)
	requestJSON, _ := json.Marshal(hashRequest)
	requestSum := sha256.Sum256(requestJSON)
	requestHash := hex.EncodeToString(requestSum[:])
	old, err := app.FlowRuns.ByInvocation(ctx, conversation, req.InvocationID)
	if err == nil {
		var original storedFlowDefinition
		if err = json.Unmarshal(old.Snapshot, &original); err != nil {
			return tools.FlowResult{}, err
		}
		if original.RequestHash != requestHash {
			return tools.FlowResult{}, flowrun.ErrConflict
		}
		if (old.Status == "ready" || old.Status == "planning") && !app.FlowBusy(conversation) {
			if err = app.startStoredFlow(ctx, old, true, req.Args); err != nil && !errors.Is(err, flowrun.ErrBusy) {
				return tools.FlowResult{}, err
			}
		}
		return flowResult(old, req.Operation), nil
	}
	if !errors.Is(err, flowrun.ErrNotFound) {
		return tools.FlowResult{}, err
	}
	if _, lookupErr := app.FlowRuns.Latest(ctx, conversation); errors.Is(lookupErr, flowrun.ErrNotFound) {
		legacy, importErr := app.importLegacyFlow(ctx, conversation)
		if importErr == nil && legacy.Status != "completed" && legacy.Status != "terminated" {
			return tools.FlowResult{}, flowrun.ErrBusy
		}
		if importErr != nil && !errors.Is(importErr, flowrun.ErrNotFound) {
			return tools.FlowResult{}, importErr
		}
	} else if lookupErr != nil {
		return tools.FlowResult{}, lookupErr
	}
	kind := "generated"
	definition := storedFlowDefinition{RequestHash: requestHash, Args: flowArgsForStorage(req.Args), Prompts: map[string]storedFlowPrompt{}}
	definition.Manifest.Agent = string(name)
	definition.Manifest.RootSessionID = conversation
	size := info.DynamicFlow.FlowComplexity
	if size == "" {
		size = req.Complexity
	}
	definition.Manifest.Complexity = size
	if req.Operation == "run" {
		kind = "named"
		if strings.TrimSpace(req.FlowID) == "" {
			return tools.FlowResult{}, fmt.Errorf("%w: flowId required", ErrFlowInput)
		}
		f, err := flow.Get(req.FlowID)
		if err != nil {
			return tools.FlowResult{}, err
		}
		if f.Disabled {
			return tools.FlowResult{}, flow.ErrFlowDisabled
		}
		if err = flow.ValidateDefinitionArgs(f, req.Args); err != nil {
			return tools.FlowResult{}, fmt.Errorf("%w: %v", ErrFlowInput, err)
		}
		for _, step := range f.Spec.Steps {
			if strings.Contains(step.Agent, "${") {
				continue
			}
			id := step.Agent
			if id == "" {
				id = "coder"
			}
			if err = app.authorizeFlowAgent(ctx, conversation, string(name), id, false); err != nil {
				return tools.FlowResult{}, err
			}
		}
		raw, err := json.Marshal(f)
		if err != nil {
			return tools.FlowResult{}, err
		}
		if err = json.Unmarshal(raw, &definition.Manifest.Definition); err != nil {
			return tools.FlowResult{}, err
		}
		sum := sha256.Sum256(raw)
		definition.SourceHash = hex.EncodeToString(sum[:])
		definition.Manifest.Standalone = true
	} else {
		if _, err := exec.LookPath("openspec"); err != nil {
			return tools.FlowResult{}, fmt.Errorf("%w: install OpenSpec before generated planning", ErrFlowInput)
		}
		if size != "" {
			if _, ok := config.DynamicFlowLimits(size); !ok {
				return tools.FlowResult{}, fmt.Errorf("%w: invalid complexity", ErrFlowInput)
			}
		}
		if req.Operation == "plan" {
			if strings.TrimSpace(req.Goal) == "" {
				return tools.FlowResult{}, fmt.Errorf("%w: goal required", ErrFlowInput)
			}
			definition.Planning = true
			definition.Manifest.Prompt = req.Goal
		} else {
			if size == "" {
				return tools.FlowResult{}, fmt.Errorf("%w: create requires complexity", ErrFlowInput)
			}
			f, err := flow.ParseDynamic(req.Definition, "dynamic-"+flowKey(conversation+"\x00"+req.InvocationID), string(name), size, app.flowSubagents(string(name)))
			if err != nil {
				return tools.FlowResult{}, fmt.Errorf("%w: %v", ErrFlowInput, err)
			}
			definition.Manifest.Definition = *f
		}
	}
	if req.InvocationID == "" {
		req.InvocationID = uuid.NewString()
	}
	origin := flowOrigin{Requester: tools.RequesterFromContext(ctx), AuthorizedPeers: tools.FlowAuthorizedPeers(ctx)}
	origin.Peer, _ = tools.PeerFromContext(ctx)
	if args, ok := tools.JobArgsFromContext(ctx); ok {
		origin.JobArgs = flowArgsForStorage(args)
	}
	originJSON, _ := json.Marshal(origin)
	snapshot, err := json.Marshal(definition)
	if err != nil {
		return tools.FlowResult{}, err
	}
	r := flowrun.Run{ID: uuid.NewString(), ConversationID: conversation, Kind: kind, Status: "planning", InvocationID: req.InvocationID, Snapshot: snapshot, Origin: originJSON}
	if !definition.Planning {
		r.Status = "ready"
	}
	if deadline := tools.SessionFlowDeadline(ctx); !deadline.IsZero() {
		r.Deadline = deadline.UnixMilli()
	}
	r, created, err := app.FlowRuns.Create(ctx, r)
	if err != nil {
		return tools.FlowResult{}, err
	}
	if !created {
		return flowResult(r, req.Operation), nil
	}
	exportFlowDefinition(r)
	if err = app.startStoredFlow(ctx, r, false, req.Args); err != nil {
		return tools.FlowResult{}, err
	}
	return flowResult(r, req.Operation), nil
}
func (app *App) flowSubagents(primary string) map[string]bool {
	out := map[string]bool{}
	for _, a := range app.Registry.ListByMode(config.AgentModeSubagent) {
		if !a.Disabled && app.Registry.EvaluatePermission(primary, "task", a.ID) != permission.ActionDeny {
			out[a.ID] = true
		}
	}
	return out
}
func (app *App) authorizeFlowAgent(ctx context.Context, conversation, primary, target string, prompt bool) error {
	info, ok := app.Registry.Get(target)
	if !ok || info.Disabled {
		return fmt.Errorf("%w: unknown or disabled step agent %s", ErrFlowPermission, target)
	}
	if target == primary {
		return nil
	}
	action := app.Registry.EvaluatePermission(primary, "task", target)
	if action == permission.ActionDeny {
		return fmt.Errorf("%w: step agent %s", ErrFlowPermission, target)
	}
	if action == permission.ActionAsk && prompt && (app.Permissions == nil || !app.Permissions.Request(ctx, permission.CreatePermissionRequest{SessionID: conversation, ToolName: "task", Action: target, Description: "Run flow step with agent " + target})) {
		return ErrFlowPermission
	}
	return nil
}

func (app *App) startStoredFlow(source context.Context, record flowrun.Run, recover bool, args map[string]any) error {
	var saved storedFlowDefinition
	if err := json.Unmarshal(record.Snapshot, &saved); err != nil {
		return err
	}
	if record.Deadline > 0 && time.Now().UnixMilli() >= record.Deadline {
		return fmt.Errorf("%w: run deadline expired", ErrFlowInput)
	}

	ctx, cancel := context.WithCancel(app.flowLifetime())
	if record.Deadline > 0 {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, time.UnixMilli(record.Deadline))
		oldCancel := cancel
		cancel = func() { deadlineCancel(); oldCancel() }
	}
	var origin flowOrigin
	if err := json.Unmarshal(record.Origin, &origin); err != nil {
		cancel()
		return err
	}
	ctx = question.WithFlowRunID(ctx, record.ID)
	ctx = tools.WithRequester(ctx, origin.Requester)
	ctx = tools.WithPeer(ctx, origin.Peer)
	ctx = tools.WithFlowAuthorizedPeers(ctx, origin.AuthorizedPeers)
	jobArgs := mapsCopy(origin.JobArgs)
	if fresh, ok := tools.JobArgsFromContext(source); ok {
		for k, v := range fresh {
			jobArgs[k] = v
		}
	}
	ctx = tools.WithJobArgs(ctx, jobArgs)
	baseline := map[string]bool{}
	sessionBaseline := map[string]bool{}
	if children, e := app.Sessions.ListChildren(source, record.ConversationID); e == nil && task.GlobalRegistry() != nil {
		for _, s := range children {
			sessionBaseline[s.ID] = true
			for _, task := range task.GlobalRegistry().PendingForSession(s.ID, nil) {
				baseline[task.ID] = true
			}
		}
	}

	app.dynamicMu.Lock()
	if app.mcpPolicyChanging || app.dynamicRuns[record.ConversationID] != nil {
		app.dynamicMu.Unlock()
		cancel()
		return flowrun.ErrBusy
	}
	if app.dynamicRuns == nil {
		app.dynamicRuns = map[string]*dynamicRun{}
	}
	run := &dynamicRun{sessionBaseline: sessionBaseline, taskBaseline: baseline, record: &record, manifest: &saved.Manifest, status: record.Status, steps: map[string]context.CancelFunc{}, done: make(chan struct{}), runtimeArgs: args, cancel: cancel}
	reserved, hasReservation := app.flowReservations[record.ID]
	if hasReservation {
		delete(app.flowReservations, record.ID)
	}
	app.dynamicRuns[record.ConversationID] = run
	app.dynamicMu.Unlock()
	var lease flowrun.Lease
	var err error
	if hasReservation {
		lease, err = app.FlowRuns.Renew(source, reserved, flowLeaseTTL)
	} else {
		lease, err = app.FlowRuns.Claim(source, record.ID, "opencode-"+uuid.NewString(), flowLeaseTTL)
	}
	if err != nil {
		cancel()
		app.dynamicMu.Lock()
		delete(app.dynamicRuns, record.ConversationID)
		app.dynamicMu.Unlock()
		close(run.done)
		return err
	}
	run.mu.Lock()
	run.lease = lease
	run.mu.Unlock()
	ctx = question.WithStartHook(ctx, func(questionCtx context.Context, request question.Request) error {
		err := app.startStoredQuestion(questionCtx, run, request)
		if err != nil {
			cancel()
		}
		return err
	})
	ctx = message.WithWriteGuard(ctx, func(checkCtx context.Context, tx *sql.Tx) error {
		return app.FlowRuns.CheckLeaseTx(checkCtx, tx, lease)
	})
	ctx = agentpkg.WithFlowInboxDrain(ctx, func(drainCtx context.Context, sessionID string) ([]message.Message, error) {
		messages, err := app.drainStoredInteraction(drainCtx, run, sessionID)
		if err != nil {
			cancel()
		}
		return messages, err
	})
	ctx = tools.WithFlowOwnershipGuard(ctx, func(checkCtx context.Context) error {
		err := app.FlowRuns.CheckLease(checkCtx, lease)
		if err != nil {
			cancel()
		}
		return err
	})

	go func() {
		ticker := time.NewTicker(flowLeaseTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-run.done:
				return
			case <-ticker.C:
				run.mu.Lock()
				current := run.lease
				run.mu.Unlock()
				renewed, err := app.FlowRuns.Renew(ctx, current, flowLeaseTTL)
				if err != nil {
					cancel()
					return
				}
				run.mu.Lock()
				run.lease = renewed
				run.mu.Unlock()
			}
		}
	}()
	go func() {
		defer func() {
			cancel()
			run.mu.Lock()
			current := run.lease
			run.mu.Unlock()
			app.dynamicMu.Lock()
			delete(app.dynamicRuns, record.ConversationID)
			app.dynamicMu.Unlock()
			_ = app.FlowRuns.Release(context.Background(), current, "")
			close(run.done)
		}()
		if err := app.emitStoredEvent(ctx, run, FlowRunEvent{Type: "session.flow.started", Status: record.Status}); err != nil {
			return
		}
		if recover && !saved.Planning {
			rows, e := app.flowQuery.ListFlowStatesByRootSession(ctx, record.ID)
			if e != nil {
				app.finishStoredFlow(run, "failed", e.Error())
				return
			}
			if len(rows) > 0 && !flow.HasPendingWork(rows, &saved.Manifest.Definition) {
				app.finishStoredFlow(run, "completed", storedFlowTerminalOutput(rows, saved.Manifest.Definition, ""))
				return
			}
		}
		primary := app.PrimaryAgents[config.AgentName(saved.Manifest.Agent)]
		if primary == nil {
			app.finishStoredFlow(run, "failed", "Configured primary agent is unavailable")
			return
		}
		for agentpkg.SessionBusy(record.ConversationID) || primary.IsSessionBusy(record.ConversationID) {
			select {
			case <-ctx.Done():
				primary.Cancel(record.ConversationID)
				app.finishStoredFlow(run, "suspended", ctx.Err().Error())
				return
			case <-time.After(15 * time.Millisecond):
			}
		}
		if saved.Planning {
			if recover {
				if err := app.restoreFlowQuestions(ctx, run); err != nil {
					app.finishStoredFlow(run, "failed", err.Error())
					return
				}
			}
			if err := app.planStoredFlow(ctx, run, &saved, primary); err != nil {
				run.mu.Lock()
				status := run.stopStatus
				run.mu.Unlock()
				if status == "" {
					status = "failed"
					if ctx.Err() != nil {
						status = "suspended"
					}
				}
				app.finishStoredFlow(run, status, err.Error())
				return
			}
		}
		err := app.executeStoredFlow(ctx, record.ConversationID, run, saved.Manifest, recover)
		status := "completed"
		output := "Flow completed."
		run.mu.Lock()
		requested := run.stopStatus
		run.mu.Unlock()
		if requested != "" {
			status = requested
			output = "Flow " + status + "."
		} else if ctx.Err() != nil {
			status = "suspended"
			output = ctx.Err().Error()
		} else if err != nil {
			status = "failed"
			output = err.Error()
		} else {
			rows, e := app.flowQuery.ListFlowStatesByRootSession(context.Background(), record.ID)
			if e != nil {
				status = "failed"
				output = e.Error()
			} else {
				for _, row := range rows {
					if row.Status == "failed" {
						status = "failed"
						output = row.Output.String
						break
					}
					if row.Status == "postponed" || row.Status == "waiting_for_input" {
						status = "suspended"
						output = "Flow is suspended at " + row.StepID
						break
					}
					if row.Status == "running" {
						status = "suspended"
						output = "Flow interrupted"
						break
					}

				}
				run.mu.Lock()
				latestDefinition := run.manifest.Definition
				lastOutput := run.lastCompletedOutput
				run.mu.Unlock()
				if status == "completed" {
					output = storedFlowTerminalOutput(rows, latestDefinition, lastOutput)
				}
				if status == "completed" && flow.HasPendingWork(rows, &latestDefinition) {
					status = "suspended"
					output = "Flow has pending steps"
				}
			}
		}
		if status != "completed" {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
			app.stopStoredTasks(stopCtx, run)
			stopCancel()
		}
		app.finishStoredFlow(run, status, output)
	}()
	return nil
}
func mapsCopy(source map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range source {
		out[k] = v
	}
	return out
}

func (app *App) updateStoredDefinition(ctx context.Context, run *dynamicRun, edit func(*storedFlowDefinition) error) error {
	var exported *flowrun.Run
	run.mu.Lock()
	defer func() {
		run.mu.Unlock()
		if exported != nil {
			exportFlowDefinition(*exported)
		}
	}()
	current, err := app.FlowRuns.Get(ctx, run.record.ID)
	if err != nil {
		return err
	}
	var definition storedFlowDefinition
	if err = json.Unmarshal(current.Snapshot, &definition); err != nil {
		return err
	}
	if err = edit(&definition); err != nil {
		return err
	}
	data, err := json.Marshal(definition)
	if err != nil {
		return err
	}
	current, err = app.FlowRuns.SaveSnapshot(ctx, run.lease, data, current.Version)
	if err != nil {
		return err
	}
	run.record = &current
	run.manifest = &definition.Manifest
	exported = &current
	return nil
}
func (app *App) emitStoredEvent(ctx context.Context, run *dynamicRun, event FlowRunEvent) error {
	run.mu.Lock()
	record := *run.record
	lease := run.lease
	run.mu.Unlock()
	event.RunID = record.ID
	event.ConversationID = record.ConversationID
	event.Kind = record.Kind
	event.Origin = record.Origin
	event.Revision = record.Version
	event.Fence = lease.Token
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	stored, err := app.FlowRuns.AppendEvent(ctx, lease, uuid.NewString(), event.Type, data)
	if err != nil {
		return err
	}
	event.ID = stored.ID
	event.Sequence = stored.Sequence
	app.publishFlowEvent(event)
	if event.Type == "session.flow.step" && app.Messages != nil {
		_, _ = app.Messages.Create(ctx, record.ConversationID, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: fmt.Sprintf("Flow · %s · %s", event.StepID, event.Status)}}})
	}
	return nil
}
func (app *App) finishStoredFlow(run *dynamicRun, status, output string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if status != "completed" {
		app.stopStoredTasks(ctx, run)
	}
	run.mu.Lock()
	conversationID := run.record.ConversationID
	run.mu.Unlock()
	for agentpkg.SessionBusy(conversationID) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
	run.mu.Lock()
	lease := run.lease
	conversation := run.record.ConversationID
	run.mu.Unlock()
	run.mu.Lock()
	record := *run.record
	run.mu.Unlock()
	event := FlowRunEvent{Type: "session.flow." + status, Status: status, Output: output, RunID: record.ID, ConversationID: record.ConversationID, Kind: record.Kind, Origin: record.Origin, Revision: record.Version, Fence: lease.Token}
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	var note message.Message
	allInputs, readErr := app.FlowRuns.InboxHistory(ctx, record.ID)
	if readErr != nil {
		return
	}
	var received []FlowSteeringMessage
	for _, item := range allInputs {
		var input FlowSteeringMessage
		if json.Unmarshal(item.Payload, &input) == nil {
			received = append(received, input)
		}
	}
	var rootInputs []message.Message
	stored, err := app.FlowRuns.TransitionEvent(ctx, lease, status, uuid.NewString(), event.Type, payload, func(q db.QuerierWithTx) error {
		if app.Messages == nil {
			return nil
		}
		var writeErr error
		rootInputs, writeErr = app.persistRootFlowInputs(ctx, q, record, received)
		if writeErr != nil {
			return writeErr
		}
		note, writeErr = message.CreateInTransaction(ctx, app.Messages, q, conversation, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: output}}})
		return writeErr
	})
	if err != nil {
		if record.Deadline > 0 {
			_ = app.terminateExpiredStoredFlow(ctx, record)
		}
		return
	}
	event.ID = stored.ID
	event.Sequence = stored.Sequence
	for _, input := range rootInputs {
		message.PublishCreated(app.Messages, input)
	}
	message.PublishCreated(app.Messages, note)
	app.publishFlowEvent(event)
}
func (app *App) planStoredFlow(ctx context.Context, run *dynamicRun, saved *storedFlowDefinition, primary agentpkg.Service) error {
	if _, err := exec.LookPath("openspec"); err != nil {
		return fmt.Errorf("OpenSpec is required to resume planning")
	}
	var names []string
	for name := range app.flowSubagents(saved.Manifest.Agent) {
		names = append(names, name)
	}
	sort.Strings(names)
	selectedSize := saved.Manifest.Complexity
	run.mu.Lock()
	record := *run.record
	run.mu.Unlock()
	submit := func(callCtx context.Context, sid string, request tools.FlowRequest) error {
		if sid != record.ConversationID || request.Operation != "create" {
			return fmt.Errorf("only the planning conversation may submit its definition")
		}
		size := selectedSize
		if size == "" {
			size = request.Complexity
		}
		f, err := flow.ParseDynamic(request.Definition, "dynamic-"+record.ID, saved.Manifest.Agent, size, app.flowSubagents(saved.Manifest.Agent))
		if err != nil {
			return err
		}
		return app.updateStoredDefinition(callCtx, run, func(d *storedFlowDefinition) error {
			if !d.Planning {
				return fmt.Errorf("definition already submitted")
			}
			d.Planning = false
			d.Manifest.Definition = *f
			d.Manifest.Complexity = size
			return nil
		})
	}
	planningCtx := tools.WithFlowSubmitter(ctx, submit)
	plannerSize := selectedSize
	if plannerSize == "" {
		plannerSize = "M"
	}
	plannerLimits, _ := config.DynamicFlowLimits(plannerSize)
	planningCtx = tools.WithFlowTaskLimit(planningCtx, plannerLimits.MaxParallel)
	planningCtx = tools.WithFlowTaskTurns(planningCtx, plannerLimits.MaxTurns)
	size := selectedSize
	if size == "" {
		size = "auto"
	}
	events, err := primary.RunWith(planningCtx, record.ConversationID, flow.BuilderPrompt(saved.Manifest.Prompt, saved.Manifest.Agent, size, names), plannerLimits.MaxTurns, agentpkg.RunOptions{NonInteractive: true})
	if err != nil {
		return err
	}
	for event := range events {
		if event.Error != nil {
			err = event.Error
		}
	}
	if err != nil {
		return err
	}
	run.mu.Lock()
	record = *run.record
	run.mu.Unlock()
	*saved = storedFlowDefinition{}
	if err = json.Unmarshal(record.Snapshot, saved); err != nil {
		return err
	}
	if saved.Planning {
		return fmt.Errorf("planner ended without submitting a definition")
	}
	return nil
}

func (app *App) executeStoredFlow(ctx context.Context, conversation string, run *dynamicRun, m flow.DynamicManifest, recover bool) error {
	run.mu.Lock()
	record := *run.record
	lease := run.lease
	runtimeArgs := mapsCopy(run.runtimeArgs)
	run.mu.Unlock()
	var saved storedFlowDefinition
	if err := json.Unmarshal(record.Snapshot, &saved); err != nil {
		return err
	}
	args := mapsCopy(saved.Args)
	if supplied, ok := tools.JobArgsFromContext(ctx); ok {
		for key := range config.RedactedFlowArgs() {
			if value, present := supplied[key]; present {
				args[key] = value
			}
		}
	}
	for k, v := range runtimeArgs {
		args[k] = v
	}
	if m.Prompt != "" {
		args["prompt"] = m.Prompt
	}
	if record.Kind == "generated" {
		limits, _ := config.DynamicFlowLimits(m.Complexity)
		ctx = agentpkg.WithPrimaryFlowPrefix(ctx, app.PrimaryAgents[config.AgentName(m.Agent)], conversation)
		ctx = tools.WithFlowTaskLimit(ctx, limits.MaxParallel)
		ctx = tools.WithFlowTaskTurns(ctx, limits.MaxTurns)
	}
	if err := app.FlowRuns.Transition(ctx, lease, "running"); err != nil {
		return err
	}
	run.mu.Lock()
	run.status = "running"
	run.mu.Unlock()
	if strings.HasPrefix(record.InvocationID, "legacy-") && len(saved.Manifest.Steering) > 0 {
		var legacy []flowrun.Inbox
		for i, text := range saved.Manifest.Steering {
			id := fmt.Sprintf("legacy-%d-%s", i, flowKey(text))
			input := FlowSteeringMessage{ID: id, Text: text, RootMessageID: "flow-input-" + flowKey(record.ID+"\x00"+id)}
			payload, _ := json.Marshal(input)
			legacy = append(legacy, flowrun.Inbox{ID: id, Payload: payload})
		}
		if _, err := app.FlowRuns.QueueInboxBatch(ctx, record.ID, legacy); err != nil {
			return err
		}
	}
	if recover {
		if err := app.restoreFlowQuestions(ctx, run); err != nil {
			return err
		}
	}
	before := func(ctx context.Context, step *flow.Step, stepArgs map[string]any) (context.Context, error) {
		run.mu.Lock()
		defer run.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return ctx, err
		}
		batch, err := app.FlowRuns.PeekInbox(ctx, record.ID)
		if err != nil {
			return ctx, err
		}
		delete(stepArgs, "_flow_inbox_ids")
		if len(batch) > 0 {
			var texts []string
			var ids []string
			for _, in := range batch {
				var msg FlowSteeringMessage
				if err = json.Unmarshal(in.Payload, &msg); err != nil {
					return ctx, err
				}
				if msg.TargetSessionID != "" {
					continue
				}
				texts = append(texts, msg.Text)
				ids = append(ids, in.ID)
			}
			stepArgs["user_steering"] = strings.Join(texts, "\n\n")
			stepArgs["_flow_inbox_ids"] = ids
		}
		if steering, ok := stepArgs["user_steering"].(string); ok && steering != "" {
			step.Prompt += "\n\nUser steering (supplemental task input; do not alter an authored flow):\n${args.user_steering}"
		}
		stepCtx, cancel := context.WithCancel(ctx)
		run.steps[step.ID] = cancel
		return stepCtx, nil
	}
	ctx = tools.WithFlowSubmitter(ctx, func(callCtx context.Context, sid string, req tools.FlowRequest) error {
		if req.Operation != "update" || record.Kind != "generated" {
			return fmt.Errorf("%w: existing YAML runs are immutable and nesting is forbidden", ErrFlowPermission)
		}
		name, _ := callCtx.Value(tools.AgentIDContextKey).(config.AgentName)
		if string(name) != m.Agent {
			return ErrFlowPermission
		}
		rows, err := app.flowQuery.ListFlowStatesByRootSession(callCtx, record.ID)
		if err != nil {
			return err
		}
		entered := map[string]bool{}
		for _, row := range rows {
			entered[row.StepID] = true
		}
		next, err := flow.ParseDynamic(req.Definition, m.Definition.ID, m.Agent, m.Complexity, app.flowSubagents(m.Agent))
		if err != nil {
			return err
		}
		return app.updateStoredDefinition(callCtx, run, func(d *storedFlowDefinition) error {
			if err := flow.ValidateDynamicAmendment(&d.Manifest.Definition, next, entered); err != nil {
				return err
			}
			d.Manifest.Definition = *next
			return nil
		})
	})
	definition := m.Definition
	// Recovery must resume failure while preserving the authored graph and all
	// other settings. This run option is represented only in the execution copy.
	definition.Spec.Session.ResumeOnFailure = true
	jobArgs, _ := tools.JobArgsFromContext(ctx)
	jobArgs = mapsCopy(jobArgs)
	for k, v := range args {
		jobArgs[k] = v
	}
	var runOrigin flowOrigin
	_ = json.Unmarshal(record.Origin, &runOrigin)
	opts := flow.RunOptions{PromptShell: func(shellCtx context.Context, command string) (string, error) {
		return app.runManagedPromptShell(shellCtx, conversation, m.Agent, record.Kind, command)
	}, ConversationInteraction: runOrigin.Peer.PeerID != "", RuntimeJobArgs: jobArgs, Definition: &definition, RootSessionID: record.ID, SessionRootID: conversation, RecoverRunning: recover, Attended: true, BeforeStep: before, Querier: app.FlowRuns.FencedQuerier(lease),
		BeforeResolvedStep: func(ctx context.Context, step flow.Step, id string, args map[string]any) error {
			if step.Interactive && step.Interaction != nil {
				if err := app.validateFlowInteraction(ctx, record, step, args); err != nil {
					return err
				}
			}
			return app.authorizeFlowAgent(ctx, conversation, m.Agent, id, true)
		},
		DefinitionSnapshot: func() *flow.Flow {
			run.mu.Lock()
			defer run.mu.Unlock()
			d := run.manifest.Definition
			d.Spec.Session.ResumeOnFailure = true
			return &d
		},
		ResolveStepPrompt: func(ctx context.Context, step flow.Step) (string, error) {
			if step.LangfusePromptPath == "" {
				return step.Prompt, nil
			}
			data, err := app.FlowRuns.GetPrompt(ctx, record.ID, step.ID)
			if err == nil {
				var p storedFlowPrompt
				if err = json.Unmarshal(data, &p); err != nil {
					return "", err
				}
				return p.Text + step.Prompt, nil
			}
			if !errors.Is(err, flowrun.ErrNotFound) {
				return "", err
			}
			resolved, err := langfuse.GetPrompts().Resolve(ctx, step.LangfusePromptPath, step.LangfusePromptLabel)
			if err != nil {
				return "", err
			}
			data, err = json.Marshal(storedFlowPrompt{Text: resolved.Text, Path: resolved.Path, Version: resolved.Version})
			if err != nil {
				return "", err
			}
			data, err = app.FlowRuns.PutPrompt(ctx, lease, step.ID, data)
			if err != nil {
				return "", err
			}
			var p storedFlowPrompt
			err = json.Unmarshal(data, &p)
			return p.Text + step.Prompt, err
		},
	}
	if record.Kind == "generated" {
		opts.MaxParallel = 1
	}
	prefix := record.ID
	if saved.Manifest.SessionPrefix != "" {
		prefix = saved.Manifest.SessionPrefix
	}
	events, states, err := app.Flows.RunWithOptions(ctx, prefix, definition.ID, args, opts)
	if err != nil {
		return err
	}
	for events != nil || states != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event.Error != nil {
				err = event.Error
			}
		case state, ok := <-states:
			if !ok {
				states = nil
				continue
			}
			if state == nil {
				continue
			}
			if state.Status == flow.FlowStatusRunning {
				if ackErr := app.ackStoredInbox(ctx, run, state.Args); ackErr != nil {
					run.cancel()
					err = ackErr
				}
			}
			run.mu.Lock()
			if state.Status == flow.FlowStatusCompleted {
				run.lastCompletedOutput = state.Output
			}
			if state.Status == flow.FlowStatusCompleted || state.Status == flow.FlowStatusFailed || state.Status == flow.FlowStatusPostponed {
				delete(run.steps, state.StepID)
			}
			run.mu.Unlock()
			if emitErr := app.emitStoredEvent(ctx, run, FlowRunEvent{Type: "session.flow.step", Status: string(state.Status), StepID: state.StepID, SessionID: state.SessionID, Output: state.Output}); emitErr != nil {
				run.cancel()
				err = emitErr
			}
		}
	}
	return err
}
func (app *App) validateFlowInteraction(ctx context.Context, record flowrun.Run, step flow.Step, args map[string]any) error {
	var origin flowOrigin
	if err := json.Unmarshal(record.Origin, &origin); err != nil {
		return err
	}
	if origin.Peer.PeerID == "" {
		if record.Kind == "standalone" {
			return nil
		}
		return fmt.Errorf("%w: named interactive flow requires an authorized bridge conversation", ErrFlowPermission)
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(step.Interaction.Target, "${args."), "}")
	target, ok := args[raw]
	if !ok {
		return fmt.Errorf("%w: missing interaction target", ErrFlowInput)
	}
	data, err := json.Marshal(target)
	if err != nil {
		return err
	}
	var targets []map[string]any
	var one map[string]any
	if json.Unmarshal(data, &one) == nil {
		targets = []map[string]any{one}
	} else if err = json.Unmarshal(data, &targets); err != nil {
		return ErrFlowInput
	}
	if len(targets) == 0 {
		return ErrFlowPermission
	}
	allowed := append([]tools.Peer{origin.Peer}, origin.AuthorizedPeers...)
	for _, target := range targets {
		matched := false
		for _, peer := range allowed {
			if target["channel"] == peer.Channel && target["identity"] == peer.Identity && target["peerId"] == peer.PeerID {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%w: interaction targets must already belong to this conversation", ErrFlowPermission)
		}
	}
	return nil
}

func (app *App) QueueFlowMessages(ctx context.Context, conversation string, messages []FlowSteeringMessage) error {
	record, err := app.CurrentFlow(ctx, conversation)
	if err != nil {
		return err
	}
	if record.Status == "completed" || record.Status == "terminated" {
		return fmt.Errorf("%w: no active flow", ErrFlowInput)
	}
	pending, durable, targetStep, err := app.pendingFlowQuestion(ctx, record)
	if err != nil {
		return err
	}
	var items []flowrun.Inbox
	hasNew := false
	for _, msg := range messages {
		msg.RootMessageID = ""
		msg.NoQuestionRetarget = false
		if visible, hinted := question.SteeringQuestion(ctx); hinted && visible != "" && pending.ID == "" {
			msg.NoQuestionRetarget = true
		}
		msg.TargetSessionID = ""
		msg.TargetStepID = ""
		msg.TargetQuestionID = ""
		if pending.ID != "" {
			msg.TargetSessionID = pending.SessionID
			msg.TargetStepID = targetStep
			msg.TargetQuestionID = durable.ID
		}
		if strings.TrimSpace(msg.Text) == "" {
			return fmt.Errorf("%w: empty steering message", ErrFlowInput)
		}
		if msg.ID == "" {
			msg.ID = uuid.NewString()
		}
		var origin any
		if len(msg.Origin) > 0 {
			if err = json.Unmarshal(msg.Origin, &origin); err != nil {
				return err
			}
			msg.Origin, _ = json.Marshal(origin)
		}
		sum := sha256.Sum256(append(append([]byte(nil), msg.Origin...), []byte("\x00"+msg.ID)...))
		id := hex.EncodeToString(sum[:])
		msg.RootMessageID = "flow-input-" + flowKey(record.ID+"\x00"+id)
		payload, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		existing, lookupErr := app.FlowRuns.GetInbox(ctx, record.ID, id)
		if lookupErr == nil {
			var original FlowSteeringMessage
			if err = json.Unmarshal(existing.Payload, &original); err != nil {
				return err
			}
			if original.ID != msg.ID || original.Text != msg.Text || string(original.Origin) != string(msg.Origin) {
				return flowrun.ErrConflict
			}
			payload = existing.Payload
		} else if errors.Is(lookupErr, flowrun.ErrNotFound) {
			hasNew = true
		} else {
			return lookupErr
		}
		items = append(items, flowrun.Inbox{ID: id, RunID: record.ID, Payload: payload})
	}
	_, err = app.FlowRuns.QueueInboxBatch(ctx, record.ID, items)
	if err != nil {
		return err
	}
	if hasNew && pending.ID == "" && app.Questions != nil {
		visible, hinted := question.SteeringQuestion(ctx)
		if !hinted || visible == "" {
			app.dynamicMu.Lock()
			local := app.dynamicRuns[conversation]
			app.dynamicMu.Unlock()
			if local != nil {
				for _, request := range app.Questions.List() {
					if request.FlowRunID != record.ID {
						continue
					}
					if wakeErr := app.startStoredQuestion(ctx, local, request); wakeErr != nil {
						local.cancel()
						logging.Warn("Flow input accepted but question-start reconciliation failed", "run_id", record.ID, "err", wakeErr)
						break
					}
				}
			}
		}
	}
	if hasNew && pending.ID != "" {
		if lease, ok := app.FlowLease(record.ID); ok {
			closed, e := app.FlowRuns.InterruptQuestion(ctx, lease, durable.ID, "The user sent new directions instead of answering. Read the queued input before continuing.")
			if e != nil {
				// Acceptance already committed. Never tell the caller to send
				// the same text again under a new id. Pause and let recovery
				// consume its durable target if the live wakeup failed.
				app.dynamicMu.Lock()
				local := app.dynamicRuns[conversation]
				app.dynamicMu.Unlock()
				if local != nil && local.cancel != nil {
					local.cancel()
				}
				logging.Warn("Flow input accepted but question wakeup failed; run paused", "run_id", record.ID, "err", e)
				return nil
			}
			if closed.Status == "interrupted" {
				_ = app.Questions.Unanswered(pending.ID, "The user sent new directions instead of answering. Read the queued input before continuing.")
			}
		}
	}
	return nil
}
func (app *App) ackStoredInbox(ctx context.Context, run *dynamicRun, args map[string]any) error {
	data, err := json.Marshal(args["_flow_inbox_ids"])
	if err != nil {
		return err
	}
	var ids []string
	if err = json.Unmarshal(data, &ids); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	run.mu.Lock()
	lease := run.lease
	runID := run.record.ID
	run.mu.Unlock()
	pending, err := app.FlowRuns.PeekInbox(ctx, runID)
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	var messages []FlowSteeringMessage
	for _, item := range pending {
		if selected[item.ID] {
			var msg FlowSteeringMessage
			if err = json.Unmarshal(item.Payload, &msg); err != nil {
				return err
			}
			messages = append(messages, msg)
		}
	}
	if len(messages) > 0 {
		run.mu.Lock()
		record := *run.record
		lease := run.lease
		run.mu.Unlock()
		event := FlowRunEvent{Type: "session.flow.messages.delivered", Status: "running", Messages: messages, RunID: record.ID, ConversationID: record.ConversationID, Kind: record.Kind, Origin: record.Origin, Revision: record.Version, Fence: lease.Token}
		for _, m := range messages {
			event.MessageIDs = append(event.MessageIDs, m.ID)
		}
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		var rootMessages []message.Message
		stored, err := app.FlowRuns.AckInboxEvent(ctx, lease, ids, uuid.NewString(), event.Type, payload, func(q db.QuerierWithTx) error {
			rootMessages = nil
			if agentpkg.SessionBusy(record.ConversationID) {
				return nil
			}
			var writeErr error
			rootMessages, writeErr = app.persistRootFlowInputs(ctx, q, record, messages)
			return writeErr
		})
		if err != nil {
			return err
		}
		for _, msg := range rootMessages {
			message.PublishCreated(app.Messages, msg)
		}
		event.ID = stored.ID
		event.Sequence = stored.Sequence
		app.publishFlowEvent(event)
	} else {
		return app.FlowRuns.AckInbox(ctx, lease, ids)
	}
	return nil
}

func (app *App) ReserveFlowAdmission(ctx context.Context, conversation string) (func(), error) {
	noop := func() {}
	if err := app.SessionFlowAccess(ctx, conversation); err != nil {
		return noop, err
	}
	if app.FlowRuns == nil {
		return noop, nil
	}
	current, err := app.FlowRuns.Current(ctx, conversation)
	if errors.Is(err, flowrun.ErrNotFound) {
		return noop, nil
	}
	if err != nil {
		return noop, err
	}
	var origin flowOrigin
	if err = json.Unmarshal(current.Origin, &origin); err != nil {
		return noop, err
	}
	if peer, provided := tools.PeerFromContext(ctx); provided && !flowOriginAllowsPeer(origin, peer) {
		return noop, ErrFlowPermission
	}
	app.dynamicMu.Lock()
	local := app.dynamicRuns[conversation]
	reserved, has := app.flowReservations[current.ID]
	app.dynamicMu.Unlock()
	if local != nil {
		return noop, nil
	}
	if has {
		if _, err = app.FlowRuns.Renew(ctx, reserved, flowLeaseTTL); err == nil {
			return noop, nil
		}
	}
	lease, err := app.FlowRuns.Claim(ctx, current.ID, "admission-"+uuid.NewString(), flowLeaseTTL)
	if err != nil {
		return noop, err
	}
	app.dynamicMu.Lock()
	if app.flowReservations == nil {
		app.flowReservations = map[string]flowrun.Lease{}
	}
	app.flowReservations[current.ID] = lease
	app.dynamicMu.Unlock()
	return func() {
		app.dynamicMu.Lock()
		stored, ok := app.flowReservations[current.ID]
		if ok && stored.Token == lease.Token {
			delete(app.flowReservations, current.ID)
		}
		app.dynamicMu.Unlock()
		if ok && stored.Token == lease.Token {
			_ = app.FlowRuns.Release(context.Background(), lease, "")
		}
	}, nil
}
func (app *App) resumeStoredFlow(ctx context.Context, conversation string) error {
	record, err := app.CurrentFlow(ctx, conversation)
	if errors.Is(err, flowrun.ErrNotFound) {
		record, err = app.importLegacyFlow(ctx, conversation)
	}
	if err != nil {
		return err
	}
	if record.Status == "completed" || record.Status == "terminated" {
		return fmt.Errorf("%w: run is already %s", ErrFlowInput, record.Status)
	}
	var origin flowOrigin
	if err = json.Unmarshal(record.Origin, &origin); err != nil {
		return err
	}
	if peer, ok := tools.PeerFromContext(ctx); ok && !flowOriginAllowsPeer(origin, peer) {
		return ErrFlowPermission
	}
	var args map[string]any
	if supplied, ok := tools.JobArgsFromContext(ctx); ok {
		args = map[string]any{}
		for key := range config.RedactedFlowArgs() {
			if value, ok := supplied[key]; ok {
				args[key] = value
			}
		}
	}
	return app.startStoredFlow(ctx, record, true, args)
}
func (app *App) storedFlowSnapshot(ctx context.Context, conversation string) (FlowSnapshot, error) {
	record, err := app.CurrentFlow(ctx, conversation)
	if errors.Is(err, flowrun.ErrNotFound) {
		record, err = app.importLegacyFlow(ctx, conversation)
	}
	if err != nil {
		return FlowSnapshot{}, err
	}
	var saved storedFlowDefinition
	if err = json.Unmarshal(record.Snapshot, &saved); err != nil {
		return FlowSnapshot{}, err
	}
	rows, err := app.flowQuery.ListFlowStatesByRootSession(ctx, record.ID)
	if err != nil {
		return FlowSnapshot{}, err
	}
	snapshot := FlowSnapshot{RunID: record.ID, RootSessionID: conversation, FlowID: saved.Manifest.Definition.ID, Status: record.Status, Dynamic: record.Kind == "generated", Running: app.FlowBusy(conversation)}
	byID := map[string]flow.FlowState{}
	for _, row := range rows {
		byID[row.StepID] = flow.FlowState{StepID: row.StepID, SessionID: row.SessionID, Status: flow.FlowStatus(row.Status), Output: row.Output.String, Iteration: int(row.Iteration)}
	}
	for _, step := range saved.Manifest.Definition.Spec.Steps {
		item := FlowStepSnapshot{ID: step.ID, Agent: step.Agent, Prompt: step.Prompt, Status: "pending"}
		if state, ok := byID[step.ID]; ok {
			item.SessionID = state.SessionID
			item.Status = string(state.Status)
			item.Output = state.Output
			item.Iteration = state.Iteration
		}
		if item.Status == "running" && snapshot.Running {
			item.RunningAgents = 1
			if children, e := app.Sessions.ListChildren(ctx, conversation); e == nil {
				item.RunningAgents += countSynchronousFlowAgents(item.SessionID, children, map[string]bool{}, agentpkg.SessionBusy)
			}
			if app.Messages != nil {
				if messages, e := app.Messages.List(ctx, item.SessionID); e == nil {
					var content strings.Builder
					for j := max(0, len(messages)-8); j < len(messages); j++ {
						if messages[j].Role == message.Assistant {
							content.WriteString(messages[j].Content().String())
							content.WriteByte('\n')
						}
					}
					text := content.String()
					if len(text) > 16000 {
						text = text[len(text)-16000:]
					}
					if text != "" {
						item.Output = text
					}
				}
			}
		}
		snapshot.Steps = append(snapshot.Steps, item)
	}
	return snapshot, nil
}
func (app *App) controlStoredFlow(ctx context.Context, conversation, step, action string) error {
	if action == "resume" {
		return app.resumeStoredFlow(ctx, conversation)
	}
	if action != "cancel" && action != "skip" && action != "terminate" {
		return fmt.Errorf("%w: unknown action", ErrFlowInput)
	}
	record, err := app.CurrentFlow(ctx, conversation)
	if err != nil {
		return err
	}
	if record.Status == "completed" || record.Status == "terminated" {
		return fmt.Errorf("%w: run is terminal", ErrFlowInput)
	}
	var saved storedFlowDefinition
	if err = json.Unmarshal(record.Snapshot, &saved); err != nil {
		return err
	}
	if action == "skip" {
		if err = validateFlowSkip(saved.Manifest, step); err != nil {
			return err
		}
	}
	if (action == "cancel" && step != "") || action == "skip" {
		rows, e := app.flowQuery.ListFlowStatesByRootSession(ctx, record.ID)
		if e != nil {
			return e
		}
		found := false
		for _, r := range rows {
			if r.StepID == step {
				found = true
				if r.Status == "completed" {
					return fmt.Errorf("%w: step already completed", ErrFlowInput)
				}
				if action == "cancel" && r.Status != "running" && r.Status != "waiting_for_input" {
					return fmt.Errorf("%w: selected step is not running", ErrFlowInput)
				}
			}
		}
		if !found {
			return fmt.Errorf("%w: selected step has not started", ErrFlowInput)
		}
	}
	app.dynamicMu.Lock()
	run := app.dynamicRuns[conversation]
	app.dynamicMu.Unlock()
	if run != nil {
		run.mu.Lock()
		if action == "terminate" {
			run.stopStatus = "terminated"
		} else {
			run.stopStatus = "suspended"
		}
		cancel := run.cancel
		run.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		app.stopStoredTasks(ctx, run)
		select {
		case <-run.done:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
			return fmt.Errorf("flow still stopping; retry after it settles")
		}
	}
	if run != nil {
		if action == "cancel" {
			return nil
		}
		if action == "terminate" {
			fresh, e := app.FlowRuns.Get(ctx, record.ID)
			if e != nil {
				return e
			}
			if fresh.Status == "terminated" {
				return nil
			}
		}
	}
	lease, err := app.FlowRuns.Claim(ctx, record.ID, "control-"+uuid.NewString(), flowLeaseTTL)
	if err != nil {
		if action == "terminate" && record.Deadline > 0 {
			return app.terminateExpiredStoredFlow(ctx, record)
		}
		return err
	}
	defer app.FlowRuns.Release(context.Background(), lease, "")
	if action == "terminate" {
		return app.FlowRuns.Transition(ctx, lease, "terminated")
	}
	if action == "cancel" {
		return app.FlowRuns.Transition(ctx, lease, "suspended")
	}
	rows, err := app.flowQuery.ListFlowStatesByRootSession(ctx, record.ID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.StepID == step {
			if row.Status == "completed" {
				return fmt.Errorf("%w: step already completed", ErrFlowInput)
			}
			_, err = app.FlowRuns.FencedQuerier(lease).UpdateFlowState(ctx, db.UpdateFlowStateParams{SessionID: row.SessionID, Status: "completed", Args: row.Args, Output: sql.NullString{String: "Skipped by user", Valid: true}, Iteration: row.Iteration})
			if err != nil {
				return err
			}
			if err = app.FlowRuns.Release(ctx, lease, "suspended"); err != nil {
				return err
			}
			return app.resumeStoredFlow(ctx, conversation)
		}
	}
	return fmt.Errorf("%w: selected step has not started", ErrFlowInput)
}
func (app *App) importLegacyFlow(ctx context.Context, conversation string) (flowrun.Run, error) {
	if err := app.SessionFlowAccess(ctx, conversation); err != nil {
		return flowrun.Run{}, err
	}
	dir, err := flow.DynamicDirectory()
	if err != nil {
		return flowrun.Run{}, err
	}
	legacy, loadErr := flow.LoadDynamic(dir, conversation)
	rows, err := app.flowQuery.ListFlowStatesByRootSession(ctx, conversation)
	if err != nil {
		return flowrun.Run{}, err
	}
	if app.FlowRuns.SupportsDistributedRecovery() {
		for _, row := range rows {
			if row.Status == "running" || row.Status == "waiting_for_input" {
				return flowrun.Run{}, fmt.Errorf("%w: legacy MySQL run may still have a remote owner; stop or reconcile it through the original orchestrator before adoption", ErrFlowPermission)
			}
		}
	}
	if loadErr != nil {
		if !errors.Is(loadErr, os.ErrNotExist) {
			return flowrun.Run{}, loadErr
		}
		if len(rows) == 0 {
			return flowrun.Run{}, flowrun.ErrNotFound
		}
		f, err := flow.Get(rows[0].FlowID)
		if err != nil {
			return flowrun.Run{}, err
		}
		suffix := "-" + strings.ReplaceAll(f.ID, "/", "--") + "-" + f.Spec.Steps[0].ID
		if !strings.HasSuffix(conversation, suffix) {
			return flowrun.Run{}, fmt.Errorf("cannot resolve standalone flow prefix")
		}
		agentID := f.Spec.Steps[0].Agent
		if agentID == "" {
			agentID = "coder"
		}
		legacy = flow.DynamicManifest{RootSessionID: conversation, Standalone: true, SessionPrefix: strings.TrimSuffix(conversation, suffix), Definition: *f, Agent: agentID}
	}
	for _, row := range rows {
		s, err := app.Sessions.Get(ctx, row.SessionID)
		if err != nil {
			return flowrun.Run{}, err
		}
		if s.RootSessionID != conversation && s.ID != conversation {
			return flowrun.Run{}, ErrFlowPermission
		}
		if s.ProjectID != app.sessionFlowProjectID() {
			return flowrun.Run{}, ErrFlowPermission
		}
	}

	// Legacy states already use this conversation's ID. Retain it as the imported
	// run ID; all new invocations receive distinct UUIDs instead.
	kind := "generated"
	if legacy.Standalone {
		kind = "standalone"
	}
	status := "suspended"
	if legacy.Terminated {
		status = "terminated"
	} else if len(rows) > 0 && !flow.HasPendingWork(rows, &legacy.Definition) {
		status = "completed"
	}
	legacyArgs := map[string]any{}
	if len(rows) > 0 && rows[0].Args.Valid {
		if err = json.Unmarshal([]byte(rows[0].Args.String), &legacyArgs); err != nil {
			return flowrun.Run{}, err
		}
	}
	if legacy.Prompt != "" {
		legacyArgs["prompt"] = legacy.Prompt
	}
	snapshot, err := json.Marshal(storedFlowDefinition{Manifest: legacy, Args: legacyArgs})
	if err != nil {
		return flowrun.Run{}, err
	}
	returnRun, _, err := app.FlowRuns.Create(ctx, flowrun.Run{ID: conversation, ConversationID: conversation, Kind: kind, Status: status, InvocationID: "legacy-" + conversation, Snapshot: snapshot, Origin: json.RawMessage(`{}`)})
	return returnRun, err
}

// Restore the exact unanswered tool call before a recovered agent sees its
// transcript. A committed answer is replayed into that call once; it is never
// matched to a merely similar question from a later iteration.
func (app *App) restoreFlowQuestions(ctx context.Context, run *dynamicRun) error {
	run.mu.Lock()
	runID := run.record.ID
	lease := run.lease
	run.mu.Unlock()
	questions, err := app.FlowRuns.Questions(ctx, runID)
	if err != nil {
		return err
	}
	for _, saved := range questions {
		var descriptor question.DurableDescriptor
		if err = json.Unmarshal(saved.Descriptor, &descriptor); err != nil {
			return err
		}
		if descriptor.ToolCallID == "" {
			return fmt.Errorf("cannot recover legacy question without tool-call identity")
		}
		existing, err := app.Messages.List(ctx, saved.SessionID)
		if err != nil {
			return err
		}
		answered := false
		priorError := false
		knownCall := false
		for _, m := range existing {
			for _, call := range m.ToolCalls() {
				if call.ID == descriptor.ToolCallID && call.Name == tools.QuestionToolName {
					knownCall = true
				}
			}
			for _, result := range m.ToolResults() {
				if result.ToolCallID == descriptor.ToolCallID {
					answered = true
					priorError = result.IsError
				}
			}
		}
		if answered {
			if saved.Status == "answered" && priorError {
				var accepted question.DurableAnswer
				if err = json.Unmarshal(saved.Answer, &accepted); err != nil {
					return err
				}
				if !accepted.Rejected {
					response := tools.FormatQuestionResponse(descriptor.Questions, accepted.Answers)
					var supplemental message.Message
					err = app.FlowRuns.CommitAnswer(ctx, lease, saved.ID, func(q db.QuerierWithTx) error {
						var writeErr error
						supplemental, writeErr = message.CreateInTransaction(ctx, app.Messages, q, saved.SessionID, message.CreateMessageParams{Role: message.User, Synthetic: true, Parts: []message.ContentPart{message.TextContent{Text: "A durable user answer arrived for interrupted question " + descriptor.ToolCallID + ". The earlier cancellation result does not discard it. " + response.Content}}})
						return writeErr
					})
					if err != nil {
						return err
					}
					message.PublishCreated(app.Messages, supplemental)
					continue
				}
			}
			if saved.Status == "pending" {
				closed, _ := json.Marshal(question.DurableAnswer{Rejected: true})
				if _, _, err = app.FlowRuns.AnswerQuestion(ctx, runID, saved.ID, "closed-"+saved.ID, closed); err != nil {
					return err
				}
			}
			if err = app.FlowRuns.AckAnswer(ctx, lease, saved.ID); err != nil {
				return err
			}
			continue
		}
		if !knownCall {
			return fmt.Errorf("persisted question does not match its requesting tool call")
		}
		var answer question.DurableAnswer
		if saved.Status == "pending" {
			queued, e := app.FlowRuns.PeekInbox(ctx, runID)
			if e != nil {
				return e
			}
			for _, item := range queued {
				var input FlowSteeringMessage
				if e = json.Unmarshal(item.Payload, &input); e != nil {
					return e
				}
				if input.TargetQuestionID == saved.ID {
					saved, e = app.FlowRuns.InterruptQuestion(ctx, lease, saved.ID, "The user sent new directions instead of answering. Read the queued input before continuing.")
					if e != nil {
						return e
					}
					break
				}
			}
		}
		if saved.Status == "pending" {
			if app.Questions == nil {
				return fmt.Errorf("question service unavailable during recovery")
			}
			answers, askErr := app.Questions.Ask(question.WithToolCallID(ctx, descriptor.ToolCallID), saved.SessionID, descriptor.Questions)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			answer = question.DurableAnswer{Answers: answers, Rejected: askErr != nil}
			// A direct TUI answer has no bridge receipt writer. Save it with a stable
			// recovery receipt before creating the matching tool result.
			payload, _ := json.Marshal(answer)
			current, e := app.FlowRuns.GetQuestion(ctx, runID, saved.ID)
			if e != nil {
				return e
			}
			if current.Status == "pending" {
				if _, _, e = app.FlowRuns.AnswerQuestion(ctx, runID, saved.ID, "recovery-"+saved.ID, payload); e != nil {
					return e
				}
			} else if e = json.Unmarshal(current.Answer, &answer); e != nil {
				return e
			}
		} else if err = json.Unmarshal(saved.Answer, &answer); err != nil {
			return err
		}
		response := tools.FormatQuestionResponse(descriptor.Questions, answer.Answers)
		if answer.Unanswered != "" {
			response = tools.NewTextResponse(answer.Unanswered)
		}
		if answer.Rejected {
			response = tools.NewTextErrorResponse("The user dismissed the question")
		}
		var created message.Message
		if err = app.FlowRuns.CommitAnswer(ctx, lease, saved.ID, func(q db.QuerierWithTx) error {
			var writeErr error
			created, writeErr = message.CreateInTransaction(ctx, app.Messages, q, saved.SessionID, message.CreateMessageParams{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: descriptor.ToolCallID, Name: tools.QuestionToolName, Content: response.Content, IsError: response.IsError}}, Synthetic: true})
			return writeErr
		}); err != nil {
			return err
		}
		message.PublishCreated(app.Messages, created)
	}
	pending, err := app.FlowRuns.PeekInbox(ctx, runID)
	if err != nil {
		return err
	}
	targets := map[string]bool{}
	for _, item := range pending {
		var input FlowSteeringMessage
		if err = json.Unmarshal(item.Payload, &input); err != nil {
			return err
		}
		if input.TargetSessionID != "" {
			targets[input.TargetSessionID] = true
		}
	}
	for target := range targets {
		if _, err = app.drainStoredInteraction(ctx, run, target); err != nil {
			return err
		}
	}
	return nil
}

func (app *App) stopStoredTasks(ctx context.Context, run *dynamicRun) {
	run.mu.Lock()
	record := *run.record
	baseline := run.taskBaseline
	sessionBaseline := run.sessionBaseline
	run.mu.Unlock()
	rows, err := app.flowQuery.ListFlowStatesByRootSession(ctx, record.ID)
	if err != nil {
		return
	}
	owned := map[string]bool{}
	for _, row := range rows {
		owned[row.SessionID] = true
	}
	children, err := app.Sessions.ListChildren(ctx, record.ConversationID)
	if err != nil {
		return
	}
	parents := map[string]string{}
	for _, s := range children {
		parents[s.ID] = s.ParentSessionID
	}
	registry := task.GlobalRegistry()
	if registry == nil {
		return
	}
	for _, s := range children {
		descendant := owned[s.ID]
		planningDescendant := s.ID == record.ConversationID
		historical := sessionBaseline[s.ID] && s.ID != record.ConversationID
		for p, depth := s.ParentSessionID, 0; p != "" && depth < len(children); p, depth = parents[p], depth+1 {
			if owned[p] {
				descendant = true
				break
			}
			if p == record.ConversationID {
				planningDescendant = true
				break
			}
			if sessionBaseline[p] {
				historical = true
			}
		}
		for _, work := range registry.PendingForSession(s.ID, nil) {
			if work.Kind == task.KindCron {
				continue
			}
			if !descendant && (!planningDescendant || historical || baseline[work.ID]) {
				continue
			}
			if registry.Kill(work.ID) == nil && work.Proc != nil {
				task.SignalProcessGroup(work.Proc, os.Kill)
			}
		}
	}
}

func (app *App) handleFlowSessionDelete(ctx context.Context, request session.DeleteRequest, mutate func(db.Querier) error) error {
	if app.FlowRuns == nil {
		return mutate(app.flowQuery)
	}
	if request.Tree && request.RootSessionID != "" {
		request.SessionID = request.RootSessionID
	}
	state, err := app.flowQuery.GetFlowState(ctx, request.SessionID)
	if err == nil {
		owner, e := app.FlowRuns.Get(ctx, state.RootSessionID)
		if e == nil && owner.ConversationID != request.SessionID {
			return fmt.Errorf("%w: delete the flow's root conversation instead of its step", ErrFlowPermission)
		}
		if e != nil && !errors.Is(e, flowrun.ErrNotFound) {
			return e
		}
	} else if !errors.Is(err, flowrun.ErrNotFound) {
		return err
	}
	if request.RootSessionID != "" && request.RootSessionID != request.SessionID {
		return mutate(app.flowQuery)
	}
	if err = app.SessionFlowAccess(ctx, request.SessionID); err != nil {
		return err
	}
	app.dynamicMu.Lock()
	run := app.dynamicRuns[request.SessionID]
	app.dynamicMu.Unlock()
	if run != nil {
		run.cancel()
		app.stopStoredTasks(ctx, run)
		select {
		case <-run.done:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
			return fmt.Errorf("flow still stopping; retry deletion")
		}
	}
	records, err := app.FlowRuns.List(ctx, request.SessionID)
	if err != nil {
		return err
	}
	for _, record := range records {
		app.dynamicMu.Lock()
		lease, ok := app.flowReservations[record.ID]
		if ok {
			delete(app.flowReservations, record.ID)
		}
		app.dynamicMu.Unlock()
		if ok {
			_ = app.FlowRuns.Release(ctx, lease, "")
		}
	}
	return app.FlowRuns.DeleteConversation(ctx, request.SessionID, func(q db.QuerierWithTx) error {
		if len(records) > 0 && !request.Tree {
			return q.DeleteSessionTree(ctx, db.DeleteSessionTreeParams{ID: request.SessionID, RootSessionID: sql.NullString{String: request.SessionID, Valid: true}})
		}
		return mutate(q)
	})
}

func (app *App) sessionFlowProjectID() string {
	if app.FlowRuns != nil {
		return app.FlowRuns.ProjectID()
	}
	return app.flowProjectID
}

func flowOriginAllowsPeer(origin flowOrigin, peer tools.Peer) bool {
	if origin.Peer.PeerID == "" {
		return true
	}
	if peer == origin.Peer {
		return true
	}
	if origin.Peer.Channel == "external" || peer.Channel == "external" {
		return false
	}
	for _, allowed := range origin.AuthorizedPeers {
		if peer == allowed {
			return true
		}
	}
	return false
}
func (app *App) terminateExpiredStoredFlow(ctx context.Context, record flowrun.Run) error {
	event := FlowRunEvent{Type: "session.flow.terminated", Status: "terminated", RunID: record.ID, ConversationID: record.ConversationID, Kind: record.Kind, Origin: record.Origin, Revision: record.Version, Output: "Flow terminated because its job deadline expired."}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	stored, err := app.FlowRuns.TerminateExpired(ctx, record.ID, "expired-"+flowKey(record.ID), event.Type, payload)
	if err != nil {
		return err
	}
	event.ID = stored.ID
	event.Sequence = stored.Sequence
	app.publishFlowEvent(event)
	return nil
}

func storedFlowTerminalOutput(rows []db.FlowState, definition flow.Flow, last string) string {
	byID := map[string]db.FlowState{}
	for _, row := range rows {
		byID[row.StepID] = row
	}
	var outputs []string
	for _, step := range definition.Spec.Steps {
		row, ok := byID[step.ID]
		if ok && row.Status == "completed" && len(step.Rules) == 0 && strings.TrimSpace(row.Output.String) != "" {
			outputs = append(outputs, "Step "+step.ID+":\n"+row.Output.String)
		}
	}
	if len(outputs) > 0 {
		return strings.Join(outputs, "\n\n")
	}
	if strings.TrimSpace(last) != "" {
		return last
	}
	for i := len(definition.Spec.Steps) - 1; i >= 0; i-- {
		if row, ok := byID[definition.Spec.Steps[i].ID]; ok && row.Status == "completed" && strings.TrimSpace(row.Output.String) != "" {
			return "Step " + row.StepID + ":\n" + row.Output.String
		}
	}
	return "Flow completed."
}

func (app *App) pendingFlowQuestion(ctx context.Context, record flowrun.Run) (question.Request, flowrun.Question, string, error) {
	var empty question.Request
	if app.Questions == nil {
		return empty, flowrun.Question{}, "", nil
	}
	lease, owned := app.FlowLease(record.ID)
	if !owned {
		return empty, flowrun.Question{}, "", nil
	}
	requests := app.Questions.List()
	if visible, hinted := question.SteeringQuestion(ctx); hinted {
		selected := requests[:0]
		for _, request := range requests {
			if request.ID == visible {
				selected = append(selected, request)
				break
			}
		}
		requests = selected
	}
	sort.Slice(requests, func(i, j int) bool {
		if requests[i].SessionID == requests[j].SessionID {
			return requests[i].ID < requests[j].ID
		}
		return requests[i].SessionID < requests[j].SessionID
	})
	for _, request := range requests {
		if request.SessionID == record.ConversationID && request.FlowRunID != record.ID {
			continue
		}
		step := ""
		if request.SessionID != record.ConversationID {
			current := request.SessionID
			matched := false
			for depth := 0; depth < 64; depth++ {
				state, err := app.flowQuery.GetFlowState(ctx, current)
				if err == nil {
					if state.RootSessionID == record.ID {
						step = state.StepID
						matched = true
					}
					break
				}
				if !errors.Is(err, flowrun.ErrNotFound) {
					return empty, flowrun.Question{}, "", err
				}
				session, err := app.Sessions.Get(ctx, current)
				if err != nil {
					return empty, flowrun.Question{}, "", err
				}
				if session.ParentSessionID == "" {
					break
				}
				current = session.ParentSessionID
			}
			if !matched {
				continue
			}
		}
		durable, err := app.FlowRuns.FindQuestionByRequest(ctx, request.ID)
		if err == nil {
			if durable.Status != "pending" {
				return empty, flowrun.Question{}, "", nil
			}
			return request, durable, step, nil
		}
		if !errors.Is(err, flowrun.ErrNotFound) {
			return empty, flowrun.Question{}, "", err
		}
		descriptor, err := json.Marshal(question.DurableDescriptor{Questions: request.Questions, ToolCallID: request.ToolCallID})
		if err != nil {
			return empty, flowrun.Question{}, "", err
		}
		id := request.ID
		if request.ToolCallID != "" {
			sum := sha256.Sum256([]byte(record.ID + "\x00" + request.SessionID + "\x00" + request.ToolCallID))
			id = hex.EncodeToString(sum[:])
		}
		durable, err = app.FlowRuns.PutQuestion(ctx, lease, flowrun.Question{ID: id, RunID: record.ID, SessionID: request.SessionID, RequestID: request.ID, Descriptor: descriptor})
		return request, durable, step, err
	}
	return empty, flowrun.Question{}, "", nil
}
func (app *App) drainStoredInteraction(ctx context.Context, run *dynamicRun, sessionID string) ([]message.Message, error) {
	run.mu.Lock()
	record := *run.record
	lease := run.lease
	run.mu.Unlock()
	pending, err := app.FlowRuns.PeekInbox(ctx, record.ID)
	if err != nil {
		return nil, err
	}
	var ids []string
	var inputs []FlowSteeringMessage
	for _, item := range pending {
		var input FlowSteeringMessage
		if err = json.Unmarshal(item.Payload, &input); err != nil {
			return nil, err
		}
		if input.TargetSessionID == sessionID {
			ids = append(ids, item.ID)
			inputs = append(inputs, input)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	event := FlowRunEvent{Type: "session.flow.messages.delivered", Status: "running", RunID: record.ID, ConversationID: record.ConversationID, Kind: record.Kind, Origin: record.Origin, Revision: record.Version, Fence: lease.Token, SessionID: sessionID, Messages: inputs}
	for _, input := range inputs {
		event.MessageIDs = append(event.MessageIDs, input.ID)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	// Deterministic event identity makes replay after a committed injection a no-op.
	sum := sha256.Sum256([]byte(record.ID + "\x00" + sessionID + "\x00" + strings.Join(ids, "\x00")))
	var created []message.Message
	var published []message.Message
	stored, err := app.FlowRuns.AckInboxEvent(ctx, lease, ids, "interaction-"+hex.EncodeToString(sum[:]), event.Type, payload, func(q db.QuerierWithTx) error {
		created = nil
		published = nil
		if sessionID == record.ConversationID || !agentpkg.SessionBusy(record.ConversationID) {
			roots, e := app.persistRootFlowInputs(ctx, q, record, inputs)
			if e != nil {
				return e
			}
			published = append(published, roots...)
		}
		for _, input := range inputs {
			var msg message.Message
			var err error
			if sessionID == record.ConversationID {
				msg, err = message.GetInTransaction(ctx, app.Messages, q, rootFlowMessageID(record.ID, input))
			} else {
				msg, err = message.CreateInTransaction(ctx, app.Messages, q, sessionID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: input.Text}}})
				if err == nil {
					published = append(published, msg)
				}
			}
			if err != nil {
				return err
			}
			created = append(created, msg)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	event.ID = stored.ID
	event.Sequence = stored.Sequence
	for _, msg := range published {
		message.PublishCreated(app.Messages, msg)
	}
	app.publishFlowEvent(event)
	return created, nil
}

func rootFlowMessageID(runID string, input FlowSteeringMessage) string {
	if input.RootMessageID != "" {
		return input.RootMessageID
	}
	return "flow-input-" + flowKey(runID+"\x00"+input.ID+"\x00"+string(input.Origin))
}
func (app *App) persistRootFlowInputs(ctx context.Context, q db.QuerierWithTx, record flowrun.Run, inputs []FlowSteeringMessage) ([]message.Message, error) {
	var created []message.Message
	if app.Messages == nil {
		return created, nil
	}
	for _, input := range inputs {
		id := rootFlowMessageID(record.ID, input)
		previous, err := message.GetInTransaction(ctx, app.Messages, q, id)
		if err == nil {
			if previous.SessionID != record.ConversationID || previous.Role != message.User || previous.Content().String() != input.Text {
				return nil, flowrun.ErrConflict
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		msg, err := message.CreateInTransactionWithID(ctx, app.Messages, q, id, record.ConversationID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: input.Text}}})
		if err != nil {
			return nil, err
		}
		created = append(created, msg)
	}
	return created, nil
}

func (app *App) admitSessionTurn(ctx context.Context, sessionID string) error {
	if app.FlowRuns == nil {
		return nil
	}
	s, err := app.Sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if s.ParentSessionID != "" || s.RootSessionID != "" && s.RootSessionID != s.ID {
		return tools.CheckFlowOwnership(ctx)
	}
	record, err := app.FlowRuns.Current(ctx, sessionID)
	if errors.Is(err, flowrun.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch record.Status {
	case "planning", "ready", "running", "waiting_for_input", "interrupted":
		if tools.HasFlowOwnershipGuard(ctx) {
			return tools.CheckFlowOwnership(ctx)
		}
		return fmt.Errorf("%w: the conversation is owned by flow %s", agentpkg.ErrSessionBusy, record.ID)
	default:
		return nil
	}
}

// Persist question identity before publishing a widget, then atomically consume
// input that arrived before Ask. A post-enqueue reconciliation calls the same
// hook to cover the opposite race; the backend run lock selects one claimant.
func (app *App) startStoredQuestion(ctx context.Context, run *dynamicRun, request question.Request) error {
	run.mu.Lock()
	record := *run.record
	lease := run.lease
	run.mu.Unlock()
	if request.FlowRunID != record.ID || request.ToolCallID == "" {
		return nil
	}
	if request.SessionID != record.ConversationID {
		owner, ok, err := app.SessionFlowConversation(ctx, request.SessionID)
		if err != nil {
			return err
		}
		if !ok || owner != record.ConversationID {
			return ErrFlowPermission
		}
	}
	sum := sha256.Sum256([]byte(record.ID + "\x00" + request.SessionID + "\x00" + request.ToolCallID))
	descriptor, err := json.Marshal(question.DurableDescriptor{Questions: request.Questions, ToolCallID: request.ToolCallID})
	if err != nil {
		return err
	}
	saved, err := app.FlowRuns.PutQuestion(ctx, lease, flowrun.Question{ID: hex.EncodeToString(sum[:]), SessionID: request.SessionID, RequestID: request.ID, Descriptor: descriptor})
	if err != nil {
		return err
	}
	saved, _, err = app.FlowRuns.TargetInbox(ctx, lease, saved.ID, "The user sent new directions instead of answering. Read the queued input before continuing.")
	if err != nil {
		return err
	}
	if saved.Status == "pending" {
		return nil
	}
	var answer question.DurableAnswer
	if err = json.Unmarshal(saved.Answer, &answer); err != nil {
		return err
	}
	if answer.Unanswered != "" {
		_ = app.Questions.Unanswered(request.ID, answer.Unanswered)
	} else if answer.Rejected {
		_ = app.Questions.Reject(request.ID)
	} else {
		_ = app.Questions.Reply(request.ID, answer.Answers)
	}
	return nil
}
