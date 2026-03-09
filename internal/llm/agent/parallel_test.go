package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
)

type fakeTool struct {
	name     string
	runFn    func(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error)
	parallel bool
}

func (f *fakeTool) Info() tools.ToolInfo {
	return tools.ToolInfo{Name: f.name}
}

func (f *fakeTool) Run(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
	return f.runFn(ctx, call)
}

func (f *fakeTool) AllowParallelism(_ tools.ToolCall, _ []tools.ToolCall) bool {
	return f.parallel
}

func (f *fakeTool) IsBaseline() bool { return true }

func buildToolCalls(names ...string) []message.ToolCall {
	tcs := make([]message.ToolCall, len(names))
	for i, n := range names {
		tcs[i] = message.ToolCall{
			ID:       n + "-id",
			Name:     n,
			Input:    "{}",
			Finished: true,
		}
	}
	return tcs
}

func TestParallelExecution_ReadOnlyToolsRunConcurrently(t *testing.T) {
	var running atomic.Int32
	var maxConcurrent atomic.Int32

	sleepTool := func(name string) *fakeTool {
		return &fakeTool{
			name:     name,
			parallel: true,
			runFn: func(_ context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
				cur := running.Add(1)
				for {
					old := maxConcurrent.Load()
					if cur > old {
						if maxConcurrent.CompareAndSwap(old, cur) {
							break
						}
					} else {
						break
					}
				}
				time.Sleep(100 * time.Millisecond)
				running.Add(-1)
				return tools.NewTextResponse("ok"), nil
			},
		}
	}

	toolSet := []tools.BaseTool{
		sleepTool("read"),
		sleepTool("glob"),
		sleepTool("grep"),
	}

	toolCalls := buildToolCalls("read", "glob", "grep")
	toolResults := make([]message.ToolResult, len(toolCalls))

	allToolCalls := make([]tools.ToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		allToolCalls[i] = tools.ToolCall{ID: tc.ID, Name: tc.Name, Input: tc.Input}
	}

	type toolEntry struct {
		index    int
		tool     tools.BaseTool
		toolCall message.ToolCall
	}
	var parallelGroup []toolEntry
	for i, tc := range toolCalls {
		for _, t := range toolSet {
			if t.Info().Name == tc.Name {
				parallelGroup = append(parallelGroup, toolEntry{index: i, tool: t, toolCall: tc})
				break
			}
		}
	}

	start := time.Now()
	ctx := context.Background()
	permCtx, permCancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, entry := range parallelGroup {
		wg.Add(1)
		go func(e toolEntry) {
			defer wg.Done()
			type runResult struct {
				resp tools.ToolResponse
				err  error
			}
			ch := make(chan runResult, 1)
			go func() {
				r, err := e.tool.Run(permCtx, tools.ToolCall{ID: e.toolCall.ID, Name: e.toolCall.Name, Input: e.toolCall.Input})
				ch <- runResult{r, err}
			}()
			select {
			case <-permCtx.Done():
				return
			case res := <-ch:
				toolResults[e.index] = message.ToolResult{
					ToolCallID: e.toolCall.ID,
					Name:       e.toolCall.Name,
					Content:    res.resp.Content,
					IsError:    res.err != nil,
				}
			}
		}(entry)
	}
	wg.Wait()
	permCancel()
	elapsed := time.Since(start)

	if elapsed > 250*time.Millisecond {
		t.Errorf("parallel execution took %v, expected ~100ms (3 tools should run concurrently)", elapsed)
	}

	if maxConcurrent.Load() < 2 {
		t.Errorf("max concurrent = %d, expected >= 2 (tools should overlap)", maxConcurrent.Load())
	}

	for i, r := range toolResults {
		if r.Content != "ok" {
			t.Errorf("toolResults[%d].Content = %q, want %q", i, r.Content, "ok")
		}
	}
}

