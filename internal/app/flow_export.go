package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/flowrun"
	"github.com/opencode-ai/opencode/internal/logging"
)

// Local exports are optional recovery aids; the session backend is authoritative.
// A version-specific path means a delayed export cannot overwrite a newer plan.
func exportFlowDefinition(record flowrun.Run) {
	dir, err := flow.DynamicDirectory()
	if err == nil {
		dir = filepath.Join(dir, "exports")
		err = os.MkdirAll(dir, 0700)
	}
	if err != nil {
		logging.Warn("Could not export flow definition", "run_id", record.ID, "err", err)
		return
	}
	payload, err := json.MarshalIndent(struct {
		RunID          string          `json:"runId"`
		ConversationID string          `json:"conversationId"`
		Kind           string          `json:"kind"`
		Version        int64           `json:"version"`
		Definition     json.RawMessage `json:"definition"`
	}{record.ID, record.ConversationID, record.Kind, record.Version, record.Snapshot}, "", "  ")
	if err != nil {
		return
	}
	temporary, err := os.CreateTemp(dir, ".flow-export-*")
	if err != nil {
		logging.Warn("Could not create flow export", "run_id", record.ID, "err", err)
		return
	}
	defer os.Remove(temporary.Name())
	if _, err = temporary.Write(payload); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary.Name(), filepath.Join(dir, flowKey(record.ID)+"-v"+strconv.FormatInt(record.Version, 10)+".json"))
	}
	if err != nil {
		logging.Warn("Could not save flow export", "run_id", record.ID, "err", err)
	}
}
