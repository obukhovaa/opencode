package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
)

type PersistentShell struct {
	cmd *exec.Cmd
	// stdin is the pipe commands are written to. It is never a terminal — see
	// detachFromTerminal for why that is not sufficient on its own.
	stdin *os.File
	// alive is read and written from several goroutines: the cmd.Wait watcher,
	// the panic-recovery path, Close, and every Exec caller. It is checked
	// outside mu (Exec must not block behind a running command just to learn
	// the shell died), so it has to be atomic rather than mutex-guarded.
	alive atomic.Bool
	// quit is closed exactly once when the shell dies. The command queue itself
	// is NEVER closed: Exec checks isAlive and then sends, and closing a channel
	// out from under a live sender panics the sender's goroutine — which in
	// production is an unrecovered crash of the whole process. Signalling
	// through a separate channel makes the send unconditionally safe.
	quit      chan struct{}
	closeQuit sync.Once
	// exitStatus is the shell process's own exit code, recorded before the
	// shell is marked dead. When a command ends the shell session (`exit 3`
	// runs in the shell itself, because commands are eval'd there), this IS the
	// status the user asked for — there is no other place to read it from.
	exitStatus atomic.Int32

	// cwd has its own lock rather than sharing mu. mu is held for the entire
	// duration of a running command, so a Cwd() guarded by it would block for
	// up to the tool timeout — and Cwd() is read by the TUI's interactive
	// handoff, which must not stall the event loop behind an agent's bash call.
	cwd   string
	cwdMu sync.RWMutex

	mu           sync.Mutex
	commandQueue chan *commandExecution
}

// setCwd records the working directory the last command left behind.
func (s *PersistentShell) setCwd(dir string) {
	s.cwdMu.Lock()
	defer s.cwdMu.Unlock()
	s.cwd = dir
}

// isAlive reports whether the shell process is still usable.
func (s *PersistentShell) isAlive() bool { return s.alive.Load() }

// markDead records the shell as unusable and wakes everyone waiting on it. It
// is safe to call from any goroutine, any number of times.
func (s *PersistentShell) markDead() {
	s.alive.Store(false)
	s.closeQuit.Do(func() { close(s.quit) })
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

	if sh, ok := shellInstances[workingDir]; ok && sh != nil && sh.isAlive() {
		return sh
	}

	// A shell that died — most often because a command contained `exit` — is
	// replaced transparently. Start the replacement where the old one left off:
	// silently teleporting the user back to the project root after an `!exit`
	// is a surprise nothing on screen explains.
	startDir := workingDir
	if prev, ok := shellInstances[workingDir]; ok && prev != nil {
		if last := prev.Cwd(); last != "" {
			if st, err := os.Stat(last); err == nil && st.IsDir() {
				startDir = last
			}
		}
	}

	sh := newPersistentShell(startDir)
	if sh == nil {
		return nil
	}
	shellInstances[workingDir] = sh
	return sh
}

// CommandArgs returns the argv that runs a single command string in the
// configured shell, honouring shell.args.
//
// The captured path feeds commands to a long-lived shell over a pipe, so it
// only needs shell.args. The interactive handoff spawns a fresh shell per
// command and needs a "-c"-style flag as well — without this it hardcoded
// "-lc", which silently ignored a user's configured args and breaks outright on
// a shell that spells the flag differently.
func CommandArgs(command string) []string {
	var args []string
	if cfg := config.Get(); cfg != nil {
		args = append(args, cfg.Shell.Args...)
	}
	// Drop a configured "-s" (read from stdin): it is meaningful for the
	// persistent shell's pipe and contradictory here, where the command comes
	// from argv.
	args = slices.DeleteFunc(args, func(a string) bool { return a == "-s" })
	if !slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, "c") && strings.HasPrefix(a, "-") }) {
		args = append(args, "-c")
	}
	return append(args, command)
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
		cwd:          cwd,
		commandQueue: make(chan *commandExecution, 10),
		quit:         make(chan struct{}),
	}
	shell.alive.Store(true)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "Panic in shell command processor: %v\n", r)
				// Record a failure status before the shell is torn down. Left
				// at its zero value it would report a panicked shell as having
				// exited successfully.
				shell.exitStatus.Store(1)
				shell.markDead()
			}
		}()
		shell.processCommands()
	}()

	go func() {
		err := cmd.Wait()
		if err != nil {
			logging.Error(fmt.Sprintf("Can't complete shell command: %s", err.Error()))
		}
		// Record the status BEFORE marking the shell dead: execCommand notices
		// the death through the alive flag, and the atomic store ordering is
		// what lets it read a status that is already there.
		shell.exitStatus.Store(int32(exitStatusOf(cmd.ProcessState)))
		shell.markDead()
	}()

	return shell
}

func (s *PersistentShell) processCommands() {
	for {
		select {
		case <-s.quit:
			return
		case cmd := <-s.commandQueue:
			cmd.resultChan <- s.execCommand(cmd.command, cmd.timeout, cmd.ctx)
		}
	}
}

