# Git Worktree Isolation

**Date**: 2026-03-11
**Status**: Draft
**Author**: AI-assisted

## Overview

Add git worktree support to OpenCode, enabling parallel, isolated working directories backed by `git worktree`. Three entry points:

1. **CLI flag** (`--worktree/-w`): creates or reuses a worktree at startup, sets it as the process working directory
2. **Mid-session tools** (`EnterWorktree` / `ExitWorktree`): agent-invocable tools that switch into/out of a worktree during a conversation
3. **Subagent isolation** (`isolation: worktree`): agent config/frontmatter or per-invocation param that spawns subagents in ephemeral worktrees

All worktrees live under `<repo>/.opencode/worktrees/<name>` with branches named `worktree-<name>`.

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

Mid-session worktree entry by the agent:

```
User: "Work on the auth feature in a worktree"
Agent: [calls EnterWorktree with name="auth-feature"]
       → creates .opencode/worktrees/auth-feature/
       → switches working directory
       → all subsequent tool calls operate in the worktree
```

## Research Findings

### Claude Code Worktree Implementation (Full Source Analysis)

Claude Code's implementation spans ~2000 lines across tools, utilities, and lifecycle management. Key architectural findings:

#### Three Entry Points

| Entry Point | Trigger | `projectRoot` Changed? | Context Files Source |
|---|---|---|---|
| `--worktree` CLI flag | Startup | Yes → worktree path | Worktree dir |
| `EnterWorktreeTool` | Mid-session, model-invoked | No → stays at original repo | Original repo (cache invalidated, re-read from worktree) |
| Agent `isolation: worktree` | Subagent spawn | N/A (separate process context) | Main repo (cache hit) |

The `projectRoot` vs `originalCwd` distinction is critical: `--worktree` makes the worktree the session's identity anchor (skills, hooks, history live there). Mid-session `EnterWorktree` does NOT change `projectRoot` — the original repo stays the anchor, only tool path resolution changes.

#### Tool Implementations

**EnterWorktreeTool:**
- Input: `{ name?: string }` (optional slug, max 64 chars, `[a-zA-Z0-9._-]` per segment)
- Guards: refuses if already in a worktree (no nesting)
- Resolves canonical git root (handles being invoked from inside a worktree)
- Creates or resumes worktree via `getOrCreateWorktree()`
- Calls `process.chdir()` + cache invalidation (system prompt, memory files)
- Does NOT change `projectRoot`

**ExitWorktreeTool:**
- Input: `{ action: 'keep' | 'remove', discard_changes?: boolean }`
- Scope guard: only operates on worktrees created by `EnterWorktree` in THIS session
- For `remove` without `discard_changes`: calls `countWorktreeChanges()` (status + commit count). If changes exist, returns error with counts forcing the model to either `keep` or explicitly `discard_changes: true`
- `keep`: restores original cwd, nulls worktree state
- `remove`: `git worktree remove --force` + `git branch -D` (with 100ms sleep for lock release)

#### Core Utility Functions

**Slug validation (`validateWorktreeSlug`):**
- Max 64 chars
- Each path segment matches `[a-zA-Z0-9._-]+`
- No `.` or `..` segments (path traversal prevention)
- `/` in slugs flattened to `+` (`flattenSlug`) to prevent D/F conflicts in git refs

**Fast resume (`getOrCreateWorktree`):**
- Reads `.git` pointer file directly (~0ms) instead of spawning `git rev-parse` (~15ms)
- Falls through to full creation only if pointer file doesn't exist

**Fail-closed `hasWorktreeChanges`:**
- Returns `true` (fail-closed) if `git status --porcelain` shows changes
- Returns `true` if `git rev-list --count <originalHead>..HEAD` > 0 (new commits)
- Returns `true` on ANY git command failure — never accidentally deletes work

**Canonical git root for agent worktrees:**
- `createAgentWorktree()` uses `findCanonicalGitRoot()` (not `findGitRoot()`), so agent worktrees always land in MAIN repo's `.opencode/worktrees/`, even when spawned from inside another worktree
- Bumps mtime on resume to prevent stale-cleanup race

**Post-creation setup (`performPostCreationSetup`):**
- Copies local settings to worktree's config directory
- Configures `core.hooksPath` to point to main repo's hooks
- Symlinks directories from `worktree.symlinkDirectories` config (e.g., `node_modules`)
- Copies gitignored files matching `.worktreeinclude` patterns

**Stale cleanup (`cleanupStaleAgentWorktrees`):**
- Pattern-matched ephemeral names only (`agent-*`, `wf_*`, `bridge-*`)
- Configurable cutoff (30 days default)
- Fail-closed: skips if tracked changes or unpushed commits exist
- Runs `git worktree prune` after removal

#### Configuration

```typescript
worktree: {
  symlinkDirectories?: string[]  // e.g. ["node_modules", ".cache"]
  sparsePaths?: string[]         // git sparse-checkout cone paths (deferred)
}
```

#### Permissions

- `.claude/worktrees/` is whitelisted as a structural path in filesystem permissions
- `CLAUDE_PROJECT_DIR` env var in hooks always resolves to the stable project root, not the worktree

### Anomaly OpenCode TypeScript Implementation

The reference TypeScript implementation (`anomalyco/opencode`) stores worktrees under a global data directory (`Global.Path.data/worktree/<projectID>/<name>`) with branches prefixed `opencode/`. It has `create`, `remove`, and `reset` operations. It uses a "start command" pattern for bootstrapping worktree environments (e.g., `npm install`).

### Comparison Summary

