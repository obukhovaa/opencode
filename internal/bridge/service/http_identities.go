package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
)

// identityList is the GET response for /router/identities/{channel}.
type identityList struct {
	Identities []identityPublic `json:"identities"`
}

// identityPublic redacts the token-bearing fields so a GET doesn't leak
// secrets. Operators can still inspect the IDs, access mode, and
// groupsEnabled flag.
type identityPublic struct {
	Channel         string `json:"channel"`
	ID              string `json:"id"`
	Enabled         bool   `json:"enabled"`
	GroupsEnabled   bool   `json:"groupsEnabled,omitempty"`
	Access          string `json:"access,omitempty"`
	PairingCodeHash string `json:"pairingCodeHash,omitempty"`
	ServerURL       string `json:"serverUrl,omitempty"`
	HasToken        bool   `json:"hasToken"`
}

// identityUpsertRequest is the body for POST /router/identities/{channel}.
// Fields not relevant to a given channel are simply ignored.
type identityUpsertRequest struct {
	ID              string `json:"id"`
	Enabled         *bool  `json:"enabled,omitempty"`
	GroupsEnabled   *bool  `json:"groupsEnabled,omitempty"`
	Access          string `json:"access,omitempty"`
	PairingCodeHash string `json:"pairingCodeHash,omitempty"`
	// Telegram
	Token string `json:"token,omitempty"`
	// Slack
	BotToken string `json:"botToken,omitempty"`
	AppToken string `json:"appToken,omitempty"`
	// Mattermost
	ServerURL   string `json:"serverUrl,omitempty"`
	AccessToken string `json:"accessToken,omitempty"`
}

func (s *Service) handleIdentitiesList(channel string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, identityList{Identities: s.listIdentitiesPublic(channel)})
	}
}

func (s *Service) listIdentitiesPublic(channel string) []identityPublic {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	out := make([]identityPublic, 0, 4)
	switch channel {
	case "telegram":
		if s.cfg.Channels.Telegram == nil {
			return out
		}
		for _, b := range s.cfg.Channels.Telegram.Bots {
			out = append(out, identityPublic{
				Channel:         "telegram",
				ID:              b.ID,
				Enabled:         b.Enabled,
				GroupsEnabled:   b.GroupsEnabled,
				Access:          b.Access,
				PairingCodeHash: b.PairingCodeHash,
				HasToken:        b.Token != "",
			})
		}
	case "slack":
		if s.cfg.Channels.Slack == nil {
			return out
		}
		for _, a := range s.cfg.Channels.Slack.Apps {
			out = append(out, identityPublic{
				Channel:       "slack",
				ID:            a.ID,
				Enabled:       a.Enabled,
				GroupsEnabled: a.GroupsEnabled,
				HasToken:      a.BotToken != "" && a.AppToken != "",
			})
		}
	case "mattermost":
		if s.cfg.Channels.Mattermost == nil {
			return out
		}
		for _, m := range s.cfg.Channels.Mattermost.Instances {
			out = append(out, identityPublic{
				Channel:       "mattermost",
				ID:            m.ID,
				Enabled:       m.Enabled,
				GroupsEnabled: m.GroupsEnabled,
				ServerURL:     m.ServerURL,
				HasToken:      m.AccessToken != "",
			})
		}
	}
	return out
}

func (s *Service) handleIdentityUpsert(channel string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req identityUpsertRequest
		if err := readJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.ID = strings.TrimSpace(req.ID)
		if req.ID == "" {
			writeAPIError(w, http.StatusBadRequest, "id is required")
			return
		}
		// If this identity already had an adapter running (re-POST to
		// update a token), tear it down first so LaunchAdapter rebuilds
		// against the new config.
		s.DeregisterAdapter(channel, req.ID)

		if err := s.upsertIdentity(channel, req); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Hot-launch: per the bridge-http-api spec scenario "Bridge sees
		// its own write without restart", the new identity must observably
		// start running before the next process restart. Adapter launch
		// failures are reported but the config write succeeded — operators
		// can fix tokens and re-POST.
		if err := s.LaunchAdapter(r.Context(), channel, req.ID); err != nil {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"channel":     channel,
				"id":          req.ID,
				"launchError": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"channel": channel,
			"id":      req.ID,
		})
	}
}

