package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type sessionListState struct {
	query, cursor, next string
	previous            []string
	deleting            bool
	request             uint64
	cancel              context.CancelFunc
}
type sessionPageLoadedMsg struct {
	request uint64
	page    session.Page
	err     error
}

func (a appModel) sessionPageOpen(deleting bool) bool {
	return a.sessionList.deleting == deleting && (a.showSessionDialog || a.showDeleteSessionDialog)
}
func (a appModel) sessionPageDialog() dialog.SessionDialog {
	if a.sessionList.deleting {
		return a.deleteSessionDialog
	}
	return a.sessionDialog
}
func (a appModel) openSessionPage(deleting bool) (tea.Model, tea.Cmd) {
	if a.sessionList.cancel != nil {
		a.sessionList.cancel()
	}
	request := a.sessionList.request + 1
	a.sessionList = sessionListState{deleting: deleting, request: request}
	a.showSessionDialog = !deleting
	a.showDeleteSessionDialog = deleting
	if deleting {
		a.deleteSessionDialog.SetTitle("Prune Session")
	}
	a.sessionPageDialog().SetPagination(deleting)
	return a.loadSessionPage(false)
}
func (a appModel) loadSessionPage(debounce bool) (tea.Model, tea.Cmd) {
	if a.sessionList.cancel != nil {
		a.sessionList.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	a.sessionList.cancel = cancel
	a.sessionList.request++
	a.sessionPageDialog().SetLoading(true)
	request, application := a.sessionList.request, a.app
	options := session.PageOptions{Limit: session.DefaultPageSize, Cursor: a.sessionList.cursor, Search: a.sessionList.query, RootsOnly: true, WithFlow: true}
	selected := a.selectedSession.ID
	if options.Search == "" && !a.sessionList.deleting {
		options.ExcludeID = selected
	}
	deleting := a.sessionList.deleting
	return a, func() tea.Msg {
		defer cancel()
		if debounce {
			timer := time.NewTimer(180 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return sessionPageLoadedMsg{request: request, err: ctx.Err()}
			case <-timer.C:
			}
		}
		page, err := application.Sessions.Page(ctx, options)
		if err == nil && !deleting && options.Cursor == "" && options.Search == "" && selected != "" {
			found := false
			for _, row := range page.Sessions {
				found = found || row.ID == selected
			}
			if !found {
				current, getErr := application.Sessions.Get(ctx, selected)
				if getErr == nil && current.ParentSessionID == "" && (current.RootSessionID == "" || current.RootSessionID == current.ID) {
					page.Sessions = append([]session.Session{current}, page.Sessions...)
				}
			}
		}
		return sessionPageLoadedMsg{request: request, page: page, err: err}
	}
}
func (a appModel) acceptSessionPage(msg sessionPageLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.request != a.sessionList.request || (!a.showSessionDialog && !a.showDeleteSessionDialog) {
		return a, nil
	}
	a.sessionList.cancel = nil
	a.sessionPageDialog().SetLoading(false)
	if msg.err != nil {
		a.sessionList.next = ""
		a.sessionPageDialog().SetPage(nil, "", len(a.sessionList.previous) > 0)
		return a, util.ReportError(msg.err)
	}
	a.sessionList.next = msg.page.NextCursor
	a.sessionPageDialog().SetPage(msg.page.Sessions, msg.page.NextCursor, len(a.sessionList.previous) > 0)
	return a, nil
}
