package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	registry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/flowrun"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

type sessionFlowTestSessions struct {
	session.Service
	mu      sync.Mutex
	rows    map[string]session.Session
	creates int
}

func (s *sessionFlowTestSessions) Get(_ context.Context, id string) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.rows[id]
	if !ok {
		return v, sql.ErrNoRows
	}
	return v, nil
}
func (s *sessionFlowTestSessions) CreateWithID(_ context.Context, id, title string) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creates++
	v := session.Session{ID: id, RootSessionID: id, Title: title}
	s.rows[id] = v
	return v, nil
}
func (s *sessionFlowTestSessions) SetAgentName(_ context.Context, id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.rows[id]
	v.AgentName = name
	s.rows[id] = v
	return nil
}

type sessionFlowTestRegistry struct{ registry.Registry }

func (sessionFlowTestRegistry) Get(id string) (registry.AgentInfo, bool) {
	return registry.AgentInfo{ID: id, Mode: config.AgentModeAgent}, id == "coder"
}

type sessionFlowTestBridge struct {
	existing, bound string
	reject          bool
}

func (*sessionFlowTestBridge) RegisterRoutes(*http.ServeMux) {}
func (b *sessionFlowTestBridge) ResolveSessionFlowConversation(context.Context, bridge.PeerRef, string) (string, error) {
	return b.existing, nil
}
func (b *sessionFlowTestBridge) AdmitSessionFlow(_ context.Context, id string, _ bridge.PeerRef, _ string) error {
	b.bound = id
	b.existing = id
	return nil
}
func (*sessionFlowTestBridge) ReleaseSessionFlow(context.Context, string, string) error { return nil }
func (b *sessionFlowTestBridge) ValidateSessionFlowOrigin(context.Context, string, bridge.PeerRef, string) error {
	if b.reject {
		return errors.New("foreign job")
	}
	return nil
}
func (*sessionFlowTestBridge) BridgeSessionFlowContext(ctx context.Context, _ string, _ bridge.PeerRef, _ string) (context.Context, error) {
	return ctx, nil
}
func sessionFlowAPIFixture() (*Server, *sessionFlowTestSessions, *sessionFlowTestBridge) {
	sessions := &sessionFlowTestSessions{rows: map[string]session.Session{"original": {ID: "original", RootSessionID: "original", AgentName: "coder"}}}
	b := &sessionFlowTestBridge{existing: "original"}
	return &Server{app: &app.App{Sessions: sessions, Registry: sessionFlowTestRegistry{}}, sessionFlowBridge: b}, sessions, b
}
func TestSessionFlowAdmissionPreservesExistingConversation(t *testing.T) {
	s, sessions, b := sessionFlowAPIFixture()
	req := httptest.NewRequest("POST", "/session-flow/admit", strings.NewReader(`{"jobId":"daemon","agent":"coder","peer":{"channel":"external","identity":"c3","peerId":"aid:daemon:conversation"}}`))
	w := httptest.NewRecorder()
	s.handleSessionFlowAdmit(w, req)
	if w.Code != 200 || b.bound != "original" || sessions.creates != 0 {
		t.Fatalf("code=%d body=%s bound=%s creates=%d", w.Code, w.Body.String(), b.bound, sessions.creates)
	}
}
func TestSessionFlowAdmissionRejectsConversationSwitch(t *testing.T) {
	s, sessions, b := sessionFlowAPIFixture()
	req := httptest.NewRequest("POST", "/session-flow/admit", strings.NewReader(`{"sessionId":"other","jobId":"daemon","agent":"coder","peer":{"channel":"external","identity":"c3","peerId":"aid:daemon:conversation"}}`))
	w := httptest.NewRecorder()
	s.handleSessionFlowAdmit(w, req)
	if w.Code != 409 || b.bound != "" || sessions.creates != 0 {
		t.Fatalf("code=%d effects=%+v", w.Code, b)
	}
}
func TestSessionFlowOriginRefusesForeignJobBeforeExecution(t *testing.T) {
	s, _, b := sessionFlowAPIFixture()
	b.reject = true
	req := httptest.NewRequest("POST", "/session/original/flow", strings.NewReader(`{"operation":"plan","goal":"work","origin":{"jobId":"foreign","peer":{"channel":"external","identity":"c3","peerId":"aid:d:c"}}}`))
	req.SetPathValue("sessionID", "original")
	w := httptest.NewRecorder()
	s.handleSessionFlowPost(w, req)
	if w.Code != 403 {
		t.Fatalf("code=%d %s", w.Code, w.Body.String())
	}
	if s.app.FlowBusy("original") {
		t.Fatal("foreign origin started a run")
	}
}
func TestSessionFlowOperationsRejectChildSession(t *testing.T) {
	s, sessions, _ := sessionFlowAPIFixture()
	sessions.rows["child"] = session.Session{ID: "child", RootSessionID: "original"}
	req := httptest.NewRequest("POST", "/session/child/flow", strings.NewReader(`{"operation":"plan","goal":"nested"}`))
	req.SetPathValue("sessionID", "child")
	w := httptest.NewRecorder()
	s.handleSessionFlowPost(w, req)
	if w.Code != 403 {
		t.Fatalf("code=%d", w.Code)
	}
}
func TestSessionFlowReplayRejectsInvalidCursor(t *testing.T) {
	for _, cursor := range []string{"12", "run:-1", "run:nope", ":1"} {
		s, _, _ := sessionFlowAPIFixture()
		req := httptest.NewRequest("GET", "/session/original/flow/events?after="+cursor, nil)
		req.SetPathValue("sessionID", "original")
		w := httptest.NewRecorder()
		s.handleSessionFlowEvents(w, req)
		if w.Code != 400 {
			t.Fatalf("cursor=%s code=%d", cursor, w.Code)
		}
	}
}
func TestSessionFlowAdmissionExcludesStandaloneAndRecycle(t *testing.T) {
	svc := newPoolStubFlowService(true)
	s, server := newPoolTestServer(t, poolTestOpts{svc: svc, bound: "https://example.test/workspace", idleResetGrace: time.Hour})
	s.flowRunner.sessionConversation = &s.sessionFlowAdmission
	next := &sessionFlowAdmission{SessionID: "conversation", JobID: "job"}
	if claimed, err := s.claimConversation(next); err != nil || !claimed {
		t.Fatalf("claim=%t %v", claimed, err)
	}
	if _, err := s.flowRunner.StartWithOptions(context.Background(), "test", nil, false, flowStartOptions{}); !errors.Is(err, errFlowAlreadyRunning) {
		t.Fatalf("standalone start=%v", err)
	}
	if got := s.flowRunner.latchExclusive(&s.poolDraining); got != latchInFlight {
		t.Fatalf("recycle=%v", got)
	}
	resp, err := server.Client().Get(server.URL + "/global/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var health struct {
		Pool struct {
			Mode         string `json:"mode"`
			Conversation string `json:"conversationSessionId"`
		}
	}
	if err = json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health.Pool.Mode != "busy" || health.Pool.Conversation != "conversation" {
		t.Fatalf("health=%+v", health)
	}
	s.forgetConversation(next)
	if s.flowRunner.InFlight() {
		t.Fatal("released admission remains busy")
	}
}
func TestSessionFlowRoutesRequirePassword(t *testing.T) {
	t.Setenv("OPENCODE_SERVER_PASSWORD", "secret")
	s := NewServer(&app.App{}, ServerOptions{})
	for _, path := range []string{"/session-flow/capabilities", "/session-flow/admit", "/session-flow/release", "/session/root/flow", "/session/root/flow/events"} {
		for _, method := range []string{"GET", "POST"} {
			w := httptest.NewRecorder()
			s.httpSrv.Handler.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(`{}`)))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s code=%d", method, path, w.Code)
			}
		}
	}
}

