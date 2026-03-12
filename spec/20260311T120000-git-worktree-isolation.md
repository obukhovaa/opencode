# Git Worktree Isolation

**Date**: 2026-03-11
**Status**: Draft
**Author**: AI-assisted

## Overview

Add git worktree support to OpenCode, enabling parallel, isolated working directories backed by `git worktree`. A CLI flag (`--worktree/-w`) creates or reuses a worktree under `<repo>/.opencode/worktrees/<name>`, ties it to a session of the same name, and points all agent tools at that directory. Agents can also declare `isolation: worktree` to automatically spawn subagents in dedicated worktrees.

## Motivation

### Current State

The working directory is a process-global singleton, set once at startup:

```go
// cmd/root.go
if cwd != "" {
    os.Chdir(cwd)
}
_, err := config.Load(cwd, debug)
```

```go
// config/config.go
func WorkingDirectory() string {
    return cfg.WorkingDir
}
```

Every tool resolves paths through this single global:

```go
// tools/read.go, edit.go, write.go, etc.
if !filepath.IsAbs(filePath) {
    filePath = filepath.Join(config.WorkingDirectory(), filePath)
}
```

The system prompt bakes it in at provider creation time:

```go
// prompt/prompt.go
func getEnvironmentInfo() string {
    cwd := config.WorkingDirectory()
    // ...
}
```

This creates problems:

1. **No parallel isolation**: Two sessions editing the same files will collide. There is no way for one session to work on a feature branch while another fixes a bug without interference.
2. **No per-session working directory**: Sessions have a `ProjectID` but no `WorkingDir` field. Switching sessions never changes where tools operate.
3. **Subagent collision**: Task-spawned subagents share the parent's working directory. Two workhorse subagents running in parallel will overwrite each other's files.

### Desired State

```bash
# Start OpenCode in an isolated worktree
opencode --worktree feature-auth

# Creates:
#   .opencode/worktrees/feature-auth/   (git worktree)
#   branch: worktree-feature-auth
#   session ID: feature-auth
# All tools operate on the worktree path.
```

```json
// Agent with automatic worktree isolation
{
  "agents": {
    "parallel-worker": {
      "mode": "subagent",
      "isolation": "worktree"
    }
  }
}
```

```yaml
# Or in markdown frontmatter
---
isolation: worktree
---
```

## Research Findings

### Claude Code Worktree Implementation

Claude Code uses `--worktree <name>` (`-w`) to create worktrees at `<repo>/.claude/worktrees/<name>` with branches named `worktree-<name>`. Auto-generates random names when no name is given. Subagents can use `isolation: worktree` in frontmatter. Cleanup is automatic when no changes exist; prompts user otherwise.

| Aspect | Claude Code | Proposed OpenCode |
|---|---|---|
| Storage path | `.claude/worktrees/<name>` | `.opencode/worktrees/<name>` |
| Branch naming | `worktree-<name>` | `worktree-<name>` |
| Session binding | Implicit | Session ID = worktree name |
| CLI flag | `--worktree / -w` | `--worktree / -w` |
| Subagent isolation | `isolation: worktree` in frontmatter | Same + config JSON |
| Auto-cleanup | Yes, when no changes | Yes, when no changes |
| Random names | adjective-noun pattern | Same pattern |

### Anomaly OpenCode TypeScript Implementation

The reference TypeScript implementation (`anomalyco/opencode`) stores worktrees under a global data directory (`Global.Path.data/worktree/<projectID>/<name>`) with branches prefixed `opencode/`. It has `create`, `remove`, and `reset` operations. It uses a "start command" pattern for bootstrapping worktree environments (e.g., `npm install`).

**Key finding**: Both implementations treat worktrees as git-managed, ephemeral working directories with automatic lifecycle management. The critical difference is storage location — Claude uses repo-local (`.claude/worktrees/`), while anomaly uses a global data directory.

