## Purpose

Governs how the shared persistent shell that backs both the agent `bash` tool and the
TUI's `!` mode is spawned, insulated from opencode's own terminal, and torn down — so
that no command it runs can read from or write to the terminal opencode is rendering on,
and so that a cancelled command leaves nothing behind.

## ADDED Requirements

### Requirement: The persistent shell SHALL have no controlling terminal

On platforms with POSIX sessions, the persistent shell process SHALL be started in a new
session so that it and every descendant have no controlling terminal. Opening `/dev/tty`
from any descendant MUST fail rather than resolve to the terminal opencode renders on.

This is the load-bearing isolation guarantee: redirecting a command's standard streams is
not sufficient, because credential-prompting programs deliberately bypass redirected
stdin by opening `/dev/tty` directly. Isolation MUST therefore be a property of how the
shell is spawned, not of how each command is invoked.

On platforms without POSIX sessions the requirement is satisfied vacuously and the spawn
path MUST still succeed.

#### Scenario: A command tries to prompt on the terminal
- **GIVEN** opencode is rendering its TUI on a terminal
- **WHEN** a command run through the persistent shell attempts to open `/dev/tty`
- **THEN** the open fails, and no byte written by that command appears on opencode's
  terminal

#### Scenario: Password-prompting command under the agent bash tool
- **GIVEN** the agent invokes the `bash` tool with a command that requires a password
- **WHEN** the command runs
- **THEN** it terminates with its own diagnostic on stderr (for example, that no terminal
  is present) and a non-zero exit code, rather than blocking until the tool timeout

#### Scenario: Ordinary command is unaffected
- **GIVEN** the persistent shell has been started
- **WHEN** a command that neither reads nor writes a terminal is run
- **THEN** its stdout, stderr, exit code, and resulting working directory are exactly what
  they were before terminal isolation was introduced

### Requirement: The shell environment SHALL disable interactive prompting

The persistent shell's environment SHALL be configured so that tools which would
otherwise prompt for input fail immediately with a diagnostic instead of waiting. At
minimum, git's terminal prompting SHALL be disabled and git's editor SHALL remain
non-interactive.

The system MUST NOT configure any askpass helper that could cause a credential prompt to
be answered silently or incorrectly; the required outcome is a fast, explicit failure.

#### Scenario: Git operation that would prompt for credentials
- **WHEN** a command run through the persistent shell performs a git operation against a
  remote requiring credentials that are not already available
- **THEN** git fails immediately with a diagnostic rather than waiting for input

#### Scenario: Caller-supplied environment is preserved
- **GIVEN** the process environment contains a variable the prompting policy does not name
- **WHEN** the persistent shell is started
- **THEN** that variable is visible to commands run through the shell, unchanged

### Requirement: Terminating a command SHALL terminate its descendants

When a command is cancelled or exceeds its timeout, the system SHALL signal the command's
entire descendant process tree, not only its direct children. Termination SHALL first
request graceful exit and then, after a bounded grace period, force termination of any
descendant still running.

The termination path MUST NOT signal opencode itself or any process outside the
persistent shell's descendant tree.

#### Scenario: A cancelled command has grandchildren
- **GIVEN** a command that spawns a child which spawns its own long-running child
- **WHEN** the command is cancelled
- **THEN** every process in that tree has been signalled, and none is left running after
  the grace period

#### Scenario: Graceful exit is honoured
- **GIVEN** a command that exits promptly on a graceful termination request
- **WHEN** it is cancelled
- **THEN** it is not force-terminated

#### Scenario: Termination is scoped to the shell's descendants
- **WHEN** a command is cancelled
- **THEN** processes that are not descendants of the persistent shell — including
  opencode itself — receive no signal

### Requirement: The system SHALL expose the shell's current working directory

The persistent shell tracks the working directory that survives across commands. That
directory SHALL be readable by callers, so a command executed outside the persistent
shell can be started in the same directory the next `!` or `bash` command would run in.

#### Scenario: Directory reflects the last command's effect
- **GIVEN** a command that changes the working directory has completed
- **WHEN** the current working directory is read
- **THEN** it is the directory that command left behind
