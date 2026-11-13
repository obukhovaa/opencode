# Review and verification

The proposal was reviewed before implementation. Independent implementation reviews covered flow ownership, recovery, schema isolation, tool policy, cancellation and steering. Findings resolved include unsafe skips, premature steering acknowledgement, cross-root agent-cache reuse, attended permission inheritance, concurrent questions, and bare `/flow` navigation on a copied TUI model.

Validation:

- `make test` passed with the race detector, vet, formatting, schema generation and 70.2% aggregate coverage, using an isolated HOME and `LC_ALL=C`.
- `scripts/test/tui_workflows.sh` passed with real SQLite, the real flow engine/tool/question service, and scripted agents: planning, question, steering, cancellation, a new App instance recovering the same definition, and completion. It also exercises activity isolation/cancellation and page navigation.
- All 24 deterministic scripts under `scripts/test` passed. `serve.sh` requires `OPENCODE_ENABLE_QUESTION_TOOL=1` for its expected question endpoints.
- Actual terminal smoke checks exercised `/tasks`, `/mcp`, `/flow`, return to chat and clean exit without a provider call.
- Live-provider attachment tests failed without credentials in the isolated environment. Live grep and native deferred-tool tests were excluded from the deterministic run. Generated-plan quality against a paid provider was not evaluated.

Decisions for human review:

- Dynamic flows serialize their steps and use bounded task fanout within steps. Adopted standalone flows preserve their original concurrency.
- The initiating primary agent remains pinned; specialist configured subagents may own steps. Main-agent model/context overrides and recursive flows are rejected.
- Cancel pauses execution before the user chooses resume, skip or terminate. Skip refuses structured-output and conditional/looping steps instead of inventing required results.
- Definitions live under the active profile's `flows/dynamic` directory; transcripts remain in the existing database. Recovery requires obtaining local execution ownership. Remote MySQL deployments still need their orchestrator's distributed ownership controls.
- Dynamic flows are disabled by default. XS through XXL enforce explicit resource ceilings; omitted size lets the builder choose.
- OpenSpec installation is offered in the TUI only when missing and runs only after the user's choice.
