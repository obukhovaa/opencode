package tui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
)

type tuiSessionPager struct {
	session.Service
	calls atomic.Int32
	fetch func(context.Context, session.PageOptions) (session.Page, error)
}

func (p *tuiSessionPager) Page(ctx context.Context, o session.PageOptions) (session.Page, error) {
	p.calls.Add(1)
	return p.fetch(ctx, o)
}
func (*tuiSessionPager) List(context.Context) ([]session.Session, error) {
	panic("TUI must not load every session")
}
func pagingModel(p *tuiSessionPager) appModel {
	return appModel{app: &app.App{Sessions: p}, sessionDialog: dialog.NewSessionDialogCmp(), deleteSessionDialog: dialog.NewSessionDialogCmp()}
}

func TestSessionPickerLoadsAsynchronouslyAndCancelsOnClose(t *testing.T) {
	entered := make(chan struct{})
	pager := &tuiSessionPager{fetch: func(ctx context.Context, o session.PageOptions) (session.Page, error) {
		if o.Limit != session.DefaultPageSize || !o.RootsOnly || !o.WithFlow {
			t.Errorf("unbounded/expensive list options: %+v", o)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("page has no deadline")
		}
		close(entered)
		<-ctx.Done()
		return session.Page{}, ctx.Err()
	}}
	model, cmd := pagingModel(pager).openSessionPage(false)
	a := model.(appModel)
	if pager.calls.Load() != 0 || !a.showSessionDialog || !strings.Contains(a.sessionDialog.View().Content, "loading") {
		t.Fatal("opening dialog performed blocking I/O")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	model, _ = a.Update(dialog.CloseSessionDialogMsg{})
	a = model.(appModel)
	select {
	case msg := <-result:
		model, _ = a.acceptSessionPage(msg.(sessionPageLoadedMsg))
		a = model.(appModel)
	case <-time.After(time.Second):
		t.Fatal("closing did not cancel database work")
	}
	if a.showSessionDialog {
		t.Fatal("late reply reopened a closed picker")
	}
}
func TestSessionPickerDebouncesAndIgnoresStaleSearch(t *testing.T) {
	var searches []string
	pager := &tuiSessionPager{fetch: func(ctx context.Context, o session.PageOptions) (session.Page, error) {
		searches = append(searches, o.Search)
		return session.Page{Sessions: []session.Session{{ID: "id-" + o.Search, Title: "Result-" + o.Search}}}, nil
	}}
	model, initial := pagingModel(pager).openSessionPage(false)
	a := model.(appModel)
	old := initial().(sessionPageLoadedMsg)
	model, obsolete := a.Update(dialog.SessionSearchMsg{Query: "n"})
	a = model.(appModel)
	model, current := a.Update(dialog.SessionSearchMsg{Query: "new"})
	a = model.(appModel)
	if msg := obsolete().(sessionPageLoadedMsg); msg.err != context.Canceled {
		t.Fatalf("obsolete debounced query ran: %+v", msg)
	}
	model, _ = a.acceptSessionPage(old)
	a = model.(appModel)
	if strings.Contains(a.sessionDialog.View().Content, "Result-") {
		t.Fatal("stale reply painted old results")
	}
	model, _ = a.acceptSessionPage(current().(sessionPageLoadedMsg))
	a = model.(appModel)
	if !strings.Contains(a.sessionDialog.View().Content, "Result-new") || len(searches) != 2 || searches[1] != "new" {
		t.Fatalf("searches=%v view=%s", searches, a.sessionDialog.View().Content)
	}
}
func TestSessionPickerReplacesPagesAndClearsRowsOnFailure(t *testing.T) {
	pager := &tuiSessionPager{fetch: func(ctx context.Context, o session.PageOptions) (session.Page, error) {
		if o.Cursor == "next" {
			return session.Page{Sessions: []session.Session{{ID: "second", Title: "SECOND PAGE"}}}, nil
		}
		return session.Page{Sessions: []session.Session{{ID: "first", Title: "FIRST PAGE"}}, NextCursor: "next"}, nil
	}}
	model, cmd := pagingModel(pager).openSessionPage(false)
	a := model.(appModel)
	model, _ = a.acceptSessionPage(cmd().(sessionPageLoadedMsg))
	a = model.(appModel)
	model, cmd = a.Update(dialog.SessionPageMoveMsg{Direction: 1})
	a = model.(appModel)
	model, _ = a.acceptSessionPage(cmd().(sessionPageLoadedMsg))
	a = model.(appModel)
	view := a.sessionDialog.View().Content
	if !strings.Contains(view, "SECOND PAGE") || strings.Contains(view, "FIRST PAGE") || len(a.sessionList.previous) != 1 {
		t.Fatal("pages accumulated or navigation lost", view)
	}
	model, _ = a.acceptSessionPage(sessionPageLoadedMsg{request: a.sessionList.request, err: errors.New("query failed")})
	a = model.(appModel)
	if strings.Contains(a.sessionDialog.View().Content, "SECOND PAGE") {
		t.Fatal("failed search left selectable stale rows")
	}
	_, selected := a.sessionDialog.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if selected != nil {
		t.Fatal("stale row could be selected after failure")
	}
}
func TestPruneAndSwitchPagingNeverOverlap(t *testing.T) {
	pager := &tuiSessionPager{fetch: func(context.Context, session.PageOptions) (session.Page, error) { return session.Page{}, nil }}
	model, cmd := pagingModel(pager).openSessionPage(true)
	a := model.(appModel)
	cmd()
	model, cmd = a.openSessionPage(false)
	a = model.(appModel)
	cmd()
	if !a.showSessionDialog || a.showDeleteSessionDialog {
		t.Fatal("switch picker retained prune mode")
	}
}
