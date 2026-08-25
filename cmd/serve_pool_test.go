package cmd

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// fixtureGit runs a git command against the fixture directory with an
// environment built FROM SCRATCH — never inherited. A pre-commit hook
// exports GIT_DIR/GIT_INDEX_FILE/GIT_WORK_TREE into the test process,
// and an inherited env would point the fixture's `git init` at the REAL
// repository (the GENAI-123 incident class). The explicit allowlist
// (PATH + a throwaway HOME) makes that structurally impossible.
func fixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

func TestValidatePoolModeInbound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		router  *bridge.Config
		wantErr string // "" = boot allowed
	}{
		{
			name:   "nil router config passes",
			router: nil,
		},
		{
			name:   "empty channels pass",
			router: &bridge.Config{},
		},
		{
			name: "all enabled identities inbound-disabled pass",
			router: &bridge.Config{Channels: bridge.ChannelsConfig{
				Telegram: &bridge.TelegramChannelConfig{Enabled: true, Bots: []bridge.TelegramIdentity{
					{ID: "bot1", Enabled: true, Inbound: "disabled"},
				}},
				Slack: &bridge.SlackChannelConfig{Enabled: true, Apps: []bridge.SlackIdentity{
					{ID: "default", Enabled: true, Inbound: "disabled"},
				}},
				Mattermost: &bridge.MattermostChannelConfig{Enabled: true, Instances: []bridge.MattermostIdentity{
					{ID: "mm1", Enabled: true, Inbound: "disabled"},
				}},
			}},
		},
		{
			name: "slack identity with inbound enabled fails citing channel:identity",
			router: &bridge.Config{Channels: bridge.ChannelsConfig{
				Slack: &bridge.SlackChannelConfig{Enabled: true, Apps: []bridge.SlackIdentity{
					{ID: "default", Enabled: true, Inbound: "enabled"},
				}},
			}},
			wantErr: "pool mode requires inbound:disabled for all bridge channels (slack:default has inbound:enabled)",
		},
		{
			name: "empty inbound string counts as enabled (violation)",
			router: &bridge.Config{Channels: bridge.ChannelsConfig{
				Slack: &bridge.SlackChannelConfig{Enabled: true, Apps: []bridge.SlackIdentity{
					{ID: "default", Enabled: true},
				}},
			}},
			wantErr: "slack:default has inbound:enabled",
		},
		{
			name: "telegram bot violation cites telegram identity",
			router: &bridge.Config{Channels: bridge.ChannelsConfig{
				Telegram: &bridge.TelegramChannelConfig{Enabled: true, Bots: []bridge.TelegramIdentity{
					{ID: "bot1", Enabled: true, Inbound: "enabled"},
				}},
			}},
			wantErr: "telegram:bot1 has inbound:enabled",
		},
		{
			name: "mattermost instance violation cites mattermost identity",
			router: &bridge.Config{Channels: bridge.ChannelsConfig{
				Mattermost: &bridge.MattermostChannelConfig{Enabled: true, Instances: []bridge.MattermostIdentity{
					{ID: "mm1", Enabled: true},
				}},
			}},
			wantErr: "mattermost:mm1 has inbound:enabled",
		},
		{
			name: "disabled identity is skipped",
			router: &bridge.Config{Channels: bridge.ChannelsConfig{
				Slack: &bridge.SlackChannelConfig{Enabled: true, Apps: []bridge.SlackIdentity{
					{ID: "default", Enabled: false, Inbound: "enabled"},
				}},
			}},
		},
		{
			name: "identity inside a disabled channel is skipped (never launches)",
			router: &bridge.Config{Channels: bridge.ChannelsConfig{
				Slack: &bridge.SlackChannelConfig{Enabled: false, Apps: []bridge.SlackIdentity{
					{ID: "default", Enabled: true, Inbound: "enabled"},
				}},
			}},
		},
		{
			name: "external consumers have no inbound field and are exempt",
			router: &bridge.Config{Channels: bridge.ChannelsConfig{
				External: &bridge.ExternalChannelConfig{Enabled: true, Consumers: []bridge.ExternalIdentity{
					{ID: "c3", Enabled: true},
				}},
			}},
		},
		{
			name: "multiple violations are all cited",
			router: &bridge.Config{Channels: bridge.ChannelsConfig{
				Slack: &bridge.SlackChannelConfig{Enabled: true, Apps: []bridge.SlackIdentity{
					{ID: "default", Enabled: true, Inbound: "enabled"},
				}},
				Telegram: &bridge.TelegramChannelConfig{Enabled: true, Bots: []bridge.TelegramIdentity{
					{ID: "bot1", Enabled: true},
				}},
			}},
			wantErr: "telegram:bot1 has inbound:enabled; slack:default has inbound:enabled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validatePoolModeInbound(tt.router)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestDerivePoolBoundWorkspace(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	t.Run("non-git directory reports unbound", func(t *testing.T) {
		t.Parallel()
		if got := derivePoolBoundWorkspace(t.TempDir()); got != "" {
			t.Errorf("derivePoolBoundWorkspace = %q, want empty", got)
		}
	})

	t.Run("git checkout reports its origin URL", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		origin := "https://gitlab.com/piano/composer/agents/developer.git"
		fixtureGit(t, dir, "init", "--quiet")
		fixtureGit(t, dir, "remote", "add", "origin", origin)
		if got := derivePoolBoundWorkspace(dir); got != origin {
			t.Errorf("derivePoolBoundWorkspace = %q, want %q", got, origin)
		}
	})

	t.Run("git repo without origin reports unbound", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		fixtureGit(t, dir, "init", "--quiet")
		if got := derivePoolBoundWorkspace(dir); got != "" {
			t.Errorf("derivePoolBoundWorkspace = %q, want empty", got)
		}
	})
}
