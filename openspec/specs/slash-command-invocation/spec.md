# slash-command-invocation Specification

## Purpose
Governs how `/`-prefixed input is classified, staged for editing, argument-filled,
and expanded into the message that reaches the model — in the interactive TUI and in
non-interactive prompts alike. It exists so a user can compose one turn out of
several skills plus their own instructions, review it before sending, and never have
a raw `/command` string delivered to the model.
## Requirements
### Requirement: Slash invocations are classified into action and prompt kinds

Every `/`-prefixed invocation SHALL resolve to exactly one of three outcomes:

- **Action command** — a command that performs an application action and carries no
  prompt content (`/new`, `/reset`, `/compact`, `/agents`, `/auto-approve`, `/vim`,
  `/sessions-cleanup`, `/rename`, `/loop`, `/crons`). It is never staged and never
  composed into a message.
- **Prompt invocation** — a command that contributes text to the user's message: a
  builtin command with prompt content (`/init`, `/review`, `/commit`), a `user:` or
  `project:` custom command, or `/skill:<name>` naming a user-invocable skill.
- **Unresolved** — the token matches no known command or skill.

Classification SHALL be derived from the command's own declaration (whether it has
prompt content) rather than from a hand-maintained list at each call site, so a
newly added command is classified consistently by every caller.

#### Scenario: Prompt command is classified as a prompt invocation

- **WHEN** the user selects `/review` from the command completion popup
- **THEN** it is classified as a prompt invocation and staged for editing

#### Scenario: Action command is classified as an action

- **WHEN** the user selects `/compact` from the command completion popup
- **THEN** it is classified as an action command and its handler runs immediately,
  with no text written into the editor

#### Scenario: Unknown token is left alone

- **WHEN** the user submits a message whose first line is `/notacommand do a thing`
- **THEN** no expansion occurs and the message is sent with that text unchanged

### Requirement: Resolving a prompt invocation stages editable text instead of sending

Selecting a prompt invocation in the TUI — from the command completion popup or from
the command dialog — SHALL write the invocation back into the editor as ordinary
editable text of the form `/<name> <args>` and SHALL NOT create or send a message.
The `<name>` written back SHALL be a form that resolves to the same command or skill
when submitted (`skill:<skill-name>` for skills).

Staged text SHALL be inserted so that the invocation occupies its own line: when the
cursor is not already at the start of a line, a line break is inserted before it.

The editor's existing content SHALL be preserved; staging is additive, so several
invocations and free-form prose can accumulate in one message.

#### Scenario: Selecting a skill stages it in the editor

- **WHEN** the user types `/flow-c`, selects `skill:flow-creator` from the popup, and
  the skill declares no argument placeholders
- **THEN** the editor contains `/skill:flow-creator ` with the cursor after it, no
  message is created, and the agent is not invoked

#### Scenario: A second invocation is appended to the same message

- **GIVEN** the editor contains `/skill:flow-creator build a review flow` followed by
  the user's own line `make it run on MR events only`
- **WHEN** the user selects `skill:review` from the popup
- **THEN** the editor keeps its existing text and gains `/skill:review` on a new line

#### Scenario: Staging is possible while the agent is busy

- **GIVEN** the agent is running for the current session
- **WHEN** the user selects a prompt invocation from the popup
- **THEN** the invocation is staged in the editor and no "agent is busy" warning is
  reported, because nothing is being sent

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

### Requirement: Submitting expands every prompt invocation in the message

On submit, the message text SHALL be scanned for prompt invocations and each one
SHALL be replaced, in place, by its expanded content. Text that is not an invocation
SHALL be preserved verbatim, in its original position relative to the expanded blocks.

A line is a prompt invocation if and only if all of the following hold:

