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
