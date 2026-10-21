# Spec Delta

## MODIFIED Requirements

### Requirement: The process is executed without a shell under the manifest's confinement

The system SHALL start the manifest's `command` directly with an argument vector consisting of `prefixArgs` followed by the model-influenced arguments; no shell SHALL interpret the vector. The working directory SHALL be the manifest `cwd` resolved inside the working directory (default: the working directory). The parent environment SHALL be opencode's environment filtered by the tool environment deny list (see the `tool-process-environment` capability). The child environment SHALL be the parent environment when `env.inherit` is true; otherwise only `PATH`, `HOME`, `TMPDIR` and the names in `env.pass`; `env.set` entries SHALL be added last, with `${env.NAME}` tokens expanded from the parent environment. The call SHALL be bounded by the call's `timeout` clamped to `maxTimeout` (built-in defaults 2 and 10 minutes, inherited as the limits requirement describes when the manifest sets neither); on expiry the process group SHALL be killed and the result SHALL be an error naming the timeout.

#### Scenario: Shell operators are inert

- **WHEN** the model passes an argument `x; rm -rf /tmp/y` in argv mode
- **THEN** the binary receives that string as a single argument and no second process is started by the system

#### Scenario: Restricted environment

- **WHEN** the manifest sets `env.inherit: false` and `env.pass: ["SNOWFLAKE_HOME"]`
- **THEN** the child sees `PATH`, `HOME`, `TMPDIR`, `SNOWFLAKE_HOME` and nothing else from the parent environment

#### Scenario: Denied names are not passed or expanded

- **GIVEN** `MYSQL_DSN` is on the tool environment deny list and set in opencode's environment
- **WHEN** a manifest sets `env.inherit: true`, or lists `MYSQL_DSN` in `env.pass`, or sets `env.set: {DSN: "${env.MYSQL_DSN}"}`
- **THEN** the child sees no `MYSQL_DSN`, and `DSN` (when set) is empty

#### Scenario: Timeout kills the process group

- **WHEN** the call's timeout elapses before the process exits
- **THEN** the process and its children are terminated and the model receives an error response stating the timeout
