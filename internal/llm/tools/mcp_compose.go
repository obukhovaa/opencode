package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/permission"
)

type mcpComposeTool struct {
	activeBudget time.Duration
	readTools    []string
}

type composeLimitKey struct{}

type composeHookContextKey struct{}

// WithCompositionHookContextObserver retains trusted hook context even when
// cancellation abandons a child before its dispatch outcome is returned.
func WithCompositionHookContextObserver(ctx context.Context, observe func(string)) context.Context {
	return context.WithValue(ctx, composeHookContextKey{}, observe)
}

func PublishCompositionHookContext(ctx context.Context, value string) {
	if observe, ok := ctx.Value(composeHookContextKey{}).(func(string)); ok {
		observe(value)
	}
}

// WithCompositionLimit applies a smaller agent watchdog bound to active work.
func WithCompositionLimit(ctx context.Context, limit time.Duration) context.Context {
	return context.WithValue(ctx, composeLimitKey{}, limit)
}

func NewMCPComposeTool(readTools ...string) BaseTool {
	names := slices.Clone(readTools)
	slices.Sort(names)
	return &mcpComposeTool{activeBudget: 120 * time.Second, readTools: slices.Compact(names)}
}
func (*mcpComposeTool) IsBaseline() bool                           { return true }
func (*mcpComposeTool) AllowParallelism(ToolCall, []ToolCall) bool { return false }
func (*mcpComposeTool) ValidateInput(input string) error {
	_, err := parseComposePlan(input)
	return err
}
func (t *mcpComposeTool) Info() ToolInfo {
	pointer := map[string]any{"type": "string", "maxLength": 1024, "description": "RFC6901 JSON pointer; empty selects the root."}
	fields := map[string]any{"type": "object", "minProperties": 1, "maxProperties": 32, "additionalProperties": pointer, "propertyNames": map[string]any{"minLength": 1, "maxLength": 64}}
	id := map[string]any{"type": "string", "pattern": `^[A-Za-z][A-Za-z0-9_-]{0,63}$`}
	selection := map[string]any{"oneOf": []any{
		map[string]any{"type": "object", "properties": map[string]any{"fields": fields}, "required": []string{"fields"}, "additionalProperties": false},
		map[string]any{"type": "object", "properties": map[string]any{"rows": pointer, "fields": fields, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "required": []string{"rows", "fields", "limit"}, "additionalProperties": false},
	}}
	childTool := map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": "Only configured candidate tools are composable; ordinary tool access alone is insufficient. Each child must also be granted and available. Deferred reads are resolved internally. Use other tools directly."}
	if len(t.readTools) > 0 {
		childTool["enum"] = slices.Clone(t.readTools)
	}
	return ToolInfo{Name: MCPComposeToolName, Description: "Run 1–8 authorized MCP reads sequentially, returning only selected JSON facts. Use only configured candidate tools listed in steps.tool. Select paths observed in this MCP tool's actual JSON response, not assumed upstream REST fields. Every field is required: one missing path fails the step. If the response shape is unknown or projection fails, inspect the ordinary tool response before composing again; do not repeatedly retry the same child arguments with guessed selectors. For arrays, use rows with a small limit and row-relative fields (e.g. rows: empty string, fields: {path: /new_path}); do not select a whole file array, diff or large description with a root pointer. The 16 KiB projected-value budget is cumulative across ALL steps, including intermediate results. Bindings read earlier projected results and replace existing argument locations. Only complete text-only JSON outputs are supported. Limits: 32 KiB plan, depth 64, 256 KiB per response, 1 MiB total response text, 16 KiB projected values and 120 seconds active execution. Row limits explicitly mark results incomplete. Configured reads are resolved internally without loading their schemas or changing discovery state. No pagination occurs.", Required: []string{"steps", "return"}, Parameters: map[string]any{
		"steps": map[string]any{"type": "array", "minItems": 1, "maxItems": 8, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "tool", "arguments", "select"}, "properties": map[string]any{
			"id": id, "tool": childTool, "arguments": map[string]any{"type": "object"}, "select": selection,
			"bindings": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"target", "step", "pointer"}, "properties": map[string]any{"target": pointer, "step": id, "pointer": pointer}}},
		}}},
		"return": map[string]any{"type": "array", "minItems": 1, "maxItems": 8, "uniqueItems": true, "items": id},
	}}
}

