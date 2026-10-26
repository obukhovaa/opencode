## 1. Enforcement
- [x] 1.1 Reproduce env-wrapper denial bypass with a harmless local marker fixture.
- [x] 1.2 Make the common permission gate mandatory for every bash call.
- [x] 1.3 Reclassify the legacy list as scheduling only and update stale comments.
- [x] 1.4 Cover wrapper/plain-command denials in both modes, ask and explicit allow.

## 2. Validation and delivery
- [x] 2.1 Full make test and focused race checks pass; no provider-backed e2e required.
- [x] 2.2 Strict OpenSpec validation and final diff inspection.
- [ ] 2.3 Open the separate stacked review PR and post in the original board thread.
- [ ] 2.4 Human review, merge after #110, engine release and downstream rollout.
