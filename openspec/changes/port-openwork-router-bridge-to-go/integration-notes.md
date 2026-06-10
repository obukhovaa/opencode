# Integration Notes — port-openwork-router-bridge-to-go

External-consumer coordination work that lives outside this repo. Tracked here so the work is documented; the actual edits land in the consumer repos, not in opencode.

## 11.4 — Deprecation and eventual deletion of the TS `apps/opencode-router` package

**Repo:** https://github.com/different-ai/openwork.git → `apps/opencode-router/`

**Status in this PR:** marked deprecated in opencode's docs ([`interoperability/openwork/README.md`](../../../interoperability/openwork/README.md), [`interoperability/openwork/DEPLOY.md`](../../../interoperability/openwork/DEPLOY.md)). The package is kept buildable for rollback during the cutover window.

**Follow-up change (separate PR in the openwork repo, after stability validated):**

1. Mark `apps/opencode-router/package.json` as `"deprecated"` on npm (so `npm install -g opencode-router` shows a deprecation banner).
2. Update `apps/opencode-router/README.md` to point users at the in-process bridge in this repo.
3. After 2–4 weeks of cutover bake-in:
   - Remove `apps/opencode-router/` from the openwork monorepo.
   - Remove the npm package (`npm unpublish` if within the grace window, else `npm deprecate`).
   - Delete the orchestrator's TS router supervision logic (process spawn, health probe, restart loop).
   - Update `openwork-orchestrator` to rely on the bridge being in-process — no need for the orchestrator to manage a second Node process.

**Owner:** openwork repo maintainers. Not tracked further in this opencode change.

## 11.5 — c2-agent coordination

**Repo:** `c2-agent` (external orchestrator repository — the primary external consumer of the bridge).

**Status in this PR:** the bridge and Flow API ship with the contract c2-agent needs. The actual c2-agent edits land in that repo.

**Required edits in c2-agent:**

### k8s Job spec single-container update

- Remove the `opencode-router` sidecar container from the pod spec.
- Keep the `opencode` container as the only chat-platform-facing process.
- Remove the volume mount for `~/.openwork/opencode-router/opencode-router.json` (no longer used).
- Mount `.opencode.json` (with the `router` section populated) into the opencode container as a Secret. Use `defaultMode: 0o600` since the file contains tokens.
- Remove the `OPENCODE_ROUTER_HEALTH_PORT` env var and any references to it.

Reference layout: see [`interoperability/openwork/DEPLOY.md` → Kubernetes single-container deployment](../../../interoperability/openwork/DEPLOY.md#kubernetes-single-container-deployment).

### Orchestrator changes

1. **Read `/flow/*` SSE for incremental progress.** Switch from polling `/health` or process-exit detection to subscribing to opencode's `/event` SSE stream and filtering for `flow.step.*`, `flow.waiting_for_input`, `flow.completed`, `flow.failed` event types. The orchestrator can derive run progress without waiting for the pod to terminate.

   Event shape (from `internal/api/handler_flow.go::FlowEvent`):
   ```json
   {"type":"flow.step.started",  "runID":"...","flowID":"...","stepID":"...","stepIndex":2,"startedAt":1734567890123}
   {"type":"flow.step.completed","runID":"...","flowID":"...","stepID":"...","stepIndex":2,"completedAt":1734567990123}
   {"type":"flow.waiting_for_input","runID":"...","flowID":"...","stepID":"...","sessionID":"...","target":[{"channel":"slack","identity":"default","peerId":"U123"}]}
   {"type":"flow.completed","runID":"...","flowID":"..."}
   ```

2. **Populate `--flow-args` with the reviewer's `PeerRef`(s).** At pod startup, write a JSON args file mountable as a Secret and point `opencode serve` at it via `--flow-args /etc/opencode/flow-args.json`. The flow engine resolves `${args.NAME}` expressions in the step's `interaction.target` block (single PeerRef or array).

   Args file shape:
   ```json
   {
     "reviewer":  {"channel":"slack","identity":"default","peerId":"U123ABC"},
     "reviewers": [
       {"channel":"slack",   "identity":"default","peerId":"U123"},
       {"channel":"telegram","identity":"default","peerId":"344281281"}
     ]
   }
   ```

3. **(Optional) Call `POST /router/bind` for reactive re-binding mid-flow.** If the orchestrator needs to add a reviewer after the flow has started (e.g. a Slack `@mention` paged in a second engineer), POST to `/router/bind` with the existing `sessionId`. The dispatcher picks up the new peer for both fan-out (outbound) and fan-in (inbound replies route to the same session).

4. **Drop loopback HTTP for prompt submission.** The bridge no longer self-calls `/router/send`; nothing in c2-agent should be assuming the bridge talks to opencode over HTTP. All inbound dispatch is in-process via `agent.Run`.

### Final-output retrieval

The `/flow/output` endpoint from the prior spec was intentionally not shipped — final output is read via the existing `/session/{id}/messages` endpoint after `flow.completed` fires.

### Smoke-test plan

Success criterion 12.13 in `tasks.md`:

> c2-agent submits a job; opencode pod boots; flow runs to completion; c2-agent observed `flow.step.*` SSE events and read final output from `/session/{id}/messages`.

Owner: c2-agent maintainer. Tracked in c2-agent's repo, not here.