func TestParallelExecution_SameFileEditsRunSequentially(t *testing.T) {
	var order []string
	var mu sync.Mutex

	editTool := func(name string) *fakeTool {
		return &fakeTool{
			name:     name,
			parallel: false,
			runFn: func(_ context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
				mu.Lock()
				order = append(order, call.ID)
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				return tools.NewTextResponse("ok"), nil
			},
		}
	}

	toolSet := []tools.BaseTool{editTool("edit")}

	toolCalls := []message.ToolCall{
		{ID: "edit-1", Name: "edit", Input: `{"file_path":"/a.go"}`, Finished: true},
		{ID: "edit-2", Name: "edit", Input: `{"file_path":"/a.go"}`, Finished: true},
	}
	toolResults := make([]message.ToolResult, len(toolCalls))

	type toolEntry struct {
		index    int
		tool     tools.BaseTool
		toolCall message.ToolCall
	}
	var sequentialGroup []toolEntry
	for i, tc := range toolCalls {
		for _, t := range toolSet {
			if t.Info().Name == tc.Name {
				sequentialGroup = append(sequentialGroup, toolEntry{index: i, tool: t, toolCall: tc})
				break
			}
		}
	}

	start := time.Now()
	ctx := context.Background()
	for _, entry := range sequentialGroup {
		r, _ := entry.tool.Run(ctx, tools.ToolCall{ID: entry.toolCall.ID, Name: entry.toolCall.Name, Input: entry.toolCall.Input})
		toolResults[entry.index] = message.ToolResult{
			ToolCallID: entry.toolCall.ID,
			Name:       entry.toolCall.Name,
			Content:    r.Content,
		}
	}
	elapsed := time.Since(start)

	if elapsed < 90*time.Millisecond {
		t.Errorf("sequential execution took %v, expected ~100ms (2 x 50ms sequential)", elapsed)
	}

	if len(order) != 2 || order[0] != "edit-1" || order[1] != "edit-2" {
		t.Errorf("execution order = %v, want [edit-1, edit-2]", order)
	}
}

func TestParallelExecution_PermissionDeniedCancelsOthers(t *testing.T) {
	slowTool := &fakeTool{
		name:     "slow",
		parallel: true,
		runFn: func(ctx context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
			select {
			case <-ctx.Done():
				return tools.NewEmptyResponse(), ctx.Err()
			case <-time.After(5 * time.Second):
				return tools.NewTextResponse("should not reach"), nil
			}
		},
	}

	denyTool := &fakeTool{
		name:     "deny",
		parallel: true,
		runFn: func(_ context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
			time.Sleep(10 * time.Millisecond)
			return tools.NewEmptyResponse(), permission.ErrorPermissionDenied
		},
	}

	toolCalls := []message.ToolCall{
		{ID: "slow-id", Name: "slow", Input: "{}", Finished: true},
		{ID: "deny-id", Name: "deny", Input: "{}", Finished: true},
		{ID: "slow2-id", Name: "slow", Input: "{}", Finished: true},
	}
	toolResults := make([]message.ToolResult, len(toolCalls))

	type toolEntry struct {
		index    int
		tool     tools.BaseTool
		toolCall message.ToolCall
	}
	entries := []toolEntry{
		{0, slowTool, toolCalls[0]},
		{1, denyTool, toolCalls[1]},
		{2, slowTool, toolCalls[2]},
	}

	start := time.Now()
	ctx := context.Background()
	permCtx, permCancel := context.WithCancel(ctx)
	var wg sync.WaitGroup

	for _, entry := range entries {
		wg.Add(1)
		go func(e toolEntry) {
			defer wg.Done()
			type runResult struct {
				resp tools.ToolResponse
				err  error
			}
			ch := make(chan runResult, 1)
			go func() {
				r, err := e.tool.Run(permCtx, tools.ToolCall{ID: e.toolCall.ID, Name: e.toolCall.Name, Input: e.toolCall.Input})
				ch <- runResult{r, err}
			}()
			select {
			case <-permCtx.Done():
				return
			case res := <-ch:
				if res.err != nil && res.err == permission.ErrorPermissionDenied {
					toolResults[e.index] = message.ToolResult{
						ToolCallID: e.toolCall.ID,
						Name:       e.toolCall.Name,
						Content:    "Permission denied",
						IsError:    true,
					}
					permCancel()
					return
				}
				toolResults[e.index] = message.ToolResult{
					ToolCallID: e.toolCall.ID,
					Name:       e.toolCall.Name,
					Content:    res.resp.Content,
				}
			}
		}(entry)
	}
	wg.Wait()
	permCancel()
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Fatalf("permission denied should cancel slow tools quickly, took %v", elapsed)
	}

	// Deny tool should have "Permission denied" result
	if toolResults[1].Content != "Permission denied" {
		t.Errorf("deny tool result = %q, want %q", toolResults[1].Content, "Permission denied")
	}

	// Slow tools should have empty ToolCallID (cancelled before completion)
	for _, idx := range []int{0, 2} {
		if toolResults[idx].ToolCallID != "" && toolResults[idx].Content == "should not reach" {
			t.Errorf("slow tool %d should have been cancelled, got content=%q", idx, toolResults[idx].Content)
		}
	}

	// Fill unset results (as the real code does)
	permissionDenied := false
	for _, r := range toolResults {
		if r.IsError && r.Content == "Permission denied" {
			permissionDenied = true
			break
		}
	}
	if !permissionDenied {
		t.Error("permissionDenied flag should be true")
	}

	for i := range toolResults {
		if toolResults[i].ToolCallID == "" {
			toolResults[i] = message.ToolResult{
				ToolCallID: toolCalls[i].ID,
				Name:       toolCalls[i].Name,
				Content:    "Tool execution canceled by user",
				IsError:    true,
			}
		}
	}

	for i, r := range toolResults {
		if r.ToolCallID == "" {
			t.Errorf("toolResults[%d] has empty ToolCallID after fill", i)
		}
	}
}