type composeStatus struct {
	ID           string `json:"id"`
	Tool         string `json:"tool"`
	Status       string `json:"status"`
	TotalRows    *int   `json:"totalRows,omitempty"`
	ReturnedRows *int   `json:"returnedRows,omitempty"`
	Limited      *bool  `json:"limited,omitempty"`
}
type composeError struct {
	Code      string                    `json:"code"`
	Step      string                    `json:"step"`
	Message   string                    `json:"message"`
	Selection []composeSelectionFailure `json:"selection,omitempty"`
	Limit     *composeLimitFailure      `json:"limit,omitempty"`
}

// Limit diagnostics expose byte counts only, never unprojected response data.
type composeLimitFailure struct {
	Scope       string `json:"scope"`
	MaxBytes    int    `json:"maxBytes"`
	ActualBytes int    `json:"actualBytes"`
}

// Diagnostics contain only selectors supplied by the caller and static reasons.
// Never include response keys or values, including after hook redaction.
type composeSelectionFailure struct {
	Source  string `json:"source"`
	Field   string `json:"field,omitempty"`
	Pointer string `json:"pointer"`
	Row     *int   `json:"row,omitempty"`
	Reason  string `json:"reason"`
}

type composeProjectionError struct{ failures []composeSelectionFailure }

func (*composeProjectionError) Error() string { return "composition selection failed" }

type composeEnvelope struct {
	Complete bool            `json:"complete"`
	Results  map[string]any  `json:"results"`
	Steps    []composeStatus `json:"steps"`
	Error    *composeError   `json:"error,omitempty"`
}

var composeMessages = map[string]string{
	"invalid_input":      "The composition plan is invalid or exceeds an input limit.",
	"unavailable":        "The composition runtime is unavailable.",
	"ineligible_tool":    "Use only configured candidate tools in steps.tool; each must also be granted and available. Call other tools directly.",
	"binding_failed":     "An earlier projected value could not be bound.",
	"child_failed":       "A child call failed.",
	"rule_denied":        "A child call was denied by a permission rule.",
	"hook_blocked":       "A child call was blocked by a hook.",
	"unsupported_output": "A child response is not complete text-only JSON.",
	"projection_failed":  "Requested selectors could not be resolved. Inspect error.selection and the ordinary tool response; use observed MCP paths, not assumed REST fields. Missing is not null.",
	"output_limit":       "A byte limit was exceeded; see error.limit. Project fewer fields and bounded rows instead of whole responses or large descriptions. Split the plan if needed; do not retry it unchanged.",
	"cancelled":          "Composition was cancelled.",
	"active_timeout":     "The active execution budget was exceeded.",
	"permission_denied":  "A child permission request was rejected.",
	"loop_detected":      "Repeated child calls exceeded the run limit.",
	"ownership_lost":     "The run no longer owns its execution lease.",
}

