package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/session"
)

func TestShouldAutoApprove(t *testing.T) {
	tests := []struct {
		name  string
		rules []APIPermissionRule
		want  bool
	}{
		{"nil", nil, false},
		{"empty", []APIPermissionRule{}, false},
		{
			"wildcard allow",
			[]APIPermissionRule{{Permission: "*", Pattern: "*", Action: "allow"}},
			true,
		},
		{
			"wildcard deny",
			[]APIPermissionRule{{Permission: "*", Pattern: "*", Action: "deny"}},
			false,
		},
		{
			"wildcard ask",
			[]APIPermissionRule{{Permission: "*", Pattern: "*", Action: "ask"}},
			false,
		},
		{
			"specific allow not honored",
			[]APIPermissionRule{{Permission: "bash", Pattern: "git *", Action: "allow"}},
			false,
		},
		{
			"wildcard allow among other rules",
			[]APIPermissionRule{
				{Permission: "bash", Pattern: "rm -rf *", Action: "deny"},
				{Permission: "*", Pattern: "*", Action: "allow"},
			},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAutoApprove(tt.rules); got != tt.want {
				t.Fatalf("shouldAutoApprove() = %v, want %v", got, tt.want)
			}
		})
	}
}

func newSessionTestServer(sessions session.Service) *Server {
	return &Server{app: &app.App{Sessions: sessions}}
}

func patchSession(t *testing.T, s *Server, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/session/"+id, bytes.NewReader([]byte(body)))
	req.SetPathValue("sessionID", id)
	// Set the directory header so resolveDirectory doesn't touch global config.
	req.Header.Set("X-Opencode-Directory", "/tmp")
	w := httptest.NewRecorder()
	s.handleSessionUpdate(w, req)
	return w
}

// TestHandleSessionUpdate_TitleMarksUserSet verifies the title branch now goes
// through Rename (which marks the session user-titled).
func TestHandleSessionUpdate_TitleMarksUserSet(t *testing.T) {
	stub := &stubSessions{byID: map[string]session.Session{"sess-1": {ID: "sess-1", Title: "Old"}}}
	s := newSessionTestServer(stub)

	w := patchSession(t, s, "sess-1", `{"title":"My Title"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var resp APISession
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Title != "My Title" {
		t.Errorf("response title = %q, want %q", resp.Title, "My Title")
	}
	if got := stub.byID["sess-1"]; !got.UserSetTitle || got.Title != "My Title" {
		t.Errorf("stored session = {%q, userSet=%v}, want {My Title, true}", got.Title, got.UserSetTitle)
	}
}

// TestHandleSessionUpdate_EmptyTitleIsBadRequest verifies the intentional
// contract change: an empty/whitespace title is now rejected with 400.
func TestHandleSessionUpdate_EmptyTitleIsBadRequest(t *testing.T) {
	stub := &stubSessions{byID: map[string]session.Session{"sess-1": {ID: "sess-1", Title: "Old"}}}
	s := newSessionTestServer(stub)

	w := patchSession(t, s, "sess-1", `{"title":"   "}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	if got := stub.byID["sess-1"]; got.Title != "Old" || got.UserSetTitle {
		t.Errorf("stored session = {%q, userSet=%v}, want {Old, false} (unchanged)", got.Title, got.UserSetTitle)
	}
}

// TestHandleSessionUpdate_PermissionOnlyLeavesTitle verifies a PATCH without a
// title (permission-only) does not touch the title or the user-titled mark.
func TestHandleSessionUpdate_PermissionOnlyLeavesTitle(t *testing.T) {
	stub := &stubSessions{byID: map[string]session.Session{"sess-1": {ID: "sess-1", Title: "Old"}}}
	s := newSessionTestServer(stub)

	w := patchSession(t, s, "sess-1", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if got := stub.byID["sess-1"]; got.Title != "Old" || got.UserSetTitle {
		t.Errorf("stored session = {%q, userSet=%v}, want {Old, false} (unchanged)", got.Title, got.UserSetTitle)
	}
}