**Implication**: Storing worktrees in `<repo>/.opencode/worktrees/` (repo-local) is preferred because:
- Discoverable by any OpenCode instance working on the same repo
- Easy to inspect and manage manually
- Natural `.gitignore` entry (`.opencode/worktrees/`)
- Session-to-worktree association is trivial via naming convention

### Current Codebase Architecture

| Component | Current Behavior | Worktree Impact |
|---|---|---|
| `config.WorkingDirectory()` | Global singleton, set once | Must support per-context override |
| `Session` struct | No `WorkingDir` field | No DB change needed — worktree path derived from filesystem detection |
| `AgentInfo` struct | No `Isolation` field | Needs `Isolation` field |
| `TaskParams` struct | No `Isolation` field | Needs optional `Isolation` field |
| System prompt (`getEnvironmentInfo`) | Reads global `WorkingDir` | Must accept override path |
| Context files (`getContextFromPaths`) | Cached globally via `sync.Once` | `sync.Once` → `sync.Map` keyed by cwd |
| Tools path resolution | `config.WorkingDirectory()` | Must read from context, not global |
| TUI sidebar | Shows project + session provider | Needs worktree indicator |
| TUI session dialog | Shows `sess.Title` only | Needs worktree badge |

**Key finding**: The hardest change is decoupling tool path resolution from the global `config.WorkingDirectory()`. Currently 15+ call sites across all tools read the global directly. The cleanest approach is to introduce a context-key-based working directory override that tools check first.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Working dir override mechanism | Context value (`WorkingDirContextKey`) checked before global fallback | Non-breaking: tools fall back to global when no context value is set. Avoids refactoring all tool constructors. |
| Session-to-worktree binding | Filesystem detection: session ID = worktree name | No DB schema changes needed. Detection is `os.Stat(<repo>/.opencode/worktrees/<sessionID>)` + `git worktree list` validation. If worktree is removed from disk, session silently reverts to main repo — no stale data. |
| Worktree storage path | `<repo>/.opencode/worktrees/<name>` | Repo-local, discoverable, matches Claude Code pattern. Easy `.gitignore`. |
| Branch naming | `worktree-<name>` | Matches Claude Code convention. Clear namespace, unlikely to collide with user branches. |
| Random name generation | `adjective-noun` word list | Same pattern as Claude Code and anomaly impl. Memorable, low collision rate with 29×31=899 combinations. |
| Isolation field type | `string` enum, single value `"worktree"` for now | Extensible if other isolation modes are added later (e.g., `container`). |
| Agent config placement | `Isolation` field on `AgentInfo` and `config.Agent` | Consistent with existing fields like `Mode`, `Color`, `Hidden`. |
| Task tool parameter | Optional `isolation` field in `TaskParams` | Allows callers to request isolation on a per-invocation basis, independent of agent config. |
| Context file loading for worktrees | Cache keyed by cwd; fresh worktrees reuse main repo cache key | `getContextFromPaths` changes from `sync.Once` to `sync.Map[cwd]string`. Fresh worktree creation passes main repo cwd → hits existing cache (no redundant I/O). On restart with existing worktree, worktree cwd becomes a new cache key → reads context files from worktree dir. Consistent with current behavior where context files are never hot-reloaded within a process. |
| Cleanup behavior | Auto-remove on subagent exit if no git-tracked changes; prompt user on CLI exit | Consistent with Claude Code. Subagent cleanup is silent; user-facing cleanup is interactive. |
| Worktree detection on session load | Check `<repo>/.opencode/worktrees/<sessionID>` existence at session switch time | Zero-cost when no worktree exists. Allows manual worktree creation. |

## Architecture

### Worktree Package

```
internal/worktree/
├── worktree.go        # Core create/remove/list/detect logic
└── worktree_test.go   # Tests
```

```
┌──────────────────────────────────────────────────────┐
│ worktree.Service                                     │
│                                                      │
│  Create(name string) (Info, error)                   │
│  Remove(name string) error                           │
│  List() ([]Info, error)                              │
│  Detect(sessionID string) (*Info, error)             │
│  HasChanges(name string) (bool, error)               │
│  WorktreeDir() string      // .opencode/worktrees    │
│                                                      │
│  Info{Name, Branch, Directory string}                 │
└──────────────────────────────────────────────────────┘
```

