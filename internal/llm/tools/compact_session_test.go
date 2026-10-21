package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func runCompactSession(t *testing.T, tool *CompactSessionTool, input string) ToolResponse {
	t.Helper()
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "S1")
	resp, err := tool.Run(ctx, ToolCall{Name: CompactSessionToolName, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func fixedCounter(n int64) SessionTokenCounter {
	return func(context.Context, string) (int64, error) { return n, nil }
}

// Below the floor the call is answered "skipped" and its result carries no
// request: nothing is stored, so nothing is compacted.
func TestCompactSession_BelowFloorStoresNothing(t *testing.T) {
	tool := NewCompactSessionTool(150_000)
	tool.BindCounter(fixedCounter(90_000))

	resp := runCompactSession(t, tool, `{"keep":"open: review the change"}`)
	if resp.IsError {
		t.Fatalf("skipped call returned an error: %s", resp.Content)
	}
	if want := "skipped: 90000 tokens < floor 150000"; resp.Content != want {
		t.Errorf("content = %q, want %q", resp.Content, want)
	}
	if resp.Metadata != "" {
		t.Errorf("metadata = %q, want none", resp.Metadata)
	}
	if _, ok := ParseCompactSessionRequest(resp.Metadata); ok {
		t.Error("a skipped call parsed as a stored request")
	}
}

func TestCompactSession_AboveFloorStoresTheRequest(t *testing.T) {
	tool := NewCompactSessionTool(150_000)
	var gotSession string
	tool.BindCounter(func(_ context.Context, sessionID string) (int64, error) {
		gotSession = sessionID
		return 180_000, nil
	})

	resp := runCompactSession(t, tool, `{"keep":"  open: review the change  "}`)
	if resp.IsError {
		t.Fatalf("error: %s", resp.Content)
	}
	if gotSession != "S1" {
		t.Errorf("counter got session %q, want S1", gotSession)
	}
	if !strings.Contains(resp.Content, "180000 tokens") {
		t.Errorf("content = %q, want the context size", resp.Content)
	}
	req, ok := ParseCompactSessionRequest(resp.Metadata)
	if !ok {
		t.Fatalf("metadata %q holds no request", resp.Metadata)
	}
	if req.Tokens != 180_000 || req.Keep != "open: review the change" {
		t.Errorf("request = %+v, want tokens 180000 and the trimmed keep text", req)
	}
}

func TestCompactSession_NoFloorAndNoKeep(t *testing.T) {
	tool := NewCompactSessionTool(0)
	tool.BindCounter(fixedCounter(10))
	resp := runCompactSession(t, tool, ``)
	req, ok := ParseCompactSessionRequest(resp.Metadata)
	if !ok || req.Keep != "" {
		t.Fatalf("request = %+v, ok %v; want a stored request without notes", req, ok)
	}
}

func TestCompactSession_Errors(t *testing.T) {
	cases := []struct {
		name    string
		counter SessionTokenCounter
		ctx     context.Context
		input   string
		want    string
	}{
		{"unbound counter", nil, context.WithValue(context.Background(), SessionIDContextKey, "S1"), `{}`, "not available"},
		{"no session", fixedCounter(1), context.Background(), `{}`, "session context required"},
		{"bad input", fixedCounter(1), context.WithValue(context.Background(), SessionIDContextKey, "S1"), `{`, "error parsing parameters"},
		{"count fails", func(context.Context, string) (int64, error) { return 0, errors.New("boom") },
			context.WithValue(context.Background(), SessionIDContextKey, "S1"), `{}`, "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewCompactSessionTool(0)
			if tc.counter != nil {
				tool.BindCounter(tc.counter)
			}
			resp, err := tool.Run(tc.ctx, ToolCall{Name: CompactSessionToolName, Input: tc.input})
			if err != nil {
				t.Fatal(err)
			}
			if !resp.IsError || !strings.Contains(resp.Content, tc.want) {
				t.Errorf("resp = %+v, want an error containing %q", resp, tc.want)
			}
			if _, ok := ParseCompactSessionRequest(resp.Metadata); ok {
				t.Error("a failed call stored a request")
			}
		})
	}
}

func TestParseCompactSessionRequest(t *testing.T) {
	for _, md := range []string{"", "{", `{"compaction_requested":false}`, `{"other":1}`} {
		if _, ok := ParseCompactSessionRequest(md); ok {
			t.Errorf("ParseCompactSessionRequest(%q) = ok, want not a request", md)
		}
	}
}
