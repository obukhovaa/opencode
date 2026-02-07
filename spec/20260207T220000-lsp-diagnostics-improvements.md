# LSP & Diagnostics Improvements

**Date**: 2026-02-07
**Status**: Implemented
**Author**: AI-assisted

## Overview

Overhaul the LSP integration layer: add an `lsp` tool exposing code-intelligence operations to the LLM, fix bugs and race conditions in the existing diagnostics/client code, expand the language extension map, introduce auto-install for common LSP servers, and align the config schema with the reference implementation.

## Motivation

### Current State

The diagnostics tool only returns errors/warnings. The LSP client already implements `Definition`, `References`, `Hover`, `DocumentSymbol`, `Implementation`, `PrepareCallHierarchy`, `IncomingCalls`, `OutgoingCalls` in `methods.go` — but none are exposed as agent tools.

```go
// diagnostics.go — the ONLY LSP tool today
type DiagnosticsParams struct {
    FilePath string `json:"file_path"`
}
```

The extension→language map is incomplete (missing ~30 extensions). Server↔file routing is hardcoded in two places with duplicated switch statements. The config struct has a bug where `Disabled` maps to JSON `"enabled"`.

```go
// config.go — inverted semantics bug
type LSPConfig struct {
    Disabled bool     `json:"enabled"`  // ← wrong JSON tag
    Command  string   `json:"command"`
    Args     []string `json:"args"`
    Options  any      `json:"options"`
}
```

LSP servers must be manually installed and configured. There is no auto-install or auto-detection.

This creates problems:

1. **No code navigation for the LLM**: The agent can't go-to-definition, find references, or inspect types — operations that dramatically improve code understanding and reduce hallucination
2. **Incomplete language support**: Files like `.vue`, `.svelte`, `.astro`, `.zig`, `.nix`, `.kt`, `.mjs`, `.cjs`, `.mts`, `.cts` etc. aren't recognized
3. **Fragile server routing**: Hardcoded switch statements in `notifyLspOpenFile()` and `shouldOpenFile()` break for any server not in the list
4. **Race condition**: `GetDiagnostics()` returns the internal map without lock protection while writers hold `diagnosticsMu`
5. **Config bug**: `Disabled bool` with tag `json:"enabled"` means JSON `{"enabled": true}` sets `Disabled = true`
6. **Manual setup burden**: Users must find, install, and configure LSP servers themselves
7. **Wrong client identity**: Server reports itself as `"mcp-language-server"` v0.1.0
8. **Hardcoded init options**: Gopls-specific `codelenses` sent to every server

### Desired State

```json
{
  "lsp": {
    "gopls": {
      "extensions": [".go"],
      "initialization": { "codelenses": { "test": true } }
    },
    "custom-server": {
      "command": ["my-lsp", "--stdio"],
      "extensions": [".custom"],
      "env": { "DEBUG": "1" }
    },
    "typescript": { "disabled": true }
  },
  "disableLSPDownload": false
}
```

The LLM can call:
```
lsp(operation="goToDefinition", filePath="main.go", line=42, character=10)
lsp(operation="findReferences", filePath="handler.go", line=15, character=5)
```

Common LSP servers auto-install when their file types are detected in the project.

## Research Findings

### Reference Implementation (anomalyco/opencode TypeScript)

The reference has a full `lsp` tool with 9 operations and ~40 built-in server definitions with auto-install.

| Feature | Our Go implementation | Reference TS implementation |
|---|---|---|
| Diagnostics tool | ✅ | ✅ (integrated into file tools) |
| LSP tool (navigation) | ❌ | ✅ (9 operations) |
| Language extensions | ~45 | ~75 |
| Auto-install servers | ❌ | ✅ (~22 servers) |
| Config: `disabled` flag | Buggy (`"enabled"` tag) | ✅ |
| Config: `extensions` | ❌ | ✅ |
| Config: `env` | ❌ | ✅ |
| Config: `initialization` | ❌ | ✅ |
| Global disable LSP download | ❌ | ✅ (`OPENCODE_DISABLE_LSP_DOWNLOAD`) |
| Server↔file routing | Hardcoded switches | Extension-based from config |
| Race-safe diagnostics | ❌ | ✅ |

