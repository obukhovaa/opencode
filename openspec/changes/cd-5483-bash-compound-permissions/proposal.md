## Why

CD-5483 records a live prefix-glob bypass: an agent allowed to run a reporting
script could append another command, because bash permission matching examined
the entire string only. Agent shells hold service credentials, so a reporting
allow rule must not authorize another executable statement.

## What Changes

- Parse Bash syntax before permitting a non-exempt bash invocation.
- Evaluate the existing permission chain on the full command and every
  executable statement, including nested command and process substitutions.
- Deny the whole call before execution if any check denies or syntax cannot be
  validated; otherwise preserve ask precedence and the existing single prompt.
- Cover synchronous and background dispatch with the same gate and tests.
- Document compatibility and the boundary between command rules and a sandbox.

## Capabilities

### New Capabilities
- `bash-command-permissions`: syntax-aware permission checking for executable
  parts of a Bash invocation.

### Modified Capabilities
None. Rule-denial results and the existing simple-command exemption retain
their behavior.

## Impact

`internal/permission`, the bash tool's common permission gate,
`docs/tool-permissions.md`, and a direct `mvdan.cc/sh/v3` v3.12.0 dependency
(Go 1.23 minimum, compatible with this project's Go 1.25.8). No config field,
schema, credential, migration or deployment change.
