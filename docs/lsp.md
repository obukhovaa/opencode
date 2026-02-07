# LSP Servers

OpenCode integrates with Language Server Protocol (LSP) servers for code intelligence. Diagnostics feed back into the LLM after every file edit, and the `lsp` tool gives the agent access to go-to-definition, find-references, hover, and more.

## Built-in Servers

Built-in servers are auto-detected based on file extensions in your project. If the binary is not found on your PATH, OpenCode can auto-install it.

| Server | Extensions | Install Method | Requirements |
|--------|-----------|----------------|--------------|
| gopls | `.go` | `go install` | `go` on PATH |
| typescript-language-server | `.ts` `.tsx` `.js` `.jsx` `.mjs` `.cjs` `.mts` `.cts` | npm | `npm` on PATH |
| bash-language-server | `.sh` `.bash` `.zsh` `.ksh` | npm | `npm` on PATH |
| yaml-language-server | `.yaml` `.yml` | npm | `npm` on PATH |
| vue-language-server | `.vue` | npm | `npm` on PATH |
| svelte-language-server | `.svelte` | npm | `npm` on PATH |
| astro-ls | `.astro` | npm | `npm` on PATH |
| pyright | `.py` | npm | `npm` on PATH |
| intelephense | `.php` | npm | `npm` on PATH |
| lua-language-server | `.lua` | GitHub release | — |
| terraform-ls | `.tf` `.tfvars` | GitHub release | — |
| tinymist | `.typ` `.typc` | GitHub release | — |
| rust-analyzer | `.rs` | — | Pre-installed |
| clangd | `.c` `.cpp` `.cc` `.cxx` `.c++` `.h` `.hpp` `.hh` `.hxx` `.h++` | — | Pre-installed |
| dart | `.dart` | — | `dart` on PATH |
| zls | `.zig` `.zon` | — | Pre-installed |
| ocamllsp | `.ml` `.mli` | — | Pre-installed |
| nixd | `.nix` | — | Pre-installed |
| haskell-language-server | `.hs` `.lhs` | — | Pre-installed |
| gleam | `.gleam` | — | `gleam` on PATH |
| sourcekit-lsp | `.swift` | — | Xcode / Swift |
| kotlin-language-server | `.kt` `.kts` | — | Pre-installed |
| clojure-lsp | `.clj` `.cljs` `.cljc` `.edn` | — | Pre-installed |
| elixir-ls | `.ex` `.exs` | — | `elixir` on PATH |
| jdtls | `.java` | — | Java SDK 21+ |
| ruby-lsp | `.rb` `.rake` `.gemspec` `.ru` | — | `ruby` on PATH |
| csharp-ls | `.cs` | — | .NET SDK |
| fsautocomplete | `.fs` `.fsi` `.fsx` `.fsscript` | — | .NET SDK |
| prisma | `.prisma` | — | Pre-installed |
| metals | `.scala` | — | Pre-installed |

**Install methods:**
- **`go install`** — uses `go install pkg@latest` with `GOBIN=~/.opencode/bin/`
- **npm** — uses `npm install --prefix ~/.opencode/bin/ <package>`
- **GitHub release** — downloads platform-specific binary from GitHub releases to `~/.opencode/bin/`
- **—** — must be pre-installed on your system PATH

## How It Works

When OpenCode starts, it:

1. Merges the built-in server registry with your config overrides
2. For each enabled server, resolves the binary (system PATH → `~/.opencode/bin/` → auto-install)
3. Starts the LSP server process and initializes it

After each file mutation (edit, write, patch), OpenCode:

1. Notifies the LSP server of the change
2. Waits for updated diagnostics (up to 5 seconds)
3. Includes errors and warnings in the tool response so the LLM can fix them

The `lsp` tool provides code-navigation operations:

| Operation | Description |
|-----------|-------------|
| `goToDefinition` | Find where a symbol is defined |
| `findReferences` | Find all references to a symbol |
| `hover` | Get type info and documentation |
| `documentSymbol` | List all symbols in a file |
| `workspaceSymbol` | Search symbols across the workspace |
| `goToImplementation` | Find interface implementations |
| `prepareCallHierarchy` | Get call hierarchy item at a position |
| `incomingCalls` | Find all callers of a function |
| `outgoingCalls` | Find all callees of a function |

## Configuration

Configure LSP servers in `.opencode.json` under the `lsp` key:

```json
{
  "lsp": {
    "gopls": {
      "initialization": {
        "codelenses": { "test": true, "tidy": true }
      }
    }
  }
}
```

Each server supports:

| Property | Type | Description |
|----------|------|-------------|
| `disabled` | `boolean` | Disable this server |
| `command` | `string` | Override the server command |
| `args` | `string[]` | Command arguments |
| `extensions` | `string[]` | File extensions to handle |
| `env` | `object` | Environment variables |
| `initialization` | `object` | LSP initialization options (server-specific) |

### Disabling a built-in server

```json
{
  "lsp": {
    "typescript": { "disabled": true }
  }
}
```

### Adding a custom server

```json
{
  "lsp": {
    "my-lsp": {
      "command": "my-lsp-server",
      "args": ["--stdio"],
      "extensions": [".custom"]
    }
  }
}
```

### Overriding a built-in server command

```json
{
  "lsp": {
    "gopls": {
      "command": "/usr/local/bin/gopls",
      "env": { "GOFLAGS": "-mod=vendor" }
    }
  }
}
```

### Passing initialization options

```json
{
  "lsp": {
    "typescript": {
      "initialization": {
        "preferences": {
          "importModuleSpecifierPreference": "relative"
        }
      }
    }
  }
}
```

## Disabling Auto-Install

To prevent OpenCode from downloading LSP server binaries:

**Via config:**
```json
{
  "disableLSPDownload": true
}
```

**Via environment variable:**
```bash
export OPENCODE_DISABLE_LSP_DOWNLOAD=true
```

When disabled, only servers already on your system PATH or in `~/.opencode/bin/` are used. Missing servers are silently skipped.