func TestParallelExecution_PermissionDeniedWithBlockedTools(t *testing.T) {
	// This tests the specific bug where tools blocked on permission.Request()
	// would hang forever when another tool got denied. The fix uses a select
	// on permCtx.Done() to unblock.
	blocked := make(chan struct{})
	blockingTool := &fakeTool{
		name:     "blocking",
		parallel: true,
		runFn: func(ctx context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
			close(blocked)
			// Simulate blocking on permission request (no context awareness)
			select {
			case <-ctx.Done():
				return tools.NewEmptyResponse(), ctx.Err()
			case <-time.After(10 * time.Second):
				return tools.NewTextResponse("should not reach"), nil
			}
		},
	}

	denyTool := &fakeTool{
		name:     "deny",
		parallel: true,
		runFn: func(_ context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
			// Wait for blocking tool to start
			<-blocked
			return tools.NewEmptyResponse(), permission.ErrorPermissionDenied
		},
	}

	toolCalls := []message.ToolCall{
		{ID: "blocking-id", Name: "blocking", Input: "{}", Finished: true},
		{ID: "deny-id", Name: "deny", Input: "{}", Finished: true},
	}
	toolResults := make([]message.ToolResult, len(toolCalls))

	type toolEntry struct {
		index    int
		tool     tools.BaseTool
		toolCall message.ToolCall
	}
	entries := []toolEntry{
		{0, blockingTool, toolCalls[0]},
		{1, denyTool, toolCalls[1]},
	}

	done := make(chan struct{})
	go func() {
		permCtx, permCancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup

		for _, entry := range entries {
			wg.Add(1)
			go func(e toolEntry) {
				defer wg.Done()
				type runResult struct {
					resp tools.ToolResponse
					err  error
				}
				ch := make(chan runResult, 1)
				go func() {
					r, err := e.tool.Run(permCtx, tools.ToolCall{ID: e.toolCall.ID, Name: e.toolCall.Name, Input: e.toolCall.Input})
					ch <- runResult{r, err}
				}()
				select {
				case <-permCtx.Done():
					return
				case res := <-ch:
					if res.err != nil && res.err == permission.ErrorPermissionDenied {
						toolResults[e.index] = message.ToolResult{
							ToolCallID: e.toolCall.ID,
							Name:       e.toolCall.Name,
							Content:    "Permission denied",
							IsError:    true,
						}
						permCancel()
						return
					}
					toolResults[e.index] = message.ToolResult{
						ToolCallID: e.toolCall.ID,
						Name:       e.toolCall.Name,
						Content:    res.resp.Content,
					}
				}
			}(entry)
		}
		wg.Wait()
		permCancel()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK: parallel execution hung when permission was denied while another tool was blocked (the original bug)")
	}

	if toolResults[1].Content != "Permission denied" {
		t.Errorf("deny tool result = %q, want %q", toolResults[1].Content, "Permission denied")
	}
}