### Working Directory Override via Context

```
┌─────────────────────────────┐
│ tools.WorkingDirContextKey  │  ← set by agent-tool.go or cmd/root.go
└─────────────┬───────────────┘
              │
              ▼
┌─────────────────────────────┐
│ tools.ResolveWorkingDir(ctx)│  ← new helper function
│                             │
│  if ctx has WorkingDirKey:  │
│    return ctx value         │
│  else:                      │
│    return config.WorkDir()  │
└─────────────────────────────┘
              │
              ▼
  ┌───────────────────────┐
  │ All tool Run() methods│  ← replace config.WorkingDirectory() calls
  │ call ResolveWorkingDir│
  └───────────────────────┘
```

### CWD-Aware Agent Creation

```
NewAgent(ctx, agentID, outputSchema, stepID)
         │
         ▼
  ctx has WorkingDirContextKey?
         │
    ┌────┴────┐
    │ yes     │ no
    ▼         ▼
  cwd =    cwd =
  ctx val  config.WorkingDirectory()
    │         │
    └────┬────┘
         ▼
  createAgentProvider(agentName, cwd)
         │
         ▼
  prompt.GetAgentPrompt(agentName, provider, cwd)
         │
    ┌────┴────────────────────┐
    │                         │
    ▼                         ▼
  getEnvironmentInfo(cwd)   getContextFromPaths(cwd)
  (ls, git check on cwd)   (sync.Map cache keyed by cwd)
```

### Context File Caching Strategy

```
┌──────────────────────────────────────────────────────────┐
│ getContextFromPaths(cwd string) string                   │
│                                                          │
│  var contextCache sync.Map  // replaces sync.Once        │
│                                                          │
│  if cached, ok := contextCache.Load(cwd); ok {           │
│    return cached.(string)                                │
│  }                                                       │
│  content := processContextPaths(cwd, cfg.ContextPaths)   │
│  contextCache.Store(cwd, content)                        │
│  return content                                          │
└──────────────────────────────────────────────────────────┘

Scenario A: Fresh worktree (just created by TaskTool)
───────────────────────────────────────────────────
  TaskTool creates worktree, passes MAIN REPO cwd to NewAgent
  → cache hit on main repo key → reuses existing context
  (worktree mirrors HEAD, reading its files would be identical)

Scenario B: Existing worktree (process restart)
───────────────────────────────────────────────
  Session loaded, worktree detected on disk via Detect()
  → passes WORKTREE cwd to NewAgent
  → cache miss → reads context files from worktree dir
  (worktree may have diverged, e.g. modified AGENTS.md)
```

### CLI Flag Flow

```
opencode --worktree feature-auth
         │
         ▼
STEP 1: Parse flag
─────────────────
  worktreeName = "feature-auth"

STEP 2: Detect or create worktree
─────────────────────────────────
  dir = <repo>/.opencode/worktrees/feature-auth
  if dir exists:
    verify git worktree is valid
  else:
    git worktree add --no-checkout -b worktree-feature-auth <dir>
    git -C <dir> checkout HEAD

STEP 3: Bind session
─────────────────────
  sessionID = "feature-auth"
  session = sessions.Get(sessionID) || sessions.CreateWithID(sessionID, "feature-auth")

STEP 4: Set working directory context
──────────────────────────────────────
  Store worktree path so it is available to agent provider creation.
  The system prompt's getEnvironmentInfo() receives the worktree path.
  Tools resolve paths against the worktree directory.

STEP 5: Launch TUI / non-interactive
─────────────────────────────────────
  Normal startup, but with working dir pointing to worktree.
```

### Subagent Isolation Flow

