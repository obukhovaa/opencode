## Context

Named flows preserve authored `interactive` steps. The conversation controller stores a trusted origin peer, while the engine resolves targets from flow arguments. The reported run had an origin but no `reviewer`. Conversation-owned steps already route questions through their root and avoid rebinding the chat to each child.

## Goals / Non-Goals

Provide routing defaults for ordinary chat invocations and recovery. Keep standalone execution, generated-flow restrictions, source YAML, tool schemas and model cache prefixes unchanged. Do not restart the reported terminated business workflow automatically.

## Decisions

- Derive only absent arguments referenced by exact supported interaction target expressions from the authenticated origin. Validate effective named arguments before saving; preserve caller request hashing for idempotent retries.
- Do not overwrite explicit null, malformed or foreign targets. Keep current authorized-peer checks and reject invalid delivery before running the affected step.
- Recover from the persisted origin, not the latest user or active thread. Reapply missing defaults after engine state restoration because legacy step rows may lack them.
- Reuse conversation question routing for direct and external adapters. Do not inject arbitrary orchestrator args, mutate the source definition, or bind a second chat listener.

## Risks / Trade-offs

- Incorrect target reuse could leak questions across threads → compare complete channel, identity and peer/thread scope; test hostile targets and recovery from a different caller context.
- Authored argument schemas may constrain target shape → validate the effective args and retain clear failures rather than weakening schemas.
- Recovery may restore older argument maps → test saved snapshots and restored step-state args as well as new admission.

## Migration Plan

No database migration. Release an OpenCode patch, update the C2 runtime pin, and redeploy idle affected daemons through the existing release process. Old runs use the stored origin on explicit authorized resume. Roll back through the previous image if verification fails.
