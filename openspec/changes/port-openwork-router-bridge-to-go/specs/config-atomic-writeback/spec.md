## ADDED Requirements

### Requirement: Exported UpdateCfgFile helper

The `internal/config` package SHALL expose `UpdateCfgFile(updateCfg func(*Config)) error` as a public function. The function MUST take the same closure-style API as the prior package-private `updateCfgFile`. All in-tree callers (`UpdateTheme`, `UpdateVimMode`, `UpdateAgentModel`) MUST migrate to the exported name. The package-private symbol MUST be removed.

#### Scenario: External package calls the helper

- **WHEN** `internal/bridge/send.go` calls `config.UpdateCfgFile(func(c *config.Config) { ... })`
- **THEN** the call compiles and the closure executes against the parsed config

### Requirement: Atomic safe-replace via temp file + rename + parent-dir fsync

`UpdateCfgFile` SHALL replace the prior `os.WriteFile` call with the canonical POSIX safe-replace sequence: open `<configFile>.tmp` in the SAME DIRECTORY as the target (same filesystem is required for atomic rename), write the encoded JSON, call `f.Sync()`, call `f.Close()`, call `os.Rename(tmp, target)`, then open the parent directory and call `f.Sync()` on it. The parent-directory fsync is REQUIRED — without it, a power-loss-class crash can lose the rename even after the file's contents have been fsync'd.

On Windows, Go's `os.Rename` calls `MoveFileEx(MOVEFILE_REPLACE_EXISTING)`, which is best-effort atomic on NTFS but not formally guaranteed by Microsoft. The Windows code path MAY ship best-effort initially; POSIX is the production target.

#### Scenario: SIGKILL mid-writeback leaves prior file

- **WHEN** `UpdateCfgFile` is interrupted by SIGKILL between the temp-file write and the rename
- **THEN** the prior `.opencode.json` is intact on disk and the `.tmp` file is discarded on next process start (or remains as a harmless stray that the next `UpdateCfgFile` overwrites)

#### Scenario: SIGKILL after rename but before parent-dir fsync (POSIX)

- **WHEN** a power-loss-class crash occurs after `os.Rename` but before the parent-directory fsync
- **THEN** because the implementation MUST include the parent-directory fsync, the crash window is closed in practice — operators do not see a missing-but-renamed file after recovery

#### Scenario: Temp file in same directory as target

- **WHEN** `UpdateCfgFile` writes its temp file
- **THEN** the temp file lives in the same directory as the target `.opencode.json` (required for `os.Rename` to be atomic on POSIX)

### Requirement: File mode forced to 0o600 when tokens are present

`UpdateCfgFile` SHALL detect whether the configuration being written contains any token-bearing field — at minimum `cfg.Router.Channels.Telegram.Bots[].Token`, `cfg.Router.Channels.Slack.Apps[].BotToken`, `cfg.Router.Channels.Slack.Apps[].AppToken`, `cfg.Router.Channels.Mattermost.Instances[].AccessToken`. When ANY such field is non-empty, the temp file MUST be written with mode `0o600`. This MUST upgrade a pre-existing `0o644` file the moment it gains a token. The function MUST NOT silently inherit a wider mode under these conditions.

For configs without secrets, `UpdateCfgFile` MUST preserve the existing file mode via `os.Stat` before writing the temp file. For new files (no existing target), the default mode MUST be `0o600`.

#### Scenario: Existing 0o644 install gains a token

- **WHEN** an operator with `.opencode.json` at `0o644` (no tokens) calls `POST /identities/telegram` with a valid bot token
- **THEN** after the call returns, `stat .opencode.json` reports mode `0o600`

#### Scenario: Operator hardened to 0o400 stays hardened

- **WHEN** an operator has chmod'd `.opencode.json` to `0o400` and an `UpdateTheme` call (no tokens involved) runs
- **THEN** the file mode is preserved as `0o400` — `UpdateCfgFile` does not surprise-widen it

#### Scenario: New file with tokens defaults to 0o600

- **WHEN** `UpdateCfgFile` is called with no existing `.opencode.json` and the closure adds a Slack token
- **THEN** the new file is written with mode `0o600`

### Requirement: In-memory cfg mutation is the caller's responsibility

`UpdateCfgFile` SHALL document that it does NOT update the `config.cfg` singleton returned by `config.Get()`. Every caller MUST mutate `config.cfg` in-process alongside calling `UpdateCfgFile` if subsequent reads of `cfg.X` must reflect the change without a process restart. The documentation MUST cite this as a contract, not an implementation detail.

#### Scenario: Theme update reflects immediately

- **WHEN** `UpdateTheme("dark")` is called
- **THEN** `config.Get().TUI.Theme == "dark"` is true synchronously after the call returns, because `UpdateTheme` mutates `config.cfg.TUI.Theme` alongside invoking `UpdateCfgFile`

#### Scenario: Bridge identity add reflects immediately

- **WHEN** `POST /identities/slack` returns 200 for a new identity
- **THEN** `config.Get().Router.Channels.Slack.Apps` includes the new identity synchronously, because the handler mutated `cfg.Router` alongside invoking `UpdateCfgFile`

#### Scenario: Direct UpdateCfgFile call without in-memory mutation

- **WHEN** a caller invokes `UpdateCfgFile` to update field X but does NOT mutate `cfg.X` in-process
- **THEN** subsequent `config.Get().X` reads return the stale value until the next process restart — this is documented behavior, not a defect, but callers in `internal/bridge/` MUST NOT rely on it

### Requirement: Migrated existing callers preserve behavior

The three existing callers — `UpdateTheme` (`config.go:1311`), `UpdateVimMode` (`config.go:1326`), `UpdateAgentModel` (`config.go:1274`) — MUST be updated to call the exported `UpdateCfgFile` and MUST retain their existing in-memory `cfg` mutations. Their externally observable behavior MUST NOT change.

#### Scenario: UpdateTheme behavior unchanged

- **WHEN** `UpdateTheme("solarized")` is called
- **THEN** `cfg.TUI.Theme` is `"solarized"` in-process AND `.opencode.json` on disk reflects the new value atomically — identical to behavior before the rework
