package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
)

type PersistentShell struct {
	cmd          *exec.Cmd
	stdin        *os.File
	isAlive      bool
	cwd          string
	mu           sync.Mutex
	commandQueue chan *commandExecution
}

type commandExecution struct {
	command    string
	timeout    time.Duration
	resultChan chan commandResult
	ctx        context.Context
}

type commandResult struct {
	stdout      string
	stderr      string
	exitCode    int
	interrupted bool
	err         error
}

var (
	shellInstances   = make(map[string]*PersistentShell)
	shellInstancesMu sync.Mutex
)

func GetPersistentShell(workingDir string) *PersistentShell {
	shellInstancesMu.Lock()
	defer shellInstancesMu.Unlock()

	if sh, ok := shellInstances[workingDir]; ok && sh != nil && sh.isAlive {
		return sh
	}

	sh := newPersistentShell(workingDir)
	if sh == nil {
		return nil
	}
	shellInstances[workingDir] = sh
	return sh
}

// GetShellPath returns the shell path resolved from config, $SHELL, or /bin/bash default.
func GetShellPath() string {
	cfg := config.Get()
	if cfg != nil && cfg.Shell.Path != "" {
		return cfg.Shell.Path
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/bash"
}

func newPersistentShell(cwd string) *PersistentShell {
	shellPath := GetShellPath()

	cfg := config.Get()
	var shellArgs []string
	if cfg != nil {
		shellArgs = cfg.Shell.Args
	}

	// Default shell args
	if len(shellArgs) == 0 {
		shellArgs = []string{"-l"}
	}

	cmd := exec.Command(shellPath, shellArgs...)
	cmd.Dir = cwd

	// Own session, no controlling terminal. Without this every descendant can
	// open /dev/tty and write straight onto the terminal opencode renders on.
	detachFromTerminal(cmd)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}

	// The environment's job here is to make an interactive prompt fail fast and
	// legibly, never to answer one. Do NOT add SUDO_ASKPASS / SSH_ASKPASS /
	// GIT_ASKPASS: a helper returning an empty string turns a clear "no
	// terminal" failure into a confusing "authentication failed", and one
	// returning anything real would put opencode in the business of handling
	// plaintext credentials.
	cmd.Env = append(os.Environ(),
		"GIT_EDITOR=true",
		"GIT_TERMINAL_PROMPT=0",
	)

	err = cmd.Start()
	if err != nil {
		logging.Error(fmt.Sprintf("Can't start shell: %s", err.Error()))
		return nil
	}

	shell := &PersistentShell{
		cmd:          cmd,
		stdin:        stdinPipe.(*os.File),
		isAlive:      true,
		cwd:          cwd,
		commandQueue: make(chan *commandExecution, 10),
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "Panic in shell command processor: %v\n", r)
				shell.isAlive = false
				close(shell.commandQueue)
			}
		}()
		shell.processCommands()
	}()

	go func() {
		err := cmd.Wait()
		if err != nil {
			logging.Error(fmt.Sprintf("Can't complete shell command: %s", err.Error()))
		}
		shell.isAlive = false
		close(shell.commandQueue)
	}()

	return shell
}

func (s *PersistentShell) processCommands() {
	for cmd := range s.commandQueue {
		result := s.execCommand(cmd.command, cmd.timeout, cmd.ctx)
		cmd.resultChan <- result
	}
}

func (s *PersistentShell) execCommand(command string, timeout time.Duration, ctx context.Context) commandResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isAlive {
		return commandResult{
			stderr:   "Shell is not alive",
			exitCode: 1,
			err:      errors.New("shell is not alive"),
		}
	}

	tempDir := os.TempDir()
	stdoutFile := filepath.Join(tempDir, fmt.Sprintf("opencode-stdout-%d", time.Now().UnixNano()))
	stderrFile := filepath.Join(tempDir, fmt.Sprintf("opencode-stderr-%d", time.Now().UnixNano()))
	statusFile := filepath.Join(tempDir, fmt.Sprintf("opencode-status-%d", time.Now().UnixNano()))
	cwdFile := filepath.Join(tempDir, fmt.Sprintf("opencode-cwd-%d", time.Now().UnixNano()))

	defer func() {
		os.Remove(stdoutFile)
		os.Remove(stderrFile)
		os.Remove(statusFile)
		os.Remove(cwdFile)
	}()

	fullCommand := fmt.Sprintf(`
eval %s < /dev/null > %s 2> %s
EXEC_EXIT_CODE=$?
pwd > %s
echo $EXEC_EXIT_CODE > %s
`,
		shellQuote(command),
		shellQuote(stdoutFile),
		shellQuote(stderrFile),
		shellQuote(cwdFile),
		shellQuote(statusFile),
	)

	_, err := s.stdin.Write([]byte(fullCommand + "\n"))
	if err != nil {
		return commandResult{
			stderr:   fmt.Sprintf("Failed to write command to shell: %v", err),
			exitCode: 1,
			err:      err,
		}
	}

	interrupted := false

	startTime := time.Now()

	done := make(chan bool)
	go func() {
		for {
			select {
			case <-ctx.Done():
				s.terminateDescendants()
				interrupted = true
				done <- true
				return

			case <-time.After(10 * time.Millisecond):
				if fileExists(statusFile) && fileSize(statusFile) > 0 {
					done <- true
					return
				}

				if !s.isAlive {
					interrupted = true
					done <- true
					return
				}

				if timeout > 0 {
					elapsed := time.Since(startTime)
					if elapsed > timeout {
						s.terminateDescendants()
						interrupted = true
						done <- true
						return
					}
				}
			}
		}
	}()

	<-done

	stdout := readFileOrEmpty(stdoutFile)
	stderr := readFileOrEmpty(stderrFile)
	exitCodeStr := readFileOrEmpty(statusFile)
	newCwd := readFileOrEmpty(cwdFile)

	exitCode := 0
	if exitCodeStr != "" {
		fmt.Sscanf(exitCodeStr, "%d", &exitCode)
	} else if interrupted {
		exitCode = 143
		stderr += "\nCommand execution timed out or was interrupted"
	}

	if newCwd != "" {
		s.cwd = strings.TrimSpace(newCwd)
	}

	return commandResult{
		stdout:      stdout,
		stderr:      stderr,
		exitCode:    exitCode,
		interrupted: interrupted,
	}
}

