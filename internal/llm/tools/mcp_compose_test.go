package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/permission"
)

const composeTestPlan = `{"steps":[{"id":"a","tool":"catalog_get","arguments":{},"select":{"fields":{"id":"/id"}}}],"return":["a"]}`

func composeTestInput(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	value, err := composeJSON(composeTestPlan)
	if err != nil {
		t.Fatal(err)
	}
	object := value.(map[string]any)
	edit(object)
	data, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func composeFirst(plan map[string]any) map[string]any {
	return plan["steps"].([]any)[0].(map[string]any)
}
func composeGood(text string) CompositionChildOutcome {
	return CompositionChildOutcome{Executed: true, Response: ToolResponse{Type: ToolResponseTypeText, Content: text, MCPOutput: &MCPOutputMetadata{TextOnly: true, Complete: true}}}
}
func composeRun(t *testing.T, input string, dispatch func(context.Context, ToolCall) CompositionChildOutcome) (ToolResponse, composeEnvelope, error) {
	t.Helper()
	ctx := WithCompositionRuntime(context.Background(), CompositionRuntime{Check: func(context.Context, string) error { return nil }, Dispatch: dispatch})
	response, err := NewMCPComposeTool().Run(ctx, ToolCall{ID: "outer", Name: MCPComposeToolName, Input: input})
	var envelope composeEnvelope
	if json.Unmarshal([]byte(response.Content), &envelope) != nil {
		t.Fatalf("invalid envelope: %s", response.Content)
	}
	return response, envelope, err
}

func TestMCPComposePlanRejectsBeforeDispatch(t *testing.T) {
	cases := map[string]string{
		"trailing value":        composeTestPlan + `{}`,
		"duplicate key":         strings.Replace(composeTestPlan, `"arguments":{}`, `"arguments":{"id":1,"id":2}`, 1),
		"duplicate escaped key": strings.Replace(composeTestPlan, `"arguments":{}`, `"arguments":{"id":1,"\u0069d":2}`, 1),
		"invalid utf8":          composeTestPlan + string([]byte{0xff}),
		"oversize":              composeTestPlan + strings.Repeat(" ", composeMaxPlan),
		"case folded root":      strings.Replace(composeTestPlan, `"return":`, `"Return":`, 1),
		"case folded step":      strings.Replace(composeTestPlan, `"id":`, `"ID":`, 1),
		"case folded selection": strings.Replace(composeTestPlan, `"fields":`, `"Fields":`, 1),
		"unknown root":          strings.Replace(composeTestPlan, `"return":`, `"budget":100,"return":`, 1),
		"unknown step":          strings.Replace(composeTestPlan, `"tool":`, `"extra":1,"tool":`, 1),
		"unknown selection":     strings.Replace(composeTestPlan, `"fields":`, `"extra":1,"fields":`, 1),
		"bad pointer escape":    strings.Replace(composeTestPlan, `"/id"`, `"/~2"`, 1),
		"bad pointer prefix":    strings.Replace(composeTestPlan, `"/id"`, `"id"`, 1),
		"null field pointer":    strings.Replace(composeTestPlan, `"/id"`, `null`, 1),
	}
	edits := map[string]func(map[string]any){
		"zero steps": func(p map[string]any) { p["steps"] = []any{} },
		"nine steps": func(p map[string]any) {
			steps := []any{}
			for i := 0; i < 9; i++ {
				steps = append(steps, map[string]any{"id": fmt.Sprintf("s%d", i), "tool": "get", "arguments": map[string]any{}, "select": map[string]any{"fields": map[string]any{"id": "/id"}}})
			}
			p["steps"] = steps
			p["return"] = []string{"s0"}
		},
		"duplicate id":     func(p map[string]any) { p["steps"] = append(p["steps"].([]any), composeFirst(p)) },
		"invalid id":       func(p map[string]any) { composeFirst(p)["id"] = "1a" },
		"id length":        func(p map[string]any) { composeFirst(p)["id"] = strings.Repeat("a", 65) },
		"empty tool":       func(p map[string]any) { composeFirst(p)["tool"] = "" },
		"tool byte length": func(p map[string]any) { composeFirst(p)["tool"] = strings.Repeat("é", 129) },
		"null arguments":   func(p map[string]any) { composeFirst(p)["arguments"] = nil },
		"array arguments":  func(p map[string]any) { composeFirst(p)["arguments"] = []any{} },
		"missing select":   func(p map[string]any) { delete(composeFirst(p), "select") },
		"empty fields":     func(p map[string]any) { composeFirst(p)["select"] = map[string]any{"fields": map[string]any{}} },
		"field byte length": func(p map[string]any) {
			composeFirst(p)["select"] = map[string]any{"fields": map[string]any{strings.Repeat("é", 33): "/id"}}
		},
		"33 fields": func(p map[string]any) {
			f := map[string]any{}
			for i := 0; i < 33; i++ {
				f[fmt.Sprint(i)] = ""
			}
			composeFirst(p)["select"] = map[string]any{"fields": f}
		},
		"pointer length": func(p map[string]any) {
			composeFirst(p)["select"] = map[string]any{"fields": map[string]any{"id": "/" + strings.Repeat("a", 1024)}}
		},
		"rows without limit": func(p map[string]any) { composeFirst(p)["select"].(map[string]any)["rows"] = "" },
		"limit without rows": func(p map[string]any) { composeFirst(p)["select"].(map[string]any)["limit"] = 1 },
		"null rows": func(p map[string]any) {
			s := composeFirst(p)["select"].(map[string]any)
			s["rows"] = nil
			s["limit"] = 1
		},
		"null limit": func(p map[string]any) {
			s := composeFirst(p)["select"].(map[string]any)
			s["rows"] = ""
			s["limit"] = nil
		},
		"zero limit": func(p map[string]any) {
			s := composeFirst(p)["select"].(map[string]any)
			s["rows"] = ""
			s["limit"] = 0
		},
		"101 limit": func(p map[string]any) {
			s := composeFirst(p)["select"].(map[string]any)
			s["rows"] = ""
			s["limit"] = 101
		},
		"fraction limit": func(p map[string]any) {
			s := composeFirst(p)["select"].(map[string]any)
			s["rows"] = ""
			s["limit"] = 1.5
		},
		"empty return":     func(p map[string]any) { p["return"] = []string{} },
		"duplicate return": func(p map[string]any) { p["return"] = []string{"a", "a"} },
		"unknown return":   func(p map[string]any) { p["return"] = []string{"missing"} },
		"null bindings":    func(p map[string]any) { composeFirst(p)["bindings"] = nil },
		"future binding": func(p map[string]any) {
			composeFirst(p)["arguments"] = map[string]any{"id": nil}
			composeFirst(p)["bindings"] = []any{map[string]any{"target": "/id", "step": "a", "pointer": ""}}
		},
	}
	for name, edit := range edits {
		cases[name] = composeTestInput(t, edit)
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			calls := 0
			response, envelope, err := composeRun(t, input, func(context.Context, ToolCall) CompositionChildOutcome { calls++; return composeGood(`{"id":1}`) })
			if err != nil || !response.IsError || envelope.Error.Code != "invalid_input" || calls != 0 {
				t.Fatalf("unexpected result: %+v %v calls=%d", envelope, err, calls)
			}
			if NewMCPComposeTool().(PreHookInputValidator).ValidateInput(input) == nil {
				t.Fatal("pre-hook validation accepted invalid plan")
			}
		})
	}
}