- the line's first character is `/` at column 0 (no leading whitespace);
- the line is not inside a fenced code block (a region delimited by ```` ``` ````);
- the first whitespace-delimited token after `/` resolves to a prompt invocation.

The remainder of that line, trimmed, is the invocation's arguments.

A line beginning with `\/` SHALL be emitted as `/…` with the backslash removed and
SHALL NOT be expanded, giving the user an escape for literal text.

Expansion SHALL apply the invocation's argument bindings, then expand ``!`cmd` ``
shell markup within the expanded content only — never within the user's own prose.
Skill expansions SHALL be wrapped in a `<skill_content name="…">` block so the model
recognises the skill as already loaded.

At most 10 invocations SHALL be expanded in a single submit; a message containing more
SHALL be rejected with a warning stating the count, and the editor content SHALL be
preserved.

#### Scenario: Two skills and prose compose one message

- **WHEN** the user submits

  ```
  /skill:flow-creator build a review flow
  make it run on MR events only
  /skill:review
  ```

- **THEN** the message sent to the model is the expanded `flow-creator` skill block,
  then the line `make it run on MR events only`, then the expanded `review` skill
  block, in that order

#### Scenario: Slash lines inside a code fence are literal

- **WHEN** the user submits a message containing a fenced code block whose lines begin
  with `/review` and `/commit`
- **THEN** those lines are sent unchanged and no expansion occurs for them

#### Scenario: Backslash escapes an invocation

- **WHEN** the user submits a line `\/review this by hand`
- **THEN** the message contains the line `/review this by hand` and the `review`
  command is not expanded

#### Scenario: Prose that mentions a command mid-line is untouched

- **WHEN** the user submits `remember that /commit runs the git flow`
- **THEN** the message is sent verbatim with no expansion

### Requirement: Action commands are exclusive within a submitted message

A submitted message whose only content is a single action-command invocation SHALL
run that command's action and SHALL NOT create a message.

A submitted message that contains an action-command invocation together with any other
non-empty content — prose, another invocation, or a second action command — SHALL be
rejected with a warning naming the offending command. Nothing is sent, no action runs,
and the editor content is preserved so the user can correct it.

Text on the action command's own line counts as other content unless the command
declares an argument hint. That hint is what distinguishes an action command that takes
arguments (`/rename`, `/loop`) from one that does not (`/new`, `/compact`): trailing
text after an argument-less action command SHALL be treated as a rejected composition
rather than silently discarded.

#### Scenario: A bare action command still runs

- **WHEN** the user types `/compact` and submits
- **THEN** the session compaction action runs and no user message is created

#### Scenario: Mixing an action command with prose is rejected

- **WHEN** the user submits `/new and then /skill:review the diff`
- **THEN** a warning names `/new` as not composable, no session is cleared, no message
  is sent, and the text remains in the editor

#### Scenario: An argument-taking action command keeps its arguments

- **WHEN** the user submits `/rename a better title`
- **THEN** the rename action runs, because `/rename` declares an argument hint, and no
  message is sent

### Requirement: Expansion happens before the message enters the queue

Expansion SHALL be complete before the submitted message is either dispatched to the
agent or appended to the session's message queue. A queued message SHALL therefore
carry the fully expanded prompt, and delivery of a queued message SHALL NOT perform
any further slash resolution.

Argument bindings, `${SESSION_ID}` / `${SKILL_DIR}` substitution, and ``!`cmd` ``
shell markup SHALL all be resolved at submit time, using the session that was active
when the user submitted.

#### Scenario: A slash invocation typed while busy is expanded, not sent literally

- **GIVEN** the agent is running for the current session
- **WHEN** the user submits `/skill:review the diff`
- **THEN** the queued message contains the expanded `<skill_content name="review">`
  block, and when the queue drains, the model receives that expanded text rather than
  the string `/skill:review the diff`

### Requirement: A failed expansion preserves the user's message

When expansion cannot complete — a named skill exists but is not user-invocable, a
command is only available in interactive mode, the invocation cap is exceeded, or an
action command was mixed with other content — the TUI SHALL report a warning and
SHALL leave the submitted text in the editor. No partially expanded message is sent
and no queue entry is created.

#### Scenario: Non-user-invocable skill does not consume the message

- **WHEN** the user submits `/skill:internal-helper go` for a skill whose frontmatter
  does not set `user-invocable: true`
- **THEN** a warning explains that the skill is not user-invocable, the text remains in
  the editor, and no message is sent

### Requirement: Argument binding is quote-aware and positionally named

Argument parsing SHALL split the argument string on whitespace while honouring single
and double quotes, so a quoted value is one positional argument.

Bindings SHALL be applied as follows:

- `$ARGUMENTS` — the full, unsplit argument string.
- `$ARGUMENTS[N]` and `$N` — the Nth positional argument (0-based); out-of-range
  indices bind to the empty string.
- A named placeholder `$FOO` in a custom command's content binds to the positional
  argument at the placeholder's index in first-appearance order within that content —
  the same order the argument dialog presents its fields.
- When the content declares no placeholder at all and the argument string is
  non-empty, `ARGUMENTS: <args>` SHALL be appended to the expanded content, so a
  user's instruction is never silently dropped.

#### Scenario: A quoted argument stays whole

- **WHEN** a command whose content references `$0` and `$1` is invoked as
  `/<name> "two words" third`
- **THEN** `$0` binds to `two words` and `$1` binds to `third`

#### Scenario: Named placeholders bind by first appearance

- **WHEN** a custom command whose content mentions `$TARGET` before `$SCOPE` is invoked
  as `/<name> HEAD~3 src/`
- **THEN** `$TARGET` binds to `HEAD~3` and `$SCOPE` binds to `src/`

#### Scenario: Instructions survive a placeholder-free skill

- **WHEN** the user submits `/skill:writing-voice apply this to the README only` for a
  skill whose content contains no placeholder
- **THEN** the expanded block ends with `ARGUMENTS: apply this to the README only`

### Requirement: Expanded skill blocks render collapsed in the transcript

In the chat transcript, a user message region delimited by
`<skill_content name="…">` … `</skill_content>` SHALL render as a single summary line
identifying the skill and the size of the elided content. The user's own prose in the
same message SHALL render normally, in its original position.

This is presentation only: the stored message and the payload sent to the model SHALL
retain the complete expanded text.

#### Scenario: A two-skill message renders as two summary lines plus prose

- **WHEN** a submitted message expanded to two `<skill_content>` blocks with a line of
  prose between them
- **THEN** the transcript shows one summary line per block and the prose line between
  them, and the message stored for the session still contains both full blocks

### Requirement: Non-interactive prompts expand the same way

A prompt supplied outside the TUI (`opencode -p`, flow `--prompt`) SHALL be expanded
by the same rules: every prompt invocation in the text is expanded in place, and
unresolved text passes through unchanged. Commands marked interactive-only SHALL
continue to be rejected with an explanatory error in this context.

Chat-bridge commands handled by the Slack / Telegram / Mattermost adapters are a
separate namespace and SHALL NOT be affected by these rules.

#### Scenario: A non-interactive prompt carries two invocations

- **WHEN** `opencode -p` receives a prompt whose first line is `/skill:review HEAD~1`
  and whose third line is `/commit`
- **THEN** both are expanded in place before the prompt is sent to the agent

#### Scenario: Interactive-only command in a non-interactive prompt

- **WHEN** a non-interactive prompt contains `/compact`
- **THEN** the run fails with an error naming the command as interactive-only

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
