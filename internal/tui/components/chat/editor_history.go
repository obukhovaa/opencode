package chat

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/fileutil"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

const inputHistoryLimit = 200
const inputHistoryTextLimit = 64 * 1024

type InputHistoryLoadedMsg struct {
	entries []string
	err     error
}
type InputHistorySavedMsg struct{ err error }

type inputHistory struct {
	ready              bool
	path               string
	entries            []string
	pending            []string
	offset             int
	draft              string
	draftAttachments   []message.Attachment
	draftRow, draftCol int
}

func inputHistoryPath(home, profileDir, workingDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(workingDir)))
	return filepath.Join(home, profileDir, "tui", "history", fmt.Sprintf("%x.json", sum[:12]))
}
func readInputHistory(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []string
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return trimInputHistory(entries), nil
}
func trimInputHistory(entries []string) []string {
	out := make([]string, 0, min(len(entries), inputHistoryLimit))
	for _, text := range entries {
		if strings.TrimSpace(text) == "" || len(text) > inputHistoryTextLimit {
			continue
		}
		if len(out) == 0 || out[len(out)-1] != text {
			out = append(out, text)
		}
	}
	if len(out) > inputHistoryLimit {
		out = out[len(out)-inputHistoryLimit:]
	}
	return out
}
func appendInputHistory(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Serialize read/replace across OpenCode instances. This runs in a Tea
	// command, never on the input/render path.
	var lock *fileutil.Lock
	var err error
	for attempt := 0; attempt < 100; attempt++ {
		lock, err = fileutil.Acquire(path + ".lock")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	defer lock.Release()
	entries, err := readInputHistory(path)
	if err != nil {
		return err
	}
	data, err := json.Marshal(trimInputHistory(append(entries, text)))
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".history-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func (m *editorCmp) loadInputHistory() tea.Cmd {
	path := m.history.path
	if path == "" {
		return nil
	}
	return func() tea.Msg { entries, err := readInputHistory(path); return InputHistoryLoadedMsg{entries, err} }
}
func (m *editorCmp) saveInputHistory() tea.Cmd {
	if !m.history.ready || m.history.path == "" || len(m.history.pending) == 0 {
		return nil
	}
	path, text := m.history.path, m.history.pending[0]
	return func() tea.Msg { return InputHistorySavedMsg{appendInputHistory(path, text)} }
}
func (m *editorCmp) rememberInput(text string) tea.Cmd {
	m.history.offset = 0
	m.history.draft = ""
	m.history.draftAttachments = nil
	if strings.TrimSpace(text) == "" || len(text) > inputHistoryTextLimit {
		return nil
	}
	entries := m.history.entries
	if len(entries) > 0 && entries[len(entries)-1] == text {
		return nil
	}
	m.history.entries = trimInputHistory(append(entries, text))
	if m.history.path == "" {
		return nil
	}
	m.history.pending = append(m.history.pending, text)
	if len(m.history.pending) == 1 {
		return m.saveInputHistory()
	}
	return nil
}
func (m *editorCmp) recallInput(up bool) bool {
	if len(m.history.entries) == 0 {
		return false
	}
	if up {
		if m.history.offset == 0 {
			m.history.draft = m.textarea.Value()
			m.history.draftAttachments = append([]message.Attachment(nil), m.attachments...)
			m.history.draftRow, m.history.draftCol = m.textarea.Line(), m.textarea.Column()
		}
		if m.history.offset < len(m.history.entries) {
			m.history.offset++
		}
	} else {
		if m.history.offset == 0 {
			return false
		}
		m.history.offset--
	}
	if m.history.offset == 0 {
		m.textarea.SetValue(m.history.draft)
		m.attachments = m.history.draftAttachments
		restoreCursor(&m.textarea, m.history.draftRow, m.history.draftCol)
	} else {
		m.textarea.SetValue(m.history.entries[len(m.history.entries)-m.history.offset])
		m.attachments = nil
		// Start at the first row when moving backwards, so consecutive Up
		// presses can traverse history even when the recalled text is multiline.
		if up {
			m.textarea.MoveToBegin()
		}
	}
	m.syncTextareaHeight()
	return true
}
func (m *editorCmp) restoreHistoryDraft() {
	if m.history.offset > 0 {
		m.history.offset = 1
		m.recallInput(false)
	}
}

func (m *editorCmp) inputHistoryUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case InputHistoryLoadedMsg:
		m.history.ready = true
		if msg.err != nil {
			// Preserve an unread file rather than overwriting it with only this
			// launch's entries. Recall still works in memory.
			m.history.path = ""
			m.history.pending = nil
			return util.ReportWarn("Could not load input history; recall is available for this launch only"), true
		}
		// Submissions made while disk loading was in flight remain newest.
		m.history.entries = trimInputHistory(append(msg.entries, m.history.entries...))
		return m.saveInputHistory(), true
	case InputHistorySavedMsg:
		if len(m.history.pending) > 0 {
			m.history.pending = m.history.pending[1:]
		}
		next := m.saveInputHistory()
		if msg.err != nil {
			return tea.Batch(next, util.ReportWarn("Could not save input history; available for this launch only")), true
		}
		return next, true
	}
	return nil, false
}
func newInputHistory() inputHistory {
	home, err := os.UserHomeDir()
	if err != nil {
		return inputHistory{}
	}
	return inputHistory{path: inputHistoryPath(home, config.ProfileAgentsDirName(), config.Get().WorkingDir)}
}