func (s *Service) handleIdentityDelete(channel string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			writeAPIError(w, http.StatusBadRequest, "id is required")
			return
		}
		if err := s.deleteIdentity(r.Context(), channel, id); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"channel": channel,
			"id":      id,
			"deleted": true,
		})
	}
}

// upsertIdentity mutates s.cfg (in-memory snapshot) AND persists via
// config.UpdateCfgFile. Per the config-atomic-writeback caller contract,
// both updates happen in the same call so the running bridge sees the
// change without a process restart.
func (s *Service) upsertIdentity(channel string, req identityUpsertRequest) error {
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	groupsEnabled := false
	if req.GroupsEnabled != nil {
		groupsEnabled = *req.GroupsEnabled
	}

	apply := func(cfg *bridge.Config) error {
		switch channel {
		case "telegram":
			if cfg.Channels.Telegram == nil {
				cfg.Channels.Telegram = &bridge.TelegramChannelConfig{Enabled: true}
			}
			access := strings.TrimSpace(req.Access)
			if access != "" && access != "public" && access != "private" {
				return fmt.Errorf("telegram access must be 'public' or 'private'")
			}
			if access == "private" && req.PairingCodeHash == "" {
				// Allow keeping the existing hash when not provided.
				if existing := findTelegram(cfg, req.ID); existing == nil || existing.PairingCodeHash == "" {
					return fmt.Errorf("pairingCodeHash is required when access=private")
				}
			}
			existing := findTelegram(cfg, req.ID)
			if existing == nil {
				cfg.Channels.Telegram.Bots = append(cfg.Channels.Telegram.Bots, bridge.TelegramIdentity{
					ID:              req.ID,
					Token:           req.Token,
					Enabled:         enabled,
					Access:          access,
					PairingCodeHash: req.PairingCodeHash,
					GroupsEnabled:   groupsEnabled,
				})
			} else {
				if req.Token != "" {
					existing.Token = req.Token
				}
				existing.Enabled = enabled
				if access != "" {
					existing.Access = access
				}
				if req.PairingCodeHash != "" {
					existing.PairingCodeHash = req.PairingCodeHash
				}
				if req.GroupsEnabled != nil {
					existing.GroupsEnabled = groupsEnabled
				}
			}
			cfg.Channels.Telegram.Enabled = true
		case "slack":
			if cfg.Channels.Slack == nil {
				cfg.Channels.Slack = &bridge.SlackChannelConfig{Enabled: true}
			}
			existing := findSlack(cfg, req.ID)
			if existing == nil {
				if req.BotToken == "" || req.AppToken == "" {
					return fmt.Errorf("slack identity requires both botToken and appToken")
				}
				cfg.Channels.Slack.Apps = append(cfg.Channels.Slack.Apps, bridge.SlackIdentity{
					ID:            req.ID,
					BotToken:      req.BotToken,
					AppToken:      req.AppToken,
					Enabled:       enabled,
					GroupsEnabled: groupsEnabled,
				})
			} else {
				if req.BotToken != "" {
					existing.BotToken = req.BotToken
				}
				if req.AppToken != "" {
					existing.AppToken = req.AppToken
				}
				existing.Enabled = enabled
				if req.GroupsEnabled != nil {
					existing.GroupsEnabled = groupsEnabled
				}
			}
			cfg.Channels.Slack.Enabled = true
		case "mattermost":
			if cfg.Channels.Mattermost == nil {
				cfg.Channels.Mattermost = &bridge.MattermostChannelConfig{Enabled: true}
			}
			existing := findMattermost(cfg, req.ID)
			if existing == nil {
				if req.ServerURL == "" || req.AccessToken == "" {
					return fmt.Errorf("mattermost identity requires serverUrl and accessToken")
				}
				cfg.Channels.Mattermost.Instances = append(cfg.Channels.Mattermost.Instances, bridge.MattermostIdentity{
					ID:            req.ID,
					ServerURL:     req.ServerURL,
					AccessToken:   req.AccessToken,
					Enabled:       enabled,
					GroupsEnabled: groupsEnabled,
				})
			} else {
				if req.ServerURL != "" {
					existing.ServerURL = req.ServerURL
				}
				if req.AccessToken != "" {
					existing.AccessToken = req.AccessToken
				}
				existing.Enabled = enabled
				if req.GroupsEnabled != nil {
					existing.GroupsEnabled = groupsEnabled
				}
			}
			cfg.Channels.Mattermost.Enabled = true
		default:
			return fmt.Errorf("unknown channel %q", channel)
		}
		return nil
	}

	s.cfgMu.Lock()
	if err := apply(s.cfg); err != nil {
		s.cfgMu.Unlock()
		return err
	}
	s.cfgMu.Unlock()
	return config.UpdateCfgFile(func(c *config.Config) {
		if c.Router == nil {
			c.Router = &bridge.Config{}
		}
		_ = apply(c.Router)
	})
}

