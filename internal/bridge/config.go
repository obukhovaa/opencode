package bridge

// Config is the top-level configuration block hosted under the `router` key
// in .opencode.json. It is referenced by internal/config.Config.Router so the
// bridge boots when the operator opts in by adding this section.
//
// This file MUST NOT import internal/config — config imports bridge for this
// type, so the bridge package depends on config only via constructor
// arguments, never via import. Keeping config.go zero-dependency preserves
// that one-way import direction.
type Config struct {
	// QuestionMode controls how questions from the agent are surfaced on
	// chat: "interactive" renders platform-native UI (buttons/blocks),
	// "auto-reject" returns the default answer without prompting,
	// "disabled" suppresses the question flow entirely.
	QuestionMode string `json:"questionMode,omitempty"`

	// PermissionMode is how permission requests from the agent are handled
	// while routed via chat: "allow" auto-approves, "deny" auto-denies.
	// More modes may be added in future without a config break.
	PermissionMode string `json:"permissionMode,omitempty"`

	// ToolUpdatesEnabled controls whether the bridge surfaces tool
	// transition events ([tool] pending|running|completed) to the chat
	// surface. When false, only typing indicators and final agent
	// messages are emitted.
	ToolUpdatesEnabled bool `json:"toolUpdatesEnabled,omitempty"`

	// Channels carries per-platform configuration and identity lists.
	Channels ChannelsConfig `json:"channels,omitempty"`

	// AgentPeerAllowlist is a forward-compat hook for restricting the
	// router_send agent tool to specific peers. NOT enforced in v1 (see
	// chat-bridge-agent-tool spec). Schema-defined but ignored at runtime.
	AgentPeerAllowlist []PeerRef `json:"agentPeerAllowlist,omitempty"`
}

// ChannelsConfig holds per-platform channel sections.
type ChannelsConfig struct {
	Telegram   *TelegramChannelConfig   `json:"telegram,omitempty"`
	Slack      *SlackChannelConfig      `json:"slack,omitempty"`
	Mattermost *MattermostChannelConfig `json:"mattermost,omitempty"`
}

// TelegramChannelConfig configures the Telegram channel and its bot identities.
type TelegramChannelConfig struct {
	Enabled bool               `json:"enabled"`
	Bots    []TelegramIdentity `json:"bots,omitempty"`
}

// TelegramIdentity is a single Telegram bot identity.
type TelegramIdentity struct {
	ID      string `json:"id"`
	Token   string `json:"token,omitempty"`
	Enabled bool   `json:"enabled"`
	// Access is "private" or "public". In private mode the bot only
	// responds to peers in the allowlist; public mode accepts any peer
	// who messages the bot.
	Access          string `json:"access,omitempty"`
	PairingCodeHash string `json:"pairingCodeHash,omitempty"`
	GroupsEnabled   bool   `json:"groupsEnabled,omitempty"`
}

// SlackChannelConfig configures the Slack channel and its app identities.
type SlackChannelConfig struct {
	Enabled bool            `json:"enabled"`
	Apps    []SlackIdentity `json:"apps,omitempty"`
}

// SlackIdentity is a single Slack app identity (Socket Mode).
type SlackIdentity struct {
	ID            string `json:"id"`
	BotToken      string `json:"botToken,omitempty"`
	AppToken      string `json:"appToken,omitempty"`
	Enabled       bool   `json:"enabled"`
	GroupsEnabled bool   `json:"groupsEnabled,omitempty"`
}

// MattermostChannelConfig configures the Mattermost channel and its instance
// identities.
type MattermostChannelConfig struct {
	Enabled   bool                 `json:"enabled"`
	Instances []MattermostIdentity `json:"instances,omitempty"`
}

// MattermostIdentity is a single Mattermost server connection.
type MattermostIdentity struct {
	ID            string `json:"id"`
	ServerURL     string `json:"serverUrl,omitempty"`
	AccessToken   string `json:"accessToken,omitempty"`
	Enabled       bool   `json:"enabled"`
	GroupsEnabled bool   `json:"groupsEnabled,omitempty"`
}

// HasTokenBearingFields reports whether the config contains any token-bearing
// field that warrants 0o600 mode on the .opencode.json file. Used by
// config.UpdateCfgFile to decide whether to force-tighten the file mode on
// write. Returns false for a nil receiver.
func (c *Config) HasTokenBearingFields() bool {
	if c == nil {
		return false
	}
	if t := c.Channels.Telegram; t != nil {
		for i := range t.Bots {
			if t.Bots[i].Token != "" {
				return true
			}
		}
	}
	if s := c.Channels.Slack; s != nil {
		for i := range s.Apps {
			if s.Apps[i].BotToken != "" || s.Apps[i].AppToken != "" {
				return true
			}
		}
	}
	if m := c.Channels.Mattermost; m != nil {
		for i := range m.Instances {
			if m.Instances[i].AccessToken != "" {
				return true
			}
		}
	}
	return false
}

// AnyChannelEnabled reports whether at least one channel has at least one
// enabled identity. Used by cmd/serve.go to decide whether to instantiate
// the orchestrator. Returns false for a nil receiver.
func (c *Config) AnyChannelEnabled() bool {
	if c == nil {
		return false
	}
	if t := c.Channels.Telegram; t != nil && t.Enabled {
		for i := range t.Bots {
			if t.Bots[i].Enabled {
				return true
			}
		}
	}
	if s := c.Channels.Slack; s != nil && s.Enabled {
		for i := range s.Apps {
			if s.Apps[i].Enabled {
				return true
			}
		}
	}
	if m := c.Channels.Mattermost; m != nil && m.Enabled {
		for i := range m.Instances {
			if m.Instances[i].Enabled {
				return true
			}
		}
	}
	return false
}
