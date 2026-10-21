# Spec Delta

## Purpose

Governs which environment variables opencode passes to the processes it starts, so that the
engine's own credentials, and any others an operator names, stay readable by opencode but
never reach a command the model runs.

## ADDED Requirements

### Requirement: Child processes SHALL NOT inherit denied environment variables

Every process opencode starts SHALL receive an inherited environment from which every
variable whose name matches the tool environment deny list has been removed. opencode's own
environment SHALL be unchanged, so opencode itself still reads those variables.

#### Scenario: The bash tool cannot see the API password
- **GIVEN** opencode runs with `OPENCODE_SERVER_PASSWORD=s3cret` in its environment
- **WHEN** the agent runs `env` through the `bash` tool
- **THEN** the output contains no `OPENCODE_SERVER_PASSWORD` line and no `s3cret`

#### Scenario: Background bash and monitor are filtered too
- **GIVEN** `OPENCODE_MYSQL_DSN` is set in opencode's environment
- **WHEN** the agent runs `env` with `bash` `run_in_background: true`, or through the `monitor` tool
- **THEN** the captured output contains no `OPENCODE_MYSQL_DSN` line

#### Scenario: opencode still uses the value
- **GIVEN** `OPENCODE_SERVER_PASSWORD` is set and denied
- **WHEN** a client calls the HTTP API with that password
- **THEN** the request is authenticated as before

#### Scenario: Other variables pass through
- **GIVEN** `PATH`, `HOME` and `GIT_AUTHOR_NAME` are set and not denied
- **WHEN** any tool process starts
- **THEN** it sees them with their values unchanged

### Requirement: The deny list SHALL always contain the engine's own secrets

The deny list SHALL always contain `OPENCODE_SERVER_PASSWORD`,
`OPENCODE_BRIDGE_REGISTRAR_PASSWORD` and `OPENCODE_MYSQL_DSN`. No configuration SHALL remove
them.

#### Scenario: No configuration
- **GIVEN** neither `toolEnv.deny` nor `OPENCODE_TOOL_ENV_DENY` is set
- **WHEN** a tool process starts while all three variables are set
- **THEN** it sees none of the three

### Requirement: Operators SHALL be able to extend the deny list

The deny list SHALL be the union of the built-in names, the `toolEnv.deny` list in
`.opencode.json` and the comma-separated `OPENCODE_TOOL_ENV_DENY` environment variable.
Entries SHALL be trimmed; empty entries SHALL be ignored. Matching SHALL be case-sensitive.
The `toolEnv` object SHALL appear in the generated configuration JSON schema.

#### Scenario: Config entry
- **GIVEN** `.opencode.json` sets `"toolEnv": {"deny": ["MYSQL_DSN"]}` and `MYSQL_DSN` is set
- **WHEN** the agent runs `env` through the `bash` tool
- **THEN** the output contains no `MYSQL_DSN` line

#### Scenario: Environment entry
- **GIVEN** `OPENCODE_TOOL_ENV_DENY=" MYSQL_DSN, ,ORC_JOB_TOKEN"` and both named variables are set
- **WHEN** a tool process starts
- **THEN** it sees neither `MYSQL_DSN` nor `ORC_JOB_TOKEN`

#### Scenario: Case-sensitive names
- **GIVEN** the deny list contains `MYSQL_DSN` and the environment has `mysql_dsn`
- **WHEN** a tool process starts
- **THEN** it sees `mysql_dsn`

### Requirement: Deny entries SHALL support glob patterns

A deny entry containing `*`, `?` or `[` SHALL be matched as a shell-style glob against the
whole variable name. An entry that is not a valid glob SHALL match only the identical name.

#### Scenario: Prefix glob
- **GIVEN** the deny list contains `AWS_SECRET_*`
- **WHEN** `AWS_SECRET_ACCESS_KEY` and `AWS_REGION` are set and a tool process starts
- **THEN** it sees `AWS_REGION` but not `AWS_SECRET_ACCESS_KEY`

#### Scenario: Malformed glob
- **GIVEN** the deny list contains `BAD[`
- **WHEN** a tool process starts with `BAD[` and `BADX` set
- **THEN** `BAD[` is removed and `BADX` is passed

### Requirement: Explicit per-child values SHALL still be passed

A value an operator writes into one child's own configuration (an MCP server's `env`, an LSP
server's `env`, a literal value in a CLI tool's `env.set`) SHALL be passed to that child even
when its name is denied. Such values SHALL NOT be resolved from the denied part of opencode's
environment.

#### Scenario: MCP server granted the DSN explicitly
- **GIVEN** an MCP server configured with `"env": ["MYSQL_DSN=user:pw@tcp(db)/x"]` and `MYSQL_DSN` denied
- **WHEN** opencode starts that server
- **THEN** the server sees `MYSQL_DSN=user:pw@tcp(db)/x`

#### Scenario: CLI tool cannot expand a denied name
- **GIVEN** a CLI tool manifest with `env.set: {DSN: "${env.MYSQL_DSN}"}` and `MYSQL_DSN` denied
- **WHEN** the tool runs
- **THEN** the tool sees `DSN` with an empty value

### Requirement: opencode SHALL NOT expose denied values through its own process

On Linux, when at least one variable in opencode's own environment matched the deny list at
startup, opencode SHALL mark its process non-dumpable, so a process running as the same user
cannot read opencode's initial environment from `/proc/<pid>/environ`. When nothing matched,
the process SHALL be left as it is. On other platforms the requirement is satisfied
vacuously.

#### Scenario: Reading the parent's environ
- **GIVEN** opencode on Linux started with `OPENCODE_SERVER_PASSWORD` set
- **WHEN** the agent runs `cat /proc/$PPID/environ` through the `bash` tool
- **THEN** the read fails with a permission error

#### Scenario: Nothing to hide
- **GIVEN** opencode started with no denied variable in its environment
- **WHEN** a same-user process reads `/proc/<opencode pid>/environ`
- **THEN** the read succeeds as before