**Key finding**: The reference uses three auto-install strategies:
1. **npm/bun install** — for JS ecosystem servers (vue, svelte, astro, eslint, yaml-ls, bash-ls, typescript, php intelephense)
2. **Language toolchain** — `go install` for gopls, `dotnet tool install` for C#/F#, `gem install` for Ruby
3. **GitHub releases** — download binaries for clangd, zls, jdtls, kotlin-ls, lua-ls, terraform-ls, tinymist

**Implication**: We should implement equivalent auto-install in Go, using `exec.Command` for package managers and `net/http` for GitHub releases. Binaries go into a centralized directory (e.g., `~/.opencode/bin/`).

### Reference LSP Tool Operations

| Operation | LSP Method | Use Case |
|---|---|---|
| `goToDefinition` | `textDocument/definition` | Find where a symbol is defined |
| `findReferences` | `textDocument/references` | Find all usages of a symbol |
| `hover` | `textDocument/hover` | Get type info / documentation |
| `documentSymbol` | `textDocument/documentSymbol` | List all symbols in a file |
| `workspaceSymbol` | `workspace/symbol` | Search symbols across workspace |
| `goToImplementation` | `textDocument/implementation` | Find interface implementations |
| `prepareCallHierarchy` | `textDocument/prepareCallHierarchy` | Get call hierarchy item |
| `incomingCalls` | `callHierarchy/incomingCalls` | Find callers |
| `outgoingCalls` | `callHierarchy/outgoingCalls` | Find callees |

All methods already exist in `internal/lsp/methods.go`. They just need a tool wrapper.

### Auto-Install Server Registry

Servers to support, grouped by install strategy:

**Go toolchain install:**
| Server | Extensions | Install command |
|---|---|---|
| gopls | `.go` | `go install golang.org/x/tools/gopls@latest` |

**npm install (via `npm install --prefix <bindir>`):**
| Server | Extensions | Package |
|---|---|---|
| typescript-language-server | `.ts`, `.tsx`, `.js`, `.jsx`, `.mjs`, `.cjs`, `.mts`, `.cts` | `typescript-language-server typescript` |
| bash-language-server | `.sh`, `.bash`, `.zsh`, `.ksh` | `bash-language-server` |
| vscode-langservers-extracted (eslint) | `.ts`, `.tsx`, `.js`, `.jsx`, `.vue`, `.svelte` | `vscode-langservers-extracted` |
| yaml-language-server | `.yaml`, `.yml` | `yaml-language-server` |
| @astrojs/language-server | `.astro` | `@astrojs/language-server` |
| svelte-language-server | `.svelte` | `svelte-language-server` |
| @vue/language-server | `.vue` | `@vue/language-server` |
| intelephense | `.php` | `intelephense` |
| pyright | `.py` | `pyright` |

**GitHub release download:**
| Server | Extensions | Repository |
|---|---|---|
| lua-language-server | `.lua` | LuaLS/lua-language-server |
| terraform-ls | `.tf`, `.tfvars` | hashicorp/terraform-ls |
| tinymist | `.typ`, `.typc` | Myriad-Dreamin/tinymist |

**System PATH lookup only (no auto-install):**
| Server | Extensions | Binary |
|---|---|---|
| rust-analyzer | `.rs` | `rust-analyzer` |
| clangd | `.c`, `.cpp`, `.cc`, `.cxx`, `.h`, `.hpp` | `clangd` |
| dart | `.dart` | `dart language-server` |
| zls | `.zig`, `.zon` | `zls` |
| ocamllsp | `.ml`, `.mli` | `ocamllsp` |
| nixd | `.nix` | `nixd` |
| haskell-language-server | `.hs`, `.lhs` | `haskell-language-server-wrapper` |
| gleam | `.gleam` | `gleam lsp` |
| sourcekit-lsp | `.swift` | `sourcekit-lsp` |
| kotlin-language-server | `.kt`, `.kts` | `kotlin-language-server` |
| clojure-lsp | `.clj`, `.cljs`, `.cljc`, `.edn` | `clojure-lsp` |
| elixir-ls | `.ex`, `.exs` | `elixir-ls` |
| jdtls | `.java` | `jdtls` |
| ruby-lsp | `.rb`, `.rake`, `.gemspec`, `.ru` | `ruby-lsp` |
| csharp-ls | `.cs` | `csharp-ls` |
| fsautocomplete | `.fs`, `.fsi`, `.fsx`, `.fsscript` | `fsautocomplete` |
| prisma-language-server | `.prisma` | `prisma` |

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| LSP tool parameter style | 1-based line/char (matching editor display) | Matches user mental model; internally convert to 0-based for LSP protocol |
| Auto-install binary location | `~/.opencode/bin/` | Centralized, doesn't pollute project dirs, survives project deletion |
| npm install strategy | `npm install --prefix <bindir>` | Works without bun dependency; falls back to `npx` if npm unavailable |
| Config: global disable download | `disableLSPDownload` config field + `OPENCODE_DISABLE_LSP_DOWNLOAD` env var | Matches reference; env var is simpler for CI/enterprise |
| Extension-based routing | Replace hardcoded switches with extension matching from server registry | Eliminates duplication, supports custom servers automatically |
| Built-in server registry | Go map in `internal/lsp/servers/registry.go` | Easy to extend, can be overridden by user config |
| Init options | Per-server from config, not hardcoded | Current gopls codelenses config breaks non-Go servers |
| Fix `LSPConfig` JSON tag | Change `json:"enabled"` to `json:"disabled"` | Fixes inverted semantics bug |
| `GetDiagnostics()` race fix | Return a copy under lock | Simple, correct |
| Client identity | `"opencode"` + version from `version.go` | Accurate identification |