func (t *mcpComposeTool) Run(parent context.Context, call ToolCall) (ToolResponse, error) {
	envelope := composeEnvelope{Complete: true, Results: map[string]any{}, Steps: []composeStatus{}}
	accounting := &CompositionOutcome{}
	var selectionFailures []composeSelectionFailure
	var limitFailure *composeLimitFailure
	appendHookContext := func(value string) {
		if value == "" {
			return
		}
		if accounting.HookContext != "" {
			accounting.HookContext += "\n"
		}
		accounting.HookContext += value
	}
	finish := func(index int, code string, control error) (ToolResponse, error) {
		if code != "" {
			message, ok := composeMessages[code]
			if !ok {
				code = "child_failed"
				message = composeMessages[code]
			}
			step := ""
			if index >= 0 && index < len(envelope.Steps) {
				envelope.Steps[index].Status = "failed"
				step = envelope.Steps[index].ID
			}
			envelope.Complete = false
			envelope.Error = &composeError{Code: code, Step: step, Message: message, Selection: selectionFailures, Limit: limitFailure}
		}
		data, _ := json.Marshal(envelope)
		return ToolResponse{Type: ToolResponseTypeText, Content: string(data), IsError: envelope.Error != nil, Composition: accounting}, control
	}
	plan, err := parseComposePlan(call.Input)
	if err != nil {
		return finish(-1, "invalid_input", nil)
	}
	for _, step := range plan.Steps {
		envelope.Steps = append(envelope.Steps, composeStatus{ID: step.ID, Tool: step.Tool, Status: "skipped"})
	}
	if err := parent.Err(); err != nil {
		return finish(0, "cancelled", err)
	}
	runtime, ok := CompositionRuntimeFromContext(parent)
	if !ok {
		return finish(0, "unavailable", nil)
	}
	duration := t.activeBudget
	if limit, ok := parent.Value(composeLimitKey{}).(time.Duration); ok && limit > 0 && limit < duration {
		duration = limit
	}
	ctx, budget := newComposeBudget(parent, duration)
	defer budget.close()
	stopped := func(index int) (ToolResponse, error) {
		if err := parent.Err(); err != nil {
			return finish(index, "cancelled", err)
		}
		return finish(index, "active_timeout", nil)
	}
	for i, step := range plan.Steps {
		if ctx.Err() != nil {
			return stopped(i)
		}
		if err := runtime.Check(ctx, step.Tool); err != nil {
			if ctx.Err() != nil {
				return stopped(i)
			}
			return finish(i, "ineligible_tool", nil)
		}
	}
	projections := map[string]any{}
	requested := map[string]bool{}
	for _, id := range plan.Return {
		requested[id] = true
	}
	totalText, totalProjected := 0, 0
	for i, step := range plan.Steps {
		if ctx.Err() != nil {
			return stopped(i)
		}
		if err := runtime.Check(ctx, step.Tool); err != nil {
			if ctx.Err() != nil {
				return stopped(i)
			}
			return finish(i, "ineligible_tool", nil)
		}
		for _, binding := range step.Bindings {
			source, _ := composePointer(binding.Pointer)
			value, err := composeLookup(projections[binding.Step], source)
			if err != nil {
				return finish(i, "binding_failed", nil)
			}
			target, _ := composePointer(binding.Target)
			if err := composeBind(step.Arguments, target, value); err != nil {
				return finish(i, "binding_failed", nil)
			}
		}
		input, err := json.Marshal(step.Arguments)
		if err != nil {
			return finish(i, "binding_failed", nil)
		}
		digest := sha256.Sum256([]byte(call.ID + "\x00" + step.ID))
		child := ToolCall{ID: "compose_" + hex.EncodeToString(digest[:]), Name: step.Tool, Input: string(input)}
		outcomes := make(chan CompositionChildOutcome, 1)
		childCtx := context.WithValue(ctx, permission.HookAllowKey, false)
		var hookMu sync.Mutex
		hookContext := ""
		childCtx = WithCompositionHookContextObserver(childCtx, func(value string) {
			hookMu.Lock()
			defer hookMu.Unlock()
			hookContext = value
		})
		go func() { outcomes <- runtime.Dispatch(childCtx, child) }()
		var outcome CompositionChildOutcome
		select {
		case outcome = <-outcomes:
		case <-ctx.Done():
			hookMu.Lock()
			appendHookContext(hookContext)
			hookMu.Unlock()
			return stopped(i)
		}
		if outcome.Executed && !outcome.RuleDenied {
			accounting.Progressed++
		}
		if outcome.RuleDenied {
			accounting.RuleDenials++
		}
		appendHookContext(outcome.HookContext)
		if ctx.Err() != nil {
			return stopped(i)
		}
		if outcome.ControlErr != nil {
			code := outcome.FailureCode
			if code == "" {
				code = "cancelled"
				if errors.Is(outcome.ControlErr, permission.ErrorPermissionDenied) {
					code = "permission_denied"
				}
			}
			return finish(i, code, outcome.ControlErr)
		}
		if outcome.PostBlocked {
			return finish(i, "hook_blocked", nil)
		}
		if outcome.RuleDenied {
			return finish(i, "rule_denied", nil)
		}
		if outcome.FailureCode != "" {
			return finish(i, outcome.FailureCode, nil)
		}
		response := outcome.Response
		if response.IsError || !outcome.Executed {
			return finish(i, "child_failed", nil)
		}
		if response.MCPOutput == nil || !response.MCPOutput.TextOnly || !response.MCPOutput.Complete || response.Type != ToolResponseTypeText {
			return finish(i, "unsupported_output", nil)
		}
		if len(response.Content) > composeMaxChild {
			limitFailure = &composeLimitFailure{Scope: "child_response", MaxBytes: composeMaxChild, ActualBytes: len(response.Content)}
			return finish(i, "output_limit", nil)
		}
		if totalText+len(response.Content) > composeMaxTotal {
			limitFailure = &composeLimitFailure{Scope: "total_response", MaxBytes: composeMaxTotal, ActualBytes: totalText + len(response.Content)}
			return finish(i, "output_limit", nil)
		}
		totalText += len(response.Content)
		value, err := composeJSON(response.Content)
		if err != nil {
			return finish(i, "unsupported_output", nil)
		}
		projected, err := composeProject(value, step.Select, &envelope.Steps[i])
		if err != nil {
			var projection *composeProjectionError
			if errors.As(err, &projection) {
				selectionFailures = projection.failures
			}
			return finish(i, "projection_failed", nil)
		}
		// Only the projection remains reachable after this iteration.
		value = nil
		response = ToolResponse{}
		outcome.Response = ToolResponse{}
		data, err := json.Marshal(projected)
		if err != nil {
			return finish(i, "projection_failed", nil)
		}
		if totalProjected+len(data) > composeMaxProjected {
			limitFailure = &composeLimitFailure{Scope: "projected_values", MaxBytes: composeMaxProjected, ActualBytes: totalProjected + len(data)}
			return finish(i, "output_limit", nil)
		}
		if ctx.Err() != nil {
			return stopped(i)
		}
		totalProjected += len(data)
		projections[step.ID] = projected
		if requested[step.ID] {
			envelope.Results[step.ID] = projected
		}
		envelope.Steps[i].Status = "ok"
		if envelope.Steps[i].Limited != nil && *envelope.Steps[i].Limited {
			envelope.Complete = false
		}
	}
	return finish(-1, "", nil)
}

