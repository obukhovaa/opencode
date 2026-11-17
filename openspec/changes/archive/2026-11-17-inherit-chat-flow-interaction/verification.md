# Verification

- Reproduced on published OpenCode 0.31.3 through C2's real subprocess integration: the initial step completes and the interactive step fails with `invalid flow request: missing interaction target`. A required-reviewer schema also rejects admission before the fix.
- The same C2 integration passes against the patched binary with both schema variants. It uses the real authenticated daemon proxy, external relay, question widget, exact-request answer API and structured completion. Model responses are scripted locally; no live user messages are sent.
- `GOTOOLCHAIN=go1.25.8 make test` passes, including vet and repository-wide race tests. The final recovery-precedence refinement also passes a fresh full `internal/app` race run.
- `TestStoredNamedInteraction*` covers Slack DM/thread, Mattermost, Telegram, external peer, explicit targets, malformed/foreign targets, strict argument schemas, idempotency, unchanged source definitions, and legacy step-state recovery. These tests run in the existing release-tag session-flow E2E gate.
- Independent final code review approved the change. Recovery prefers explicit saved invocation arguments before the persisted originating peer.

## Downstream delivery

Publish OpenCode 0.31.4 through the normal release checks. C2's companion change upgrades its three generated/runtime pins and strengthens `TestSessionFlowOpenCodeIntegration`; it requires no production orchestrator routing change. Verify again with the published binary, obtain Piano Developer Agent review, and release the C2 patch. Redeploy affected idle C3/Neo daemons through CI and verify their runtime, adapters and preserved persistent data.

The reported business run was subsequently terminated. Deployment must not automatically restart it.
