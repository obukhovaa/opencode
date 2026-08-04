# Shared step definitions via `include` + `extends`

## Why

A flow step is only definable inline, so a step reused across flows is
copy-pasted. In the Piano unified workspace that has produced a measured
divergence problem: the `resolve-team` guard is duplicated across **seven** flows,
~460 lines in total, and the two richest variants are 65 of 65 lines identical
with four lines differing. Every rule added to it during GENAI-103 had to be
applied twice by script, and the other five flows carry a reduced, drifted copy.

Duplication is the current state, not a risk — and the step in question is
`steps[0]`, a mandatory guard whose entire purpose is preventing an agent from
doing work as the wrong team. Divergence there is a correctness problem, not an
aesthetic one.

The alternatives available to a flow author today are all worse. Extracting the
prose into a skill trades a certain cost (duplication) for an uncertain one: a
step prompt is unavoidably in context, a skill must be invoked, and a guard that
is sometimes skipped is worse than one that is verbose. Generating the step into
each flow from one source puts machine-written content into hand-authored files.

## What Changes

- A flow file MAY declare `include:` at the top level, listing files that
  contribute reusable **step templates**.
- A step MAY declare `extends:` naming one or more templates; the template's keys
  seed the step and the step's own keys win.
- Templates are hidden top-level keys in an included file — a leading `.` marks a
  key as a template rather than a flow, mirroring GitLab CI.
- Resolution happens at **load time, before validation**, so a merged step is
  validated exactly as an inline one and a template cannot smuggle an invalid
  step past `validateFlow`.
- A template carries a step's **behaviour**, not its identity or scheduling: `id`,
  `interactive`, `interaction` and `resume_after` are rejected by name, because the
  orchestrator reads them out of the same file and never resolves templates. Every
  other real step key is inheritable, including ones added later. See design D4;
  this is the non-obvious part of the change.
- Existing flows are untouched and keep working: no `include`, no `extends`, no
  behaviour change.

## Capabilities

### New Capabilities

None. This extends the existing flow-definition contract rather than adding a
runtime capability — nothing about execution, scheduling or state changes.

### Modified Capabilities

- `flow-api`: the flow-definition contract gains `include` / `extends`, with the
  merge semantics and the four rejected keys stated as requirements.

## Impact

**`github.com/obukhovaa/opencode`**

- `internal/flow/registry.go` — resolve includes and merge `extends` inside
  `parseFlowFile`, before `$ref` resolution and `validateFlow`.
- `internal/flow/flow.go` — `Step` gains `Extends`; a template type is added.

**`piano/ai-agents/piano-developer`** (separate change, separate MR)

- The `resolve-team` step moves to a shared template under `.agents/steps/`, and
  the seven flows extend it. That migration is where the benefit is realised; it
  is deliberately not in this change, which only makes it expressible.

**`piano/composer/tools/c2-agent`**

- **No changes, by design.** It parses the same flow files with its own structs
  and is built separately (cloned at a pinned tag into the agent image), so it
  cannot see resolved templates. D4's four rejected keys are what keep that safe
  rather than silently wrong.