## Architecture

### LSP Tool Flow

```
┌─────────────────────────────────────────────────┐
│ LLM Agent                                       │
│  calls: lsp(op="goToDefinition", file, line, col) │
└──────────────────────┬──────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────┐
│ internal/llm/tools/lsp.go                       │
│  - Validates params                             │
│  - Resolves file path                           │
│  - Finds matching LSP client by file extension  │
│  - Ensures file is open (touchFile)             │
│  - Dispatches to LSP client method              │
│  - Formats result for LLM consumption           │
└──────────────────────┬──────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────┐
│ internal/lsp/client.go                          │
│  - Definition(), References(), Hover(), etc.    │
│  - Already implemented in methods.go            │
└─────────────────────────────────────────────────┘
```

### Auto-Install Flow

```
STEP 1: Project scan (on startup)
──────────────────────────────────
Walk project directory, collect unique file extensions.
Match extensions against built-in server registry.

STEP 2: Server resolution
─────────────────────────
For each matched server:
  a) Check if disabled in config
  b) Check if command override in config
  c) Check system PATH for binary
  d) Check ~/.opencode/bin/ for binary
  e) If not found and auto-install supported → install

STEP 3: Auto-install (if enabled)
─────────────────────────────────
Check OPENCODE_DISABLE_LSP_DOWNLOAD env var and disableLSPDownload config.
If allowed:
  - npm servers: exec `npm install --prefix ~/.opencode/bin <package>`
  - go servers: exec `go install <package>@latest` with GOBIN=~/.opencode/bin
  - github servers: HTTP GET release, extract binary to ~/.opencode/bin/

STEP 4: Start server
─────────────────────
Launch LSP process with:
  - Command from registry or config override
  - Environment from config env field
  - Initialization options from config initialization field
  - Working directory from project root detection
```

### Server Registry Structure

```
┌─────────────────────────────────────────────────┐
│ ServerDefinition                                │
│  ├── ID: string           ("gopls")             │
│  ├── Extensions: []string ([".go"])             │
│  ├── Command: []string    (["gopls"])           │
│  ├── InstallStrategy: enum                      │
│  │    (None | Npm | GoInstall | GitHubRelease)  │
│  ├── InstallPackage: string                     │
│  ├── InstallRepo: string  (for GitHub)          │
│  ├── RootMarkers: []string (["go.mod"])         │
│  └── DefaultInit: map[string]any                │
└─────────────────────────────────────────────────┘
         │
         ▼  merged with user config overrides
┌─────────────────────────────────────────────────┐
│ Resolved LSP Server                             │
│  ├── uses config command if provided            │
│  ├── uses config extensions if provided         │
│  ├── uses config env if provided                │
│  ├── merges config initialization               │
│  └── respects config disabled flag              │
└─────────────────────────────────────────────────┘
```

### Updated Config Schema