func (s *PersistentShell) execCommand(command string, timeout time.Duration, ctx context.Context) commandResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isAlive() {
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
	// sessionEnded is kept separate from interrupted. A command that ends the
	// shell session (`exit`, `exec`, a fatal shell error) is not an abort: the
	// command did exactly what it was asked to. Reporting it as an interruption
	// produced a bogus "timed out or was interrupted" for a command that ran to
	// completion in a few milliseconds.
	sessionEnded := false

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
				if fileHasContent(statusFile) {
					done <- true
					return
				}

				if !s.isAlive() {
					sessionEnded = true
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

	// An interrupted command whose wrapper never finished leaves the shell in
	// one of two states. Usually the command had a child, terminateDescendants
	// killed it, and the wrapper resumes and writes its status within
	// milliseconds — the shell is fine. But a shell BUILTIN (`while :; do :;
	// done`) has no child to signal, so nothing stopped it and the shell will
	// never accept another command: every later call returns 143 with no
	// output, for the life of the process, because isAlive stays true and
	// GetPersistentShell keeps handing back the wedged instance.
	//
	// Waiting briefly separates the two, and taking the shell down is the fix
	// for the second: GetPersistentShell respawns it in the preserved working
	// directory on the next call.
	if interrupted && !fileHasContent(statusFile) {
		deadline := time.Now().Add(wedgeGrace)
		for time.Now().Before(deadline) && !fileHasContent(statusFile) {
			time.Sleep(10 * time.Millisecond)
		}
		if !fileHasContent(statusFile) {
			s.forceRestart()
		}
	}

	stdout := readFileOrEmpty(stdoutFile)
	stderr := readFileOrEmpty(stderrFile)
	exitCodeStr := readFileOrEmpty(statusFile)
	newCwd := readFileOrEmpty(cwdFile)

	exitCode := 0
	switch {
	case exitCodeStr != "":
		fmt.Sscanf(exitCodeStr, "%d", &exitCode)
	case sessionEnded:
		// Commands are eval'd in the persistent shell itself — that is what
		// makes `cd` persist — so a command containing `exit` ends the shell.
		// The shell's own exit status is the status the command asked for; no
		// status file was ever written, because the shell never got that far.
		exitCode = int(s.exitStatus.Load())
		if stderr != "" && !strings.HasSuffix(stderr, "\n") {
			stderr += "\n"
		}
		stderr += "The command ended the shell session. A new shell starts on the next command, " +
			"in the same working directory; exported variables and shell functions are lost."
	case interrupted:
		exitCode = 143
		stderr += "\nCommand execution timed out or was interrupted"
	}

	if newCwd != "" {
		s.setCwd(strings.TrimSpace(newCwd))
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

// wedgeGrace is how long a terminated command's wrapper gets to write its
// status before the shell is assumed stuck. terminateDescendants has already
// signalled; a live shell resumes in microseconds.
const wedgeGrace = 500 * time.Millisecond

// fileHasContent reports whether path exists and is non-empty.
func fileHasContent(path string) bool {
	return fileExists(path) && fileSize(path) > 0
}

// forceRestart kills a shell that can no longer make progress. The next
// GetPersistentShell call replaces it, starting in the directory this one was
// last in.
func (s *PersistentShell) forceRestart() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.markDead()
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

// ShellPID returns the persistent shell process's pid, or 0 when it never
// started. It exists for the isolation e2e harness, which asserts the shell is
// in a different POSIX session than opencode.
func (s *PersistentShell) ShellPID() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Cwd returns the working directory the persistent shell is currently in — the
// directory the next command run through it will start in. The TUI's
// interactive handoff reads it so a command that bypasses the persistent shell
// still runs where the user expects.
func (s *PersistentShell) Cwd() string {
	s.cwdMu.RLock()
	defer s.cwdMu.RUnlock()
	return s.cwd
}

func (s *PersistentShell) Exec(ctx context.Context, command string, timeoutMs int) (string, string, int, bool, error) {
	if !s.isAlive() {
		return "", "Shell is not alive", 1, false, errors.New("shell is not alive")
	}

	timeout := time.Duration(timeoutMs) * time.Millisecond

	// Buffered so the processor is never blocked by a caller that has gone away.
	resultChan := make(chan commandResult, 1)
	execution := &commandExecution{
		command:    command,
		timeout:    timeout,
		resultChan: resultChan,
		ctx:        ctx,
	}

	select {
	case s.commandQueue <- execution:
	case <-s.quit:
		// The shell died between the isAlive check above and this send. Nothing
		// was queued, so no result is coming.
		return "", "Shell is not alive", 1, false, errors.New("shell is not alive")
	case <-ctx.Done():
		return "", "Command was cancelled before it started", 1, true, nil
	}

	select {
	case result := <-resultChan:
		return result.stdout, result.stderr, result.exitCode, result.interrupted, result.err
	case <-s.quit:
		// The shell died. A command that had already STARTED still reports for
		// itself — execCommand's watcher notices the death within a poll and
		// returns the honest exit status — so prefer that answer and only fall
		// back once it is clear none is coming. Without this grace a `!exit 3`
		// would race its own result and report "shell is not alive" instead of 3.
		select {
		case result := <-resultChan:
			return result.stdout, result.stderr, result.exitCode, result.interrupted, result.err
		case <-time.After(shellDeathGrace):
			return "", "Shell is not alive", 1, false, errors.New("shell is not alive")
		}
	}
}

// shellDeathGrace bounds how long Exec waits for an in-flight command to report
// after the shell has died. execCommand's watcher polls every 10ms, so this is
// several orders of magnitude more than it needs.
const shellDeathGrace = 2 * time.Second

func (s *PersistentShell) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isAlive() {
		return
	}

	s.stdin.Write([]byte("exit\n"))

	s.cmd.Process.Kill()
	// The cmd.Wait watcher also calls markDead when the process exits; both
	// paths are idempotent, and marking it here means a caller that returns
	// straight from Close never observes a shell that is dead but still
	// advertising itself as alive.
	s.markDead()
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
