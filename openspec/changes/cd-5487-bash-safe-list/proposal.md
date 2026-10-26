## Why

CD-5487: the legacy safe list bypasses every configured bash permission check.
`env bash -c '...'` runs under a deny-all rule because env is on that list.
Removing individual wrappers cannot keep a growing command list safe.

## What Changes

- Every bash invocation uses the common policy/syntax gate before dispatch.
- Keep the legacy list solely as a parallel-scheduling hint, never authority.
- Preserve the existing allow, ask, rule-denial and rejected-prompt behavior.
- Document the default-ask compatibility change and cover wrappers plus plain
  safe-list entries in both dispatch modes.

## Capabilities

### New Capabilities
None.

### Modified Capabilities
- `bash-command-permissions`: all bash calls require configured permission.
- `bash-background-mode`: remove the execution-exemption requirement.

## Impact

Depends on PR #110 (CD-5483), which supplies the syntax-aware common gate.
No config/schema field, credentials, migration or deployment changes. Commands
that previously skipped rules now use their normal default-ask behavior.