func TestMCPComposePointerBindings(t *testing.T) {
	for _, target := range []string{"", "/missing", "/array/01", "/array/-", "/array/+1", "/array/9", "/array/99999999999999999999999999"} {
		t.Run(target, func(t *testing.T) {
			input := `{"steps":[{"id":"a","tool":"get","arguments":{},"select":{"fields":{"id":"/id"}}},{"id":"b","tool":"get","arguments":{"array":[null,null]},"bindings":[{"target":` + fmt.Sprintf("%q", target) + `,"step":"a","pointer":"/id"}],"select":{"fields":{"id":"/id"}}}],"return":["b"]}`
			if _, err := parseComposePlan(input); err == nil {
				t.Fatal("accepted invalid binding target")
			}
		})
	}
	for _, targets := range [][2]string{{"/a~1b", "/a~1b/x"}, {"/a~1b", "/a~1b"}, {"/a~0b/x", "/a~0b"}} {
		input := `{"steps":[{"id":"a","tool":"get","arguments":{},"select":{"fields":{"id":"/id"}}},{"id":"b","tool":"get","arguments":{"a/b":{"x":null},"a~b":{"x":null}},"bindings":[{"target":` + fmt.Sprintf("%q", targets[0]) + `,"step":"a","pointer":"/id"},{"target":` + fmt.Sprintf("%q", targets[1]) + `,"step":"a","pointer":"/id"}],"select":{"fields":{"id":"/id"}}}],"return":["b"]}`
		if _, err := parseComposePlan(input); err == nil {
			t.Fatal("accepted overlapping targets")
		}
	}
	input := `{"steps":[{"id":"a","tool":"get","arguments":{},"select":{"fields":{"id":"/a~1b/~0id","nil":"/nil"}}},{"id":"b","tool":"get","arguments":{"array":[null,null],"empty":0},"bindings":[{"target":"/array/0","step":"a","pointer":"/id"},{"target":"/empty","step":"a","pointer":"/nil"}],"select":{"fields":{"id":"/id"}}}],"return":["a","b"]}`
	calls := []ToolCall{}
	response, envelope, err := composeRun(t, input, func(_ context.Context, call ToolCall) CompositionChildOutcome {
		calls = append(calls, call)
		if len(calls) == 1 {
			return composeGood(`{"a/b":{"~id":9007199254740993},"nil":null,"secret":"hidden"}`)
		}
		if call.Input != `{"array":[9007199254740993,null],"empty":null}` {
			t.Errorf("numeric/null binding corrupted: %s", call.Input)
		}
		return composeGood(`{"id":9007199254740993}`)
	})
	if err != nil || response.IsError || !envelope.Complete || len(calls) != 2 || calls[0].ID == calls[1].ID || strings.Contains(response.Content, "hidden") {
		t.Fatalf("unexpected response %s err=%v calls=%v", response.Content, err, calls)
	}
	if !strings.Contains(response.Content, "9007199254740993") {
		t.Fatal("numeric result corrupted")
	}
}