```
┌─────────────────────────────────────────────────┐
│ LSPConfig (updated)                             │
│  ├── Disabled: bool        `json:"disabled"`    │
│  ├── Command: string       `json:"command"`     │
│  ├── Args: []string        `json:"args"`        │
│  ├── Extensions: []string  `json:"extensions"`  │
│  ├── Env: map[string]string `json:"env"`        │
│  └── Initialization: any   `json:"initialization"` │
└─────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────┐
│ Config (new fields)                             │
│  └── DisableLSPDownload: bool                   │
│       `json:"disableLSPDownload"`               │
│       Also reads OPENCODE_DISABLE_LSP_DOWNLOAD  │
└─────────────────────────────────────────────────┘
```

## Implementation Plan

### Phase 1: Bug fixes and LSP tool

- [x] **1.1** Fix `LSPConfig.Disabled` JSON tag from `"enabled"` to `"disabled"`
- [x] **1.2** Fix `GetDiagnostics()` race condition — return a copy of the map under `diagnosticsMu.RLock()`
- [x] **1.3** Fix client identity from `"mcp-language-server"` to `"opencode"` with proper version
- [x] **1.4** Remove hardcoded gopls `codelenses` from `InitializeLSPClient` — move to server-specific defaults
- [x] **1.5** Create `internal/llm/tools/lsp.go` — new LSP tool with 9 operations (`goToDefinition`, `findReferences`, `hover`, `documentSymbol`, `workspaceSymbol`, `goToImplementation`, `prepareCallHierarchy`, `incomingCalls`, `outgoingCalls`)
- [x] **1.6** Register the LSP tool in the tool registry alongside diagnostics
- [x] **1.7** Update `opencode-schema.json` with new config fields

### Phase 2: Language map & extension-based routing

- [x] **2.1** Expand `language.go` `DetectLanguageID` with missing extensions (`.vue`, `.svelte`, `.astro`, `.zig`, `.zon`, `.ml`, `.mli`, `.tf`, `.tfvars`, `.nix`, `.typ`, `.typc`, `.gleam`, `.mjs`, `.cjs`, `.mts`, `.cts`, `.kt`, `.kts`, `.rake`, `.gemspec`, `.ru`, `.erb`, `.cljs`, `.cljc`, `.edn`, `.lhs`, `.prisma`, `.pm6`)
- [x] **2.2** Add corresponding `protocol.LanguageKind` constants for new languages
- [x] **2.3** Replace hardcoded `notifyLspOpenFile()` switch with extension-based routing using a server→extensions mapping derived from config
- [x] **2.4** Replace `shouldOpenFile()` / `detectServerType()` with extension-based lookup
- [x] **2.5** Update `LSPConfig` struct: add `Extensions`, `Env`, `Initialization` fields; fix `Disabled` tag

### Phase 3: Auto-install infrastructure

- [x] **3.1** Create `internal/lsp/install/` package with `Installer` interface and strategies:
  - `NpmInstaller` — runs `npm install --prefix <dir> <package>`
  - `GoInstaller` — runs `go install <pkg>@latest` with `GOBIN`
  - `GitHubReleaseInstaller` — downloads from GitHub releases API, extracts platform-specific binary
- [x] **3.2** Create `internal/lsp/install/registry.go` — built-in server definitions with install metadata
- [x] **3.3** Add `DisableLSPDownload` to `Config` struct, read from config and `OPENCODE_DISABLE_LSP_DOWNLOAD` env var
- [x] **3.4** Update `app.initLSPClients()` to:
  - Scan project for file extensions
  - Match against server registry
  - Merge with user config overrides
  - Auto-install missing servers (if enabled)
  - Start matched servers
- [x] **3.5** Add `Env` support — pass `LSPConfig.Env` variables when spawning LSP server process
- [x] **3.6** Add `Initialization` support — pass `LSPConfig.Initialization` in `InitializeLSPClient`

### Phase 4: Polish

- [x] **4.1** Add tests for the LSP tool (mock LSP client, verify parameter conversion)
- [x] **4.2** Add tests for extension-based routing
- [x] **4.3** Add tests for auto-install logic (mock exec, mock HTTP for GitHub releases)
- [x] **4.4** Update schema generation (`cmd/schema/main.go`) for new config fields
- [x] **4.5** Log installed server versions for debugging

## Edge Cases

### File belongs to multiple servers

1. A `.ts` file matches both `typescript-language-server` and `eslint`
2. Both servers should receive the file open notification
3. Diagnostics from both servers are merged (already works — `getDiagnostics` iterates all clients)

### Auto-install fails

