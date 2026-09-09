## Purpose

Defines the chat editor's shell (`!`) mode: how a draft becomes a shell command whether it
was typed, pasted, or submitted; how commands whose output is captured into the chat are
distinguished from commands that need a real terminal; and what the user can do while a
command is running.

## ADDED Requirements

### Requirement: A single predicate SHALL decide whether a draft is a shell invocation

The system SHALL recognise a shell invocation by one rule, applied identically wherever
draft text can arrive. The rule is: the text begins with `!` at its first character, with
no preceding whitespace, and the `!` is followed by at least one non-whitespace character
that is not part of a syntax the rule excludes.

The rule SHALL exclude, at minimum, markdown image syntax (`![`) and the inequality
operator (`!=`), so that ordinary prose and pasted markdown are not diverted into the
shell. Recognition SHALL yield the command text with the leading `!` removed.

Any entry path that does not use this predicate is a defect: the point of the requirement
is that typed, pasted, and submitted text cannot disagree about what is a command.

#### Scenario: Recognised invocation
- **WHEN** the draft text is `!ls -la`
- **THEN** it is recognised as a shell invocation with command `ls -la`

#### Scenario: Markdown image is not an invocation
- **WHEN** the draft text is `![diagram](./a.png) what does this show?`
- **THEN** it is not recognised as a shell invocation and is treated as message text

#### Scenario: Leading whitespace is not an invocation
- **WHEN** the draft text is ` !ls`
- **THEN** it is not recognised as a shell invocation

#### Scenario: A bare bang is not an invocation
- **WHEN** the draft text is `!` or `!   `
- **THEN** it is not recognised as a shell invocation

### Requirement: Pasting a shell invocation SHALL enter shell mode

When bracketed-paste content arrives while the editor is in its normal mode and the draft
is empty, and the pasted text is recognised as a shell invocation, the editor SHALL enter
shell mode with the recognised command as the draft. The result MUST be indistinguishable
from having typed the same characters.

Paste content that is not a recognised shell invocation SHALL be inserted into the draft
as text, unchanged.

#### Scenario: Pasting a command
- **GIVEN** the editor is in normal mode with an empty draft
- **WHEN** the text `!git status` is pasted
- **THEN** the editor is in shell mode and the draft is `git status`

#### Scenario: Pasting a command into a non-empty draft
- **GIVEN** the draft already contains `please run `
- **WHEN** the text `!git status` is pasted
- **THEN** the editor stays in its normal mode and the pasted text is inserted as written

#### Scenario: Pasting prose
- **GIVEN** the editor is in normal mode with an empty draft
- **WHEN** a multi-line block of prose is pasted
- **THEN** the editor stays in its normal mode and the draft contains the pasted prose

### Requirement: Submitting a shell invocation SHALL run it as a command

When the user submits the draft from the editor's normal mode and the draft is recognised
as a shell invocation, the system SHALL execute it as a shell command instead of sending
it as a message. This is the backstop for text that reached the draft by a route that did
not switch modes.

The check SHALL run before any slash-command expansion, so a `!` draft is never expanded
or queued as a message.

#### Scenario: Enter on a bang-prefixed draft
- **GIVEN** the editor is in its normal mode with the draft `!make test`
- **WHEN** the user submits
- **THEN** `make test` is executed as a shell command and no chat message is sent

#### Scenario: Ordinary message is unaffected
- **GIVEN** the draft is `explain the shell mode code`
- **WHEN** the user submits
- **THEN** it is sent as a message exactly as before

### Requirement: Commands needing a terminal SHALL be handed the terminal

The system SHALL classify each shell-mode command as either **captured** or
**interactive** before running it.

A captured command runs with its output collected and written to the chat, as today.

An interactive command SHALL instead be run with opencode's terminal released to it, so
the command owns the terminal for its lifetime and can prompt, read a password, or run a
full-screen program. When it exits, the TUI SHALL be restored and redrawn.

Classification SHALL be deterministic and inspectable: it is derived from the command's
leading program name matched against a built-in list of known-interactive programs, which
users SHALL be able to extend through configuration. Classification MUST only ever
promote a command to interactive; a command classified as captured MUST NOT be silently
re-run interactively, because re-running a command that already had effects is not safe.