func TestMCPComposeJSONDepthAndNumbers(t *testing.T) {
	for _, depth := range []int{64, 65} {
		_, err := composeJSON(strings.Repeat("[", depth) + `9007199254740993` + strings.Repeat("]", depth))
		if (err == nil) != (depth == 64) {
			t.Fatalf("depth %d error=%v", depth, err)
		}
	}
	for _, input := range []string{`null`, `[1e20,-0,9007199254740993]`, `{"":null}`} {
		value, err := composeJSON(input)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(value)
		if string(encoded) != input {
			t.Fatalf("changed numeric lexemes: %s", encoded)
		}
	}
}

func TestMCPComposeFailureAndPartialContext(t *testing.T) {
	input := `{"steps":[{"id":"a","tool":"get","arguments":{},"select":{"fields":{"id":"/id"}}},{"id":"b","tool":"get","arguments":{},"select":{"fields":{"id":"/id"}}},{"id":"c","tool":"get","arguments":{},"select":{"fields":{"id":"/id"}}}],"return":["a","b","c"]}`
	secret := "RAW_SERVER_SECRET"
	cases := map[string]struct {
		outcome CompositionChildOutcome
		code    string
	}{
		"server error": {func() CompositionChildOutcome {
			o := composeGood(`{"id":"` + secret + `"}`)
			o.Response.IsError = true
			return o
		}(), "child_failed"},
		"post block valid JSON": {func() CompositionChildOutcome {
			o := composeGood(`{"id":"` + secret + `"}`)
			o.PostBlocked = true
			return o
		}(), "hook_blocked"},
		"not executed":               {CompositionChildOutcome{Response: composeGood(`{"id":1}`).Response}, "child_failed"},
		"rule deny":                  {CompositionChildOutcome{RuleDenied: true, Executed: true}, "rule_denied"},
		"human reject":               {CompositionChildOutcome{ControlErr: permission.ErrorPermissionDenied}, "permission_denied"},
		"control":                    {CompositionChildOutcome{ControlErr: context.Canceled}, "cancelled"},
		"unknown failure diagnostic": {CompositionChildOutcome{FailureCode: secret}, "child_failed"},
		"missing metadata":           {func() CompositionChildOutcome { o := composeGood(`{"id":1}`); o.Response.MCPOutput = nil; return o }(), "unsupported_output"},
		"structured only": {func() CompositionChildOutcome {
			o := composeGood(`{"id":1}`)
			o.Response.MCPOutput.TextOnly = false
			return o
		}(), "unsupported_output"},
		"capped": {func() CompositionChildOutcome {
			o := composeGood(`{"id":1}`)
			o.Response.MCPOutput.Complete = false
			return o
		}(), "unsupported_output"},
		"malformed":          {composeGood(secret), "unsupported_output"},
		"duplicate JSON":     {composeGood(`{"id":1,"id":2}`), "unsupported_output"},
		"trailing JSON":      {composeGood(`{"id":1}{}`), "unsupported_output"},
		"invalid UTF8":       {composeGood(`{"id":"` + string([]byte{0xff}) + `"}`), "unsupported_output"},
		"deep JSON":          {composeGood(strings.Repeat("[", 65) + `0` + strings.Repeat("]", 65)), "unsupported_output"},
		"missing projection": {composeGood(`{"secret":"` + secret + `"}`), "projection_failed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			calls := 0
			response, envelope, err := composeRun(t, input, func(context.Context, ToolCall) CompositionChildOutcome {
				calls++
				if calls == 1 {
					o := composeGood(`{"id":1}`)
					o.HookContext = "first context"
					return o
				}
				tc.outcome.HookContext = "second context"
				return tc.outcome
			})
			if !response.IsError || envelope.Complete || calls != 2 || envelope.Error.Code != tc.code || envelope.Error.Step != "b" || envelope.Steps[2].Status != "skipped" || len(envelope.Results) != 1 {
				t.Fatalf("unexpected result %s calls=%d", response.Content, calls)
			}
			if !errors.Is(err, tc.outcome.ControlErr) {
				t.Fatalf("control error lost: %v", err)
			}
			if strings.Contains(response.Content, secret) || response.Composition.HookContext != "first context\nsecond context" {
				t.Fatalf("payload/context wrong: %+v", response)
			}
			if response.Composition.Progressed != 1+boolInt(tc.outcome.Executed && !tc.outcome.RuleDenied) || response.Composition.RuleDenials != boolInt(tc.outcome.RuleDenied) {
				t.Fatalf("incorrect accounting: %+v", response.Composition)
			}
		})
	}
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestMCPComposeRows(t *testing.T) {
	input := strings.Replace(composeTestPlan, `"fields":{"id":"/id"}`, `"rows":"/items","fields":{"id":"/id"},"limit":2`, 1)
	response, envelope, err := composeRun(t, input, func(context.Context, ToolCall) CompositionChildOutcome {
		return composeGood(`{"items":[{"id":null},{"id":2},{"secret":"omitted"}]}`)
	})
	if err != nil || response.IsError || envelope.Complete || *envelope.Steps[0].TotalRows != 3 || *envelope.Steps[0].ReturnedRows != 2 || !*envelope.Steps[0].Limited || strings.Contains(response.Content, "omitted") {
		t.Fatalf("bad limited rows: %s", response.Content)
	}
	for _, text := range []string{`{"items":[]}`, `{"items":[{"id":null}]}`} {
		response, envelope, err := composeRun(t, input, func(context.Context, ToolCall) CompositionChildOutcome { return composeGood(text) })
		if err != nil || response.IsError || !envelope.Complete || *envelope.Steps[0].Limited {
			t.Fatalf("bad complete rows: %s", response.Content)
		}
	}
	for _, text := range []string{`{"items":null}`, `{"items":{}}`, `{"items":[{}]}`} {
		response, envelope, _ := composeRun(t, input, func(context.Context, ToolCall) CompositionChildOutcome { return composeGood(text) })
		if !response.IsError || envelope.Error.Code != "projection_failed" {
			t.Fatalf("bad rows accepted %s", response.Content)
		}
	}
}

