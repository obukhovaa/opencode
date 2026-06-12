package bridge

import "testing"

func TestNewToolCallHint(t *testing.T) {
	params := map[string]string{"command": "ls -la"}
	h := NewToolCallHint("bash", "a8c2f1", params)
	if h.Kind != RenderKindToolCall {
		t.Errorf("Kind = %v; want RenderKindToolCall", h.Kind)
	}
	if h.ToolName != "bash" || h.CallID != "a8c2f1" {
		t.Errorf("tool/callID mismatch: %+v", h)
	}
	if h.Status != "pending" {
		t.Errorf("Status = %q; want pending", h.Status)
	}
	if h.Params["command"] != "ls -la" {
		t.Errorf("Params not propagated: %+v", h.Params)
	}
	// Result/list/table fields must be zero-valued.
	if h.Preview != "" || h.DurationMs != 0 || len(h.Headers) != 0 || len(h.Items) != 0 {
		t.Errorf("unrelated fields not zero-valued: %+v", h)
	}
}

func TestNewToolResultHint(t *testing.T) {
	h := NewToolResultHint("bash", "a8c2f1", "ok", "12 lines of output", 1400)
	if h.Kind != RenderKindToolResult {
		t.Errorf("Kind = %v; want RenderKindToolResult", h.Kind)
	}
	if h.Status != "ok" || h.Preview != "12 lines of output" || h.DurationMs != 1400 {
		t.Errorf("result fields mismatch: %+v", h)
	}
	if h.Params != nil {
		t.Errorf("Params should be nil for result, got %+v", h.Params)
	}
}

func TestNewTableHint(t *testing.T) {
	headers := []string{"ID", "Title"}
	rows := [][]string{{"s1", "Test"}, {"s2", "Run"}}
	h := NewTableHint(headers, rows)
	if h.Kind != RenderKindTable {
		t.Errorf("Kind = %v; want RenderKindTable", h.Kind)
	}
	if len(h.Headers) != 2 || h.Headers[0] != "ID" {
		t.Errorf("Headers mismatch: %+v", h.Headers)
	}
	if len(h.Rows) != 2 || h.Rows[1][1] != "Run" {
		t.Errorf("Rows mismatch: %+v", h.Rows)
	}
}

func TestNewListHint(t *testing.T) {
	items := []ListItem{
		{Label: "coder", Marker: "active"},
		{Label: "architect"},
	}
	h := NewListHint("Available agents", items, "active")
	if h.Kind != RenderKindList {
		t.Errorf("Kind = %v; want RenderKindList", h.Kind)
	}
	if h.Title != "Available agents" {
		t.Errorf("Title = %q", h.Title)
	}
	if h.ActiveLabel != "active" {
		t.Errorf("ActiveLabel = %q", h.ActiveLabel)
	}
	if len(h.Items) != 2 || h.Items[0].Marker != "active" {
		t.Errorf("Items mismatch: %+v", h.Items)
	}
}

func TestNewStatusHint(t *testing.T) {
	h := NewStatusHint("Aborted")
	if h.Kind != RenderKindStatus {
		t.Errorf("Kind = %v; want RenderKindStatus", h.Kind)
	}
	if h.Body != "Aborted" {
		t.Errorf("Body = %q", h.Body)
	}
}

func TestErrRenderUnsupported(t *testing.T) {
	err := ErrRenderUnsupported
	if err.Error() == "" {
		t.Error("ErrRenderUnsupported has no message")
	}
}
