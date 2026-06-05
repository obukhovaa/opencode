package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
)

// These tests exercise the validation paths of the agent handlers that
// run before any s.app access. They use a Server with a nil app on
// purpose — the validation errors must be reachable without spinning
// up the full app graph (registry, DB, agents, etc.).
//
// Happy-path behavior (Active flag, mode filtering, successful select)
// is covered by integration tests against a real opencode binary.

func TestHandleAgentList_InvalidMode(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/agent?mode=primary", nil)
	rr := httptest.NewRecorder()

	s.handleAgentList(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid mode filter") {
		t.Fatalf("body: missing 'invalid mode filter' hint: %s", rr.Body.String())
	}
}

func TestHandleAgentSelect_InvalidJSON(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/agent/select", strings.NewReader("not-json"))
	rr := httptest.NewRecorder()

	s.handleAgentSelect(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", rr.Code)
	}
}

func TestHandleAgentSelect_MissingID(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/agent/select", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	s.handleAgentSelect(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "id is required") {
		t.Fatalf("body: missing 'id is required' hint: %s", rr.Body.String())
	}
}

func TestHandleAgentModelSelect_InvalidJSON(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/agent/model/select", strings.NewReader("not-json"))
	rr := httptest.NewRecorder()

	s.handleAgentModelSelect(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", rr.Code)
	}
}

func TestHandleAgentModelSelect_MissingFields(t *testing.T) {
	s := &Server{}
	cases := []struct {
		name string
		body string
	}{
		{"empty body", `{}`},
		{"only provider", `{"providerID":"anthropic"}`},
		{"only model", `{"modelID":"claude-46-sonnet"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/agent/model/select", strings.NewReader(tc.body))
			rr := httptest.NewRecorder()
			s.handleAgentModelSelect(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status: want 400, got %d (body: %s)", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestHandleAgentModelSelect_UnknownModel(t *testing.T) {
	s := &Server{}
	body := `{"providerID":"anthropic","modelID":"does-not-exist-12345"}`
	req := httptest.NewRequest(http.MethodPost, "/agent/model/select", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.handleAgentModelSelect(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "unknown model") {
		t.Fatalf("body: missing 'unknown model' hint: %s", rr.Body.String())
	}
}

func TestHandleAgentModelSelect_ProviderMismatch(t *testing.T) {
	// Pick any real model from the supported set so we hit the
	// provider-check branch instead of the unknown-model branch.
	var modelID, realProvider string
	for id, m := range models.SupportedModels {
		modelID = string(id)
		realProvider = string(m.Provider)
		break
	}
	if modelID == "" {
		t.Skip("no supported models registered in this build")
	}
	wrongProvider := realProvider + "-zzz" // guaranteed mismatch

	s := &Server{}
	body := `{"providerID":"` + wrongProvider + `","modelID":"` + modelID + `"}`
	req := httptest.NewRequest(http.MethodPost, "/agent/model/select", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.handleAgentModelSelect(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "does not match") {
		t.Fatalf("body: missing 'does not match' hint: %s", rr.Body.String())
	}
}