func TestMCPComposeBounds(t *testing.T) {
	for _, size := range []int{composeMaxChild, composeMaxChild + 1} {
		text := `{"id":1,"padding":"` + strings.Repeat("x", size-len(`{"id":1,"padding":""}`)) + `"}`
		response, _, _ := composeRun(t, composeTestPlan, func(context.Context, ToolCall) CompositionChildOutcome { return composeGood(text) })
		if response.IsError != (size > composeMaxChild) {
			t.Fatalf("child bytes=%d: %s", size, response.Content)
		}
	}
	for _, size := range []int{composeMaxProjected, composeMaxProjected + 1} {
		text := `{"id":"` + strings.Repeat("x", size-len(`{"id":""}`)) + `"}`
		response, _, _ := composeRun(t, composeTestPlan, func(context.Context, ToolCall) CompositionChildOutcome { return composeGood(text) })
		if response.IsError != (size > composeMaxProjected) {
			t.Fatalf("projected bytes=%d error=%v", size, response.IsError)
		}
	}
	input := composeTestInput(t, func(p map[string]any) {
		steps := []any{}
		for i := 0; i < 5; i++ {
			steps = append(steps, map[string]any{"id": fmt.Sprintf("s%d", i), "tool": "get", "arguments": map[string]any{}, "select": map[string]any{"fields": map[string]any{"id": "/id"}}})
		}
		p["steps"] = steps
		p["return"] = []string{"s0"}
	})
	calls := 0
	text := `{"id":1,"padding":"` + strings.Repeat("x", composeMaxChild-len(`{"id":1,"padding":""}`)) + `"}`
	response, envelope, _ := composeRun(t, input, func(context.Context, ToolCall) CompositionChildOutcome { calls++; return composeGood(text) })
	if !response.IsError || envelope.Error.Code != "output_limit" || envelope.Error.Step != "s4" || calls != 5 || len(envelope.Results) != 1 {
		t.Fatalf("bad cumulative bound: %s calls=%d", response.Content, calls)
	}
}

