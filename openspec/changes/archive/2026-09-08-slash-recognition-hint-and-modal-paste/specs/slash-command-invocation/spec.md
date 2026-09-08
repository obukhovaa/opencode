## ADDED Requirements

### Requirement: The editor surfaces which invocations will be recognized

Whether a `/…` line is expanded depends on rules the user cannot see in their own
text — the token must resolve, start at column 0, and sit outside a code fence. The
editor SHALL therefore show, before submit, which invocations the current draft
actually resolves to.

For each recognized invocation, in the order it appears, the editor SHALL display a
chip naming it. Chips SHALL distinguish an invocation that will expand into the
message from an action command that will run instead. A `/…` line that does not
resolve SHALL produce no chip: the absence of a chip is how the user learns their
text will be sent verbatim.

The hint SHALL resolve against the same registry as submission, so it cannot promise
an expansion that submitting would not perform, and SHALL be recomputed whenever the
draft changes. It SHALL NOT substitute arguments or run ``!`cmd`` shell markup —
only submission does that.

The hint SHALL occupy the single affordance row above the input (see
`chat-editor-layout`) and SHALL NOT cause the editor to render wider than its
container: chips that do not fit are omitted and accounted for by a trailing count.

In shell mode the draft is a shell command, so no invocation SHALL be recognized.

#### Scenario: A resolvable skill is announced

- **WHEN** the draft is `/skill:reviewer the diff`
- **THEN** a chip naming `/skill:reviewer` is shown above the input

#### Scenario: An unresolvable name is not announced

- **WHEN** the draft is `/reviewer the diff`, where `reviewer` is a skill but the
  slash namespace requires `skill:reviewer`
- **THEN** no chip is shown, and the text will be sent verbatim on submit

#### Scenario: Text that will not expand is not announced

- **WHEN** the draft is `  /commit` (indented), a `/commit` line inside a code fence,
  or `\/commit`
- **THEN** no chip is shown for it

#### Scenario: An action command is distinguished from an expansion

- **WHEN** the draft is `/compact`
- **THEN** the chip for it is presented differently from an invocation that expands
  into the message, because it will run an action instead of being sent

#### Scenario: The hint tracks edits in both directions

- **GIVEN** a draft with one recognized invocation and a chip shown for it
- **WHEN** the invocation is edited so that it no longer resolves
- **THEN** the chip disappears

#### Scenario: More chips than fit are counted

- **GIVEN** a container too narrow for every chip
- **WHEN** the draft recognizes more invocations than fit
- **THEN** the chips that fit are shown followed by a count of those omitted, and the
  rendered editor does not exceed the container width

#### Scenario: Shell mode recognizes nothing

- **GIVEN** the editor is in shell mode
- **WHEN** the draft is `/commit`
- **THEN** no chip is shown, because the draft is a shell command

## MODIFIED Requirements

### Requirement: The argument dialog produces round-trippable staged text

When a prompt invocation declares argument placeholders, the argument dialog SHALL
still be presented as it is today. On submit the collected values SHALL be written
into the editor as the argument portion of the staged invocation, not substituted into
a message.

The written argument text SHALL reproduce the collected values when it is parsed again
at submit time: a value containing whitespace SHALL be quoted, and a value containing
a quote character SHALL be escaped so that parsing yields the original value.

Cancelling the dialog SHALL leave the editor unchanged and stage nothing.

#### Scenario: Multi-field values round-trip through the editor

- **WHEN** the user selects a command whose content references `$0` and `$1`, and
  enters `HEAD~3` and `src/internal tools` in the dialog
- **THEN** the editor contains `/<name> HEAD~3 "src/internal tools"`, and submitting
  it unchanged binds `$0` to `HEAD~3` and `$1` to `src/internal tools`

#### Scenario: Cancelling the dialog stages nothing

- **GIVEN** the editor contains the text `look at this:`
- **WHEN** the user selects a parameterized command and presses `esc` in the argument
  dialog
- **THEN** the editor still contains exactly `look at this:` and no message is sent

#### Scenario: Pasted text reaches the focused field

- **GIVEN** the argument dialog is open
- **WHEN** the user pastes text
- **THEN** the pasted text is inserted into the focused argument field, and does not
  appear in the chat editor behind the dialog
