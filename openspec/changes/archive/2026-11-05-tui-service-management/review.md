# Review and verification

Proposal and implementation reviews addressed shared MCP connections, immutable agent policy, allowlist conversion, markdown model defaults, environment-placeholder preservation, live LSP watcher lifecycle and stale page results.

Focused tests, race tests and `make test` passed. A real stdio LSP fixture verified activation after the initial startup channel closes and deactivation with persisted configuration. Terminal smoke testing verified the MCP page opens and returns to full-width chat.

Decisions for human review: disabling MCP affects the selected agent's tools while preserving shared connections for other agents. Changes wait until all agent turns are idle. Existing allowlists convert to equivalent deny-default tool policies before applying the requested server override. LSP installation uses the supported built-in installer catalog; unsupported servers need an executable installed on PATH.