// deleteIdentity removes an identity from cfg.Router and persists. After
// removal, the corresponding adapter is deregistered and any
// bridge_sessions rows referencing it are cascade-deleted to avoid
// orphaned bindings (per the chat-bridge-http-api spec scenario "Delete
// an identity → cascade-delete orphaned bindings").
func (s *Service) deleteIdentity(ctx context.Context, channel, id string) error {
	s.cfgMu.Lock()
	if err := removeIdentity(s.cfg, channel, id); err != nil {
		s.cfgMu.Unlock()
		return err
	}
	s.cfgMu.Unlock()
	if err := config.UpdateCfgFile(func(c *config.Config) {
		if c.Router == nil {
			return
		}
		_ = removeIdentity(c.Router, channel, id)
	}); err != nil {
		return err
	}
	s.DeregisterAdapter(channel, id)
	// Cascade cleanup — silent if no rows match.
	_ = s.store.DeleteBindingsByIdentity(ctx, s.projectID, channel, id)
	return nil
}

func removeIdentity(cfg *bridge.Config, channel, id string) error {
	if cfg == nil {
		return errors.New("bridge: config is nil")
	}
	switch channel {
	case "telegram":
		if cfg.Channels.Telegram == nil {
			return fmt.Errorf("telegram channel not configured")
		}
		out := cfg.Channels.Telegram.Bots[:0]
		removed := false
		for _, b := range cfg.Channels.Telegram.Bots {
			if b.ID == id {
				removed = true
				continue
			}
			out = append(out, b)
		}
		if !removed {
			return fmt.Errorf("telegram identity %q not found", id)
		}
		cfg.Channels.Telegram.Bots = out
	case "slack":
		if cfg.Channels.Slack == nil {
			return fmt.Errorf("slack channel not configured")
		}
		out := cfg.Channels.Slack.Apps[:0]
		removed := false
		for _, a := range cfg.Channels.Slack.Apps {
			if a.ID == id {
				removed = true
				continue
			}
			out = append(out, a)
		}
		if !removed {
			return fmt.Errorf("slack identity %q not found", id)
		}
		cfg.Channels.Slack.Apps = out
	case "mattermost":
		if cfg.Channels.Mattermost == nil {
			return fmt.Errorf("mattermost channel not configured")
		}
		out := cfg.Channels.Mattermost.Instances[:0]
		removed := false
		for _, m := range cfg.Channels.Mattermost.Instances {
			if m.ID == id {
				removed = true
				continue
			}
			out = append(out, m)
		}
		if !removed {
			return fmt.Errorf("mattermost identity %q not found", id)
		}
		cfg.Channels.Mattermost.Instances = out
	default:
		return fmt.Errorf("unknown channel %q", channel)
	}
	return nil
}

func findTelegram(cfg *bridge.Config, id string) *bridge.TelegramIdentity {
	if cfg == nil || cfg.Channels.Telegram == nil {
		return nil
	}
	for i := range cfg.Channels.Telegram.Bots {
		if cfg.Channels.Telegram.Bots[i].ID == id {
			return &cfg.Channels.Telegram.Bots[i]
		}
	}
	return nil
}

func findSlack(cfg *bridge.Config, id string) *bridge.SlackIdentity {
	if cfg == nil || cfg.Channels.Slack == nil {
		return nil
	}
	for i := range cfg.Channels.Slack.Apps {
		if cfg.Channels.Slack.Apps[i].ID == id {
			return &cfg.Channels.Slack.Apps[i]
		}
	}
	return nil
}

func findMattermost(cfg *bridge.Config, id string) *bridge.MattermostIdentity {
	if cfg == nil || cfg.Channels.Mattermost == nil {
		return nil
	}
	for i := range cfg.Channels.Mattermost.Instances {
		if cfg.Channels.Mattermost.Instances[i].ID == id {
			return &cfg.Channels.Mattermost.Instances[i]
		}
	}
	return nil
}
