# Test fixtures

## `rtk-rewrite.sh`

Vendored from [`rtk-ai/rtk@develop:hooks/claude/rtk-rewrite.sh`](https://github.com/rtk-ai/rtk/blob/develop/hooks/claude/rtk-rewrite.sh). This is the
exact hook script RTK's `rtk init` writes into Claude Code's `settings.json` —
we point our `.opencode.json` hooks block at it instead. The script is a thin
delegating wrapper around `rtk rewrite`; all rewrite logic lives in RTK's Rust
binary (`src/discover/registry.rs`).

Refresh by re-downloading from the same path. Bump the `rtk-hook-version`
comment in the script when updating so the diff is grep-able.

Required at runtime by `scripts/test/rtk.sh`:
- `rtk` binary (>= 0.23.0) on `PATH` — install via `brew install rtk` or
  `cargo install --git https://github.com/rtk-ai/rtk`.
- `jq` on `PATH`.

The test script skips cleanly with `SKIP` status when either dependency is
missing, so the e2e suite stays green on machines without RTK installed.