```
Parent agent (coder) → TaskTool.Run()
                        │
                        ▼
STEP 1: Check isolation
───────────────────────
  isolation = params.Isolation OR agentInfo.Isolation
  if isolation == "worktree":
    goto STEP 2
  else:
    normal subagent flow (no change)

STEP 2: Create worktree
────────────────────────
  name = toolCallID (unique per invocation)
  worktree.Create(name) → Info{Name, Branch, Directory}

STEP 3: Create session with worktree context
─────────────────────────────────────────────
  session = sessions.CreateTaskSession(toolCallID, parentSessionID, title)
  ctx = context.WithValue(ctx, WorkingDirContextKey, info.Directory)

STEP 4: Create subagent with worktree-aware prompt
───────────────────────────────────────────────────
  factory.NewAgent(ctx, subagentType)
  │
  ├─ ctx carries worktree dir for tool path resolution
  ├─ getEnvironmentInfo(worktreeDir) → shows worktree as cwd
  ├─ getContextFromPaths(mainRepoCwd) → cache hit (fresh worktree)
  │   (fresh worktree = same content as HEAD, reuse main repo cache)
  └─ System prompt includes worktree isolation instructions

STEP 5: Subagent completes
──────────────────────────
  if worktree.HasChanges(name):
    keep worktree, report to parent
  else:
    worktree.Remove(name) → auto-cleanup
```

### TUI Worktree Indication

```
Sidebar:
┌─────────────────────────────────┐
│ Project: my-project             │
│ Session: feature-auth [worktree]│  ← muted [worktree] badge
│                                 │
│ Modified Files:                 │
│   src/auth.go  +42 -10         │
└─────────────────────────────────┘

Session Dialog:
┌─────────────────────────────────┐
│ Select Session                  │
│                                 │
│ > feature-auth     [worktree]  │  ← muted badge
│   fix-login                     │
│   refactor-db      [worktree]  │
│   main-session                  │
└─────────────────────────────────┘
```

### System Prompt Addition (when in worktree)

```
# Git Worktree Isolation

You are working in an isolated git worktree at: <worktree_path>
Branch: worktree-<name>
Main repository: <original_repo_path>

This is a dedicated workspace. All your file changes are isolated from
the main repository. When you are done with your work and there are
git-tracked changes (modified, added, or deleted files), inform the user
and ask whether the worktree should be kept for further work or removed.
If there are no changes, the worktree will be cleaned up automatically.
```

## Implementation Plan

### Phase 1: Worktree Package

- [ ] **1.1** Create `internal/worktree/worktree.go` with `Info` struct (`Name`, `Branch`, `Directory`)
- [ ] **1.2** Implement `Create(repoRoot, name string) (Info, error)` — runs `git worktree add --no-checkout -b worktree-<name> <dir>` then `git -C <dir> checkout HEAD`
- [ ] **1.3** Implement `Remove(repoRoot, name string) error` — runs `git worktree remove --force <dir>` then `git branch -D worktree-<name>`
- [ ] **1.4** Implement `List(repoRoot string) ([]Info, error)` — scans `.opencode/worktrees/` directory
- [ ] **1.5** Implement `Detect(repoRoot, sessionID string) (*Info, error)` — checks if `.opencode/worktrees/<sessionID>` exists and is a valid git worktree
- [ ] **1.6** Implement `HasChanges(dir string) (bool, error)` — runs `git -C <dir> status --porcelain` and checks for output
- [ ] **1.7** Implement random name generator (adjective-noun word lists)
- [ ] **1.8** Write unit tests

### Phase 2: Working Directory Context Override

- [ ] **2.1** Add `WorkingDirContextKey` to `internal/llm/tools/tools.go`
- [ ] **2.2** Add `ResolveWorkingDir(ctx context.Context) string` helper that checks context first, falls back to `config.WorkingDirectory()`
- [ ] **2.3** Replace all `config.WorkingDirectory()` calls in tool `Run()` methods with `ResolveWorkingDir(ctx)` — affects `bash.go`, `read.go`, `edit.go`, `write.go`, `delete.go`, `multiedit.go`, `patch.go`, `glob.go`, `grep.go`, `lsp.go`, `view_image.go`, `ls.go`, `webfetch.go`, `websearch.go`
- [ ] **2.4** Update tool description interpolation in `bash.go` to use a dynamic reference or accept the working dir is baked in at tool creation time (acceptable since tool descriptions are informational)
- [ ] **2.5** Write tests verifying `ResolveWorkingDir` behavior with and without context value