1. `npm` not in PATH, or network unavailable
2. Server should be silently skipped with a warning log
3. User can manually configure the server command as fallback
4. Must not block startup — install runs in background goroutine

### User disables a built-in server

1. Config has `"gopls": { "disabled": true }`
2. Server must not be started even if `.go` files exist
3. Other Go-related functionality (like diagnostics from other tools) unaffected

### User overrides built-in server command

1. Config has `"gopls": { "command": "/custom/path/gopls" }`
2. Should use custom command instead of auto-installed or PATH binary
3. Should still use built-in extensions unless overridden

### OPENCODE_DISABLE_LSP_DOWNLOAD set in CI

1. Env var `OPENCODE_DISABLE_LSP_DOWNLOAD=true`
2. No auto-install attempts, no network calls
3. Only servers already on PATH or in `~/.opencode/bin/` are used
4. Missing servers logged as info, not error

### LSP server crashes during operation

1. The existing `restartLSPClient` logic handles this
2. Auto-installed servers should restart the same way
3. No re-download needed — binary is already in `~/.opencode/bin/`

## Open Questions

1. **Should the LSP tool require permission (ask/allow/deny)?**
   - The reference always asks permission for `lsp` operations
   - Our diagnostics tool doesn't require permission
   - **Recommendation**: No permission needed — these are read-only operations (no code modification), similar to `view` or `grep` tools

2. **Should auto-install happen on startup or lazily on first file open?**
   - Startup: simpler, servers ready when needed, but slower cold start
   - Lazy: faster startup, but first LSP operation is slow
   - **Recommendation**: Lazy install on first file open of matching type (matches reference's `touchFile` pattern). Show a log message during install.

3. **How to handle npm vs yarn vs pnpm vs bun for JS ecosystem servers?**
   - Reference uses `bun install` exclusively (they're a Bun project)
   - We need to work across environments
   - **Recommendation**: Use `npm install --prefix` as the default. It's the most universally available. If npm is not found, log warning and skip.

4. **Should we support `lsp: false` to disable ALL LSP servers globally?**
   - Reference supports this as a shorthand
   - Our current schema has `lsp` as `map[string]LSPConfig`
   - **Recommendation**: Add a `disableLSP` boolean field to `Config` (separate from the map). Simpler than polymorphic JSON parsing.

5. **What to do with the `Options` field currently on `LSPConfig`?**
   - It's unused and untyped (`any`)
   - The reference uses `initialization` for server init options
   - **Recommendation**: Rename to `Initialization` for clarity, deprecate `Options`

## Success Criteria

- [ ] LLM can call `lsp` tool with all 9 operations and get meaningful results
- [ ] `GetDiagnostics()` no longer has a data race (verifiable with `-race` flag)
- [ ] `LSPConfig.Disabled` correctly maps to JSON `"disabled"`
- [ ] Language extension map covers all languages in the reference implementation
- [ ] Server↔file routing works based on extensions, not hardcoded server names
- [ ] At least gopls, typescript-language-server, and pyright auto-install when their file types are detected
- [ ] `OPENCODE_DISABLE_LSP_DOWNLOAD=true` prevents all auto-install attempts
- [ ] `disableLSPDownload` config field prevents all auto-install attempts
- [ ] Custom LSP servers can be configured with `command`, `extensions`, `env`, and `initialization`
- [ ] Built-in servers can be disabled individually via config
- [ ] `make test` passes with all changes
- [ ] No hardcoded server name switches remain in routing logic

## References

- `internal/llm/tools/diagnostics.go` — Existing diagnostics tool (to be kept, LSP tool is additive)
- `internal/lsp/client.go` — LSP client with file open/close, diagnostics, server type detection (to be refactored)
- `internal/lsp/methods.go` — Generated LSP method wrappers (already complete, just need tool wrapper)
- `internal/lsp/language.go` — Extension→language map (to be expanded)
- `internal/lsp/handlers.go` — Notification/request handlers (minor changes for init options)
- `internal/lsp/watcher/watcher.go` — File watcher (no changes needed)
- `internal/config/config.go` — Config struct and validation (LSPConfig changes)
- `internal/app/lsp.go` — LSP client initialization (to be refactored for auto-install)
- `opencode-schema.json` — JSON schema (to be updated)
- `internal/llm/tools/tools.go` — Tool registration (add LSP tool)
