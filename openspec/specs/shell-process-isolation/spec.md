# shell-process-isolation Specification

## Purpose
Governs how the shared persistent shell that backs both the agent `bash` tool and the
TUI's `!` mode is spawned, insulated from opencode's own terminal, and torn down — so
that no command it runs can read from or write to the terminal opencode is rendering on,
and so that a cancelled command leaves nothing behind.

## Requirements

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

### Requirement: A command that ends the shell session SHALL be reported accurately

Commands run through the persistent shell are evaluated in the shell process
itself — that is what makes a working-directory change persist across commands.
The same property means a command containing `exit` (or `exec`, or a fatal shell
error) ends the shell session.

When that happens the system SHALL report the shell's own exit status as the
command's exit status, and SHALL state that the session ended and what was lost.
It MUST NOT report the command as interrupted or timed out: the command ran to
completion and did exactly what it was asked to. Output the command produced
before ending the session SHALL still be returned.

#### Scenario: A command exits the shell with a status
- **WHEN** a command that writes output and then exits with status 3 is run
- **THEN** the reported exit code is 3, the command's output is returned, the
  result is not marked interrupted, and the message explains that the session
  ended rather than claiming a timeout

#### Scenario: A command exits the shell successfully
- **WHEN** a command ends the session with status 0
- **THEN** the reported exit code is 0 and the explanation is still present, so a
  successful-looking result does not hide the fact that the session ended

### Requirement: A replacement shell SHALL start where the previous one left off

When a dead shell is replaced, the replacement SHALL start in the working
directory the previous one was in, provided that directory still exists. A
session ending is already invisible to the user; silently returning them to the
project root as well is a second surprise that nothing on screen explains.

Session-scoped state that cannot be recovered — exported variables, shell
functions — is lost, and the message reporting the session end SHALL say so.

#### Scenario: Working directory survives a replacement
- **GIVEN** a command changed the working directory to a subdirectory
- **AND** a later command ended the shell session
- **WHEN** the next command runs in a replacement shell
- **THEN** it runs in that subdirectory

#### Scenario: A vanished directory falls back
- **GIVEN** the previous shell's working directory has since been deleted
- **WHEN** a replacement shell is created
- **THEN** it starts in the configured working directory rather than failing

### Requirement: The system SHALL expose the shell's current working directory

The persistent shell tracks the working directory that survives across commands. That
directory SHALL be readable by callers, so a command executed outside the persistent
shell can be started in the same directory the next `!` or `bash` command would run in.

Reading it MUST NOT block on a command that is currently running. The interactive
handoff reads it from the TUI's event loop, so a read that waited for an
in-flight agent command would freeze the interface for as long as that command
takes.

#### Scenario: Directory reflects the last command's effect
- **GIVEN** a command that changes the working directory has completed
- **WHEN** the current working directory is read
- **THEN** it is the directory that command left behind

#### Scenario: Reading the directory during a long command
- **GIVEN** a command has been running for several seconds
- **WHEN** the current working directory is read
- **THEN** the read returns promptly rather than waiting for the command