### Phase 3: Agent and Config Changes

- [ ] **3.1** Add `Isolation string` field to `config.Agent` struct (JSON: `"isolation"`)
- [ ] **3.2** Add `Isolation string` field to `agent.AgentInfo` struct
- [ ] **3.3** Wire `Isolation` through agent registry discovery (config + markdown frontmatter parsing)
- [ ] **3.4** Parameterize `getEnvironmentInfo(cwd string)` in `prompt/prompt.go` to accept a working directory parameter
- [ ] **3.5** Update `GetAgentPrompt()` to accept `cwd string` parameter and forward to `getEnvironmentInfo(cwd)` and `getContextFromPaths(cwd)`
- [ ] **3.5a** Replace `sync.Once` / `onceContext` in `getContextFromPaths` with `sync.Map` keyed by cwd string
- [ ] **3.6** Add worktree-specific system prompt section when agent is running in a worktree (isolation hint, branch info, cleanup instructions)
- [ ] **3.7** Update `createAgentProvider(agentName, cwd string)` in `agent/agent.go` to accept cwd and pass to `GetAgentPrompt(agentName, provider, cwd)`
- [ ] **3.8** Update `newAgent()` to extract cwd from context (`WorkingDirContextKey`) and pass to `createAgentProvider`
- [ ] **3.9** Update `AgentFactory.NewAgent()` to propagate cwd from context through the agent creation chain

### Phase 4: CLI Integration

- [ ] **4.1** Add `--worktree / -w` flag to `cmd/root.go` (optional string, can be empty for auto-generated name)
- [ ] **4.2** In `root.go` run handler: if `--worktree` is set, call `worktree.Detect()` or `worktree.Create()`, set `app.InitialSessionID` to worktree name
- [ ] **4.3** Store worktree directory path on `App` struct so it can be propagated to agent context
- [ ] **4.4** Wire worktree directory into agent provider creation context
- [ ] **4.5** Ensure `.opencode/worktrees/` is suggested for `.gitignore` (document in AGENTS.md or README)

### Phase 5: Task Tool Isolation Support

- [ ] **5.1** Add optional `Isolation string` field to `TaskParams` in `agent-tool.go`
- [ ] **5.2** Update task tool description to document the `isolation` parameter
- [ ] **5.3** In `agentTool.Run()`: resolve isolation from params or agent config, if `"worktree"` then create worktree, set `WorkingDirContextKey` on context. For fresh worktrees, use the main repo cwd as the context-paths cache key (so `getContextFromPaths` reuses existing cache)
- [ ] **5.4** After subagent completes: check `HasChanges()`, auto-remove worktree if clean
- [ ] **5.5** Pass worktree info back in `TaskResponseMetadata` (add `WorktreeName`, `WorktreeKept` fields)
- [ ] **5.6** Update `NewAgentTool` to accept worktree service dependency

### Phase 6: Session-Worktree Detection

- [ ] **6.1** On session switch (TUI or CLI `--session`): call `worktree.Detect(sessionID)` to check if a matching worktree exists
- [ ] **6.2** If worktree detected: set working directory context for the session's agent
- [ ] **6.3** On session list load: batch-detect worktrees for all sessions (for TUI display)

### Phase 7: TUI Integration

- [ ] **7.1** Add `[worktree]` muted badge to session section in `sidebar.go` (follow session provider hint pattern)
- [ ] **7.2** Add `[worktree]` muted badge to session items in `dialog/session.go`
- [ ] **7.3** Update sidebar to show worktree branch name if active

### Phase 8: Cleanup and Documentation

