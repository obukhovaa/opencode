# Design: shared step definitions via `include` + `extends`

## Context

`parseFlowFile` (`internal/flow/registry.go:330`) reads a flow file, rejects it over
`OPENCODE_MAX_FLOW_FILE_SIZE`, decodes it into `flowFile`, re-parses the raw YAML
to reject typos in `flow.session`, derives the flow ID from the filename, resolves
`$ref` in each step's output schema **relative to the flow file's directory**, and
then validates. Two of those are load-bearing precedents: there is already a
file-reference mechanism with base-directory semantics, and there is already a
place where the document is enriched before validation sees it.

One fact about the environment shapes the whole design, and it is easy to miss:
**the flow file has two independent consumers.** opencode executes it; the
orchestrator (`c2-agent`) parses the same file with its own Go structs to build
the Slack task card, decide reviewer-argument enrichment, and compute
postpone-resume timing. They are separate programs — opencode is cloned at a
pinned tag and compiled into the agent image, not imported as a module — and they
model *different subsets* of the schema. `resume_after` is read by the
orchestrator and **is not modelled in opencode at all**; unknown keys are silently
dropped by both.

So a step field is not simply "a field": it may be one nobody but the orchestrator
reads, in a file only opencode will resolve includes for.

## Goals / Non-Goals

**Goals:**

- One source for a step reused across flows, with per-flow override.
- The step's text still lands in the agent's context unavoidably, as an inline
  step's does — this is the property that makes a template better than a skill for
  a guard step.
- A merged step is indistinguishable from an inline one to everything downstream.

**Non-Goals:**

- Templating *inside* a prompt (loops, conditionals, variable substitution beyond
  the existing `${args.*}` interpolation). Sharing whole step definitions is the
  problem in hand.
