package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
)

// newOrchestratorForHTTPTest reuses newOrchestratorForTest plus a
// freshly-loaded config (so config.UpdateCfgFile can persist mutations).
func newOrchestratorForHTTPTest(t *testing.T) (*Service, *httptest.Server) {
	t.Helper()
	svc, _ := newOrchestratorForTest(t)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Seed a config singleton for the identity-CRUD writeback path.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".opencode.json")
	if err := writeFile(cfgPath, []byte(`{"router":{}}`)); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	config.Reset()
	if _, err := config.Load(dir, false); err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	t.Cleanup(config.Reset)

	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return svc, server
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

// --- 404 bare paths --------------------------------------------------------

func TestBarePathsReturn404(t *testing.T) {
	_, server := newOrchestratorForHTTPTest(t)

	for _, path := range []string{"/send", "/bind", "/unbind", "/identities/slack", "/config/groups"} {
		req, _ := http.NewRequest(http.MethodPost, server.URL+path, nil)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("request %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404 (bare paths must not alias)", path, resp.StatusCode)
		}
	}
}

// --- /router/health --------------------------------------------------------

func TestHealthDisabledWhenNoChannelEnabled(t *testing.T) {

	svc, server := newOrchestratorForHTTPTest(t)
	_ = svc

	resp, err := server.Client().Get(server.URL + "/router/health")
	if err != nil {
		t.Fatalf("GET /router/health: %v", err)
	}
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != "disabled" {
		t.Errorf("status = %v, want disabled", got["status"])
	}
}

// --- /router/send ----------------------------------------------------------

func TestSendRejectsAutoBind(t *testing.T) {

	_, server := newOrchestratorForHTTPTest(t)

	body := `{"channel":"slack","identity":"default","peerId":"D1","text":"hi","autoBind":true}`
	resp, err := server.Client().Post(server.URL+"/router/send", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSendRequiresTextOrFiles(t *testing.T) {

	_, server := newOrchestratorForHTTPTest(t)

	body := `{"channel":"slack","identity":"default","peerId":"D1"}`
	resp, err := server.Client().Post(server.URL+"/router/send", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestSendNoAdapterReturnsBadGateway(t *testing.T) {

	_, server := newOrchestratorForHTTPTest(t)

	body := `{"channel":"slack","identity":"default","peerId":"D1","text":"hi"}`
	resp, err := server.Client().Post(server.URL+"/router/send", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

// --- /router/bind + /router/unbind -----------------------------------------

func TestBindRoundTripWithStubAdapter(t *testing.T) {

	svc, server := newOrchestratorForHTTPTest(t)

	ad := newStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	body := `{"sessionId":"S1","peers":[{"channel":"slack","identity":"default","peerId":"D1","mention":"<@U1>"}]}`
	resp, err := server.Client().Post(server.URL+"/router/bind", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /bind: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("bind status = %d", resp.StatusCode)
	}

	// Verify a binding now exists.
	b, err := svc.store.GetBinding(context.Background(), "proj", "slack", "default", "D1")
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if b.SessionID != "S1" || b.MentionHandle != "<@U1>" {
		t.Errorf("binding = %+v", b)
	}

	// Unbind.
	unbindBody := `{"sessionId":"S1"}`
	resp, err = server.Client().Post(server.URL+"/router/unbind", "application/json", strings.NewReader(unbindBody))
	if err != nil {
		t.Fatalf("POST /unbind: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("unbind status = %d", resp.StatusCode)
	}
}

func TestBindMissingSessionIDIs400(t *testing.T) {

	_, server := newOrchestratorForHTTPTest(t)
	resp, err := server.Client().Post(server.URL+"/router/bind", "application/json", strings.NewReader(`{"peers":[]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

// --- /router/identities/{channel} ------------------------------------------

func TestIdentityCRUDSlack(t *testing.T) {

	svc, server := newOrchestratorForHTTPTest(t)

	// Initially empty.
	resp, err := server.Client().Get(server.URL + "/router/identities/slack")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var listResp identityList
	_ = json.NewDecoder(resp.Body).Decode(&listResp)
	if len(listResp.Identities) != 0 {
		t.Errorf("initial identities = %d", len(listResp.Identities))
	}

	// POST to create.
	upsert := `{"id":"secondary","botToken":"xoxb-x","appToken":"xapp-x","enabled":true}`
	resp, err = server.Client().Post(server.URL+"/router/identities/slack", "application/json", strings.NewReader(upsert))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST identity = %d", resp.StatusCode)
	}

	// Verify cfg.Router was mutated in memory.
	if svc.cfg.Channels.Slack == nil || len(svc.cfg.Channels.Slack.Apps) != 1 {
		t.Errorf("cfg not updated: %+v", svc.cfg.Channels.Slack)
	}

	// DELETE.
	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/router/identities/slack/secondary", nil)
	resp, err = server.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("DELETE = %d", resp.StatusCode)
	}
	if svc.cfg.Channels.Slack != nil && len(svc.cfg.Channels.Slack.Apps) != 0 {
		t.Errorf("identity not removed: %+v", svc.cfg.Channels.Slack.Apps)
	}
}

func TestIdentityUpsertMissingTokensIs400(t *testing.T) {
	_, server := newOrchestratorForHTTPTest(t)
	body := `{"id":"new"}`
	resp, err := server.Client().Post(server.URL+"/router/identities/slack", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestIdentityDeleteUnknownIDIs400(t *testing.T) {
	_, server := newOrchestratorForHTTPTest(t)
	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/router/identities/slack/ghost", nil)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

// --- /router/config/groups -------------------------------------------------

func TestGroupsRoundTrip(t *testing.T) {

	svc, server := newOrchestratorForHTTPTest(t)

	// Seed a Mattermost identity first so the toggle has a target.
	svc.cfg.Channels.Mattermost = &bridge.MattermostChannelConfig{
		Enabled:   true,
		Instances: []bridge.MattermostIdentity{{ID: "default", Enabled: true, GroupsEnabled: false}},
	}

	body := `{"channel":"mattermost","identityId":"default","enabled":true}`
	resp, err := server.Client().Post(server.URL+"/router/config/groups", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST groups = %d", resp.StatusCode)
	}
	if !svc.cfg.Channels.Mattermost.Instances[0].GroupsEnabled {
		t.Errorf("groupsEnabled not flipped")
	}
}

func TestGroupsRequiresIdentityID(t *testing.T) {
	_, server := newOrchestratorForHTTPTest(t)
	body := `{"channel":"slack","enabled":true}`
	resp, err := server.Client().Post(server.URL+"/router/config/groups", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d (expected 400 per spec: per-identity scope required)", resp.StatusCode)
	}
}