- [ ] **8.1** On CLI exit with active worktree: check `HasChanges()`, prompt user to keep or remove
- [ ] **8.2** Add `.opencode/worktrees/` to example `.gitignore` in documentation
- [ ] **8.3** Update AGENTS.md with `--worktree` flag documentation
- [ ] **8.4** Write integration tests for full CLI → worktree → session → agent flow

## Edge Cases

### 1. Worktree directory exists but is not a valid git worktree

1. User manually creates `.opencode/worktrees/my-task/` without using `git worktree add`
2. OpenCode detects the directory but `git worktree list` does not include it
3. **Expected**: Treat as non-worktree session. Log a warning. Do not attempt to use it as a worktree working directory.

### 2. Session exists but worktree was manually removed

1. User runs `git worktree remove .opencode/worktrees/feature-x` outside OpenCode
2. OpenCode loads session `feature-x`
3. `worktree.Detect("feature-x")` finds no directory
4. **Expected**: Session operates normally in the main repo directory. No error. The worktree binding is opportunistic, not mandatory.

### 3. Two OpenCode instances try to create the same worktree name

1. Instance A and B both run `opencode --worktree shared-task`
2. Both call `worktree.Create("shared-task")`
3. **Expected**: First one succeeds, second detects existing worktree and reuses it. `git worktree add` will fail if the worktree already exists — detect and reuse on that error.

### 4. Subagent worktree with uncommitted changes from a crashed parent

1. Parent agent crashes while a workhorse subagent is running in a worktree
2. The worktree has uncommitted changes
3. **Expected**: Worktree persists on disk. On next startup, `worktree.List()` shows it. User can resume the session (same ID as worktree name) or manually clean up. No data loss.

### 5. Non-git repository with `--worktree` flag

1. User runs `opencode --worktree test` in a non-git directory
2. **Expected**: Error message: "Worktrees require a git repository. Initialize a git repo first or run without --worktree." Exit with non-zero code.

### 6. Worktree name collision with existing branch

1. User runs `opencode --worktree deploy` but branch `worktree-deploy` already exists (from a previous manual operation)
2. `git worktree add -b worktree-deploy` fails
3. **Expected**: Check if the existing branch is associated with a worktree. If so, reuse. If not (orphan branch), warn user and suggest a different name or `--worktree deploy-2`.

### 7. Session switch from worktree to non-worktree session

1. User is in session `feature-auth` (worktree active)
2. User switches to session `main-session` (no worktree)
3. **Expected**: Working directory context reverts to the main repo directory. All tools operate on the main repo. No cleanup of the worktree — it persists until explicitly removed.

### 8. Nested worktree creation (subagent in a worktree spawns another subagent with isolation)

1. Agent A runs in worktree-A
2. Agent A spawns subagent B with `isolation: worktree`
3. **Expected**: Subagent B gets its own worktree (worktree-B), branched from HEAD of the main repo. This inherits existing work from HEAD. Worktrees are always siblings under `.opencode/worktrees/`, never nested.

## Open Questions

1. **~~Should `getContextFromPaths()` be loaded per-worktree or always from the main repo?~~** *Resolved.*
   - Replace `sync.Once` with `sync.Map` keyed by cwd. Fresh worktrees pass the main repo cwd (cache hit, no extra I/O). On process restart with existing worktree, the worktree cwd is used as key (cache miss → reads from worktree dir, which may have diverged). This is consistent with the current behavior where context files are never hot-reloaded within a process — only re-read on restart.

2. **Should worktree creation fetch from remote before branching?**
   - Claude Code branches from the default remote branch. The anomaly impl fetches before reset.
   - Fetching adds latency but ensures the worktree starts from latest.
   - **Recommendation**: Do a `git fetch` of the default remote branch before creating the worktree. If fetch fails (offline), branch from local HEAD and warn.

3. **~~How should the `--worktree` flag interact with `--session` flag?~~** *Resolved.*
   - `--worktree <name>` implies `--session <name>`. They are mutually exclusive — if both are provided with different values, exit with an error. Same value is fine. Session IDs are already arbitrary kebab-case strings (flows use `{prefix}-{flowID}-{stepID}`), so worktree names like `feature-auth` work directly as session IDs with no format issues, as long as they contain only filesystem-safe characters.