| Aspect | Claude Code | Our Approach |
|---|---|---|
| Storage path | `.claude/worktrees/<name>` | `.opencode/worktrees/<name>` |
| Branch naming | `worktree-<flattenedSlug>` | `worktree-<flattenedSlug>` |
| Mid-session tools | `EnterWorktree` / `ExitWorktree` | Same (new addition) |
| CLI flag | `--worktree / -w` | Same |
| Subagent isolation | `isolation: worktree` in frontmatter/config | Same |
| Working dir override | `process.chdir()` + global state | Context value (`WorkingDirContextKey`) — Go-idiomatic |
| `projectRoot` distinction | Yes (CLI vs mid-session differ) | `ProjectRoot` context key (new) |
| Slug validation | Max 64, `[a-zA-Z0-9._-]`, `/`→`+` | Same |
| Fast resume | Read `.git` pointer file | Same |
| Fail-closed changes | `true` on any git error | Same |
| Post-creation setup | hooks, symlinks, `.worktreeinclude` | hooks + symlinks (`.worktreeinclude` deferred) |
| Stale cleanup | Pattern-matched, 30d cutoff | Same |
| Sparse checkout | Supported | Deferred to follow-up |
| PR branch support | `git fetch origin pull/<N>/head` | Deferred to follow-up |

### Current Codebase Architecture

