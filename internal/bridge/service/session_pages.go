package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/session"
)

type sessionListArgs struct {
	search, cursor string
	all            bool
}

func parseSessionListArgs(text string) (sessionListArgs, error) {
	var result sessionListArgs
	words := strings.Fields(text)
	var search []string
	for i := 0; i < len(words); i++ {
		switch words[i] {
		case "--all":
			result.all = true
		case "--more":
			if i+1 == len(words) {
				return result, errors.New("use /sessions --more <cursor>")
			}
			i++
			result.cursor = words[i]
		default:
			search = append(search, words[i])
		}
	}
	result.search = strings.Join(search, " ")
	return result, nil
}
func (s *Service) bridgeSessionPage(ctx context.Context, in bridge.Inbound, current string, options session.PageOptions) (session.Page, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if in.Peer.Channel == externalChannel && current == "" {
		return session.Page{Sessions: []session.Session{}}, nil
	}
	options.CurrentID = current
	options.ExcludeExternal = s.cfg.ExternalSessionsIsolated()
	if in.Peer.Channel == externalChannel {
		options.OnlyID = current
	}
	page, err := s.app.Sessions.Page(ctx, options)
	if err == nil || errors.Is(err, session.ErrInvalidCursor) {
		return page, err
	}
	// An unavailable privacy query never widens the result. Only the already
	// authorized current binding may be shown while storage recovers.
	if current != "" && (options.ExcludeExternal || in.Peer.Channel == externalChannel) {
		logging.Warn("bridge: session visibility query failed; showing only current session", "err", err)
		row, getErr := s.app.Sessions.Get(ctx, current)
		if getErr == nil {
			if options.IDPrefix != "" && !strings.HasPrefix(row.ID, options.IDPrefix) {
				return session.Page{Sessions: []session.Session{}}, nil
			}
			if options.Search != "" && !strings.Contains(strings.ToLower(row.Title+" "+row.ID), strings.ToLower(options.Search)) {
				return session.Page{Sessions: []session.Session{}}, nil
			}
			return session.Page{Sessions: []session.Session{row}}, nil
		}
	}
	return session.Page{}, fmt.Errorf("session page unavailable: %w", err)
}