func TestSessionFlowAdmissionRacesStandaloneStartAtomically(t *testing.T) {
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		fr := newFlowRunner(newPoolStubFlowService(true))
		fr.validateFlowID = nil
		fr.poolMode = true
		s := &Server{poolMode: true, flowRunner: fr}
		fr.sessionConversation = &s.sessionFlowAdmission
		gate := make(chan struct{})
		results := make(chan bool, 2)
		go func() {
			<-gate
			claimed, err := s.claimConversation(&sessionFlowAdmission{SessionID: "conversation", JobID: "job"})
			results <- claimed && err == nil
		}()
		go func() {
			<-gate
			_, err := fr.StartWithOptions(ctx, "test", nil, false, flowStartOptions{})
			results <- err == nil
		}()
		close(gate)
		a, b := <-results, <-results
		cancel()
		fr.Abort()
		if a == b {
			t.Fatalf("iteration %d: admission and standalone results=%t,%t", i, a, b)
		}
	}
}

type sessionFlowEventRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *sessionFlowEventRecorder) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.cancel()
	return n, err
}
func TestSessionFlowSSEPreservesSeparateEventNamespace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan pubsub.Event[app.FlowRunEvent], 1)
	ch <- pubsub.Event[app.FlowRunEvent]{Payload: app.FlowRunEvent{ID: "event", Type: "session.flow.completed", RunID: "run", ConversationID: "conversation", Status: "completed", Sequence: 9}}
	w := &sessionFlowEventRecorder{httptest.NewRecorder(), cancel}
	streamLoop(ctx, w, w, nil, nil, nil, nil, nil, nil, ch)
	text := w.Body.String()
	if !strings.Contains(text, `"type":"session.flow.completed"`) || !strings.Contains(text, `"runId":"run"`) || strings.Contains(text, `"type":"flow.completed"`) {
		t.Fatalf("event shape: %s", text)
	}
}

