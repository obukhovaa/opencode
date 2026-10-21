## 1. Persistent memory tool

- [x] 1.1 Add memory configuration, shared identity/store helpers and the six filesystem operations.
- [x] 1.2 Enforce permissions, root confinement, atomic writes, shared locks and bounded views.

## 2. Runtime integration

- [x] 2.1 Add explicit registration and project-aware memory prompt guidance.
- [x] 2.2 Implement native Claude serialization, function fallback, cache/deferral behavior and token counting.
- [x] 2.3 Update and regenerate config schema and document the tool/store contract.

## 3. Validation and delivery

- [x] 3.1 Add behavior, permission, cross-session/concurrency and provider/schema tests.
- [x] 3.2 Run focused tests, schema checks, required final checks and OpenSpec validation.
- [x] 3.3 Publish the canonical GitHub branch and prepare GitLab review MR !18 assigned to piano-dev-agent.
- [ ] 3.4 Open the canonical GitHub PR when GitHub API access is available (branch feat/project-memory is published).

Validation: focused memory/config/tool/provider/registration tests pass with the race detector; schema-check and strict OpenSpec validation pass. make test ran formatting/vet and the full suite, which encountered local-port sandbox restrictions and locale-sensitive hook assertions. The latter pass with LC_ALL=C. The hidden-character check flags unchanged generated tsprotocol.go; verified identical to base. GitHub API access is unavailable in this session; the GitLab MR is the review mirror, not the canonical merge target.