func composeProject(value any, selection composeSelect, status *composeStatus) (any, error) {
	names := make([]string, 0, len(selection.Fields))
	for name := range selection.Fields {
		names = append(names, name)
	}
	slices.Sort(names)
	fields := func(value any, row *int) (map[string]any, error) {
		projected := map[string]any{}
		var failures []composeSelectionFailure
		for _, name := range names {
			pointer := selection.Fields[name]
			path, _ := composePointer(pointer)
			selected, err := composeLookup(value, path)
			if err != nil {
				failures = append(failures, composeSelectionFailure{Source: "fields", Field: name, Pointer: pointer, Row: row, Reason: "path_not_found"})
				continue
			}
			projected[name] = selected
		}
		if len(failures) > 0 {
			return nil, &composeProjectionError{failures: failures}
		}
		return projected, nil
	}
	if selection.Rows == nil {
		return fields(value, nil)
	}
	path, _ := composePointer(*selection.Rows)
	raw, err := composeLookup(value, path)
	if err != nil {
		return nil, &composeProjectionError{failures: []composeSelectionFailure{{Source: "rows", Pointer: *selection.Rows, Reason: "path_not_found"}}}
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, &composeProjectionError{failures: []composeSelectionFailure{{Source: "rows", Pointer: *selection.Rows, Reason: "expected_array"}}}
	}
	total := len(rows)
	count := min(total, *selection.Limit)
	limited := count < total
	status.TotalRows = &total
	status.ReturnedRows = &count
	status.Limited = &limited
	projected := make([]any, 0, count)
	for i, row := range rows[:count] {
		selected, err := fields(row, &i)
		if err != nil {
			return nil, err
		}
		projected = append(projected, selected)
	}
	return projected, nil
}

// Each enclosing watchdog observes the same permission waits. The mutex also
// prevents a timer firing concurrently with a pause from charging waiting time.
type composeBudget struct {
	mu        sync.Mutex
	remaining time.Duration
	started   time.Time
	waiting   int
	closed    bool
	timer     *time.Timer
	cancel    context.CancelFunc
}

func newComposeBudget(parent context.Context, duration time.Duration) (context.Context, *composeBudget) {
	ctx, cancel := context.WithCancel(parent)
	budget := &composeBudget{remaining: duration, started: time.Now(), cancel: cancel}
	budget.timer = time.AfterFunc(duration, budget.expire)
	return permission.WithWaitObserver(ctx, budget), budget
}
func (b *composeBudget) expire() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed && b.waiting == 0 {
		remaining := b.remaining - time.Since(b.started)
		if remaining <= 0 {
			b.cancel()
		} else {
			b.timer.Reset(remaining)
		}
	}
}
func (b *composeBudget) WaitStarted() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if b.waiting == 0 {
		b.timer.Stop()
		b.remaining -= time.Since(b.started)
		if b.remaining <= 0 {
			b.cancel()
		}
	}
	b.waiting++
}
func (b *composeBudget) WaitEnded() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.waiting == 0 {
		return
	}
	b.waiting--
	if b.waiting == 0 {
		b.started = time.Now()
		b.timer.Reset(b.remaining)
	}
}
func (b *composeBudget) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.timer.Stop()
	b.cancel()
}
