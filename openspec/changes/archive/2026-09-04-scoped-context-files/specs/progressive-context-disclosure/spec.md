# progressive-context-disclosure

On-demand injection of nested context files: a discovery walk identifies context files
strictly below `workDir`, a compact manifest lists them in the system prompt, and each
file's body is injected into the first tool result that touches its owning directory —
never by mutating the system prompt. Covers discovery caps, manifest format, trigger
tools, activation semantics, per-session state and budgets, failure isolation, and
opt-out.

## ADDED Requirements

### Requirement: Nested context file discovery is bounded and opt-outable

Once per process per `workDir`, the system SHALL walk the subtree strictly below
`workDir` (depth ≥ 1) to find files whose basename matches any file-type entry in the
effective global `contextPaths` (directory-type entries with a trailing `/` are
excluded from the basename set). The walk SHALL use `filepath.WalkDir` with a
hardcoded skip set mirroring `internal/llm/tools/ls.go` `shouldSkip()` (hidden-dot
prefix, `.git`, `node_modules`, `vendor`, `dist`/`build`/`target` and similar build
directories) plus the configured data directory (`data.directory`, which may be a
non-hidden name — both discovery call sites obtain the skip set through the same
config accessor so manifest and injection agree). `.gitignore` honoring is a
deliberate non-goal for v1 (no gitignore library in `go.mod`; ripgrep is an external
binary unsuitable for the prompt-build path); the caps bound over-discovery. The walk
result is cached for the process lifetime.

Candidates MUST be regular files: symlinks (and any other non-regular entry) are
rejected with a WARN — git preserves symlinks, and a committed
`docs/AGENTS.md -> ~/.ssh/id_rsa` would otherwise be read with no permission prompt.
As defense in depth, each accepted candidate is symlink-resolved (`EvalSymlinks`)
and MUST resolve strictly inside the symlink-resolved `workDir` — the same
containment order scoped entries get. Each discovered file's manifest label
(frontmatter `description`, else first markdown heading) is extracted ONCE at
discovery time, behind these checks, and cached with the walk result; prompt-build
never reads candidate files from disk.

Discovery is governed by the `contextDiscovery` top-level config object:
`enabled` (default `true`), `maxFiles` (default 100), `maxDepth` (default 8),
`maxFileBytes` (default 32,768 bytes), `maxSessionBytes` (default 131,072 bytes).
Setting `contextDiscovery.enabled = false` disables discovery and manifest injection
entirely for all agents. An individual agent or flow step may set `context.nested =
false` to opt out of manifest injection and body injection for that agent/step while
leaving discovery enabled for other agents.

#### Scenario: Discovery finds files in subdirectories

- **WHEN** `workDir` contains `AGENTS.md` (root) and `subsystem/AGENTS.md` (nested)
  and `contextPaths` contains `"AGENTS.md"`
- **THEN** the discovery set contains `subsystem/AGENTS.md` and does NOT contain
  the root `AGENTS.md` (which is resolved by the normal scoped-resolution path)

#### Scenario: `enabled: false` produces no manifest and no injection

- **WHEN** `contextDiscovery.enabled` is `false`
- **THEN** no manifest block appears in any agent's system prompt
- **AND** no `<system-reminder>` carrying a nested context body is ever appended to
  a tool result in any session

#### Scenario: `context.nested: false` on a step disables for that step only

- **WHEN** a step declares `context: { nested: false }` and another step in the same
  flow has no such override
- **THEN** the step with `nested: false` receives no manifest section and no body
  injections, while the other step still receives them

#### Scenario: `maxFiles` cap truncates the discovery set

- **WHEN** the subtree contains 150 files matching the basename set
- **THEN** the discovery set contains at most `maxFiles` entries (walk order
  determines which files make the cut) and the manifest header notes the truncation

#### Scenario: Symlinked candidate is rejected

- **WHEN** the subtree contains a symlink `docs/AGENTS.md` pointing at a file
  outside `workDir` (or anywhere — candidates must be regular files)
- **THEN** the symlink does not enter the discovery set: it is never listed in the
  manifest and never injected into a tool result

#### Scenario: Non-hidden configured data directory is skipped

