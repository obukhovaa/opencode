## ADDED Requirements

### Requirement: Every executable part is checked before execution
The bash tool SHALL evaluate the existing permission rules on the complete
invocation and every executable statement in its parsed Bash syntax before
starting any foreground command or background task. The syntax traversal SHALL
include commands separated by semicolons, newlines, background operators,
logical operators and pipelines, and commands inside structural containers,
command substitutions and process substitutions. Literal syntax inside quoted
data or comments SHALL NOT introduce executable statements.

#### Scenario: A prefix allow rule cannot grant a chained command
- **WHEN** a prefix glob allows the first command but a later part is denied
- **THEN** the complete bash call is denied and even its first command does not run

#### Scenario: Nested execution is denied
- **WHEN** a denied command occurs inside a subshell, a command substitution,
  backticks, a process substitution, a function body or an unreachable branch
- **THEN** the complete call is denied before execution

#### Scenario: Literal operators remain data
- **WHEN** an allowed command has quoted literal separator or substitution text
- **THEN** those literals introduce no additional permission checks

#### Scenario: Both execution modes share the gate
- **WHEN** the same disallowed compound call is synchronous or requests background execution
- **THEN** neither starts a shell command or creates a running background task

### Requirement: The strictest verdict wins
The tool SHALL retain the whole invocation's existing permission verdict and
combine statement verdicts with deny taking precedence over ask and ask over
allow. A rule denial SHALL keep the existing rule-denial result behavior. An
ask SHALL use the existing single prompt for the original complete invocation.
The simple-command safe list SHALL NOT exempt individual parts of a compound
call.

#### Scenario: Every check allows
- **WHEN** both the complete invocation and every executable statement allow
- **THEN** the invocation can execute without a permission prompt

#### Scenario: Ask without deny
- **WHEN** no check denies and at least one check asks
- **THEN** the tool requests permission once for the complete command

#### Scenario: Original deny remains authoritative
- **WHEN** the complete invocation denies but individual statements allow
- **THEN** the complete call still denies

### Requirement: Unvalidated syntax is refused
The tool SHALL refuse malformed or unsupported Bash syntax rather than use an
allow glob as a fallback. Parsing SHALL execute no commands or expansions.

#### Scenario: Prefix allow on malformed syntax
- **WHEN** a prefix allow glob matches an invocation whose syntax cannot be parsed
- **THEN** the tool returns a rule denial without executing any command

### Requirement: Hidden execution is not granted by a prefix rule
When the invocation contains an expansion through which bash runs code that
appears in no statement — arithmetic or a subscript, slice or indirection over
a variable, a C-style loop header, `${x@P}`, or an assigning expansion
`${x=…}` / `${x:=…}` — an allow verdict SHALL be replaced by the stricter of
itself and the agent's catch-all verdict (the verdict for a command no
specific rule matches). Arithmetic over numeric literals and expansions that
run nothing SHALL keep their verdict.

#### Scenario: Stored command run by arithmetic or prompt expansion
- **WHEN** `"*": deny` and `"printf allowed*": allow`, and the call is
  `printf allowed ${x:=\$\(touch MK\)} ${x@P}` or uses `$((x))`, `${!x}` or `${y[x]}`
- **THEN** the call is denied before execution

#### Scenario: Allow-all agents are unaffected
- **WHEN** `"*": allow` and the call contains hidden execution
- **THEN** the call is allowed as before

#### Scenario: No catch-all
- **WHEN** no catch-all rule is configured and a prefix rule allows a call with hidden execution
- **THEN** the tool asks

### Requirement: Safe-listed commands run no other command
A command SHALL skip the gate as safe-listed only when it is one simple command
with no redirect, substitution or hidden-execution expansion. No safe-list
entry SHALL start another command: wrappers (`nice`, `time`, `timeout`,
`go run`, `go test`) SHALL NOT be listed, `env` SHALL qualify only without
arguments, and options that run a program (`-toolexec`, `-exec`, `-vettool`,
`go env -w/-u`, `git grep -O` / `--open-files-in-pager`,
`git ls-remote -u` / `--upload-pack`) SHALL send the call through the gate.

#### Scenario: Wrapper under deny-all
- **WHEN** an agent has `"*": deny` and runs `env bash -c '…'`
- **THEN** the call is denied and the wrapped command does not run
