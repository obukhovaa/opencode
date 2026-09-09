package shell

import (
	"path/filepath"
	"strings"
)

// interactivePrograms are the programs whose whole job involves owning a
// terminal — reading a password, driving a full-screen UI, or running a REPL.
// A command leading with one of these is handed the real terminal instead of
// being captured.
//
// The list only ever promotes a command to the interactive path. A program
// missing from it degrades to the captured path, which after terminal isolation
// fails fast with the program's own diagnostic rather than hanging — and
// NeedsTerminal turns that into a hint naming the force prefix.
var interactivePrograms = map[string]bool{
	// Credential prompts
	"sudo": true, "su": true, "doas": true, "passwd": true, "visudo": true,
	"ssh": true, "scp": true, "sftp": true, "ssh-add": true, "ssh-keygen": true,
	"gpg": true, "gpg2": true,
	// Editors
	"vi": true, "vim": true, "nvim": true, "nano": true, "emacs": true,
	"pico": true, "helix": true, "hx": true, "micro": true,
	// Pagers and full-screen viewers
	"less": true, "more": true, "man": true, "top": true, "htop": true,
	"btop": true, "watch": true,
	// REPLs and database clients
	"psql": true, "mysql": true, "sqlite3": true, "redis-cli": true,
	"mongosh": true, "irb": true, "python": true, "python3": true, "node": true,
	// Multiplexers, remote sessions, and interactive editors of system state
	"tmux": true, "screen": true, "ftp": true, "telnet": true, "crontab": true,
}

// ttyFlaggedPrograms take an explicit "allocate a tty" flag. `docker ps` is
// captured; `docker exec -it` is not. Matching the program name alone would
// send every docker invocation down the interactive path and silently drop the
// output the user expected in the chat.
var ttyFlaggedPrograms = map[string]bool{
	"docker": true, "podman": true, "kubectl": true, "oc": true, "nerdctl": true,
}

// ClassifyInteractive reports whether a command needs to own the terminal.
// extra comes from the user's `shell.interactive` config and is matched the
// same way as the built-in list.
//
// This is deliberately a first-token check. It does not parse pipelines, `&&`
// chains, or subshells: a command whose interactivity is buried inside a
// pipeline is one the user should force with the `!!` prefix. A parser that
// tried to be clever here would produce false positives, and a false positive
// costs the user the captured output they were expecting.
func ClassifyInteractive(command string, extra []string) bool {
	fields := strings.Fields(command)

	// Skip leading VAR=value assignments: `FOO=1 sudo -v` is still sudo.
	for len(fields) > 0 && isEnvAssignment(fields[0]) {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return false
	}

	program := filepath.Base(strings.TrimSuffix(fields[0], ".exe"))

	for _, e := range extra {
		if e = strings.TrimSpace(e); e != "" && strings.EqualFold(e, program) {
			return true
		}
	}
	if interactivePrograms[program] {
		return true
	}
	if ttyFlaggedPrograms[program] {
		return hasTTYFlag(fields[1:])
	}
	return false
}

// isEnvAssignment reports whether a token is a leading `NAME=value` prefix.
// A leading `=` is not an assignment, and neither is a path like `./a=b`.
func isEnvAssignment(token string) bool {
	eq := strings.Index(token, "=")
	if eq <= 0 {
		return false
	}
	for i, r := range token[:eq] {
		isLetter := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		if !isLetter && !(isDigit && i > 0) {
			return false
		}
	}
	return true
}

// hasTTYFlag looks for the tty-allocating flags in an argument list: -t, -i,
// their combined forms (-it, -ti), and the long spellings. Both -i and -t on
// their own count, since either alone still attaches a stream to the terminal.
func hasTTYFlag(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--tty", "--interactive", "--stdin":
			return true
		}
		if len(arg) < 2 || arg[0] != '-' || arg[1] == '-' {
			continue
		}
		// A short-flag cluster: -it, -ti, -i, -t, -itd, ...
		if strings.ContainsAny(arg[1:], "it") {
			return true
		}
	}
	return false
}

// ttyFailureSignatures are the diagnostics programs emit when they wanted a
// terminal and could not get one. After terminal isolation these are what a
// captured interactive command produces instead of a hang, so recognising them
// is how the TUI knows to point the user at the force prefix.
var ttyFailureSignatures = []string{
	"no tty present",
	"a terminal is required to read the password",
	"a password is required",
	"not a terminal",
	"inappropriate ioctl for device",
	"terminal prompts disabled",
	"could not read from stdin",
	"host key verification failed",
	"pseudo-terminal will not be allocated",
	"no askpass program specified",
	"cannot open /dev/tty",
	"device not configured",
	"input device is not a tty",
}

// NeedsTerminal reports whether captured output shows a command failing for
// want of a terminal. Callers pass the command's stderr (and may pass stdout
// too — some programs write the diagnostic there).
func NeedsTerminal(output string) bool {
	if output == "" {
		return false
	}
	lower := strings.ToLower(output)
	for _, sig := range ttyFailureSignatures {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}
