package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/heartbeat"
)

type fakeHeartbeatConfigurer struct {
	session string
	applied *heartbeat.Command
	reads   int
}

func (f *fakeHeartbeatConfigurer) HeartbeatStatus(_ context.Context, sessionID string) (string, error) {
	f.session = sessionID
	f.reads++
	return "status", nil
}

func (f *fakeHeartbeatConfigurer) ApplyHeartbeat(_ context.Context, sessionID string, cmd heartbeat.Command) (string, error) {
	f.session = sessionID
	f.applied = &cmd
	return "applied", nil
}

func runHeartbeatTool(t *testing.T, c HeartbeatConfigurer, input string) ToolResponse {
	t.Helper()
	tool := NewHeartbeatTool(func() HeartbeatConfigurer { return c })
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "S1")
	resp, err := tool.Run(ctx, ToolCall{Name: HeartbeatToolName, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHeartbeatToolSetsTypedSettings(t *testing.T) {
	f := &fakeHeartbeatConfigurer{}
	resp := runHeartbeatTool(t, f, `{"state":"on","every":"30m","hours":"05-21","days":"weekdays","file":"notes/agenda.md"}`)
	if resp.IsError || resp.Content != "applied" {
		t.Fatalf("response %+v", resp)
	}
	c := f.applied
	if f.session != "S1" || c == nil || !c.On || *c.Every != 30*time.Minute ||
		*c.Window != (heartbeat.Window{Start: 300, End: 1260}) || !*c.Weekdays || *c.AgendaFile != "notes/agenda.md" {
		t.Fatalf("command %+v", c)
	}
	if c.Model != nil {
		t.Fatal("an omitted setting must stay untouched")
	}
}

func TestHeartbeatToolReadsWhenNothingChanges(t *testing.T) {
	f := &fakeHeartbeatConfigurer{}
	for _, in := range []string{"", "{}", `{"action":"get","every":"2h"}`} {
		if resp := runHeartbeatTool(t, f, in); resp.Content != "status" {
			t.Fatalf("%q: %+v", in, resp)
		}
	}
	if f.applied != nil || f.reads != 3 {
		t.Fatalf("reads %d, applied %+v", f.reads, f.applied)
	}
}

func TestHeartbeatToolRefusesInvalidSettings(t *testing.T) {
	for _, in := range []string{
		`{"state":"sometimes"}`,
		`{"every":"2m"}`,
		`{"hours":"7am-11pm"}`,
		`{"file":"../outside.md"}`,
		`{"every":"30 m"}`,
	} {
		f := &fakeHeartbeatConfigurer{}
		resp := runHeartbeatTool(t, f, in)
		if !resp.IsError || !strings.Contains(resp.Content, "Nothing was changed") || f.applied != nil {
			t.Errorf("%s: %+v (applied %+v)", in, resp, f.applied)
		}
	}
}

func TestHeartbeatToolWithoutBridge(t *testing.T) {
	resp := runHeartbeatTool(t, nil, `{"state":"on"}`)
	if !resp.IsError || !strings.Contains(resp.Content, "daemon") {
		t.Fatalf("response %+v", resp)
	}
}
