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
The simple-command safe-list exemption SHALL NOT exempt individual parts of
a compound call.

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
