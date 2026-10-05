package clitool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/task"
)

// CaptureHelp runs the manifest's `help.args` once (bounded by
// helpCaptureTimeout) and stores the first help.maxBytes of stdout+stderr
// in HelpText. It is a no-op without a `help` block or a resolved binary.
func (m *Manifest) CaptureHelp(ctx context.Context) {
	if m.Help == nil || m.ResolvedCommand == "" {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, helpCaptureTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, m.ResolvedCommand, m.Help.Args...)
	cmd.Dir = m.ResolvedCwd
	cmd.Env = m.BuildEnv(os.Environ())
	cmd.Stdin = nil
	// --help output is rarely large, but a CLI that pages or spins must not
	// hold the loader: cap the capture at the byte budget plus one.
	limit := m.Help.MaxBytes + 1
	out := &limitedBuffer{limit: limit}
	cmd.Stdout = out
	cmd.Stderr = out
	// Same containment as Exec: an own process group so the deadline kills a
	// wrapper script's children too, and a bounded wait so a grandchild that
	// inherited the pipe cannot stall discovery (this runs on the toolset
	// build path, before the agent can answer).
	task.SetProcessGroupAttr(cmd)
	cmd.Cancel = func() error {
		task.SignalProcessGroup(cmd.Process, task.KillSignal())
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	_ = cmd.Run() // a non-zero exit still yields usable text (many CLIs exit 1 on --help)
	text := strings.TrimRight(out.String(), "\n")
	if text == "" {
		return
	}
	if out.truncated || len(text) > m.Help.MaxBytes {
		if len(text) > m.Help.MaxBytes {
			text = text[:m.Help.MaxBytes]
		}
		text += fmt.Sprintf("\n… [help truncated at %d bytes; run the tool with %s for the rest]", m.Help.MaxBytes, strings.Join(m.Help.Args, " "))
	}
	m.HelpText = text
}