- Remote includes (`project:`, `remote:` in GitLab's vocabulary). Local files
  only; a remote include would make flow loading depend on the network.
- Including whole flows, or `include` inside a template. See D6.
- Any change to how the orchestrator parses flow files. See D4.

## Decisions

### D1. GitLab-CI shape: `include:` at the top, `extends:` on the step

```yaml
include:
  - local: .agents/steps/resolve-team.yaml

flow:
  steps:
    - id: resolve-team
      extends: [".resolve-team"]
      maxTurns: 20          # wins over the template
```

```yaml
# .agents/steps/resolve-team.yaml
.resolve-team:
  agent: piano-manager
  maxTurns: 15
  prompt: |
    # Resolve the owning team...
```

Chosen because the audience already knows it. Every author of these flows reads
GitLab CI daily, `extends` there means exactly what it means here, and the `.`
prefix already reads as "not a thing on its own" to them. A novel vocabulary
would have to earn the cost of being learned, and this buys nothing over the
familiar one.

The `local:` key is kept even though it is the only supported kind, so adding a
second kind later is not a breaking change to files written now.

*Alternative rejected:* a bare `include: [path]` list with templates addressed by
file rather than by name. One template per file, no naming, less to explain — but
it forces a file per step and gives no way to group the two or three variants of
one step together, which is exactly what the migration needs.

### D2. Merge is shallow, per top-level step key

A key present in the step replaces the template's value wholly. `output`, `rules`
and `fallback` are single values for this purpose — a step overriding `output`
replaces the whole block, it does not merge into the template's schema.

Deep merge is where this kind of feature becomes unpredictable: a partially
overridden `rules` list raises "is my rule appended, prepended, or matched by
index?", and a deep-merged `output.schema` can produce a schema no author wrote
and none can read. Shallow is explainable in one sentence, and the escape hatch —
override the whole block — is always available.

With several `extends` entries, templates apply left to right, later overriding
earlier, then the step's own keys override all of them. Same order as GitLab's.

### D3. Resolve at load, before validation

Includes are read and merged inside `parseFlowFile`, before `$ref` resolution and
before `validateFlow`. Consequences, all wanted:

- Every existing validation applies to merged steps unchanged — kebab-case step
  IDs (`registry.go:428`), duplicate step IDs (`:431`), rule targets naming a real
  step (`:459`), `maxTurns >= 0` (`:438`).
  *Correction from review:* `validateFlow` does **not** enforce `maxTurns >= 1`;
  it rejects only negatives, and `0` legitimately means "inherit from the agent".
  The stale doc comment at `flow.go:62-66` claims otherwise and should be fixed
  while here — believing it is how this design acquired the wrong claim.
- A template cannot introduce a step shape that inline YAML could not.
- A template's `output.schema` `$ref` MUST be resolved **at include-load time,
  against the template file's own directory, before the merge** — not after. The
  flow's existing `$ref` loop (`registry.go:365-375`) walks the merged steps with a
  single `baseDir` of the *flow's* directory, so once merged there is no per-key
  provenance left to resolve against. Resolving early is also free: that loop then
  finds no `$ref` key and `ResolveSchemaRef` returns the schema unchanged
  (`format.go:122-125`).

### D4. Only opencode-consumed keys are inheritable, enforced by an allow-list

**The decisive constraint.** Inheritable: `agent`, `prompt`, `session`, `output`,
`rules`, `fallback`, `maxTurns`, `maxIterations`, `timeout`, `compact`. A template
declaring anything else — notably `id`, `interactive`, `interaction`,
`resume_after` — is a load error naming the offending key.

Why: those four are read by the **orchestrator**, out of the same file, by a
different program that will never resolve a template. Were they inheritable, an
author would write a correct-looking flow and get silent misbehaviour:

- `interactive` / `interaction` in a template → the orchestrator sees a
  non-interactive step, skips reviewer-argument enrichment, and the job runs with
  nobody bound to answer. That is precisely the "job started but nobody was ever
  asked" failure GENAI-118 was filed to fix.
- `resume_after` in a template → the orchestrator computes no postpone-resume
  timeout and the step waits indefinitely, or resumes on the wrong bound.
- `id` in a template → two flows extending it collide, and the flow file no longer
  shows which steps it has.

`agent` and `prompt` are inheritable but DO appear in the orchestrator's struct,
re-emitted verbatim by `GET /api/v1/workspaces` and the MCP `list_workspaces`
tool. No orchestrator logic reads them, so inheriting them is safe — but those
surfaces will report `prompt: ""` for a migrated step, which is an API surface
degrading and belongs in the record rather than being discovered later.

An **allow-list, not a deny-list**, because the coupling is cross-repo and will
outlive whoever reads this. A deny-list has to be updated when the orchestrator
starts reading a new field — by someone working in the *other* repo, who has no
reason to look here. An allow-list fails safe: a field added to the schema later
is non-inheritable until this list deliberately admits it.

*Alternative rejected:* implement the resolver in the orchestrator too. It would
mean two implementations of a merge algorithm, in two independently released
programs, added in order to remove duplication — and any drift between them
reproduces exactly the silent divergence this change exists to end.

*Alternative rejected:* extract the flow schema and resolver into a module both
import. Correct in the long run and much larger than this ticket: no module
boundary exists today, the orchestrator's structs are deliberately a different
subset, and the agent image builds opencode from a pinned git tag.

### D5. `local:` paths are ROOT-relative, as GitLab's are

Resolved against `config.WorkingDir` — `/workspace` in the pod — not against the
including file's directory. An earlier revision said file-relative and contradicted
D1's own example, which writes `.agents/steps/resolve-team.yaml` from a flow living
in `.agents/flows/`; file-relative would have resolved that to
`.agents/flows/.agents/steps/…`.

Root-relative is also what makes team-hosted flows work, which is a requirement:
team flows MAY extend shared steps (requester, 2026-07-31). A flow at
`composer/flows/fix-tests.yaml` writes the same
`.agents/steps/resolve-team.yaml` as a shared flow does, instead of
`../../.agents/steps/…` — and the relative form would differ per team-directory
depth, so every team repo would carry a subtly different include line.

`config.WorkingDir` is not a new concept: `discoverCustomPathFlows` already resolves
relative `flowPaths` entries against it (`registry.go:232-262`), which is how
`"composer/flows"` finds its target. This reuses that base rather than inventing one.

**The asymmetry must be documented**, because it will surprise someone: `include:`
is root-relative while an `output.schema` `$ref` next to it stays file-relative.
GitLab CI has the same split (`include:local:` from the root, everything else
relative), so the audience has met it before — but nobody should have to infer it.

No traversal guard. Flow files are trusted workspace content already able to name
any agent and run any tool; a `../` restriction would be security theatre next to a
mechanism that has none.

### D6. Templates are leaves: no `include` or `extends` inside a template

A template file contributes templates and nothing else. Keeping the graph one
level deep means the merge order in D2 is fully explainable, there is no cycle to
detect, and no depth limit to choose. If a real second-level case appears,
generalise then — with the case in hand rather than imagined.

`OPENCODE_MAX_FLOW_FILE_SIZE` applies to **each** included file. That cap exists to
stop one runaway file from being loaded; includes must not become the way around
it. No separate limit on the NUMBER of includes is specified — the per-file cap and
the leaf rule together bound the total, and an arbitrary count limit would be a
number nobody can justify.

## Risks / Trade-offs

- **[The step's text is no longer visible in the flow file]** → Real, and the
  cost of any de-duplication. Mitigated by the text still being unavoidably in
  context at runtime, which is the property that ruled out a skill; and by the
  include being one named file away. Accepted.
- **[A shared template's `rules` name steps the extending flow lacks]** → Rejected
  at load by `validateFlow`, since D3 puts merging first. But "loud" overstates it:
  `scanFlowDirectory` (`registry.go:320-327`) logs a WARN and **skips the flow**, so
  every error path in this change surfaces as a *missing flow* rather than a stopped
  one — the shape `flow-creator/SKILL.md` itself documents as the trap behind "the
  job completed having done nothing". There is no flow-validate CLI for CI to call,
  so nothing catches it earlier. That is pre-existing and not this change's to fix,
  but the errors specified here inherit it and the spec should not imply otherwise.
- **[The allow-list drifts from what the orchestrator reads]** → The direction
  that matters is safe by construction: a newly orchestrator-read field is
  non-inheritable by default. The remaining risk is the reverse — a field
  inheritable here that the orchestrator *starts* reading later — which is why the
  allow-list entry carries the reason it is safe, not just the name.
- **[Two flows extend one template and one of them needed the old text]** → Same
  risk as any shared code; the override in D2 is the answer, and a flow that must
  differ wholesale should stop extending rather than fight the template.
- **[An author expects deep merge]** → Most likely first surprise. The error
  surface cannot help here (both shapes are valid YAML), so the spec states the
  rule and the migration's own flows demonstrate it.

## Migration Plan

Additive and independently deployable: no flow declares `include` today, so
merging this changes nothing observable until a workspace opts in. The agent image
pins an opencode tag, so the workspace migration can only start once a tag
carrying this is built into it — which is the real ordering constraint, not the
merge order of the two MRs.

Rollback is a revert; flows that had not adopted `include` are unaffected, and one
that had fails to load with a clear unknown-key error rather than misbehaving.
