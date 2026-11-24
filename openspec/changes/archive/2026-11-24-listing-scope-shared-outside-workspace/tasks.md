## 1. Matcher

- [x] 1.1 In `inListingScope`, treat a location outside the working directory like a missing one: listed in full.
- [x] 1.2 Update the matcher test: an out-of-tree location is in scope, and so is an absolute home-directory location.

## 2. Docs and verification

- [x] 2.1 Update `docs/skills.md` (two tiers and the D9 note in the archived design are superseded): items outside the working directory are listed in full.
- [x] 2.2 `go test ./internal/llm/tools/ ./internal/llm/agent/` and `openspec validate listing-scope-shared-outside-workspace --strict`.
