package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// stubBridgeSender records Send calls and lets tests configure return
// values per call. Mirrors the patterns from the bridge/service tests.
type stubBridgeSender struct {
	mu         sync.Mutex
	calls      []sendCall
	result     bridge.SendResult
	err        error
	boundPeers []bridge.PeerRef
}

type sendCall struct {
	Peer        bridge.PeerRef
	Text        string
	Mention     string
	Attachments []bridge.Attachment
}

func (s *stubBridgeSender) Send(_ context.Context, peer bridge.PeerRef, text, mention string, attachments []bridge.Attachment) (bridge.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, sendCall{Peer: peer, Text: text, Mention: mention, Attachments: attachments})
	return s.result, s.err
}

func (s *stubBridgeSender) BoundPeersSnapshot(_ context.Context) []bridge.PeerRef {
	return s.boundPeers
}

func (s *stubBridgeSender) Calls() []sendCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sendCall, len(s.calls))
	copy(out, s.calls)
	return out
}

// testCfg builds a router config snapshot with one configured identity
// per channel — the standard fixture for tool-shape tests.
func testCfg() *bridge.Config {
	return &bridge.Config{
		Channels: bridge.ChannelsConfig{
			Telegram: &bridge.TelegramChannelConfig{
				Enabled: true,
				Bots:    []bridge.TelegramIdentity{{ID: "default", Enabled: true}},
			},
			Slack: &bridge.SlackChannelConfig{
				Enabled: true,
				Apps:    []bridge.SlackIdentity{{ID: "default", Enabled: true}},
			},
			Mattermost: &bridge.MattermostChannelConfig{
				Enabled:   true,
				Instances: []bridge.MattermostIdentity{{ID: "default", Enabled: true}},
			},
		},
	}
}

