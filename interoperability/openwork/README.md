# OpenCode + OpenWork Integration

OpenWork is an open-source desktop/web control surface for agentic workflows. OpenCode serves as the backend engine that OpenWork connects to via its HTTP/SSE API.

This fork of OpenCode is fully compatible with the upstream `opencode-ai` version used by OpenWork. Any `opencode serve` instance — whether from this fork or the original — works as a drop-in backend.

- OpenWork repo: https://github.com/different-ai/openwork.git
- OpenWork docs: https://openworklabs.com/docs

## How it works

OpenWork connects to an OpenCode server via `@opencode-ai/sdk/v2/client`. By default, OpenWork spawns its own OpenCode subprocess (a "sidecar"). All variants below replace that sidecar with your own OpenCode instance.

## Quick start: desktop app with local OpenCode

Start OpenCode, then point OpenWork at it:

```bash
# Terminal 1 — start OpenCode server
opencode serve --hostname 127.0.0.1 --port 3456

# Terminal 2 — start OpenWork desktop (from the openwork repo)
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 pnpm dev
```

OpenWork will skip spawning its own OpenCode process and connect to yours instead.

## Web UI options

OpenWork has two web interfaces:

- **Full React UI** — the same rich interface used by the Electron desktop app (sessions, skills, permissions, live streaming, execution plans). Requires building or running the `apps/app` Vite project and serving it alongside the OpenWork server. The React app connects to the server via `VITE_OPENWORK_URL`.
- **Toy UI** — a lightweight built-in UI served by the OpenWork server at `/ui`. No separate build or process needed. Sufficient for basic session interaction.

The full React UI is available as a browser-accessible web app in **variants 2, 3, and 5** below.

## All integration variants

### 1. Desktop app (development from source)

Clone OpenWork and run the Electron desktop app backed by your local OpenCode:

```bash
git clone https://github.com/different-ai/openwork.git
cd openwork
pnpm install

# Start with your running OpenCode server
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 pnpm dev
```

**UI:** Full React UI inside Electron (not browser-accessible).

`pnpm dev` enables `OPENWORK_DEV_MODE=1` automatically, which isolates dev state from your personal OpenCode config.

To use a local OpenCode binary as the managed sidecar instead of connecting to an already-running server, set `OPENWORK_OPENCODE_BIN` (requires the [runtime patch](./opencode-fork-for-openwork.patch)):

```bash
OPENWORK_OPENCODE_BIN=$(which opencode) pnpm dev
```

### 2. Headless web with full UI (development from source)

Run the full React UI as a web server accessible from any browser, without the Electron shell:

```bash
cd openwork

# Local only (127.0.0.1)
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 \
  pnpm dev:headless-web

# Remote access (0.0.0.0) — accessible from other machines
OPENWORK_REMOTE_ACCESS=1 \
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 \
  pnpm dev:headless-web
```

**UI:** Full React UI served by Vite dev server (browser-accessible).

This spawns two processes:
- **Vite dev server** — serves the full React UI, configured with `VITE_OPENWORK_URL` pointing at the OpenWork server
- **Orchestrator** — runs the OpenWork server (API + Toy UI + OpenCode proxy)

Open the Vite URL printed at startup to access the full UI. Logs go to `tmp/dev-web.log` and `tmp/dev-headless.log`.

Key environment variables:

| Variable | Default | Description |
|---|---|---|
| `OPENWORK_REMOTE_ACCESS` | `0` | Bind to `0.0.0.0` for remote access |
| `OPENWORK_WORKSPACE` | cwd | Workspace directory |
| `OPENWORK_PORT` | auto | OpenWork server port |
| `OPENWORK_WEB_PORT` | auto | Vite UI port (this is what you open in the browser) |
| `OPENWORK_OPENCODE_BASE_URL` | — | URL of your running OpenCode server |

### 3. Orchestrator CLI (no desktop, no source checkout)

The `openwork-orchestrator` npm package provides the `openwork` CLI for running OpenWork as a headless service:

```bash
npm install -g openwork-orchestrator

# Connect to your OpenCode server
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 \
  openwork start \
    --workspace /path/to/workspace \
    --approval auto

# With remote access
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 \
  openwork start \
    --workspace /path/to/workspace \
    --approval auto \
    --remote-access
```

**UI:** Toy UI at `/ui` (browser-accessible). For the full React UI, see variant 5.

For log-only mode without the interactive TUI dashboard:

```bash
openwork serve --workspace /path/to/workspace --approval auto
```

### 4. Standalone server (API only)

Run just the OpenWork server layer, without the orchestrator managing sidecars:

```bash
npm install -g openwork-server

openwork-server \
  --workspace /path/to/workspace \
  --opencode-base-url http://127.0.0.1:3456 \
  --host 0.0.0.0 \
  --port 8787 \
  --cors '*' \
  --approval auto
```

**UI:** Toy UI at `/ui`. The server exposes the full REST + SSE API that any frontend can consume.

Or via environment variables:

```bash
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 \
OPENWORK_HOST=0.0.0.0 \
OPENWORK_PORT=8787 \
OPENWORK_CORS_ORIGINS='*' \
OPENWORK_APPROVAL_MODE=auto \
  openwork-server --workspace /path/to/workspace
```

If your OpenCode server uses authentication:

```bash
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 \
OPENWORK_OPENCODE_USERNAME=user \
OPENWORK_OPENCODE_PASSWORD=pass \
  openwork-server --workspace /path/to/workspace
```

Config file alternative at `~/.config/openwork/server.json`:

```json
{
  "host": "0.0.0.0",
  "port": 8787,
  "approval": { "mode": "auto" },
  "opencodeBaseUrl": "http://127.0.0.1:3456",
  "workspaces": [
    { "path": "/path/to/workspace" }
  ],
  "corsOrigins": ["*"]
}
```

### 5. Production self-hosted (full React UI + server)

Build the React UI as static files and serve them alongside the OpenWork server for a production-like self-hosted setup:

```bash
cd openwork

# Build the full React UI
VITE_OPENWORK_URL=http://your-server:8787 \
VITE_OPENWORK_TOKEN=your-token \
  pnpm build:ui
# Output: apps/app/dist/

# Run the server (variant 3 or 4)
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 \
  openwork start \
    --workspace /path/to/workspace \
    --approval auto \
    --remote-access
```

Serve `apps/app/dist/` with any static file server (nginx, caddy, etc.) and point `VITE_OPENWORK_URL` at your OpenWork server URL at build time. The Vite env vars are baked into the bundle at build time:

| Build-time variable | Description |
|---|---|
| `VITE_OPENWORK_URL` | OpenWork server URL the UI connects to |
| `VITE_OPENWORK_TOKEN` | Client bearer token (must match server's `--token`) |
| `VITE_OPENWORK_HOST_TOKEN` | Host token for approval actions (optional) |

**UI:** Full React UI (browser-accessible), served by your own static file server.

## Comparison

| Variant | UI | Browser-accessible | Requires source | Best for |
|---|---|---|---|---|
| 1. Desktop app (`pnpm dev`) | Full React (Electron) | No | Yes | Local development |
| 2. Headless web (`pnpm dev:headless-web`) | Full React (Vite) | Yes | Yes | Remote dev access |
| 3. Orchestrator CLI (`openwork start`) | Toy UI at `/ui` | Yes | No | Self-hosted headless |
| 4. Standalone server (`openwork-server`) | Toy UI at `/ui` | Yes | No | API-first / custom UI |
| 5. Production self-hosted (build + serve) | Full React (static) | Yes | Yes (build only) | Production self-host |

## Patches

- [`opencode-fork-for-openwork.patch`](./opencode-fork-for-openwork.patch) — makes the OpenWork desktop runtime respect `OPENWORK_OPENCODE_BIN` env var for custom binary paths (upstream ignores it in favor of the bundled sidecar).