| Component | Current Behavior | Worktree Impact |
|---|---|---|
| `config.WorkingDirectory()` | Global singleton, set once | Must support per-context override |
| `Session` struct | No `WorkingDir` field | No DB change needed — worktree path derived from filesystem detection |
| `AgentInfo` struct | No `Isolation` field | Needs `Isolation` field |
| `TaskParams` struct | No `Isolation` field | Needs optional `Isolation` field |
| System prompt (`getEnvironmentInfo`) | Reads global `WorkingDir` | Must accept override path |
| Context files (`getContextFromPaths`) | Cached globally via `sync.Once` | `sync.Once` → `sync.Map` keyed by cwd; invalidation on enter/exit |
| Tools path resolution | `config.WorkingDirectory()` | Must read from context, not global |
| TUI sidebar | Shows project + session provider | Needs worktree indicator |
| TUI session dialog | Shows `sess.Title` only | Needs worktree badge |

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Working dir override mechanism | Context value (`WorkingDirContextKey`) checked before global fallback | Non-breaking: tools fall back to global when no context value is set. Go-idiomatic (vs Claude's `process.chdir()`). Avoids refactoring all tool constructors. |
| Project root tracking | Separate `ProjectRootContextKey` in addition to `WorkingDirContextKey` | `--worktree` sets both to worktree path. Mid-session `EnterWorktree` sets only `WorkingDirContextKey`. Context files and skills always resolve from project root. Matches Claude Code's `projectRoot` vs `originalCwd` distinction. |
| Session-to-worktree binding | Filesystem detection: session ID = worktree name | No DB schema changes needed. Detection is `os.Stat(<repo>/.opencode/worktrees/<sessionID>)` + `.git` pointer validation. If worktree is removed from disk, session silently reverts to main repo — no stale data. |
| Worktree storage path | `<repo>/.opencode/worktrees/<name>` | Repo-local, discoverable, matches Claude Code pattern. Easy `.gitignore`. |
| Branch naming | `worktree-<flattenedSlug>` | Matches Claude Code convention. Slug flattened (`/`→`+`) to prevent D/F conflicts. |
| Slug validation | Max 64 chars, `[a-zA-Z0-9._-]` per segment, no `..` | Prevents path traversal, keeps filesystem and git ref safe. Ported from Claude Code. |
| Random name generation | `adjective-noun` word list | Same pattern as Claude Code. Memorable, low collision rate. |
| Isolation field type | `string` enum, single value `"worktree"` for now | Extensible if other isolation modes are added later (e.g., `container`). |
| Agent config placement | `Isolation` field on `AgentInfo` and `config.Agent` | Consistent with existing fields like `Mode`, `Color`, `Hidden`. |
| Task tool parameter | Optional `isolation` field in `TaskParams` | Allows callers to request isolation on a per-invocation basis, independent of agent config. |
| Fail-closed change detection | `HasChanges` returns `true` on any git error or failure to parse | Never accidentally deletes work. Matches Claude Code's fail-closed semantics. Also counts new commits via `git rev-list`. |
| Canonical git root for agent worktrees | `findCanonicalGitRoot()` resolves to main repo even when called from inside a worktree | Agent worktrees always siblings under main repo's `.opencode/worktrees/`, never nested. Prevents worktree-in-worktree filesystem issues. |
| Context file loading for worktrees | Cache keyed by cwd (`sync.Map`); invalidated on enter/exit | `getContextFromPaths` changes from `sync.Once` to `sync.Map[cwd]string`. On `EnterWorktree`/`ExitWorktree`, delete the old cwd key to force re-read. On fresh subagent worktree, pass main repo cwd → cache hit. |
| Post-creation setup | Configure `core.hooksPath`, symlink directories | Ensures git hooks work in worktree. Symlinks avoid duplicating large dirs like `node_modules`. |
| Cleanup behavior | Auto-remove on subagent exit if no changes; prompt user on CLI exit; stale cleanup on startup | Consistent with Claude Code. Subagent cleanup is silent; user-facing is interactive. Stale cleanup is pattern-matched and fail-closed. |
| Mid-session tools | `EnterWorktree` / `ExitWorktree` as agent-invocable tools | Gives the model control over worktree lifecycle during conversation. Model can enter a worktree when the user asks for isolated work, exit when done. Matches Claude Code's approach. |
| No nesting | `EnterWorktree` refuses if already in a worktree | Simplifies state management. Claude Code does the same. User must exit before entering a different worktree. |
| Worktree detection on session load | Check `<repo>/.opencode/worktrees/<sessionID>` existence at session switch time | Zero-cost when no worktree exists. Allows manual worktree creation. |

## Architecture

### Worktree Package

```
internal/worktree/
├── worktree.go        # Core create/remove/list/detect/cleanup logic
├── slug.go            # Slug validation and flattening
├── names.go           # Random adjective-noun name generation
└── worktree_test.go   # Tests
```

```
┌──────────────────────────────────────────────────────────────────┐
│ worktree.Service                                                 │
│                                                                  │
│  // Lifecycle                                                    │
│  Create(repoRoot, slug string) (Info, error)                     │
│  Remove(repoRoot, slug string) error                             │
│  GetOrCreate(repoRoot, slug string) (Info, bool, error)          │
│                                                                  │
│  // Query                                                        │
│  List(repoRoot string) ([]Info, error)                           │
│  Detect(repoRoot, sessionID string) (*Info, error)               │
│                                                                  │
│  // Change detection (fail-closed)                               │
│  HasChanges(dir string, originalHead string) (bool, error)       │
│  CountChanges(dir string, originalHead string) (*Changes, error) │
│                                                                  │
│  // Cleanup                                                      │
│  CleanupStale(repoRoot string, cutoff time.Time) error           │
│                                                                  │
│  // Post-creation setup                                          │
│  SetupHooksPath(repoRoot, worktreeDir string) error              │
│  SetupSymlinks(repoRoot, worktreeDir string, dirs []string) error│
│                                                                  │
│  // Canonical root                                               │
│  FindCanonicalGitRoot(dir string) (string, error)                │
│                                                                  │
│  // Utilities                                                    │
│  ValidateSlug(slug string) error                                 │
│  FlattenSlug(slug string) string                                 │
│  GenerateRandomName() string                                     │
│  WorktreeDir(repoRoot string) string  // .opencode/worktrees     │
│                                                                  │
│  Info{Name, Branch, Directory, OriginalHead string}              │
│  Changes{ModifiedFiles int, NewCommits int}                      │
└──────────────────────────────────────────────────────────────────┘
```

### Slug Validation and Flattening

```go
// ValidateSlug checks that a slug is safe for filesystem and git ref use.
// Rules (ported from Claude Code):
//   - Max 64 characters
//   - Split by "/" into segments
//   - Each segment matches [a-zA-Z0-9._-]+
//   - No "." or ".." segments (path traversal prevention)
//   - path.Join(base, slug) must not escape base
func ValidateSlug(slug string) error

// FlattenSlug replaces "/" with "+" to prevent:
//   - Nested directories in .opencode/worktrees/
//   - D/F (directory/file) conflicts in git refs
// Example: "feature/auth" → "feature+auth"
//          Branch: "worktree-feature+auth"
func FlattenSlug(slug string) string
```

### Working Directory Override via Context

```
┌──────────────────────────────────┐
│ tools.WorkingDirContextKey       │  ← set by EnterWorktree, agent-tool.go, or cmd/root.go
│ tools.ProjectRootContextKey      │  ← set only by --worktree CLI flag
└──────────────┬───────────────────┘
               │
               ▼
┌──────────────────────────────────┐
│ tools.ResolveWorkingDir(ctx)     │  ← new helper function
│                                  │
│  if ctx has WorkingDirKey:       │
│    return ctx value              │
│  else:                           │
│    return config.WorkDir()       │
│                                  │
│ tools.ResolveProjectRoot(ctx)    │  ← for context files, skills
│                                  │
│  if ctx has ProjectRootKey:      │
│    return ctx value              │
│  else:                           │
│    return config.WorkDir()       │
└──────────────────────────────────┘
               │
               ▼
  ┌───────────────────────┐
  │ All tool Run() methods│  ← replace config.WorkingDirectory() calls
  │ call ResolveWorkingDir│
  │                       │
  │ getContextFromPaths() │  ← uses ResolveProjectRoot (not WorkingDir)
  │ getEnvironmentInfo()  │  ← uses ResolveWorkingDir
  └───────────────────────┘
```

**Key distinction**: `getContextFromPaths` uses `ResolveProjectRoot` so that mid-session `EnterWorktree` doesn't change which context files (AGENTS.md, etc.) are loaded — they stay anchored to the original project. Only `--worktree` at startup changes the project root.

### Worktree State Tracking

Unlike Claude Code which uses module-level mutable state (`let currentWorktreeSession`), we use Go context values and a lightweight in-memory tracker:

```go
// worktree.Tracker tracks the active worktree for the current session.
// Used by EnterWorktree/ExitWorktree tools to enforce no-nesting
// and by the exit dialog to detect active worktrees.
type Tracker struct {
    mu      sync.Mutex
    active  *ActiveWorktree  // nil when not in a worktree
}

type ActiveWorktree struct {
    Info         Info
    OriginalCwd  string
    SessionID    string
}
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
  prompt.GetAgentPrompt(agentName, provider, cwd, projectRoot)
         │
    ┌────┴────────────────────┐
    │                         │
    ▼                         ▼
  getEnvironmentInfo(cwd)   getContextFromPaths(projectRoot)
  (ls, git check on cwd)   (sync.Map cache keyed by projectRoot)
```

### Context File Caching Strategy

```
┌──────────────────────────────────────────────────────────────┐
│ getContextFromPaths(projectRoot string) string                │
│                                                              │
│  var contextCache sync.Map  // replaces sync.Once            │
│                                                              │
│  if cached, ok := contextCache.Load(projectRoot); ok {       │
│    return cached.(string)                                    │
│  }                                                           │
│  content := processContextPaths(projectRoot, cfg.ContextPaths)│
│  contextCache.Store(projectRoot, content)                    │
│  return content                                              │
│                                                              │
│  InvalidateContextCache(projectRoot string)                  │
│  → contextCache.Delete(projectRoot)                          │
│  Called by EnterWorktree/ExitWorktree on transition.          │
└──────────────────────────────────────────────────────────────┘

Scenario A: --worktree at startup
─────────────────────────────────
  projectRoot = worktree path
  → cache miss → reads context files from worktree dir
  (worktree may have its own AGENTS.md)

Scenario B: Mid-session EnterWorktree
──────────────────────────────────────
  projectRoot = original repo (unchanged)
  cwd = worktree path
  → context files still read from original repo
  → tools operate in worktree dir

Scenario C: Subagent with isolation: worktree
──────────────────────────────────────────────
  Fresh worktree, passes MAIN REPO as projectRoot
  → cache hit on main repo key → reuses existing context
  (fresh worktree mirrors HEAD)
```

### CLI Flag Flow

```
opencode --worktree feature-auth
         │
         ▼
STEP 1: Parse & validate
─────────────────────────
  slug = "feature-auth"
  ValidateSlug(slug) → ok
  flatSlug = FlattenSlug(slug)

STEP 2: Verify git repo
────────────────────────
  FindCanonicalGitRoot(cwd) → repoRoot
  If not a git repo → error: "Worktrees require a git repository"

STEP 3: Detect or create worktree
──────────────────────────────────
  info, resumed, err = GetOrCreate(repoRoot, flatSlug)
  │
  ├─ Fast resume: read .git pointer file in .opencode/worktrees/<slug>
  │  If valid → return existing Info (~0ms, no subprocess)
  │
  └─ Create: git fetch origin <defaultBranch> (skip on failure)
     git worktree add -B worktree-<slug> <dir> <base>
     SetupHooksPath(repoRoot, dir)
     SetupSymlinks(repoRoot, dir, cfg.Worktree.SymlinkDirectories)

STEP 4: Bind session
─────────────────────
  sessionID = flatSlug
  session = sessions.Get(sessionID) || sessions.CreateWithID(sessionID, slug)

STEP 5: Set working directory AND project root
───────────────────────────────────────────────
  Both WorkingDirContextKey and ProjectRootContextKey → worktree path
  (This is the key difference from mid-session EnterWorktree)
  System prompt's getEnvironmentInfo() receives the worktree path.
  getContextFromPaths() reads context files from the worktree.
  Tools resolve paths against the worktree directory.

STEP 6: Launch TUI / non-interactive
─────────────────────────────────────
  Normal startup, but with working dir pointing to worktree.
```

### Mid-Session EnterWorktree Tool Flow

```
Agent calls EnterWorktree(name: "auth-feature")
         │
         ▼
STEP 1: Guard — no nesting
───────────────────────────
  tracker.Active() must be nil
  If already in a worktree → error: "Already in worktree <name>. Exit first."

STEP 2: Validate & resolve
──────────────────────────
  ValidateSlug(name)
  repoRoot = FindCanonicalGitRoot(ResolveWorkingDir(ctx))

STEP 3: Create or resume worktree
──────────────────────────────────
  info, resumed, err = GetOrCreate(repoRoot, FlattenSlug(name))
  if !resumed:
    SetupHooksPath(repoRoot, info.Directory)
    SetupSymlinks(repoRoot, info.Directory, cfg.Worktree.SymlinkDirectories)

STEP 4: Track state
────────────────────
  tracker.Enter(ActiveWorktree{
    Info:        info,
    OriginalCwd: ResolveWorkingDir(ctx),
    SessionID:   GetSessionID(ctx),
  })

STEP 5: Update context (NOT project root)
──────────────────────────────────────────
  ctx = context.WithValue(ctx, WorkingDirContextKey, info.Directory)
  // ProjectRootContextKey is NOT changed — context files stay from original repo
  InvalidateContextCache(ResolveProjectRoot(ctx))  // force re-read on next access

STEP 6: Return result
─────────────────────
  {worktreePath: info.Directory, branch: info.Branch, message: "Entered worktree"}
```

### Mid-Session ExitWorktree Tool Flow

```
Agent calls ExitWorktree(action: "keep" | "remove", discard_changes?: bool)
         │
         ▼
STEP 1: Guard — must be in a worktree
──────────────────────────────────────
  active = tracker.Active()
  If nil → error: "Not in a worktree"

STEP 2: Validate action
────────────────────────
  If action == "remove" && !discard_changes:
    changes = CountChanges(active.Info.Directory, active.Info.OriginalHead)
    If changes != nil && (changes.ModifiedFiles > 0 || changes.NewCommits > 0):
      → error: "Worktree has N modified files and M new commits.
               Use discard_changes: true to remove anyway, or action: keep."
    If changes == nil (git error):
      → error: "Cannot verify worktree state. Use keep or discard_changes: true."

STEP 3: Execute action
───────────────────────
  If action == "keep":
    // Worktree stays on disk, branch preserved
    ctx = context.WithValue(ctx, WorkingDirContextKey, active.OriginalCwd)

  If action == "remove":
    git worktree remove --force <dir>
    time.Sleep(100ms)  // lock release (ported from Claude Code)
    git branch -D worktree-<name>
    ctx = context.WithValue(ctx, WorkingDirContextKey, active.OriginalCwd)

STEP 4: Clear state & invalidate caches
────────────────────────────────────────
  tracker.Exit()
  InvalidateContextCache(ResolveProjectRoot(ctx))

STEP 5: Return result
─────────────────────
  {action, originalCwd, worktreePath, branch, message}
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

STEP 2: Create worktree (canonical root)
─────────────────────────────────────────
  repoRoot = FindCanonicalGitRoot(ResolveWorkingDir(ctx))
  slug = "agent-" + toolCallID[:7]  // ephemeral naming pattern
  info, _, err = GetOrCreate(repoRoot, slug)
  SetupHooksPath(repoRoot, info.Directory)

STEP 3: Create session with worktree context
─────────────────────────────────────────────
  session = sessions.CreateTaskSession(toolCallID, parentSessionID, title)
  ctx = context.WithValue(ctx, WorkingDirContextKey, info.Directory)
  // ProjectRootContextKey inherits from parent (main repo)

STEP 4: Create subagent with worktree-aware prompt
───────────────────────────────────────────────────
  factory.NewAgent(ctx, subagentType)
  │
  ├─ ctx carries worktree dir for tool path resolution
  ├─ getEnvironmentInfo(worktreeDir) → shows worktree as cwd
  ├─ getContextFromPaths(mainRepoRoot) → cache hit (fresh worktree)
  └─ System prompt includes worktree isolation instructions

STEP 5: Subagent completes → fail-closed cleanup
─────────────────────────────────────────────────
  if HasChanges(info.Directory, info.OriginalHead):
    keep worktree, report {worktreePath, branch} to parent
  else:
    Remove(repoRoot, slug) → auto-cleanup
  // HasChanges returns true on ANY git error → never loses work
```

### Stale Worktree Cleanup

```
On startup (background goroutine):
  CleanupStale(repoRoot, time.Now().Add(-30*24*time.Hour))
         │
         ▼
STEP 1: Scan .opencode/worktrees/
─────────────────────────────────
  List all entries, filter by ephemeral patterns:
    agent-[a-f0-9]{7}     (subagent worktrees)
  Skip entries that don't match patterns (user-created worktrees are never auto-cleaned)
  Skip current session's worktree

STEP 2: Check age
──────────────────
  stat.ModTime() < cutoff → candidate for removal

STEP 3: Fail-closed safety checks
──────────────────────────────────
  git -C <dir> status --porcelain → must be empty
  git -C <dir> rev-list HEAD --not --remotes → must be empty (no unpushed commits)
  Any git error → skip this worktree

STEP 4: Remove
───────────────
  git worktree remove --force <dir>
  git branch -D <branch>

STEP 5: Prune
─────────────
  git worktree prune
```

### Post-Creation Setup

```
SetupHooksPath(repoRoot, worktreeDir):
    // Check if main repo has custom hooks
    hooksDir = findHooksDir(repoRoot)  // .husky/, .git/hooks/, etc.
    if hooksDir != "":
      git -C worktreeDir config core.hooksPath <hooksDir>

SetupSymlinks(repoRoot, worktreeDir, dirs):
    // dirs from config: worktree.symlinkDirectories
    for _, dir := range dirs:
      src = filepath.Join(repoRoot, dir)
      dst = filepath.Join(worktreeDir, dir)
      if exists(src) && !exists(dst):
        os.Symlink(src, dst)
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

### System Prompt Additions

When in a worktree (via any entry point):

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

### EnterWorktree Tool Prompt (shown to model)

```
Create or enter an isolated git worktree for parallel development.

WHEN TO USE:
- Only when the user explicitly asks to work in a worktree
- Never use for regular branch switching or feature work
- Do not suggest worktrees proactively

INPUT:
- name (optional): Slug for the worktree (max 64 chars, [a-zA-Z0-9._-]).
  If omitted, uses the session name or generates a random name.

BEHAVIOR:
- Creates .opencode/worktrees/<name>/ with branch worktree-<name>
- All subsequent file operations happen in the worktree
- If the worktree already exists, resumes it
- Cannot nest: exit the current worktree first

AFTER ENTERING:
- Tell the user which worktree and branch you're on
- Proceed with the requested work
- When done, ask if they want to keep or remove the worktree
```

### ExitWorktree Tool Prompt (shown to model)

```
Exit the current git worktree and return to the main repository.

WHEN TO USE:
- When work in the worktree is complete
- When the user asks to leave the worktree

INPUT:
- action: "keep" or "remove"
  - keep: Leave worktree on disk for later. Branch is preserved.
  - remove: Delete worktree and its branch. All changes are lost.
- discard_changes (optional, for remove only): Set to true to confirm
  removal when uncommitted changes or new commits exist.

BEHAVIOR:
- Restores working directory to the main repository
- If removing with changes: must set discard_changes: true
- If removing fails safety check: suggests "keep" instead

AFTER EXITING:
- Confirm to the user which action was taken
- If kept: mention the branch name for later reference
```

## Configuration

### New Config Fields

```json
{
  "worktree": {
    "symlinkDirectories": ["node_modules", ".cache", "vendor"],
    "defaultBranch": "main"
  }
}
```

```go
// config.go
type WorktreeConfig struct {
    SymlinkDirectories []string `json:"symlinkDirectories,omitempty"`
    DefaultBranch      string   `json:"defaultBranch,omitempty"`
}

// Added to Config struct:
type Config struct {
    // ...existing fields...
    Worktree WorktreeConfig `json:"worktree,omitempty"`
}
```

## Implementation Plan

### Phase 1: Worktree Package (Core)

- [ ] **1.1** Create `internal/worktree/worktree.go` with `Info` struct (`Name`, `Branch`, `Directory`, `OriginalHead`) and `Changes` struct (`ModifiedFiles`, `NewCommits`)
- [ ] **1.2** Implement `ValidateSlug(slug string) error` — max 64 chars, `[a-zA-Z0-9._-]` per segment, no `..`, path traversal check
- [ ] **1.3** Implement `FlattenSlug(slug string) string` — replace `/` with `+`
- [ ] **1.4** Implement `GenerateRandomName() string` — adjective-noun word lists
- [ ] **1.5** Implement `FindCanonicalGitRoot(dir string) (string, error)` — resolves to main repo root even when called from inside a worktree (reads `.git` file → extracts `gitdir:` → resolves to main repo)
- [ ] **1.6** Implement `GetOrCreate(repoRoot, slug string) (Info, bool, error)`:
  - Fast resume path: read `.git` pointer file in `.opencode/worktrees/<slug>` directly (~0ms)
  - Create path: `git fetch origin <defaultBranch>` (skip on failure), `git worktree add -B worktree-<slug> <dir> <base>`
  - Record `OriginalHead` via `git rev-parse HEAD` in the new worktree
  - Return `(info, resumed, err)`
- [ ] **1.7** Implement `Remove(repoRoot, slug string) error` — `git worktree remove --force <dir>`, 100ms sleep, `git branch -D worktree-<slug>`
- [ ] **1.8** Implement `List(repoRoot string) ([]Info, error)` — scans `.opencode/worktrees/` directory
- [ ] **1.9** Implement `Detect(repoRoot, sessionID string) (*Info, error)` — checks `.opencode/worktrees/<sessionID>` exists and has valid `.git` pointer
- [ ] **1.10** Implement `HasChanges(dir, originalHead string) (bool, error)` — fail-closed: returns `true` on any git error. Checks `git status --porcelain` AND `git rev-list --count <originalHead>..HEAD`
- [ ] **1.11** Implement `CountChanges(dir, originalHead string) (*Changes, error)` — returns nil on git error (fail-closed at call site). Parses `git status --porcelain` line count and `git rev-list --count` output
- [ ] **1.12** Implement `SetupHooksPath(repoRoot, worktreeDir string) error` — detect hooks dir, set `core.hooksPath`
- [ ] **1.13** Implement `SetupSymlinks(repoRoot, worktreeDir string, dirs []string) error` — symlink configured directories
- [ ] **1.14** Implement `CleanupStale(repoRoot string, cutoff time.Time) error` — pattern-matched ephemeral names, fail-closed safety checks, `git worktree prune`
- [ ] **1.15** Implement `Tracker` struct for mid-session worktree state (Enter/Exit/Active methods, mutex-protected)
- [ ] **1.16** Write unit tests

### Phase 2: Working Directory Context Override

- [ ] **2.1** Add `WorkingDirContextKey` and `ProjectRootContextKey` to `internal/llm/tools/tools.go`
- [ ] **2.2** Add `ResolveWorkingDir(ctx context.Context) string` — checks `WorkingDirContextKey` first, falls back to `config.WorkingDirectory()`
- [ ] **2.3** Add `ResolveProjectRoot(ctx context.Context) string` — checks `ProjectRootContextKey` first, falls back to `config.WorkingDirectory()`
- [ ] **2.4** Replace all `config.WorkingDirectory()` calls in tool `Run()` methods with `ResolveWorkingDir(ctx)` — affects `bash.go`, `read.go`, `edit.go`, `write.go`, `delete.go`, `multiedit.go`, `patch.go`, `glob.go`, `grep.go`, `lsp.go`, `view_image.go`, `ls.go`, `webfetch.go`, `websearch.go`
- [ ] **2.5** Update tool description interpolation in `bash.go` to use a dynamic reference or accept the working dir is baked in at tool creation time (acceptable since tool descriptions are informational)
- [ ] **2.6** Write tests verifying `ResolveWorkingDir` and `ResolveProjectRoot` behavior with and without context values

### Phase 3: Agent and Config Changes

- [ ] **3.1** Add `WorktreeConfig` struct and `Worktree` field to `config.Config`
- [ ] **3.2** Add `Isolation string` field to `config.Agent` struct (JSON: `"isolation"`)
- [ ] **3.3** Add `Isolation string` field to `agent.AgentInfo` struct
- [ ] **3.4** Wire `Isolation` through agent registry discovery (config + markdown frontmatter parsing)
- [ ] **3.5** Parameterize `getEnvironmentInfo(cwd string)` in `prompt/prompt.go` to accept a working directory parameter
- [ ] **3.6** Update `GetAgentPrompt()` to accept `cwd string` and `projectRoot string` parameters; forward `cwd` to `getEnvironmentInfo()` and `projectRoot` to `getContextFromPaths()`
- [ ] **3.7** Replace `sync.Once` / `onceContext` in `getContextFromPaths` with `sync.Map` keyed by `projectRoot` string; add `InvalidateContextCache(projectRoot string)` function
- [ ] **3.8** Add worktree-specific system prompt section when agent is running in a worktree (isolation hint, branch info, cleanup instructions)
- [ ] **3.9** Update `createAgentProvider(agentName, cwd, projectRoot string)` in `agent/agent.go` to accept both paths and pass to `GetAgentPrompt`
- [ ] **3.10** Update `newAgent()` to extract cwd and projectRoot from context and pass to `createAgentProvider`
- [ ] **3.11** Update `AgentFactory.NewAgent()` to propagate both paths from context through the agent creation chain

### Phase 4: CLI Integration

- [ ] **4.1** Add `--worktree / -w` flag to `cmd/root.go` (optional string, can be empty for auto-generated name)
- [ ] **4.2** Validate `--worktree` + `--session` interaction: mutually exclusive with different values, same value is ok, `--worktree` implies `--session`
- [ ] **4.3** In `root.go` run handler: if `--worktree` is set, validate git repo, call `GetOrCreate()`, run post-creation setup, set `app.InitialSessionID` to worktree name
- [ ] **4.4** Set BOTH `WorkingDirContextKey` and `ProjectRootContextKey` to worktree path (this is the key difference from mid-session)
- [ ] **4.5** Store worktree directory path on `App` struct so it can be propagated to agent context
- [ ] **4.6** Ensure `.opencode/worktrees/` is in `.gitignore` guidance

### Phase 5: EnterWorktree / ExitWorktree Tools

- [ ] **5.1** Create `internal/llm/tools/enter_worktree.go`:
  - Input schema: `{ name?: string }`
  - Guard: `tracker.Active()` must be nil
  - Resolve canonical git root
  - Call `GetOrCreate()` + setup
  - Set `WorkingDirContextKey` on context (NOT `ProjectRootContextKey`)
  - Invalidate context cache
  - Return `{worktreePath, branch, message}`
- [ ] **5.2** Create `internal/llm/tools/exit_worktree.go`:
  - Input schema: `{ action: "keep"|"remove", discard_changes?: bool }`
  - Guard: `tracker.Active()` must be non-nil
  - For `remove`: validate via `CountChanges()`, require `discard_changes: true` if changes exist
  - Execute keep/remove
  - Restore `WorkingDirContextKey` to original cwd
  - Invalidate context cache
  - Return `{action, originalCwd, worktreePath, branch, message}`
- [ ] **5.3** Write tool prompts (LLM-facing descriptions)
- [ ] **5.4** Register tools in tool registry (conditionally, behind feature check if needed)
- [ ] **5.5** Add `worktree.Tracker` as a dependency to both tools
- [ ] **5.6** Write tests

### Phase 6: Task Tool Isolation Support

- [ ] **6.1** Add optional `Isolation string` field to `TaskParams` in `agent-tool.go`
- [ ] **6.2** Update task tool description to document the `isolation` parameter
- [ ] **6.3** In `agentTool.Run()`: resolve isolation from params or agent config, if `"worktree"`:
  - `FindCanonicalGitRoot()` to get main repo root (even if parent is in a worktree)
  - Create ephemeral worktree with slug `"agent-" + toolCallID[:7]`
  - Set `WorkingDirContextKey` on context (ProjectRoot inherits from parent)
  - Run post-creation setup
- [ ] **6.4** After subagent completes: fail-closed `HasChanges()` check, auto-remove if clean
- [ ] **6.5** Pass worktree info back in `TaskResponseMetadata` (add `WorktreePath`, `WorktreeBranch`, `WorktreeKept` fields)
- [ ] **6.6** Update `NewAgentTool` to accept worktree service dependency

### Phase 7: Session-Worktree Detection

- [ ] **7.1** On session switch (TUI or CLI `--session`): call `Detect(sessionID)` to check if a matching worktree exists
- [ ] **7.2** If worktree detected: set working directory context for the session's agent
- [ ] **7.3** On session list load: batch-detect worktrees for all sessions (for TUI display)

### Phase 8: TUI Integration

- [ ] **8.1** Add `[worktree]` muted badge to session section in `sidebar.go` (follow session provider hint pattern)
- [ ] **8.2** Add `[worktree]` muted badge to session items in `dialog/session.go`
- [ ] **8.3** Update sidebar to show worktree branch name if active

### Phase 9: Cleanup, Stale Management, and Documentation

- [ ] **9.1** On CLI exit with active worktree: check `HasChanges()`, prompt user to keep or remove
- [ ] **9.2** Background goroutine on startup: `CleanupStale()` for ephemeral subagent worktrees older than 30 days
- [ ] **9.3** Add `.opencode/worktrees/` to example `.gitignore` in documentation
- [ ] **9.4** Update AGENTS.md with `--worktree` flag and `EnterWorktree`/`ExitWorktree` tool documentation
- [ ] **9.5** Write integration tests for full CLI → worktree → session → agent flow

## Edge Cases

### 1. Worktree directory exists but is not a valid git worktree

1. User manually creates `.opencode/worktrees/my-task/` without using `git worktree add`
2. OpenCode detects the directory but `.git` pointer file is missing or invalid
3. **Expected**: Treat as non-worktree session. Log a warning. Do not attempt to use it as a worktree working directory.

### 2. Session exists but worktree was manually removed

1. User runs `git worktree remove .opencode/worktrees/feature-x` outside OpenCode
2. OpenCode loads session `feature-x`
3. `Detect("feature-x")` finds no directory
4. **Expected**: Session operates normally in the main repo directory. No error. The worktree binding is opportunistic, not mandatory.

### 3. Two OpenCode instances try to create the same worktree name

1. Instance A and B both run `opencode --worktree shared-task`
2. Both call `GetOrCreate("shared-task")`
3. **Expected**: First one succeeds, second detects existing worktree via fast resume path and reuses it. `git worktree add` will fail if the worktree already exists — detect and reuse on that error.

### 4. Subagent worktree with uncommitted changes from a crashed parent

1. Parent agent crashes while a workhorse subagent is running in a worktree
2. The worktree has uncommitted changes
3. **Expected**: Worktree persists on disk. On next startup, `List()` shows it. Stale cleanup skips it (has changes → fail-closed). User can resume the session or manually clean up. No data loss.

### 5. Non-git repository with `--worktree` flag

1. User runs `opencode --worktree test` in a non-git directory
2. **Expected**: Error message: "Worktrees require a git repository. Initialize a git repo first or run without --worktree." Exit with non-zero code.

### 6. Worktree name collision with existing branch

1. User runs `opencode --worktree deploy` but branch `worktree-deploy` already exists
2. **Expected**: Use `git worktree add -B` (force-create branch) which resets the branch to the base. If the branch is checked out in another worktree, git will error — detect this and suggest a different name.

### 7. Session switch from worktree to non-worktree session

1. User is in session `feature-auth` (worktree active)
2. User switches to session `main-session` (no worktree)
3. **Expected**: Working directory context reverts to the main repo directory. All tools operate on the main repo. No cleanup of the worktree — it persists until explicitly removed.

### 8. Nested worktree attempt (mid-session)

1. Agent is in worktree-A (entered via `EnterWorktree`)
2. Agent calls `EnterWorktree` again
3. **Expected**: Tool returns error: "Already in worktree 'A'. Call ExitWorktree first." No nesting allowed.

### 9. Nested worktree creation (subagent in a worktree spawns another subagent with isolation)

1. Agent A runs in worktree-A
2. Agent A spawns subagent B with `isolation: worktree`
3. **Expected**: `FindCanonicalGitRoot()` resolves to main repo root. Subagent B gets its own worktree (worktree-B) under main repo's `.opencode/worktrees/`, never nested. Both are siblings.

### 10. ExitWorktree with uncommitted changes

1. Agent calls `ExitWorktree(action: "remove")`
2. Worktree has uncommitted changes
3. **Expected**: Tool returns error with change counts. Agent must either use `action: "keep"` or `action: "remove", discard_changes: true`.

### 11. ExitWorktree when git is broken

1. Agent calls `ExitWorktree(action: "remove")`
2. `CountChanges()` fails (git error)
3. **Expected**: Returns nil (fail-closed). Tool refuses to remove, suggests `keep` or explicit `discard_changes: true`.

### 12. Slug with slashes

1. User runs `opencode --worktree feature/auth/login`
2. **Expected**: Slug validated → passes. Flattened to `feature+auth+login`. Directory: `.opencode/worktrees/feature+auth+login/`. Branch: `worktree-feature+auth+login`.

## Open Questions

1. **~~Should `getContextFromPaths()` be loaded per-worktree or always from the main repo?~~** *Resolved.*
   - Depends on entry point. `--worktree` at startup: reads from worktree (worktree IS the project root). Mid-session `EnterWorktree`: reads from original repo (project root unchanged). Subagent isolation: reads from main repo (cache hit). This matches Claude Code's `projectRoot` vs `originalCwd` distinction.

2. **~~Should worktree creation fetch from remote before branching?~~** *Resolved.*
   - Yes. `git fetch origin <defaultBranch>` before `git worktree add`. If fetch fails (offline), branch from local HEAD and warn. Claude Code does the same, skipping fetch if `origin/<branch>` already exists locally.

3. **~~How should the `--worktree` flag interact with `--session` flag?~~** *Resolved.*
   - `--worktree <name>` implies `--session <name>`. They are mutually exclusive with different values. Same value is fine.

4. **~~Should worktree cleanup prompt happen in the TUI or only in non-interactive mode?~~** *Resolved.*
   - Prompt on CLI exit if active worktree has changes. In TUI, worktrees persist silently. Stale cleanup runs in background on startup for ephemeral subagent worktrees only.

5. **~~Should the `ls` tool in the system prompt show the worktree directory or the main repo?~~** *Resolved.*
   - Show the worktree directory. It's the actual working directory.

6. **~~How to handle `shell/shell.go` `PersistentShell` which keys on working directory?~~** *Resolved.*
   - `ResolveWorkingDir(ctx)` naturally handles this.

7. **~~Should worktree sessions use the same `ProjectID` as the main repo?~~** *Resolved.*
   - Yes. Same `ProjectID`. All sessions appear in the same list.

8. **Should we support `.worktreeinclude` for copying gitignored files?**
   - Claude Code has this feature. It reads a `.worktreeinclude` file (gitignore syntax) and copies matching gitignored files to the worktree.
   - **Recommendation**: Defer to follow-up. The `symlinkDirectories` config handles the most common case (node_modules, vendor). `.worktreeinclude` adds complexity (gitignore parsing, large directory enumeration optimization).

9. **Should we support sparse checkout for large repos?**
   - Claude Code supports `worktree.sparsePaths` for `git sparse-checkout set --cone`.
   - **Recommendation**: Defer to follow-up. Most repos are small enough that full checkout is fine.

10. **Should we support PR branch base (`--pr 123`)?**
    - Claude Code supports `git fetch origin pull/<N>/head` to base worktrees on PR branches.
    - **Recommendation**: Defer to follow-up. Can be added as an optional parameter to `EnterWorktree` and `--worktree` flag later.

## Deferred Features (Follow-up)

These features are present in Claude Code but deferred to keep the initial implementation focused:

- **`.worktreeinclude`**: Copying gitignored files matching patterns to worktree
- **Sparse checkout**: `worktree.sparsePaths` config for `git sparse-checkout set --cone`
- **PR branch support**: `--pr 123` or `EnterWorktree(pr: 123)` to base worktree on a PR
- **tmux integration**: `--worktree --tmux` to launch worktree in a tmux session
- **Commit attribution hook**: Auto-installing `prepare-commit-msg` hook in worktrees

## Success Criteria

- [ ] `opencode --worktree <name>` creates a git worktree at `.opencode/worktrees/<name>` and starts a session with ID `<name>`
- [ ] `opencode --worktree` (no name) auto-generates a random name and starts
- [ ] Resuming a session whose ID matches an existing worktree automatically uses that worktree as working directory
- [ ] All tools (read, edit, write, bash, glob, grep, etc.) resolve paths against the worktree directory when one is active
- [ ] System prompt reflects the worktree path and includes isolation instructions
- [ ] `EnterWorktree` tool creates/resumes a worktree mid-session; `ExitWorktree` tool keeps or removes it
- [ ] Mid-session `EnterWorktree` does NOT change project root (context files stay from original repo)
- [ ] `EnterWorktree` refuses to nest (must exit first)
- [ ] `ExitWorktree` with `action: remove` requires `discard_changes: true` when changes exist (fail-closed)
- [ ] `isolation: worktree` in agent config/frontmatter causes subagents to spawn in dedicated worktrees
- [ ] Task tool accepts optional `isolation` parameter to request worktree isolation per-invocation
- [ ] Subagent worktrees with no git-tracked changes are auto-removed on completion (fail-closed: keep on error)
- [ ] Subagent worktrees always created under main repo's `.opencode/worktrees/` via canonical root resolution
- [ ] Stale ephemeral worktrees are cleaned up on startup (pattern-matched, 30d cutoff, fail-closed)
- [ ] Slug validation prevents path traversal and enforces safe characters
- [ ] Post-creation setup configures hooks path and symlinks
- [ ] Fast resume path reads `.git` pointer file directly (no subprocess)
- [ ] TUI sidebar shows `[worktree]` muted badge for worktree-bound sessions
- [ ] TUI session dialog shows `[worktree]` indicator next to worktree sessions
- [ ] `--worktree` in a non-git repo produces a clear error
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
- Claude Code source: `src/tools/EnterWorktreeTool/`, `src/tools/ExitWorktreeTool/`, `src/utils/worktree.ts`
- Claude Code source: `src/tools/AgentTool/AgentTool.tsx` (subagent isolation)
- Claude Code source: `src/setup.ts` (CLI `--worktree` handling)
- Claude Code source: `src/utils/sessionRestore.ts` (session resume with worktree)
