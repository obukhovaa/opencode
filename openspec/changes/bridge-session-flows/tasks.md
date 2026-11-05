## 1. Wave A — durable controller and immutable named execution

- [ ] 1.1 Add session-backend run records and SQLite/MySQL migrations/sqlc for definition kind/version, conversation association, origin and invocation identity.
- [ ] 1.2 Refactor TUI dynamic execution behind a shared controller with one nonterminal run per conversation and distinct identities for successive runs.
- [ ] 1.3 Import legacy profile manifests idempotently after ownership/hash validation; retain export and rollback behavior.
- [ ] 1.4 Extend the flow tool operation schema with generated plan, authorized list, named run, status and control; preserve the primary-only opt-in gate and reject all flow-step/subagent starts. Preserve create/update compatibility; plan accepts a goal and bounded complexity, waits for the submitting turn to yield, and shares bundled builder guidance plus the authorized agent inventory.
- [ ] 1.5 Resolve and validate named YAML/includes/schema refs and arguments, persist an immutable snapshot, preserve authored agent semantics and enforce delegation at admission/dispatch.
- [ ] 1.6 Pin externally resolved prompt content/version at first entry for retries; reject named-run updates and preserve original definition on resume after source edits/removal.

## 2. Wave A — direct bridge lifecycle

- [ ] 2.1 Connect direct bridge admission, progress subscriptions, final delivery and busy state to the controller's entire run lifetime.
- [ ] 2.2 Route descendant questions through authorized root conversation ownership with exact request/session IDs and concurrent-question handling.
- [ ] 2.3 Route ordinary inbound text to the durable steering inbox, keep explicit answers separate, and commit acknowledgements only after delivery.
- [ ] 2.4 Make stop/resume/terminate operate on controller work and preserve requester/job/deadline context through explicit run ownership.
- [ ] 2.5 Publish shared controller lifecycle events through the API/SSE adapter and expose compatible single-worker external routing without claiming takeover support.
- [ ] 2.6 Add deterministic direct-bridge tests for generated and named runs, questions, steering, cancel/resume, sequential run IDs, immutability and no-nesting.

## 3. Wave B — external mediated ownership and recovery

- [ ] 3.1 Agree and version the external orchestrator capability/admission protocol for whole-run ownership, job deadlines, waiting/suspended states and terminal acknowledgements.
- [ ] 3.2 Implement MySQL lease claims/renewal and fencing on state, inbox acknowledgement and outbox commits, with backend-time expiry and stale-owner refusal.
- [ ] 3.3 Persist delivery identity/receipts and pending question/answer state; provide state-plus-cursor recovery for reconnecting external relays, including late answers after worker replacement.
- [ ] 3.4 Integrate controller ownership into pool inbound admission, worker busy/shutdown, cancellation and reassignment; preserve external conversation isolation.
- [ ] 3.5 Test two workers racing, old owner returning after takeover, process death at boundaries or while waiting for a question, lost acknowledgements and recovery without profile files.
- [ ] 3.6 Validate the actual external orchestrator integration against the new protocol before enabling distributed recovery.

## 4. Review and release gates

- [ ] 4.1 Review controller/runtime/bridge changes independently, including unauthorized named agents, late widgets, duplicate batches and side-effect uncertainty.
- [ ] 4.2 Update flow, tool, bridge, profile/storage and API documentation; regenerate public schemas only where configuration fields actually change.
- [ ] 4.3 Run make test, schema-check, deterministic bridge/flow E2E and SQLite/MySQL integration tests; verify standalone and TUI behavior remains unchanged.
- [ ] 4.4 Publish supported-surface/capability rollout notes and rollback procedure, sync specs, then archive only after approved implementation is complete.