func TestShouldRegisterRouterSend(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  *bridge.Config
		want bool
	}{
		{"nil cfg", nil, false},
		{"empty cfg", &bridge.Config{}, false},
		{"all channels enabled", testCfg(), true},
		{"channel enabled but no enabled identity", &bridge.Config{
			Channels: bridge.ChannelsConfig{
				Slack: &bridge.SlackChannelConfig{Enabled: true},
			},
		}, false},
		{"channel disabled, identity enabled", &bridge.Config{
			Channels: bridge.ChannelsConfig{
				Slack: &bridge.SlackChannelConfig{
					Apps: []bridge.SlackIdentity{{ID: "default", Enabled: true}},
				},
			},
		}, false},
	}
	for _, c := range cases {
		if got := ShouldRegisterRouterSend(c.cfg); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRouterSendInfoEnumeratesIdentities(t *testing.T) {
	t.Parallel()
	sender := &stubBridgeSender{}
	tool := NewRouterSendTool(RouterSendDeps{Sender: sender, Cfg: testCfg(), MediaRoot: t.TempDir()})
	info := tool.Info()
	if info.Name != RouterSendToolName {
		t.Errorf("Name = %q", info.Name)
	}
	for _, want := range []string{"telegram", "slack", "mattermost", "default"} {
		if !strings.Contains(info.Description, want) {
			t.Errorf("description missing %q:\n%s", want, info.Description)
		}
	}
}

func TestRouterSendDescriptionListsBoundPeers(t *testing.T) {
	t.Parallel()
	sender := &stubBridgeSender{
		boundPeers: []bridge.PeerRef{
			{Channel: "slack", Identity: "default", PeerID: "D012345"},
		},
	}
	tool := NewRouterSendTool(RouterSendDeps{Sender: sender, Cfg: testCfg(), MediaRoot: t.TempDir()})
	desc := tool.Info().Description
	if !strings.Contains(desc, "Currently bound peers") {
		t.Errorf("description missing 'Currently bound peers':\n%s", desc)
	}
	if !strings.Contains(desc, "slack:default:D012345") {
		t.Errorf("description missing bound peer entry:\n%s", desc)
	}
}

func TestRouterSendValidatesUnknownChannel(t *testing.T) {
	t.Parallel()
	tool := NewRouterSendTool(RouterSendDeps{Sender: &stubBridgeSender{}, Cfg: testCfg(), MediaRoot: t.TempDir()})
	resp, err := tool.Run(context.Background(), ToolCall{Input: `{"channel":"irc","identity":"default","peerId":"x","text":"hi"}`})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !resp.IsError {
		t.Errorf("expected error response")
	}
	if !strings.Contains(resp.Content, "unknown channel") {
		t.Errorf("content = %q", resp.Content)
	}
}

func TestRouterSendValidatesUnknownIdentity(t *testing.T) {
	t.Parallel()
	tool := NewRouterSendTool(RouterSendDeps{Sender: &stubBridgeSender{}, Cfg: testCfg(), MediaRoot: t.TempDir()})
	resp, err := tool.Run(context.Background(), ToolCall{Input: `{"channel":"slack","identity":"ghost","peerId":"D1","text":"hi"}`})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !resp.IsError {
		t.Errorf("expected error response")
	}
	if !strings.Contains(resp.Content, "unknown identity") || !strings.Contains(resp.Content, "default") {
		t.Errorf("content = %q (must list available identities)", resp.Content)
	}
}

func TestRouterSendValidatesEmptyTextAndFiles(t *testing.T) {
	t.Parallel()
	tool := NewRouterSendTool(RouterSendDeps{Sender: &stubBridgeSender{}, Cfg: testCfg(), MediaRoot: t.TempDir()})
	resp, _ := tool.Run(context.Background(), ToolCall{Input: `{"channel":"slack","identity":"default","peerId":"D1"}`})
	if !resp.IsError {
		t.Errorf("expected error response")
	}
}

func TestRouterSendValidatesMalformedInput(t *testing.T) {
	t.Parallel()
	tool := NewRouterSendTool(RouterSendDeps{Sender: &stubBridgeSender{}, Cfg: testCfg(), MediaRoot: t.TempDir()})
	resp, _ := tool.Run(context.Background(), ToolCall{Input: `not-json`})
	if !resp.IsError {
		t.Errorf("expected error on bad json")
	}
	if !strings.Contains(resp.Content, "invalid input") {
		t.Errorf("content = %q", resp.Content)
	}
}

func TestRouterSendSuccessfulDelivery(t *testing.T) {
	t.Parallel()
	sender := &stubBridgeSender{
		result: bridge.SendResult{Delivered: true, ResolvedPeer: "C123|ts"},
	}
	tool := NewRouterSendTool(RouterSendDeps{Sender: sender, Cfg: testCfg(), MediaRoot: t.TempDir()})

	resp, err := tool.Run(context.Background(), ToolCall{Input: `{"channel":"slack","identity":"default","peerId":"C123","text":"hi"}`})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.IsError {
		t.Errorf("unexpected error response: %s", resp.Content)
	}
	var body routerSendResponse
	if err := json.Unmarshal([]byte(resp.Content), &body); err != nil {
		t.Fatalf("response not JSON: %v (%s)", err, resp.Content)
	}
	if !body.Delivered {
		t.Errorf("Delivered = false, want true")
	}
	if body.ResolvedPeerID != "C123|ts" {
		t.Errorf("ResolvedPeerID = %q", body.ResolvedPeerID)
	}

	calls := sender.Calls()
	if len(calls) != 1 {
		t.Fatalf("Send calls = %d", len(calls))
	}
	if calls[0].Peer.Channel != "slack" || calls[0].Peer.PeerID != "C123" || calls[0].Text != "hi" {
		t.Errorf("call = %+v", calls[0])
	}
}

func TestRouterSendDeliveryFailureSurfacesErrorWithoutPanic(t *testing.T) {
	t.Parallel()
	sender := &stubBridgeSender{
		result: bridge.SendResult{Delivered: false, Err: errors.New("DM closed")},
	}
	tool := NewRouterSendTool(RouterSendDeps{Sender: sender, Cfg: testCfg(), MediaRoot: t.TempDir()})

	resp, _ := tool.Run(context.Background(), ToolCall{Input: `{"channel":"slack","identity":"default","peerId":"D1","text":"hi"}`})
	var body routerSendResponse
	_ = json.Unmarshal([]byte(resp.Content), &body)
	if body.Delivered {
		t.Errorf("Delivered = true, want false")
	}
	if body.Error == "" || !strings.Contains(body.Error, "DM closed") {
		t.Errorf("Error = %q", body.Error)
	}
}

func TestRouterSendTopLevelErrorSurfacesNotPanics(t *testing.T) {
	t.Parallel()
	sender := &stubBridgeSender{
		err: errors.New("no adapter for slack:ghost"),
	}
	tool := NewRouterSendTool(RouterSendDeps{Sender: sender, Cfg: testCfg(), MediaRoot: t.TempDir()})

	resp, _ := tool.Run(context.Background(), ToolCall{Input: `{"channel":"slack","identity":"default","peerId":"D1","text":"hi"}`})
	var body routerSendResponse
	_ = json.Unmarshal([]byte(resp.Content), &body)
	if body.Delivered {
		t.Errorf("Delivered = true, want false")
	}
	if !strings.Contains(body.Error, "no adapter") {
		t.Errorf("Error = %q", body.Error)
	}
}

func TestRouterSendAttachmentSafePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	safePath := filepath.Join(root, "file.txt")
	if err := os.WriteFile(safePath, []byte("ok"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sender := &stubBridgeSender{result: bridge.SendResult{Delivered: true}}
	tool := NewRouterSendTool(RouterSendDeps{Sender: sender, Cfg: testCfg(), MediaRoot: root})

	input := `{"channel":"slack","identity":"default","peerId":"D1","text":"hi","files":["` + safePath + `"]}`
	resp, _ := tool.Run(context.Background(), ToolCall{Input: input})
	if resp.IsError {
		t.Errorf("safe path rejected: %s", resp.Content)
	}
	calls := sender.Calls()
	if len(calls) != 1 || len(calls[0].Attachments) != 1 {
		t.Errorf("attachments not forwarded: %+v", calls)
	}
}

func TestRouterSendAttachmentOutsideRootRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir() // distinct dir
	outsidePath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsidePath, []byte("nope"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sender := &stubBridgeSender{result: bridge.SendResult{Delivered: true}}
	tool := NewRouterSendTool(RouterSendDeps{Sender: sender, Cfg: testCfg(), MediaRoot: root})

	input := `{"channel":"slack","identity":"default","peerId":"D1","text":"hi","files":["` + outsidePath + `"]}`
	resp, _ := tool.Run(context.Background(), ToolCall{Input: input})
	if !resp.IsError {
		t.Errorf("expected error response for path outside media root")
	}
	if len(sender.Calls()) != 0 {
		t.Errorf("Send should not be called when path rejected")
	}
}

func TestRouterSendIsBaselineAndParallel(t *testing.T) {
	t.Parallel()
	tool := NewRouterSendTool(RouterSendDeps{Sender: &stubBridgeSender{}, Cfg: testCfg(), MediaRoot: t.TempDir()})
	if !tool.IsBaseline() {
		t.Errorf("IsBaseline = false; want true")
	}
	if !tool.AllowParallelism(ToolCall{}, nil) {
		t.Errorf("AllowParallelism = false; want true (multiple router_send to different peers can fan in parallel)")
	}
}
