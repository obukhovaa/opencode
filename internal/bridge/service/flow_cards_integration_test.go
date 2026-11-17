package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/slack"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/stretchr/testify/require"
)

func TestFlowCardSlackJournalToNativeHTTPAndRestart(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var requests []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		paths = append(paths, r.URL.Path)
		requests = append(requests, r.Form)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"123.4","text":"progress"}`))
	}))
	defer server.Close()
	s, _, c := newFlowCardService(t)
	ctx := context.Background()
	adapter, err := slack.New(slack.Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"}, slack.Options{APIURL: server.URL + "/", HTTPClient: server.Client()})
	require.NoError(t, err)
	s.adapters[adapterKey("slack", "default")] = adapter
	s.store = &failOnceFlowBindingStore{Store: s.store}
	require.NoError(t, s.store.DeleteBindingByPeer(ctx, "proj", "slack", "default", "D1"))
	_, err = s.store.UpsertBinding(ctx, store.Binding{ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "C1", SessionID: "S1"})
	require.NoError(t, err)
	_, err = s.SetToolVerbosity("full")
	require.NoError(t, err)
	start := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.step", StepID: "review", Agent: "reviewer", Status: "running", OccurredAt: 10000})
	s.deliverSessionFlowEvent(ctx, start)
	_, err = s.store.GetBinding(ctx, "proj", "slack", "default", "C1")
	require.NoError(t, err)
	s.deliverSessionFlowEvent(ctx, start)
	binding, err := s.store.GetBinding(ctx, "proj", "slack", "default", "C1|123.4")
	require.NoError(t, err)
	require.Equal(t, "S1", binding.SessionID)
	step := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.step", StepID: "review", Status: "completed", Output: `{"summary":"Reviewed","ok":true}`, IsStructOutput: true, OccurredAt: 15000})
	final := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.completed", OccurredAt: 16000})
	s.deliverSessionFlowEvent(ctx, final)
	mu.Lock()
	require.Equal(t, []string{"/chat.postMessage", "/chat.update"}, paths)
	require.Contains(t, requests[1].Get("blocks"), "rich_text_preformatted")
	require.Contains(t, requests[1].Get("blocks"), `"type":"plan"`)
	require.Equal(t, "123.4", requests[1].Get("ts"))
	mu.Unlock()
	card, err := s.app.FlowRuns.GetCard(ctx, c.run.ID, flowCardRecipient(binding.AsPeerRef()))
	require.NoError(t, err)
	require.Equal(t, "C1\x00123.4", card.MessageToken)
	require.Equal(t, final.Sequence, card.Sequence)
	var progress bridge.FlowProgress
	require.NoError(t, json.Unmarshal(card.State, &progress))
	require.Equal(t, "Reviewed", progress.Summary)
	fresh := &Service{app: s.app, store: s.store, sessionFlows: c, cfg: s.cfg, projectID: s.projectID, adapters: s.adapters}
	_, err = fresh.SetToolVerbosity("full")
	require.NoError(t, err)
	fresh.deliverSessionFlowEvent(ctx, step)
	mu.Lock()
	require.Len(t, paths, 2)
	mu.Unlock()
}

type failOnceFlowBindingStore struct {
	store.Store
	failed bool
}

func (s *failOnceFlowBindingStore) UpdateBindingPeerID(ctx context.Context, project, channel, identity, old, next string) error {
	if !s.failed {
		s.failed = true
		return errors.New("transient binding write failure")
	}
	return s.Store.UpdateBindingPeerID(ctx, project, channel, identity, old, next)
}
