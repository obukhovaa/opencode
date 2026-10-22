## 1. Replace the model surface

- [x] 1.1 Remove the dedicated memory tool, provider branches, grants and obsolete tests.
- [x] 1.2 Implement capability/permission-aware system guidance and bounded project index loading.
- [x] 1.3 Inject one index snapshot per active session history, including new-session and compaction behavior.

## 2. Authoring and interoperability

- [x] 2.1 Add size/metadata feedback to ordinary file-write results without changing their schemas.
- [x] 2.2 Document concise notes, updatedAt semantics, index limits and external-writer concurrency.
- [x] 2.3 Update config schema and keep the shared project identity/store contract compatible with mem-sync.

## 3. Verify and deliver

- [x] 3.1 Test capability combinations, denied reads, index bounds, no topic preload, deduplication and file feedback.
- [x] 3.2 Run relevant race/schema/OpenSpec checks and review the final diff.
- [x] 3.3 Update the canonical branch and existing GitLab review MR !18, preserving piano-dev-agent as reviewer.
- [x] 3.4 Review fixes: bind automatic snapshots to the permission-checked canonical index and preserve nondefault Git ports per URL scheme. Regression probes first reproduced a denied-root read after a store symlink change and project-ID collisions across distinct ports. After the fixes, `go test ./internal/memory ./internal/llm/prompt ./internal/llm/agent -run Memory -count=1` passes, covering stable symlink roots, changed roots, newly created indexes, snapshot refresh, and unchanged tool inventories.
- [x] 3.5 Add the hermetic `scripts/test/memory.sh` e2e (driver `cmd/memory-e2e`): `.opencode.json` → viper → `AGENTS_MEMORY_HOME` override → Git-origin identity whose key equals an independent sha256 (the mem-sync contract) → coder reminder and index snapshot; memory disabled yields neither.

GENAI-518 revision: removed the dedicated memory tool and grants; implemented bounded index snapshots, capability-aware guidance, ordinary-tool authoring feedback, and freshness-preserving metadata/lint. Validation evidence is recorded in the linked MR descriptions.
