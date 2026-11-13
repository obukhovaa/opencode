package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/slashcmd"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

func TestInputHistoryDraftAndMultilineNavigation(t *testing.T) {
	m := &editorCmp{textarea: textarea.New(), height: 6, mode: modeNormal}
	m.textarea.SetWidth(80)
	m.textarea.Focus()
	m.rememberInput("first")
	m.rememberInput("second")
	m.rememberInput("second")
	m.textarea.SetValue("unfinished draft")
	m.attachments = []message.Attachment{{}}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.textarea.Value() != "second" || len(m.attachments) != 0 {
		t.Fatal("latest input not recalled independently of draft attachments")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.textarea.Value() != "first" {
		t.Fatal("older input missing")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.textarea.Value() != "unfinished draft" || len(m.attachments) != 1 {
		t.Fatal("draft or attachments lost")
	}
	m.textarea.SetValue("first line\nsecond line")
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.textarea.Value() != "first line\nsecond line" || m.history.offset != 0 {
		t.Fatal("multiline cursor movement intercepted")
	}
	m.textarea.SetValue(strings.Repeat("word ", 50))
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.history.offset != 0 {
		t.Fatal("wrapped cursor movement intercepted")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt})
	if cmd == nil {
		t.Fatal("no activity shortcut")
	}
	if _, ok := cmd().(FocusActivitiesMsg); !ok {
		t.Fatal("wrong activity shortcut result")
	}
}
func TestInputHistoryPersistenceAndConcurrentWriters(t *testing.T) {
	path := inputHistoryPath(t.TempDir(), ".agents", "/project")
	var wg sync.WaitGroup
	for _, text := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := appendInputHistory(path, text); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entries, err := readInputHistory(path)
	if err != nil || len(entries) != 2 {
		t.Fatalf("lost concurrent writes: %v %v", entries, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("history permissions not private")
	}
	if inputHistoryPath("/home", ".agents-a", "/project") == inputHistoryPath("/home", ".agents-b", "/project") {
		t.Fatal("profiles share history")
	}
	if filepath.Base(inputHistoryPath("/home", ".agents", "/a")) == filepath.Base(inputHistoryPath("/home", ".agents", "/b")) {
		t.Fatal("projects share history")
	}
	all := make([]string, 0, 210)
	for i := 0; i < 210; i++ {
		all = append(all, strings.Repeat("x", i+1))
	}
	if len(trimInputHistory(all)) != 200 {
		t.Fatal("history not bounded")
	}
}
func TestInputHistoryLoadsBeforeSavingPendingSubmissions(t *testing.T) {
	path := inputHistoryPath(t.TempDir(), ".agents", "/project")
	if err := appendInputHistory(path, "old"); err != nil {
		t.Fatal(err)
	}
	m := &editorCmp{history: inputHistory{path: path}}
	if m.rememberInput("new") != nil {
		t.Fatal("save raced initial load")
	}
	_, cmd := m.Update(m.loadInputHistory()())
	if cmd == nil {
		t.Fatal("pending save did not start after load")
	}
	result := cmd()
	saved, ok := result.(InputHistorySavedMsg)
	if !ok || saved.err != nil {
		t.Fatalf("save failed: %#v", result)
	}
	m.Update(saved)
	entries, err := readInputHistory(path)
	if err != nil || strings.Join(entries, ",") != "old,new" {
		t.Fatalf("history reordered: %v %v", entries, err)
	}
	if _, ok := result.(util.InfoMsg); ok {
		t.Fatal("unexpected warning")
	}
}

func TestInputHistoryRetainsRawCommandAndRejectsExpansionFailure(t *testing.T) {
	m := &editorCmp{textarea: textarea.New(), expand: func(text string) (slashcmd.Expansion, error) {
		return slashcmd.Expansion{Action: &slashcmd.CommandInfo{ID: "flow"}, ActionArgs: "do work"}, nil
	}}
	m.textarea.SetValue("/flow do work")
	m.send()
	if len(m.history.entries) != 1 || m.history.entries[0] != "/flow do work" {
		t.Fatal("raw command lost")
	}
	m.expand = func(string) (slashcmd.Expansion, error) {
		return slashcmd.Expansion{}, fmt.Errorf("invalid invocation")
	}
	m.textarea.SetValue("/invalid")
	m.send()
	if len(m.history.entries) != 1 || m.textarea.Value() != "/invalid" {
		t.Fatal("rejected input changed history or draft")
	}
	m.rememberInput(strings.Repeat("x", inputHistoryTextLimit+1))
	if len(m.history.entries) != 1 {
		t.Fatal("oversized input retained")
	}
}