func TestMCPComposePreflightAndRecheck(t *testing.T) {
	input := `{"steps":[{"id":"a","tool":"first","arguments":{},"select":{"fields":{"id":"/id"}}},{"id":"b","tool":"second","arguments":{},"select":{"fields":{"id":"/id"}}}],"return":["a","b"]}`
	for _, failAt := range []int{2, 4} {
		checks, calls := 0, 0
		ctx := WithCompositionRuntime(context.Background(), CompositionRuntime{Check: func(context.Context, string) error {
			checks++
			if checks == failAt {
				return errors.New("secret")
			}
			return nil
		}, Dispatch: func(context.Context, ToolCall) CompositionChildOutcome { calls++; return composeGood(`{"id":1}`) }})
		response, err := NewMCPComposeTool().Run(ctx, ToolCall{Input: input})
		if err != nil || !response.IsError || calls != failAt/2-1 || strings.Contains(response.Content, "secret") {
			t.Fatalf("bad preflight checks=%d calls=%d response=%s", checks, calls, response.Content)
		}
	}
}

func TestMCPComposeContextAndIsolation(t *testing.T) {
	var wg sync.WaitGroup
	for _, session := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.WithValue(context.Background(), SessionIDContextKey, session)
			ctx = context.WithValue(ctx, permission.HookAllowKey, true)
			ctx = WithCompositionRuntime(ctx, CompositionRuntime{Check: func(context.Context, string) error { return nil }, Dispatch: func(ctx context.Context, _ ToolCall) CompositionChildOutcome {
				if ctx.Value(permission.HookAllowKey) != false || ctx.Value(SessionIDContextKey) != session {
					t.Error("child context corrupted")
				}
				return composeGood(`{"id":"` + session + `"}`)
			}})
			response, err := NewMCPComposeTool().Run(ctx, ToolCall{ID: session, Input: composeTestPlan})
			if err != nil || response.IsError || !strings.Contains(response.Content, session) {
				t.Errorf("bad isolated result: %+v %v", response, err)
			}
		}()
	}
	wg.Wait()
}