- **WHEN** `data.directory` is set to a non-hidden name (e.g. `opencode-data`) and
  that directory contains a matching basename
- **THEN** the walk skips the directory and the file is neither manifest-listed nor
  injected

### Requirement: Manifest block is present, cache-stable, and well-formed

When ≥1 nested file was discovered and the agent/step has not opted out, the system
SHALL append a compact manifest section to the system prompt. The section SHALL list
one line per discovered file: relative-to-workDir path + a short label (YAML
frontmatter `description`, else first markdown heading, truncated to ~120 characters;
relative path only if neither exists). The section SHALL include a sentence stating
that the listed file bodies are NOT loaded into this prompt and that they will be
injected automatically the first time a tool touches the owning directory.

The manifest content MUST be byte-identical across all turns of a session. Stability
is structural: labels ride the process-level discovery cache (computed once at walk
time), so rendering never reads the disk and editing a nested file mid-session cannot
change the manifest bytes. When the manifest would exceed a total-byte cap, it
degrades to paths-only, then to a `"... N more files not shown"` trailing line. The
manifest is absent when nothing was discovered or the agent/step is opted out —
producing no prompt delta for repos without nested context files.

Files that the agent's or step's own scoped `context.paths` layers already inline
into the system prompt (exact path entries and trailing-slash subtrees, following the
same layer-participation rule as resolution) MUST be subtracted from that agent's
manifest AND from its disclosure state — the body is already in the prompt, so
listing it as "NOT loaded" would be false and injecting it again would deliver it
twice. The subtraction is computed with the same deterministic filter on both sides,
so manifest and injection always agree; a different agent without that layer keeps
the full candidate set.

#### Scenario: Manifest appears for a repo with nested context files

- **WHEN** `workDir` contains `services/auth/AGENTS.md` and `services/billing/AGENTS.md`
  and discovery finds both
- **THEN** the system prompt contains a manifest listing two relative paths with labels
- **AND** the same manifest bytes appear on every turn of the session

#### Scenario: No manifest for repos without nested files

- **WHEN** `workDir` contains only a root `AGENTS.md` and no `AGENTS.md` files in
  subdirectories
- **THEN** no manifest section appears in any agent's system prompt and the assembled
  prompt is byte-identical to the pre-feature behavior

#### Scenario: Overflow degrades gracefully

- **WHEN** the manifest would exceed its total-byte cap due to long labels
- **THEN** the manifest degrades first to paths-only lines, then to a trailing
  `"... N more files not shown"` summary — never exceeding the cap

#### Scenario: File inlined by a scoped layer is neither listed nor injected

- **WHEN** an agent declares `context: { paths: ["services/auth/AGENTS.md"], mode: append }`
  and `services/auth/AGENTS.md` is in the discovery set
- **THEN** that agent's manifest omits `services/auth/AGENTS.md` and its first tool
  call into `services/auth/` injects nothing for it (the body is already inline)
- **AND** a different agent without that layer still lists it and still receives the
  injection on first touch

### Requirement: Trigger set and the strictly-inside activation rule

The disclosure wrapper SHALL monitor these built-in tool calls for directory context:

| Tool | Path parameter used |
|------|---------------------|
| `read` | `file_path` (parent directory) |
| `write` | `file_path` (parent directory) |
| `edit` | `file_path` (parent directory) |
| `multiedit` | `file_path` (parent directory) |
| `patch` | file paths parsed from `patch_text` section headers (parent directory of each) |
| `grep` | `path` when set (itself if directory; parent if file) |
| `glob` | `path` (itself if directory; parent if file) + directory prefix of the pattern |
| `ls` | `path` |

The `bash` tool is explicitly NOT a trigger. The `delete` tool is also not a trigger:
removing files is not working-within-a-subtree that benefits from its instructions.
Path extraction SHALL reuse and extend the existing `tools.ExtractPathsFromCall`
(`internal/llm/tools/tools.go:167`) for all trigger tools, adding the glob
pattern-prefix and grep-without-path rules on top of it. Activation fires only when
the tool's resolved target directory is **strictly equal to or strictly inside** a
nested context file's owning directory (the directory containing the discovered
file). A tool call
whose resolved directory is `workDir` itself activates nothing — `workDir` is the root
of the nested set and none of its owning directories are in the discovery set.