func TestBuildExecutionGroups_Partitioning(t *testing.T) {
	readTool := &fakeTool{name: "read", parallel: true}
	grepTool := &fakeTool{name: "grep", parallel: true}
	bashSafe := &fakeTool{name: "bash_safe", parallel: true}
	bashUnsafe := &fakeTool{name: "bash_unsafe", parallel: false}
	structOut := &fakeTool{name: "struct_output", parallel: false}

	toolSet := []tools.BaseTool{readTool, grepTool, bashSafe, bashUnsafe, structOut}

	toolCalls := []message.ToolCall{
		{ID: "1", Name: "read", Input: "{}", Finished: true},
		{ID: "2", Name: "grep", Input: "{}", Finished: true},
		{ID: "3", Name: "bash_safe", Input: "{}", Finished: true},
		{ID: "4", Name: "bash_unsafe", Input: "{}", Finished: true},
		{ID: "5", Name: "struct_output", Input: "{}", Finished: true},
		{ID: "6", Name: "unknown_tool", Input: "{}", Finished: true},
	}

	toolResults := make([]message.ToolResult, len(toolCalls))

	allToolCalls := make([]tools.ToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		allToolCalls[i] = tools.ToolCall{ID: tc.ID, Name: tc.Name, Input: tc.Input}
	}

	type toolEntry struct {
		index    int
		tool     tools.BaseTool
		toolCall message.ToolCall
		parallel bool
	}
	var parallelGroup, sequentialGroup []toolEntry
	tracker := newCallTracker()

	for i, tc := range toolCalls {
		var tool tools.BaseTool
		for _, t := range toolSet {
			if t.Info().Name == tc.Name {
				tool = t
				break
			}
		}
		if tool == nil {
			toolResults[i] = message.ToolResult{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    "Tool not found",
				IsError:    true,
			}
			continue
		}
		tracker.Track(tc.Name, tc.Input)

		entry := toolEntry{index: i, tool: tool, toolCall: tc}
		if tool.AllowParallelism(allToolCalls[i], allToolCalls) {
			entry.parallel = true
			parallelGroup = append(parallelGroup, entry)
		} else {
			sequentialGroup = append(sequentialGroup, entry)
		}
	}

	if len(parallelGroup) != 3 {
		t.Errorf("parallel group size = %d, want 3 (read, grep, bash_safe)", len(parallelGroup))
	}
	if len(sequentialGroup) != 2 {
		t.Errorf("sequential group size = %d, want 2 (bash_unsafe, struct_output)", len(sequentialGroup))
	}

	// unknown_tool should have error result
	if !toolResults[5].IsError || toolResults[5].Content != "Tool not found" {
		t.Errorf("unknown tool should have error result, got %+v", toolResults[5])
	}

	// Verify parallel group members
	parallelNames := make(map[string]bool)
	for _, e := range parallelGroup {
		parallelNames[e.toolCall.Name] = true
	}
	for _, expected := range []string{"read", "grep", "bash_safe"} {
		if !parallelNames[expected] {
			t.Errorf("%s should be in parallel group", expected)
		}
	}

	// Verify sequential group members
	seqNames := make(map[string]bool)
	for _, e := range sequentialGroup {
		seqNames[e.toolCall.Name] = true
	}
	for _, expected := range []string{"bash_unsafe", "struct_output"} {
		if !seqNames[expected] {
			t.Errorf("%s should be in sequential group", expected)
		}
	}
}