func TestMCPComposeCancellationAndActiveBudget(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		response, err := NewMCPComposeTool().Run(ctx, ToolCall{Input: composeTestPlan})
		if !errors.Is(err, context.Canceled) || !response.IsError {
			t.Fatalf("lost cancellation: %+v %v", response, err)
		}
	})
	t.Run("active", func(t *testing.T) {
		done := make(chan struct{})
		defer close(done)
		ctx := WithCompositionRuntime(context.Background(), CompositionRuntime{Check: func(context.Context, string) error { return nil }, Dispatch: func(context.Context, ToolCall) CompositionChildOutcome { <-done; return composeGood(`{"id":1}`) }})
		start := time.Now()
		response, err := (&mcpComposeTool{activeBudget: 20 * time.Millisecond}).Run(ctx, ToolCall{Input: composeTestPlan})
		if err != nil || !response.IsError || !strings.Contains(response.Content, "active_timeout") || time.Since(start) > time.Second {
			t.Fatalf("active budget failed: %+v %v", response, err)
		}
	})
	t.Run("smaller outer bound", func(t *testing.T) {
		ctx := WithCompositionRuntime(context.Background(), CompositionRuntime{Check: func(context.Context, string) error { return nil }, Dispatch: func(ctx context.Context, _ ToolCall) CompositionChildOutcome {
			<-ctx.Done()
			return CompositionChildOutcome{}
		}})
		ctx = WithCompositionLimit(ctx, 10*time.Millisecond)
		response, err := NewMCPComposeTool().Run(ctx, ToolCall{Input: composeTestPlan})
		if err != nil || !strings.Contains(response.Content, "active_timeout") {
			t.Fatalf("outer bound lost: %s %v", response.Content, err)
		}
	})
	t.Run("permission wait", func(t *testing.T) {
		ctx, budget := newComposeBudget(context.Background(), 50*time.Millisecond)
		defer budget.close()
		budget.WaitStarted()
		budget.WaitStarted()
		time.Sleep(80 * time.Millisecond)
		budget.WaitEnded()
		if ctx.Err() != nil {
			t.Fatal("nested wait charged")
		}
		budget.WaitEnded()
		select {
		case <-ctx.Done():
			t.Fatal("budget ended before active work")
		case <-time.After(5 * time.Millisecond):
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("budget never resumed")
		}
	})
}

func TestMCPComposeHookContextOutsideProjectionBudget(t *testing.T) {
	contextText := strings.Repeat("trusted hook context ", 2000)
	response, _, err := composeRun(t, composeTestPlan, func(context.Context, ToolCall) CompositionChildOutcome {
		o := composeGood(`{"id":1}`)
		o.HookContext = contextText
		return o
	})
	if err != nil || response.IsError || response.Composition.HookContext != contextText {
		t.Fatal("hook context capped or lost")
	}
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), "trusted hook") || strings.Contains(string(encoded), "MCPOutput") {
		t.Fatal("runtime metadata serialized")
	}
}

func TestMCPComposeWorkflowFixtures(t *testing.T) {
	var fixtures []struct {
		Name  string          `json:"name"`
		Plan  json.RawMessage `json:"plan"`
		Calls []struct {
			Tool        string          `json:"tool"`
			Arguments   json.RawMessage `json:"arguments"`
			Response    string          `json:"response"`
			HookContext string          `json:"hookContext"`
		} `json:"calls"`
		ExpectedResults              json.RawMessage `json:"expectedResults"`
		IntermediateDecisionsAvoided int             `json:"intermediateDecisionsAvoided"`
	}
	data, err := os.ReadFile("testdata/mcp_compose_workflows.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 3 {
		t.Fatal("need three workflows")
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			next := 0
			ordinaryBytes := 0
			response, envelope, err := composeRun(t, string(fixture.Plan), func(_ context.Context, call ToolCall) CompositionChildOutcome {
				if next >= len(fixture.Calls) {
					t.Error("unexpected child")
					return CompositionChildOutcome{FailureCode: "child_failed"}
				}
				expected := fixture.Calls[next]
				next++
				actualArgs, err := composeJSON(call.Input)
				if err != nil {
					t.Error(err)
				}
				expectedArgs, err := composeJSON(string(expected.Arguments))
				if err != nil {
					t.Error(err)
				}
				if expected.Tool != call.Name || !reflect.DeepEqual(actualArgs, expectedArgs) {
					t.Errorf("request %d differs: %+v", next, call)
				}
				if len(expected.Response) >= 50*1024 {
					t.Error("fixture exceeds ordinary cap")
				}
				ordinaryBytes += len(expected.Response) + len(expected.HookContext)
				outcome := composeGood(expected.Response)
				outcome.HookContext = expected.HookContext
				return outcome
			})
			if err != nil || response.IsError || !envelope.Complete || next != len(fixture.Calls) {
				t.Fatalf("workflow failed: %s %v", response.Content, err)
			}
			actual, err := composeJSON(response.Content)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := composeJSON(string(fixture.ExpectedResults))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual.(map[string]any)["results"], expected) {
				t.Fatalf("required facts differ: %s", response.Content)
			}
			for i, status := range envelope.Steps {
				if status.Status != "ok" || status.Tool != fixture.Calls[i].Tool {
					t.Fatalf("provenance differs: %+v", status)
				}
			}
			composedBytes := len(response.Content) + len(response.Composition.HookContext)
			if composedBytes*5 > ordinaryBytes {
				t.Fatalf("result volume target failed: C=%d B=%d", composedBytes, ordinaryBytes)
			}
			projected, _ := json.Marshal(envelope.Results)
			t.Logf("engine-only result content: B=%d C=%d envelope/status overhead=%d hookContext=%d intermediate scripted decisions avoided=%d", ordinaryBytes, composedBytes, len(response.Content)-len(projected), len(response.Composition.HookContext), fixture.IntermediateDecisionsAvoided)
		})
	}
}

