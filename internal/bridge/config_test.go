package bridge

import "testing"

func TestConfigHasTokenBearingFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{
			name: "nil config",
			cfg:  nil,
			want: false,
		},
		{
			name: "empty config",
			cfg:  &Config{},
			want: false,
		},
		{
			name: "telegram with empty token",
			cfg: &Config{
				Channels: ChannelsConfig{
					Telegram: &TelegramChannelConfig{
						Enabled: true,
						Bots:    []TelegramIdentity{{ID: "default"}},
					},
				},
			},
			want: false,
		},
		{
			name: "telegram with token",
			cfg: &Config{
				Channels: ChannelsConfig{
					Telegram: &TelegramChannelConfig{
						Bots: []TelegramIdentity{{ID: "default", Token: "abc"}},
					},
				},
			},
			want: true,
		},
		{
			name: "slack with bot token",
			cfg: &Config{
				Channels: ChannelsConfig{
					Slack: &SlackChannelConfig{
						Apps: []SlackIdentity{{ID: "default", BotToken: "xoxb-..."}},
					},
				},
			},
			want: true,
		},
		{
			name: "slack with app token only",
			cfg: &Config{
				Channels: ChannelsConfig{
					Slack: &SlackChannelConfig{
						Apps: []SlackIdentity{{ID: "default", AppToken: "xapp-..."}},
					},
				},
			},
			want: true,
		},
		{
			name: "mattermost with access token",
			cfg: &Config{
				Channels: ChannelsConfig{
					Mattermost: &MattermostChannelConfig{
						Instances: []MattermostIdentity{{ID: "default", AccessToken: "tok"}},
					},
				},
			},
			want: true,
		},
		{
			name: "external with empty relay credential",
			cfg: &Config{
				Channels: ChannelsConfig{
					External: &ExternalChannelConfig{
						Enabled:   true,
						Consumers: []ExternalIdentity{{ID: "c3", Enabled: true}},
					},
				},
			},
			want: false,
		},
		{
			name: "external with relay credential, identity not enabled",
			cfg: &Config{
				Channels: ChannelsConfig{
					External: &ExternalChannelConfig{
						Consumers: []ExternalIdentity{{ID: "c3", RelayCredential: "secret"}},
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.cfg.HasTokenBearingFields(); got != tt.want {
				t.Errorf("HasTokenBearingFields() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigAnyChannelEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{
			name: "nil",
			cfg:  nil,
			want: false,
		},
		{
			name: "channel enabled but no identity",
			cfg: &Config{
				Channels: ChannelsConfig{
					Telegram: &TelegramChannelConfig{Enabled: true},
				},
			},
			want: false,
		},
		{
			name: "channel enabled identity disabled",
			cfg: &Config{
				Channels: ChannelsConfig{
					Telegram: &TelegramChannelConfig{
						Enabled: true,
						Bots:    []TelegramIdentity{{ID: "x", Enabled: false}},
					},
				},
			},
			want: false,
		},
		{
			name: "channel disabled identity enabled",
			cfg: &Config{
				Channels: ChannelsConfig{
					Telegram: &TelegramChannelConfig{
						Enabled: false,
						Bots:    []TelegramIdentity{{ID: "x", Enabled: true}},
					},
				},
			},
			want: false,
		},
		{
			name: "telegram enabled+identity enabled",
			cfg: &Config{
				Channels: ChannelsConfig{
					Telegram: &TelegramChannelConfig{
						Enabled: true,
						Bots:    []TelegramIdentity{{ID: "x", Enabled: true}},
					},
				},
			},
			want: true,
		},
		{
			name: "slack enabled+identity enabled",
			cfg: &Config{
				Channels: ChannelsConfig{
					Slack: &SlackChannelConfig{
						Enabled: true,
						Apps:    []SlackIdentity{{ID: "x", Enabled: true}},
					},
				},
			},
			want: true,
		},
		{
			name: "mattermost enabled+identity enabled",
			cfg: &Config{
				Channels: ChannelsConfig{
					Mattermost: &MattermostChannelConfig{
						Enabled:   true,
						Instances: []MattermostIdentity{{ID: "x", Enabled: true}},
					},
				},
			},
			want: true,
		},
		{
			name: "external enabled+consumer enabled",
			cfg: &Config{
				Channels: ChannelsConfig{
					External: &ExternalChannelConfig{
						Enabled:   true,
						Consumers: []ExternalIdentity{{ID: "c3", Enabled: true}},
					},
				},
			},
			want: true,
		},
		{
			name: "external enabled but consumer disabled",
			cfg: &Config{
				Channels: ChannelsConfig{
					External: &ExternalChannelConfig{
						Enabled:   true,
						Consumers: []ExternalIdentity{{ID: "c3", Enabled: false}},
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.cfg.AnyChannelEnabled(); got != tt.want {
				t.Errorf("AnyChannelEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeToolUpdateVerbosity(t *testing.T) {
	tests := []struct {
		in       string
		wantMode string
		wantOK   bool
	}{
		{"", ToolUpdateVerbosityCompact, true},
		{"compact", ToolUpdateVerbosityCompact, true},
		{"full", ToolUpdateVerbosityFull, true},
		{"  FULL  ", ToolUpdateVerbosityFull, true},
		{"Compact", ToolUpdateVerbosityCompact, true},
		{"verbose", ToolUpdateVerbosityCompact, false},
		{"off", ToolUpdateVerbosityCompact, false},
	}
	for _, tt := range tests {
		mode, ok := NormalizeToolUpdateVerbosity(tt.in)
		if mode != tt.wantMode || ok != tt.wantOK {
			t.Errorf("NormalizeToolUpdateVerbosity(%q) = (%q, %v); want (%q, %v)",
				tt.in, mode, ok, tt.wantMode, tt.wantOK)
		}
	}
}

// TestConfig_ToolVerbosityDefaultsCompact pins the default: an operator
// who never set the key gets the quiet rendering, and a typo does not
// silently opt them into the noisy one.
func TestConfig_ToolVerbosityDefaultsCompact(t *testing.T) {
	var nilCfg *Config
	if got := nilCfg.ToolVerbosity(); got != ToolUpdateVerbosityCompact {
		t.Errorf("nil cfg ToolVerbosity() = %q; want compact", got)
	}
	if got := (&Config{}).ToolVerbosity(); got != ToolUpdateVerbosityCompact {
		t.Errorf("zero cfg ToolVerbosity() = %q; want compact", got)
	}
	if got := (&Config{ToolUpdateVerbosity: "full"}).ToolVerbosity(); got != ToolUpdateVerbosityFull {
		t.Errorf("full cfg ToolVerbosity() = %q; want full", got)
	}
	if got := (&Config{ToolUpdateVerbosity: "nonsense"}).ToolVerbosity(); got != ToolUpdateVerbosityCompact {
		t.Errorf("bad cfg ToolVerbosity() = %q; want compact fallback", got)
	}
}