#### Scenario: Password prompt is answerable
- **WHEN** the user runs `!sudo -v` from shell mode
- **THEN** the terminal is released to `sudo`, the user sees and answers the real password
  prompt, and the TUI is restored after `sudo` exits

#### Scenario: Ordinary command is still captured
- **WHEN** the user runs `!ls -la` from shell mode
- **THEN** the command is captured and its output is written to the chat, unchanged from
  previous behavior

#### Scenario: User-configured interactive program
- **GIVEN** the configuration lists an additional program as interactive
- **WHEN** the user runs that program from shell mode
- **THEN** it is handed the terminal

#### Scenario: A captured command is never retried interactively
- **GIVEN** a captured command fails because it needed a terminal
- **THEN** the system reports the failure and does not re-run the command

### Requirement: The user SHALL be able to force an interactive run

A shell-mode command whose text begins with `!` SHALL be run interactively regardless of
classification, with that leading `!` removed before execution. Entered from the editor's
normal mode this reads as `!!command`.

#### Scenario: Forcing an unclassified program
- **GIVEN** the editor is in shell mode
- **WHEN** the user runs `!my-tool --login`
- **THEN** `my-tool --login` is handed the terminal

#### Scenario: Force prefix is stripped
- **WHEN** a forced command is recorded in the chat
- **THEN** the recorded command text does not include the force prefix

### Requirement: A captured command that needed a terminal SHALL say so

When a captured command fails with output matching a known "no terminal available"
signature, the message written to the chat SHALL state that the command needs a terminal
and name the force prefix that reruns it interactively.

#### Scenario: Unclassified interactive program run as captured
- **WHEN** a captured command fails with a diagnostic that no terminal is present
- **THEN** the chat output includes the command's own diagnostic and a hint naming the
  force prefix

#### Scenario: Unrelated failure gets no hint
- **WHEN** a captured command fails for a reason unrelated to the terminal
- **THEN** no terminal hint is added to its output

### Requirement: Working directory SHALL survive an interactive run

An interactive command SHALL start in the persistent shell's current working directory,
and the working directory it leaves behind SHALL become the persistent shell's working
directory. `cd` behaves the same whether a command was captured or interactive.

#### Scenario: Directory change through the interactive path
- **GIVEN** the persistent shell's working directory is `/a`
- **WHEN** the user forces an interactive run of `cd /b`
- **THEN** a subsequent captured command runs in `/b`

#### Scenario: Interactive command starts in the current directory
- **GIVEN** a previous command changed the working directory to `/b`
- **WHEN** an interactive command is run
- **THEN** it starts in `/b`

### Requirement: The chat SHALL record every shell-mode command

Both captured and interactive runs SHALL append a record to the chat: the command as
issued and its result. For a captured run the result is its output and, when non-zero, its
exit code — unchanged from previous behavior. For an interactive run the record SHALL
state the exit code and that the command's output went to the terminal rather than being
captured.

#### Scenario: Interactive run record
- **WHEN** an interactive command exits with status 0
- **THEN** the chat shows the command and a result noting a successful interactive run
  whose output was written to the terminal

#### Scenario: Captured run record
- **WHEN** a captured command exits with status 2 and writes to stderr
- **THEN** the chat shows the command, its output, and its exit code

### Requirement: A running command SHALL be cancellable

While a captured shell command is running, `esc` and `ctrl+c` SHALL cancel it. Cancelling
SHALL terminate the command's descendant tree, return the editor to shell mode with an
empty draft, and record the cancellation in the chat.

The editor MUST NOT be left unresponsive for the duration of a long-running command: any
key that is not a cancel key MAY be ignored while a command runs, but the cancel keys MUST
be honoured.

#### Scenario: Cancelling a long command
- **GIVEN** a captured command has been running for several seconds
- **WHEN** the user presses `ctrl+c`
- **THEN** the command is terminated, the editor returns to shell mode, and the chat
  records that the command was cancelled

#### Scenario: Cancel keys are not swallowed
- **GIVEN** a captured command is running
- **WHEN** the user presses `esc`
- **THEN** the command is cancelled rather than the key being discarded

#### Scenario: Quit dialog is not raised by the cancel
- **GIVEN** a captured command is running
- **WHEN** the user presses `ctrl+c`
- **THEN** the command is cancelled and no quit dialog is shown
