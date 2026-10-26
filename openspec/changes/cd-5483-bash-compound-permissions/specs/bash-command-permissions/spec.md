## ADDED Requirements

### Requirement: Every executable part is checked before execution
For a call requiring permission, the bash tool SHALL evaluate the existing permission rules on the complete
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

### Requirement: Uninspectable re-evaluation is refused
The gate SHALL deny assigning and indirect parameter expansions, prompt
expansions, variable-address tests and arithmetic/index/slice contexts that
depend on anything beyond decimal constants and operators over constants.
The gate SHALL NOT assume that variable contents used in those contexts are
non-executable data, including when the assignment and use occur in one statement.

#### Scenario: Escaped command text stored and re-evaluated
- **WHEN** an allowed statement stores escaped command text with an assigning
  expansion and then uses arithmetic, indirection, an array subscript or @P
- **THEN** the whole call is denied before execution in both dispatch modes

#### Scenario: Inspectable literal arithmetic
- **WHEN** an otherwise allowed statement uses constant arithmetic or literal
  numeric indices/slices without variable-based evaluation
- **THEN** those expansions do not introduce a denial

### Requirement: Foreground dialect mismatches do not auto-allow
For a non-exempt foreground call, an allow verdict SHALL become ask when the
resolved shell is neither bash nor sh. Existing deny/ask verdicts SHALL remain
intact. Background calls SHALL retain their verdict because they use bash -c.

#### Scenario: zsh-specific executable glob qualifier
- **WHEN** a foreground call resolves to zsh and Bash parsing would allow it
- **THEN** the whole call requires the existing permission prompt instead of
  treating Bash literal syntax as proof of non-execution in zsh