// maxDescendantDepth bounds the process-tree walk. A pgrep result that somehow
// cycles (or a fork bomb) must not spin this loop forever; 32 levels is far
// past any real command's process depth.
const maxDescendantDepth = 32

// descendantGracePeriod is how long a descendant gets to exit after SIGTERM
// before it is force-killed.
const descendantGracePeriod = 2 * time.Second

// terminateDescendants signals the whole process tree below the persistent
// shell: SIGTERM deepest-first, then SIGKILL to whatever is still alive after
// the grace period.
//
// The old implementation signalled only the direct children reported by a
// single `pgrep -P`, so a command like `sh -c 'foo | bar'` left grandchildren
// running after a cancel or timeout.
//
// Note what this deliberately does NOT do: kill(-pgid). The shell now runs with
// Setsid (see detachFromTerminal), which makes it its own process-group leader,
// so a group-targeted signal would take the persistent shell down with its
// children — and the shell has to survive to run the next command.
//
// The walk is not atomic: a process spawned while it runs can be missed. That
// is acceptable here. This is a cancellation path, not a security boundary, and
// the behavior it replaces missed every grandchild unconditionally.
func (s *PersistentShell) terminateDescendants() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	shellPID := s.cmd.Process.Pid

	// Breadth-first by depth so the collected slice is ordered shallowest-first;
	// signalling walks it in reverse to reach leaves before their parents.
	var descendants []int
	frontier := []int{shellPID}
	for depth := 0; depth < maxDescendantDepth && len(frontier) > 0; depth++ {
		var next []int
		for _, pid := range frontier {
			for _, child := range childPIDs(pid) {
				// The shell itself can never be a descendant of itself, but
				// guard anyway: signalling it would kill the session.
				if child == shellPID || child <= 0 {
					continue
				}
				descendants = append(descendants, child)
				next = append(next, child)
			}
		}
		frontier = next
	}
	if len(descendants) == 0 {
		return
	}

	for i := len(descendants) - 1; i >= 0; i-- {
		if proc, err := os.FindProcess(descendants[i]); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}

	// Escalate off the caller's goroutine. execCommand's context watcher calls
	// this and then reports the result, so blocking here for the grace period
	// would add that delay to every cancel the user asks for.
	go func(pids []int) {
		time.Sleep(descendantGracePeriod)
		for i := len(pids) - 1; i >= 0; i-- {
			proc, err := os.FindProcess(pids[i])
			if err != nil {
				continue
			}
			// Signal 0 reports liveness without disturbing a live process. On
			// Windows Process.Signal rejects it, so nothing is force-killed
			// there — the same POSIX-first stance the background-task subsystem
			// takes (internal/task/procgroup_windows.go).
			if err := proc.Signal(syscall.Signal(0)); err != nil {
				continue
			}
			_ = proc.Kill()
		}
	}(descendants)
}

// childPIDs returns the direct children of pid according to pgrep. A pgrep that
// is missing or reports nothing yields an empty slice, which ends the walk.
func childPIDs(pid int) []int {
	output, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	var pids []int
	for line := range strings.SplitSeq(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		child, err := strconv.Atoi(line)
		if err != nil || child <= 0 {
			continue
		}
		pids = append(pids, child)
	}
	return pids
}

// Cwd returns the working directory the persistent shell is currently in — the
// directory the next command run through it will start in. The TUI's
// interactive handoff reads it so a command that bypasses the persistent shell
// still runs where the user expects.
func (s *PersistentShell) Cwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

func (s *PersistentShell) Exec(ctx context.Context, command string, timeoutMs int) (string, string, int, bool, error) {
	if !s.isAlive {
		return "", "Shell is not alive", 1, false, errors.New("shell is not alive")
	}

	timeout := time.Duration(timeoutMs) * time.Millisecond

	resultChan := make(chan commandResult)
	s.commandQueue <- &commandExecution{
		command:    command,
		timeout:    timeout,
		resultChan: resultChan,
		ctx:        ctx,
	}

	result := <-resultChan
	return result.stdout, result.stderr, result.exitCode, result.interrupted, result.err
}

func (s *PersistentShell) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isAlive {
		return
	}

	s.stdin.Write([]byte("exit\n"))

	s.cmd.Process.Kill()
	s.isAlive = false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func readFileOrEmpty(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(content)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
