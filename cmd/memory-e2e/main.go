// Command memory-e2e is a black-box driver for the project-memory feature,
// invoked from scripts/test/memory.sh with cwd set to a prepared sandbox. It
// runs the same config.Load the real binary uses (.opencode.json → viper →
// Config, plus the AGENTS_MEMORY_HOME override), the real Git-backed project
// identity, prompt assembly via prompt.GetAgentPrompt, and the index snapshot
// the agent loop injects. It emits a JSON report; the script asserts it with
// jq and recomputes the project key independently, the way mem-sync does.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/prompt"
	"github.com/opencode-ai/opencode/internal/memory"
)

type report struct {
	Root          string `json:"root"`
	ProjectID     string `json:"project_id"`
	ProjectKey    string `json:"project_key"`
	Directory     string `json:"directory"`
	CanRead       bool   `json:"can_read"`
	CanWrite      bool   `json:"can_write"`
	Reminder      bool   `json:"reminder"`
	ReminderDir   bool   `json:"reminder_names_directory"`
	Snapshot      string `json:"snapshot"`
	PromptStable  bool   `json:"prompt_stable"`
	SnapshotFresh bool   `json:"snapshot_has_marker"`
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Getwd:", err)
		os.Exit(2)
	}
	if _, err := config.Load(cwd, false); err != nil {
		fmt.Fprintln(os.Stderr, "config.Load:", err)
		os.Exit(2)
	}
	cfg := config.Get()

	m := prompt.ResolveMemoryContext(cfg, agentregistry.GetRegistry(), string(config.AgentCoder))
	first := prompt.GetAgentPrompt(config.AgentCoder, models.ProviderAnthropic)
	second := prompt.GetAgentPrompt(config.AgentCoder, models.ProviderAnthropic)

	r := report{
		Root:         m.Root,
		ProjectID:    m.ID,
		Directory:    m.Directory,
		CanRead:      m.CanRead,
		CanWrite:     m.CanWrite,
		Reminder:     strings.Contains(first, "Project memory: "),
		PromptStable: first == second,
		Snapshot:     m.Snapshot(),
	}
	if m.ID != "" {
		r.ProjectKey = memory.Key(m.ID)
	}
	r.ReminderDir = m.Directory != "" && strings.Contains(first, "Directory: "+m.Directory+".")
	r.SnapshotFresh = r.Snapshot != "" && strings.HasPrefix(r.Snapshot, "<system-reminder>\n"+m.Marker()+"\n")

	out, _ := json.Marshal(r)
	fmt.Println(string(out))
}