Owner resolution walks upward from the target directory to (but excluding) `workDir`,
collecting every nested context file whose owning directory is on the path, and
injecting not-yet-injected ones outermost-first. Both sides of the owner comparison —
the discovered owning directories and the model-supplied target directory — are
canonicalized consistently: symlink resolution plus case folding applied ONLY when
the workDir's filesystem is actually case-insensitive, probed once per workDir (stat
a case-swapped spelling, compare identity with `os.SameFile`) and cached. A
differently-cased path on macOS/Windows defaults still matches its owners, while on a
case-sensitive filesystem two sibling directories differing only by case remain
distinct trees — neither owner matching nor the scoped-layer subtraction may
cross-match them.

#### Scenario: First `read` into a subdirectory triggers injection

- **WHEN** `services/auth/AGENTS.md` is in the discovery set and the model calls
  `read` with `file_path: "services/auth/handler.go"`
- **THEN** after the `read` result, a `<system-reminder>` block carrying the content
  of `services/auth/AGENTS.md` is appended to the tool result
- **AND** the system prompt is not modified

#### Scenario: A whole-tree grep with no `path` arg activates nothing

- **WHEN** the model calls `grep` with `pattern: "TODO"` and no `path` argument
- **THEN** no nested context body is injected (the absent `path` resolves to
  `workDir`, which is excluded from the activation set)

#### Scenario: Outermost-first layered injection

- **WHEN** `services/AGENTS.md` and `services/auth/AGENTS.md` are both in the
  discovery set and the model calls `read` with
  `file_path: "services/auth/handler.go"`
- **THEN** the injection first delivers `services/AGENTS.md` (outermost), then
  `services/auth/AGENTS.md` (innermost) in the same tool result's `<system-reminder>`
  block, ordered outermost-first

### Requirement: Injection via tool-result `<system-reminder>` — never system prompt

Nested context bodies SHALL be delivered exclusively by appending to the content of the
triggering tool's result, wrapped in `<system-reminder>` tags with the `# From:<abs-path>`
header preceding the body. The system prompt MUST NOT be mutated between turns.

Any literal `<system-reminder>` / `</system-reminder>` occurrence INSIDE the body is
neutralized before wrapping (a backslash after `<` defuses the tag while preserving
the content) — a repo file must not be able to terminate the reminder block early,
forge tool output, or open a fabricated reminder with a fake `# From:` header.

Activation MUST fire only when the inner tool call SUCCEEDED (non-error result).
Injecting directory context because a `read` of a nonexistent file returned an error
is prohibited — it is noise with no useful signal.

#### Scenario: Injection format matches context-resolution header format

- **WHEN** `services/auth/AGENTS.md` is injected after a successful `read`
- **THEN** the appended content includes the text
  `<system-reminder>\n# From:/abs/path/to/services/auth/AGENTS.md\n<body>\n</system-reminder>`

#### Scenario: A body containing literal reminder tags cannot forge framing

- **WHEN** a nested context file's body contains the literal strings
  `</system-reminder>` and `<system-reminder>`
