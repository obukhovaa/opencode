package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/opencode-ai/opencode/internal/tui/page"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/question"
)

func TestMissingOpenSpecInstallationRequiresChoice(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	marker := filepath.Join(dir, "installed")
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	svc := question.NewService()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := svc.Subscribe(ctx)
	go func() { request := <-events; _ = svc.Reply(request.Payload.ID, [][]string{{"Cancel"}}) }()
	err := ensureFlowPrerequisite(ctx, svc, "root")
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("error: %v", err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("installer ran without consent")
	}
}

func TestBareFlowCommandOpensProgressPage(t *testing.T) {
	m := appModel{currentPage: page.ChatPage, pages: map[page.PageID]tea.Model{page.FlowsPage: page.NewFlowsPage(nil)}, loadedPages: map[page.PageID]bool{}, width: 80, height: 24}
	updated, _ := m.Update(page.StartDynamicFlowMsg{})
	if updated.(appModel).currentPage != page.FlowsPage {
		t.Fatal("bare /flow did not switch page")
	}
}
