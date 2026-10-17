package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestValidateMCPServersFlowArgHeaders(t *testing.T) {
	tests := []struct {
		name    string
		server  MCPServer
		wantErr string
	}{
		{name: "mapped header is accepted", server: MCPServer{FlowArgHeaders: map[string]string{"X-Tenant": "tenant"}}},
		{name: "no mapping", server: MCPServer{PeerHeader: "X-Peer-Id"}},
		{name: "Authorization", server: MCPServer{FlowArgHeaders: map[string]string{"Authorization": "tok"}}, wantErr: `mcpServers.web.flowArgHeaders: header "Authorization"`},
		{name: "authorization lower case, as viper folds it", server: MCPServer{FlowArgHeaders: map[string]string{"authorization": "tok"}}, wantErr: `header "authorization"`},
		{name: "AUTHORIZATION", server: MCPServer{FlowArgHeaders: map[string]string{"AUTHORIZATION": "tok"}}, wantErr: "not allowed"},
		{name: "peerHeader", server: MCPServer{PeerHeader: "X-Peer-Id", FlowArgHeaders: map[string]string{"X-Peer-Id": "peer"}}, wantErr: `mcpServers.web.flowArgHeaders: header "X-Peer-Id" collides with the server's peerHeader`},
		{name: "peerHeader in another letter case", server: MCPServer{PeerHeader: "X-Peer-Id", FlowArgHeaders: map[string]string{"x-peer-id": "peer"}}, wantErr: "collides"},
		{name: "same header on a server without that peerHeader", server: MCPServer{PeerHeader: "X-Other", FlowArgHeaders: map[string]string{"X-Peer-Id": "peer"}}},
		{name: "empty arg name", server: MCPServer{FlowArgHeaders: map[string]string{"X-Tenant": ""}}, wantErr: "non-empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMCPServers(map[string]MCPServer{"web": tt.server})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

// Viper folds map keys to lower case, so a header name arrives lower-cased
// while the arg name it maps to is untouched. The loader must keep the arg
// name, and Load validates what it produced.
func TestConfig_FlowArgHeadersViperRoundTrip(t *testing.T) {
	load := func(t *testing.T, body string) error {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		Reset()
		t.Cleanup(Reset)
		viper.Reset()
		t.Cleanup(viper.Reset)
		_, err := Load(dir, false)
		return err
	}

	t.Run("round trips and names the redacted args", func(t *testing.T) {
		if err := load(t, `{"mcpServers": {"web": {"type": "http", "url": "http://x.test/mcp",
			"flowArgHeaders": {"X-Tenant-Id": "tenantId", "X-Run": "Run_Id"}}}}`); err != nil {
			t.Fatalf("Load: %v", err)
		}
		got := Get().MCPServers["web"].FlowArgHeaders
		if len(got) != 2 || got["x-tenant-id"] != "tenantId" || got["x-run"] != "Run_Id" {
			t.Fatalf("FlowArgHeaders = %v, want both entries with arg names intact", got)
		}
		red := RedactedFlowArgs()
		if _, ok := red["tenantId"]; !ok || len(red) != 2 {
			t.Errorf("RedactedFlowArgs = %v, want tenantId and Run_Id", red)
		}
		if err := Validate(); err != nil {
			t.Errorf("Validate: %v", err)
		}
	})

	t.Run("Load refuses Authorization", func(t *testing.T) {
		err := load(t, `{"mcpServers": {"web": {"type": "http", "url": "http://x.test/mcp",
			"flowArgHeaders": {"Authorization": "token"}}}}`)
		if err == nil || !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), "uthorization") {
			t.Errorf("Load = %v, want an error naming the server and header", err)
		}
	})

	t.Run("Load refuses the peerHeader", func(t *testing.T) {
		err := load(t, `{"mcpServers": {"web": {"type": "http", "url": "http://x.test/mcp", "peerHeader": "X-Peer-Id",
			"flowArgHeaders": {"x-peer-id": "peer"}}}}`)
		if err == nil || !strings.Contains(err.Error(), "peerHeader") {
			t.Errorf("Load = %v, want a peerHeader collision error", err)
		}
	})
}