func TestSessionFlowPoolReadsRefuseForeignConversation(t *testing.T) {
	s, _, _ := sessionFlowAPIFixture()
	s.poolMode = true
	s.sessionFlowAdmission.Store(&sessionFlowAdmission{SessionID: "owned", JobID: "job"})
	for _, events := range []bool{false, true} {
		req := httptest.NewRequest("GET", "/session/original/flow", nil)
		req.SetPathValue("sessionID", "original")
		w := httptest.NewRecorder()
		if events {
			s.handleSessionFlowEvents(w, req)
		} else {
			s.handleSessionFlowGet(w, req)
		}
		if w.Code != 403 {
			t.Fatalf("events=%t code=%d", events, w.Code)
		}
	}
}
func TestSessionFlowAdmissionUsesExistingAgentWhenOmitted(t *testing.T) {
	s, _, b := sessionFlowAPIFixture()
	req := httptest.NewRequest("POST", "/session-flow/admit", strings.NewReader(`{"jobId":"daemon","peer":{"channel":"external","identity":"c3","peerId":"aid:daemon:conversation"}}`))
	w := httptest.NewRecorder()
	s.handleSessionFlowAdmit(w, req)
	if w.Code != 200 || b.bound != "original" {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSessionFlowInvocationIDBoundsLegacyRoots(t *testing.T) {
	root, key := strings.Repeat("r", 255), strings.Repeat("k", 200)
	first := sessionFlowInvocationID(root, key)
	if len(first) > 255 || first != sessionFlowInvocationID(root, key) || first == sessionFlowInvocationID(root, "other") {
		t.Fatalf("invalid invocation id %q", first)
	}
}

func TestSessionFlowAdmissionAfterResetDoesNotResurrectHistory(t *testing.T) {
	s, sessions, b := sessionFlowAPIFixture()
	b.existing = ""
	admit := func() string {
		req := httptest.NewRequest("POST", "/session-flow/admit", strings.NewReader(`{"jobId":"daemon","agent":"coder","peer":{"channel":"external","identity":"c3","peerId":"aid:daemon:conversation"}}`))
		w := httptest.NewRecorder()
		s.handleSessionFlowAdmit(w, req)
		if w.Code != 200 {
			t.Fatalf("admit=%d %s", w.Code, w.Body.String())
		}
		return b.bound
	}
	first := admit()
	b.existing = first
	if retry := admit(); retry != first {
		t.Fatal("existing binding did not preserve retry identity")
	}
	b.existing = "" // /reset forgets the binding, while prior history remains stored.
	second := admit()
	if second == first || sessions.creates != 2 {
		t.Fatalf("first=%s second=%s creates=%d", first, second, sessions.creates)
	}
}

func TestSessionFlowConcurrentAdmissionKeepsOneConversation(t *testing.T) {
	s, sessions, b := sessionFlowAPIFixture()
	b.existing = ""
	start := make(chan struct{})
	results := make(chan string, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			req := httptest.NewRequest("POST", "/session-flow/admit", strings.NewReader(`{"jobId":"daemon","agent":"coder","peer":{"channel":"external","identity":"c3","peerId":"aid:daemon:conversation"}}`))
			w := httptest.NewRecorder()
			s.handleSessionFlowAdmit(w, req)
			if w.Code != 200 {
				results <- w.Body.String()
				return
			}
			var result struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &result)
			results <- result.SessionID
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first == "" || first != second || sessions.creates != 1 {
		t.Fatalf("first=%s second=%s creates=%d", first, second, sessions.creates)
	}
}

type alreadyAdmittedSessionFlowBridge struct{ *sessionFlowTestBridge }

func (*alreadyAdmittedSessionFlowBridge) IsSessionFlowAdmitted(string, bridge.PeerRef, string) bool {
	return true
}
func TestSessionFlowRepeatAdmissionDoesNotReclaimDeliveryLease(t *testing.T) {
	s, _, b := sessionFlowAPIFixture()
	s.sessionFlowBridge = &alreadyAdmittedSessionFlowBridge{b}
	// A backend with no connection makes any accidental lease query fail: an
	// already-admitted local refresh must not contend with terminal delivery.
	s.app.FlowRuns = flowrun.New(nil, nil, "", "sqlite")
	req := httptest.NewRequest("POST", "/session-flow/admit", strings.NewReader(`{"jobId":"daemon","agent":"coder","peer":{"channel":"external","identity":"c3","peerId":"aid:daemon:conversation"}}`))
	w := httptest.NewRecorder()
	s.handleSessionFlowAdmit(w, req)
	if w.Code != 200 {
		t.Fatalf("repeat admission=%d %s", w.Code, w.Body.String())
	}
}