func TestParallelExecution_MixedGroups(t *testing.T) {
	var parallelFinished atomic.Int32
	var seqStartedBeforeParallelDone atomic.Bool

	parallelTool := &fakeTool{
		name:     "read",
		parallel: true,
		runFn: func(_ context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
			time.Sleep(50 * time.Millisecond)
			parallelFinished.Add(1)
			return tools.NewTextResponse("parallel-ok"), nil
		},
	}

	seqTool := &fakeTool{
		name:     "bash",
		parallel: false,
		runFn: func(_ context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
			if parallelFinished.Load() < 2 {
				seqStartedBeforeParallelDone.Store(true)
			}
			return tools.NewTextResponse("seq-ok"), nil
		},
	}

	toolCalls := []message.ToolCall{
		{ID: "r1", Name: "read", Input: "{}", Finished: true},
		{ID: "r2", Name: "read", Input: "{}", Finished: true},
		{ID: "b1", Name: "bash", Input: "{}", Finished: true},
	}
	toolResults := make([]message.ToolResult, len(toolCalls))

	type toolEntry struct {
		index    int
		tool     tools.BaseTool
		toolCall message.ToolCall
	}

	var parallelGroup, sequentialGroup []toolEntry
	allToolCalls := make([]tools.ToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		allToolCalls[i] = tools.ToolCall{ID: tc.ID, Name: tc.Name, Input: tc.Input}
	}
	toolSet := []tools.BaseTool{parallelTool, seqTool}

	for i, tc := range toolCalls {
		for _, t := range toolSet {
			if t.Info().Name == tc.Name {
				entry := toolEntry{index: i, tool: t, toolCall: tc}
				if t.AllowParallelism(allToolCalls[i], allToolCalls) {
					parallelGroup = append(parallelGroup, entry)
				} else {
					sequentialGroup = append(sequentialGroup, entry)
				}
				break
			}
		}
	}

	// Execute parallel first
	ctx := context.Background()
	permCtx, permCancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, entry := range parallelGroup {
		wg.Add(1)
		go func(e toolEntry) {
			defer wg.Done()
			r, _ := e.tool.Run(permCtx, tools.ToolCall{ID: e.toolCall.ID, Name: e.toolCall.Name, Input: e.toolCall.Input})
			toolResults[e.index] = message.ToolResult{
				ToolCallID: e.toolCall.ID,
				Name:       e.toolCall.Name,
				Content:    r.Content,
			}
		}(entry)
	}
	wg.Wait()
	permCancel()

	// Then sequential
	for _, entry := range sequentialGroup {
		r, _ := entry.tool.Run(ctx, tools.ToolCall{ID: entry.toolCall.ID, Name: entry.toolCall.Name, Input: entry.toolCall.Input})
		toolResults[entry.index] = message.ToolResult{
			ToolCallID: entry.toolCall.ID,
			Name:       entry.toolCall.Name,
			Content:    r.Content,
		}
	}

	// Sequential should NOT start before parallel is done
	if seqStartedBeforeParallelDone.Load() {
		t.Error("sequential group started before parallel group finished")
	}

	for i, r := range toolResults {
		if r.ToolCallID == "" {
			t.Errorf("toolResults[%d] has empty ToolCallID", i)
		}
	}
}

func TestParallelExecution_UserCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	slowTool := &fakeTool{
		name:     "slow",
		parallel: true,
		runFn: func(ctx context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
			select {
			case <-ctx.Done():
				return tools.NewEmptyResponse(), ctx.Err()
			case <-time.After(5 * time.Second):
				return tools.NewTextResponse("should not reach"), nil
			}
		},
	}

	toolCalls := buildToolCalls("slow", "slow", "slow")
	toolResults := make([]message.ToolResult, len(toolCalls))

	type toolEntry struct {
		index    int
		tool     tools.BaseTool
		toolCall message.ToolCall
	}
	entries := make([]toolEntry, len(toolCalls))
	for i, tc := range toolCalls {
		entries[i] = toolEntry{i, slowTool, tc}
	}

	// Cancel after 50ms
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	go func() {
		permCtx, permCancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		for _, entry := range entries {
			wg.Add(1)
			go func(e toolEntry) {
				defer wg.Done()
				type runResult struct {
					resp tools.ToolResponse
					err  error
				}
				ch := make(chan runResult, 1)
				go func() {
					r, err := e.tool.Run(permCtx, tools.ToolCall{ID: e.toolCall.ID, Name: e.toolCall.Name, Input: e.toolCall.Input})
					ch <- runResult{r, err}
				}()
				select {
				case <-permCtx.Done():
					return
				case res := <-ch:
					toolResults[e.index] = message.ToolResult{
						ToolCallID: e.toolCall.ID,
						Name:       e.toolCall.Name,
						Content:    res.resp.Content,
					}
				}
			}(entry)
		}
		wg.Wait()
		permCancel()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("user cancellation should complete quickly, but hung")
	}
}