4. **Should worktree cleanup prompt happen in the TUI or only in non-interactive mode?**
   - TUI sessions may be long-lived; prompting on every session switch is noisy.
   - **Recommendation**: Only prompt on CLI exit or explicit `/worktree cleanup` command. In TUI, worktrees persist silently. Add a `/worktree list` and `/worktree remove <name>` command for manual management.

5. **Should the `ls` tool in the system prompt show the worktree directory or the main repo?**
   - The worktree initially mirrors the repo, so `ls` output would be identical.
   - **Recommendation**: Show the worktree directory. It's the actual working directory. If the agent creates files, they'll appear there.

6. **How to handle `shell/shell.go` `PersistentShell` which keys on working directory?**
   - The persistent shell tracks its `cwd` independently. When a worktree is active, the bash tool's default `workdir` should be the worktree path.
   - **Recommendation**: `ResolveWorkingDir(ctx)` will naturally handle this since `bash.go` already uses `config.WorkingDirectory()` as the default `workdir`. Replacing it with `ResolveWorkingDir(ctx)` is sufficient.

7. **Should worktree sessions use the same `ProjectID` as the main repo?**
   - If they use a different `ProjectID`, they won't appear in the same session list.
   - **Recommendation**: Same `ProjectID`. Worktrees are isolated working directories, not separate projects. Sessions should all appear in the same list.

## Success Criteria

- [ ] `opencode --worktree <name>` creates a git worktree at `.opencode/worktrees/<name>` and starts a session with ID `<name>`
- [ ] `opencode --worktree` (no name) auto-generates a random name and starts
- [ ] Resuming a session whose ID matches an existing worktree automatically uses that worktree as working directory
- [ ] All tools (read, edit, write, bash, glob, grep, etc.) resolve paths against the worktree directory when one is active
- [ ] System prompt reflects the worktree path and includes isolation instructions
- [ ] `isolation: worktree` in agent config/frontmatter causes subagents to spawn in dedicated worktrees
- [ ] Task tool accepts optional `isolation` parameter to request worktree isolation per-invocation
- [ ] Subagent worktrees with no git-tracked changes are auto-removed on completion
- [ ] TUI sidebar shows `[worktree]` muted badge for worktree-bound sessions
- [ ] TUI session dialog shows `[worktree]` indicator next to worktree sessions
- [ ] `--worktree` in a non-git repo produces a clear error
- [ ] Manually created worktrees (matching naming convention) are detected and used
- [ ] No breaking changes to existing session management — sessions without worktrees work identically

## References

- `cmd/root.go` — CLI flags and startup flow
- `internal/config/config.go` — `WorkingDirectory()`, `Agent` struct, config loading
- `internal/session/session.go` — `Session` struct, `Service` interface
- `internal/agent/registry.go` — `AgentInfo` struct, registry discovery
- `internal/llm/agent/agent.go` — Agent loop, `createAgentProvider()`
- `internal/llm/agent/agent-tool.go` — `TaskParams`, subagent spawning
- `internal/llm/agent/factory.go` — `AgentFactory` interface
- `internal/llm/prompt/prompt.go` — `getEnvironmentInfo()`, `GetAgentPrompt()`
- `internal/llm/tools/tools.go` — Context keys, `BaseTool` interface
- `internal/llm/tools/bash.go` — Working dir resolution in bash tool
- `internal/llm/tools/shell/shell.go` — `PersistentShell` cwd tracking
- `internal/tui/components/chat/sidebar.go` — Session provider hint pattern
- `internal/tui/components/dialog/session.go` — Session selection dialog
- `internal/history/file.go` — File tracking per session tree
- https://code.claude.com/docs/en/common-workflows#run-parallel-claude-code-sessions-with-git-worktrees — Claude Code worktree docs
- https://github.com/anomalyco/opencode/blob/7ec398d8/packages/opencode/src/worktree/index.ts — Reference TypeScript implementation