func TestMCPComposeCancellationRetainsPublishedHookContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	ctx = WithCompositionRuntime(ctx, CompositionRuntime{Check: func(context.Context, string) error { return nil }, Dispatch: func(ctx context.Context, _ ToolCall) CompositionChildOutcome {
		PublishCompositionHookContext(ctx, "pre-hook context survives")
		close(started)
		<-release
		return CompositionChildOutcome{}
	}})
	go func() { <-started; cancel() }()
	response, err := NewMCPComposeTool().Run(ctx, ToolCall{Input: composeTestPlan})
	if !errors.Is(err, context.Canceled) || !response.IsError || response.Composition.HookContext != "pre-hook context survives" {
		t.Fatalf("cancel discarded hook context: %+v %v", response, err)
	}
}

func TestMCPComposeProjectedBudgetIncludesUnreturnedSteps(t *testing.T) {
	input := `{"steps":[{"id":"a","tool":"get","arguments":{},"select":{"fields":{"id":"/id"}}},{"id":"b","tool":"get","arguments":{},"select":{"fields":{"id":"/id"}}}],"return":["b"]}`
	text := `{"id":"` + strings.Repeat("x", 9000) + `"}`
	response, envelope, _ := composeRun(t, input, func(context.Context, ToolCall) CompositionChildOutcome { return composeGood(text) })
	if !response.IsError || envelope.Error.Code != "output_limit" || envelope.Error.Step != "b" || len(envelope.Results) != 0 {
		t.Fatalf("unreturned projection escaped budget: %s", response.Content)
	}
}

func TestMCPComposeExactPlanAndShapeLimits(t *testing.T) {
	input := composeTestInput(t, func(p map[string]any) {
		steps := []any{}
		for i := 0; i < 8; i++ {
			id := fmt.Sprintf("s%d", i) + strings.Repeat("x", 62)
			fields := map[string]any{}
			for n := 0; n < 32; n++ {
				fields[fmt.Sprintf("f%02d", n)+strings.Repeat("x", 61)] = ""
			}
			steps = append(steps, map[string]any{"id": id, "tool": strings.Repeat("t", 256), "arguments": map[string]any{}, "select": map[string]any{"fields": fields}})
		}
		p["steps"] = steps
		ids := []string{}
		for _, step := range steps {
			ids = append(ids, step.(map[string]any)["id"].(string))
		}
		p["return"] = ids
	})
	if len(input) > composeMaxPlan {
		t.Fatal("test plan too large")
	}
	input += strings.Repeat(" ", composeMaxPlan-len(input))
	if _, err := parseComposePlan(input); err != nil {
		t.Fatal("exact input bounds rejected", err)
	}
	if _, err := parseComposePlan(input + " "); err == nil {
		t.Fatal("input beyond exact bound accepted")
	}
	if _, err := composePointer("/" + strings.Repeat("x", 1023)); err != nil {
		t.Fatal("1024-byte pointer rejected")
	}
}

func TestMCPComposeBindingsCannotReadUnprojectedData(t *testing.T) {
	input := `{"steps":[{"id":"a","tool":"get","arguments":{},"select":{"fields":{"id":"/id"}}},{"id":"b","tool":"get","arguments":{"secret":null},"bindings":[{"target":"/secret","step":"a","pointer":"/secret"}],"select":{"fields":{"id":"/id"}}}],"return":["a","b"]}`
	calls := 0
	response, envelope, _ := composeRun(t, input, func(context.Context, ToolCall) CompositionChildOutcome {
		calls++
		return composeGood(`{"id":1,"secret":"unprojected secret"}`)
	})
	if !response.IsError || envelope.Error.Code != "binding_failed" || calls != 1 || len(envelope.Results) != 1 || strings.Contains(response.Content, "unprojected secret") {
		t.Fatalf("raw result became binding input: %s", response.Content)
	}
}
