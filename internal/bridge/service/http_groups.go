package service

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
)

// groupsRequest is the body shape POST /router/config/groups accepts.
// Per the chat-bridge-http-api spec there is NO global groupsEnabled —
// every toggle is scoped to a single identity.
type groupsRequest struct {
	Channel    string `json:"channel"`
	IdentityID string `json:"identityId"`
	Enabled    *bool  `json:"enabled"`
}

func (s *Service) handleGroupsGet(w http.ResponseWriter, _ *http.Request) {
	type row struct {
		Channel    string `json:"channel"`
		IdentityID string `json:"identityId"`
		Enabled    bool   `json:"enabled"`
	}
	// Snapshot cfg under the read lock, then release it before the
	// network write — never hold the cfg mutex across I/O.
	s.cfgMu.RLock()
	out := make([]row, 0, 8)
	if s.cfg.Channels.Telegram != nil {
		for _, b := range s.cfg.Channels.Telegram.Bots {
			out = append(out, row{"telegram", b.ID, b.GroupsEnabled})
		}
	}
	if s.cfg.Channels.Slack != nil {
		for _, a := range s.cfg.Channels.Slack.Apps {
			out = append(out, row{"slack", a.ID, a.GroupsEnabled})
		}
	}
	if s.cfg.Channels.Mattermost != nil {
		for _, m := range s.cfg.Channels.Mattermost.Instances {
			out = append(out, row{"mattermost", m.ID, m.GroupsEnabled})
		}
	}
	s.cfgMu.RUnlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) handleGroupsSet(w http.ResponseWriter, r *http.Request) {
	var req groupsRequest
	if err := readJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Channel == "" || req.IdentityID == "" {
		writeAPIError(w, http.StatusBadRequest, "channel and identityId are required (per-identity scope)")
		return
	}
	if req.Enabled == nil {
		writeAPIError(w, http.StatusBadRequest, "enabled is required")
		return
	}

	enabled := *req.Enabled
	if err := s.mutateIdentityGroupsEnabled(req.Channel, req.IdentityID, enabled); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channel":    req.Channel,
		"identityId": req.IdentityID,
		"enabled":    enabled,
	})
}

// mutateIdentityGroupsEnabled flips the groupsEnabled flag on a single
// identity in both the in-memory cfg AND the .opencode.json on disk per
// the config-atomic-writeback spec's caller contract (mutate cfg
// alongside calling UpdateCfgFile so the running bridge sees the change
// without a restart).
func (s *Service) mutateIdentityGroupsEnabled(channel, identityID string, enabled bool) error {
	s.cfgMu.Lock()
	if err := mutateIdentity(s.cfg, channel, identityID, func(t *bridge.TelegramIdentity, sl *bridge.SlackIdentity, mm *bridge.MattermostIdentity) {
		switch {
		case t != nil:
			t.GroupsEnabled = enabled
		case sl != nil:
			sl.GroupsEnabled = enabled
		case mm != nil:
			mm.GroupsEnabled = enabled
		}
	}); err != nil {
		s.cfgMu.Unlock()
		return err
	}
	s.cfgMu.Unlock()
	return config.UpdateCfgFile(func(c *config.Config) {
		if c.Router == nil {
			return
		}
		_ = mutateIdentity(c.Router, channel, identityID, func(t *bridge.TelegramIdentity, sl *bridge.SlackIdentity, mm *bridge.MattermostIdentity) {
			switch {
			case t != nil:
				t.GroupsEnabled = enabled
			case sl != nil:
				sl.GroupsEnabled = enabled
			case mm != nil:
				mm.GroupsEnabled = enabled
			}
		})
	})
}

// mutateIdentity finds the identity in cfg.Router and invokes fn with
// the right channel-specific pointer (and nils for the others). Returns
// an error if no matching identity exists.
func mutateIdentity(
	cfg *bridge.Config,
	channel, identityID string,
	fn func(*bridge.TelegramIdentity, *bridge.SlackIdentity, *bridge.MattermostIdentity),
) error {
	if cfg == nil {
		return errors.New("bridge: config is nil")
	}
	switch channel {
	case "telegram":
		if cfg.Channels.Telegram == nil {
			return fmt.Errorf("telegram channel not configured")
		}
		for i := range cfg.Channels.Telegram.Bots {
			if cfg.Channels.Telegram.Bots[i].ID == identityID {
				fn(&cfg.Channels.Telegram.Bots[i], nil, nil)
				return nil
			}
		}
		return fmt.Errorf("telegram identity %q not found", identityID)
	case "slack":
		if cfg.Channels.Slack == nil {
			return fmt.Errorf("slack channel not configured")
		}
		for i := range cfg.Channels.Slack.Apps {
			if cfg.Channels.Slack.Apps[i].ID == identityID {
				fn(nil, &cfg.Channels.Slack.Apps[i], nil)
				return nil
			}
		}
		return fmt.Errorf("slack identity %q not found", identityID)
	case "mattermost":
		if cfg.Channels.Mattermost == nil {
			return fmt.Errorf("mattermost channel not configured")
		}
		for i := range cfg.Channels.Mattermost.Instances {
			if cfg.Channels.Mattermost.Instances[i].ID == identityID {
				fn(nil, nil, &cfg.Channels.Mattermost.Instances[i])
				return nil
			}
		}
		return fmt.Errorf("mattermost identity %q not found", identityID)
	default:
		return fmt.Errorf("unknown channel %q", channel)
	}
}