- **THEN** the appended tool-result content contains exactly ONE opening and ONE
  closing `<system-reminder>` tag (the wrapper's own framing); the body's literal
  tags are defused in place with their content preserved

#### Scenario: System prompt is unchanged after activation

- **WHEN** a session's first tool call triggers injection of a nested file
- **THEN** the system prompt bytes on the next request are identical to the first
  request's system prompt bytes

#### Scenario: Failed tool call does not trigger injection

- **WHEN** the model calls `read` with a `file_path` that does not exist and the
  tool returns an error result
- **THEN** no `<system-reminder>` carrying a nested context body is appended

### Requirement: Per-session activation state and byte budget

Each session tracks independently which nested context files it has already received.
A file SHALL be injected at most once per session regardless of how many tool calls
touch its directory — including across different trigger tools (a `read` may fire the
injection and a later `grep` on the same directory must not re-inject), which requires
the activation state to be ONE shared per-toolset object passed to every trigger-tool
wrapper rather than independent per-wrapper state. Subagent sessions (spawned via the `task` tool) have their own
activation set and do NOT inherit the parent session's activations — a subagent that
never touches a directory must not receive its context.

The total bytes of nested file bodies injected into a single session SHALL not exceed
`contextDiscovery.maxSessionBytes`. When the budget is exhausted for a session, further
injection attempts are silently skipped (log INFO once per session on first exhaustion)
and the tool result is returned unchanged.

After a process restart, previously activated sessions may receive duplicate injections
on the next directory-touching tool call. This is accepted — the same tradeoff the
deferred-tools change accepted.

#### Scenario: Second touch of the same directory does not re-inject

- **WHEN** the model calls `read` on `services/auth/handler.go` (injection fires) and
  then calls `grep` targeting `services/auth/` in the same session
- **THEN** `services/auth/AGENTS.md` is NOT re-appended to the second tool result

#### Scenario: Subagent sessions are isolated from the parent

- **WHEN** the parent session triggers injection of `services/auth/AGENTS.md` and
  then spawns a subagent via the `task` tool
- **THEN** the subagent's first `read` into `services/auth/` still triggers injection
  for the subagent session (it has its own clean activation set)

#### Scenario: Per-session byte budget exhaustion is silent

- **WHEN** a session has received injections totaling `maxSessionBytes` bytes and the
  model then calls `read` into another directory with an uninjected context file
- **THEN** the tool result is returned without any `<system-reminder>` addition
- **AND** a single INFO log records budget exhaustion for the session

### Requirement: Failure isolation — disclosure never fails a tool call

A nested context file that cannot be read (permission error, deleted between discovery
and activation, or exceeding `maxFileBytes`) SHALL be silently skipped: the triggering
tool's result is returned to the model unchanged, with no error propagation. The skip
is logged at WARN level naming the file and the reason.

The activation-time read is bounded and race-free: the discovery cache lives for the
process lifetime and the filesystem can change underneath it, so before reading, the
file MUST re-pass the regular-file check (Lstat — a FIFO or device is skipped without
opening), and the open itself MUST go through kernel-enforced beneath-only resolution
(`os.Root`): the path lookup cannot escape `workDir` even when a component is swapped
for a symlink BETWEEN the check and the open — a userspace re-check alone leaves that
TOCTOU window winnable. On Unix the open also refuses a symlink at the final
component (`O_NOFOLLOW`) and cannot block on a swapped-in FIFO (`O_NONBLOCK`). The
size is checked via Stat on the OPENED descriptor (no TOCTOU window) and the read
goes through a limit reader capped at `maxFileBytes`+1 bytes, so even an
under-reporting Stat can never cause an unbounded read. The activation path never
loads more than `maxFileBytes`+1 bytes of any candidate into memory. The discovery
walk's label extraction uses the same beneath-only open.

The disclosure wrapper SHALL be entirely absent from the tool chain when discovery is
disabled (`contextDiscovery.enabled = false`) or when no nested files were found in
the walk — zero allocation and zero behavior change.

#### Scenario: Unreadable nested file does not fail the triggering tool

- **WHEN** `services/auth/AGENTS.md` was discovered but becomes unreadable before
  activation (e.g., permissions changed), and the model calls `read services/auth/handler.go`
- **THEN** the `read` result is returned to the model normally
- **AND** a WARN log records the skipped injection with file path and error

#### Scenario: Post-discovery symlink swap is not followed

- **WHEN** `services/auth/AGENTS.md` was discovered as a regular file and is later
  replaced by a symlink pointing outside `workDir` — at any moment, including between
  the activation-time checks and the open
- **THEN** nothing from outside `workDir` is injected: the beneath-only open resolves
  the path strictly inside `workDir` at the kernel level, so the swap cannot win a
  race against a check-then-open sequence, and the skip is WARN-logged

#### Scenario: Wrapper absent when discovery finds nothing

- **WHEN** the subtree below `workDir` contains no files matching the basename set
- **THEN** no disclosure wrapper is installed in any agent's tool chain
- **AND** the tool-call overhead for those agents is identical to the pre-feature behavior
