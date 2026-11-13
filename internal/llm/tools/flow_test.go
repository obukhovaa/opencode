package tools

import (
	"context"
	"fmt"
	"testing"
)

func TestFlowToolContextAndFallback(t *testing.T) {
	calls := 0
	fallback := func(context.Context, string, FlowRequest) error { calls++; return nil }
	tool := NewFlowTool(fallback)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "root")
	call := ToolCall{Input: `{"operation":"create","complexity":"S","definition":"flow: {}"}`}
	resp, err := tool.Run(ctx, call)
	if err != nil || resp.IsError || calls != 1 {
		t.Fatalf("fallback calls=%d result=%+v err=%v", calls, resp, err)
	}
	ctx = WithFlowSubmitter(ctx, func(_ context.Context, id string, r FlowRequest) error {
		if id != "root" {
			t.Fatal(id)
		}
		return fmt.Errorf("denied")
	})
	resp, err = tool.Run(ctx, call)
	if err != nil || !resp.IsError || calls != 1 {
		t.Fatalf("context override result=%+v calls=%d", resp, calls)
	}
	resp, _ = NewFlowTool().Run(context.Background(), call)
	if !resp.IsError {
		t.Fatal("unwired tool accepted plan")
	}
}
func TestFlowTaskLimitsInheritedAndReleased(t *testing.T) {
	ctx := WithFlowTaskTurns(WithFlowTaskLimit(context.Background(), 2), 20)
	release1, ok := AcquireFlowTask(ctx)
	if !ok {
		t.Fatal("first rejected")
	}
	defer release1()
	child := InheritFlowLimits(context.Background(), ctx)
	release2, ok := AcquireFlowTask(child)
	if !ok {
		t.Fatal("second rejected")
	}
	if _, ok := AcquireFlowTask(child); ok {
		t.Fatal("parallel cap ignored")
	}
	if FlowTaskTurns(child) != 20 {
		t.Fatal("turn cap not inherited")
	}
	release2()
	release3, ok := AcquireFlowTask(ctx)
	if !ok {
		t.Fatal("slot not released")
	}
	release3()
}
