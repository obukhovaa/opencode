# flow-api (delta)

Delta spec for the `scoped-context-files` change. Restates only the requirements
that change; unchanged requirements are not repeated here. For the full specification
see `openspec/specs/flow-api/spec.md`.

## MODIFIED Requirements

### Requirement: Flow files compose shared step definitions via include and extends

A flow file SHALL be able to declare `include:` at the top level, listing local
files that contribute reusable step templates, and a step SHALL be able to declare
`extends:` naming one or more of those templates. A template's keys seed the step;
the step's own keys override them. Composition MUST happen at load time before
validation, so that a merged step is validated exactly as an inline step is and a
template cannot introduce a step shape inline YAML could not.

A template is a top-level key whose name begins with `.` in an included file. Only
`local:` include entries are supported; a `remote:` or `project:` entry MUST be
rejected rather than ignored, so an unsupported reference fails loudly instead of
silently yielding an unresolved `extends`.

Merging MUST be shallow per top-level step key: a key present on the step replaces
the template's value wholly, including `output`, `rules` and `fallback`. With
several `extends` entries, templates MUST apply in declaration order with later
entries overriding earlier ones, and the step's own keys overriding all of them.

A template carries a step's **behaviour**, not its identity or its scheduling.
Concretely, a template MUST NOT declare `id`, `interactive`, `interaction` or
`resume_after`; each MUST be a load error naming the offending key. Every other step
key is inheritable — except `extends` itself, since templates are leaves.

A template key that is **not a known step field** MUST also be a load error, naming the
key and listing the real fields. Without that half of the rule a misspelled key such as
`promt:` is dropped silently by the typed decode, and in a template that loss is
multiplied across every flow extending it. A template is therefore deliberately
**stricter** than an inline step, which still tolerates an unknown key.

The reason is that the flow file is also parsed by the orchestrator — a separate
program that never resolves templates and reads a different subset of the schema.
`id` identifies the step in its task card and its postpone matching; `interactive`
and `interaction` decide reviewer-argument enrichment, so a template setting them
yields a job with nobody bound to answer; `resume_after` is read by the orchestrator to
compute a postpone-resume deadline, so a template setting it leaves the orchestrator
seeing no opt-in. Any key added to the flow schema later is
inheritable by default, and this list grows only when a specific key is shown to be
read by a second consumer.

A template MUST NOT itself declare `include` or `extends`, and neither MAY appear at an
included file's own top level — that rejects self-inclusion and rejects including a flow
file as if it were a template library. `local:` include paths MUST resolve against the
workspace root, as GitLab CI's do, so that one include line works from a flow at any
depth — including a team-hosted flow in its own directory; an absolute path MUST be
honoured as given. A `$ref`
inside a template MUST resolve relative to the **template** file, which is deliberately
a different rule and must be documented as such. The per-file size limit MUST apply to
each included file.

When two included files define a template of the same name, the later `include:` entry
MUST win and the collision MUST be logged — an author splitting one template across two
files would otherwise get silent shadowing.

Addendum — the `context` step field:

The `Step` struct gains a `context` field (`context: { paths, mode, nested }`), typed
as `Step.Context *contextfile.StepContext` — parallel to the `contextfile.AgentContext`
referenced by `config.Agent`. This field is **inheritable** via `extends`/`include`.
Concretely:

- `context` MUST NOT be listed in `nonInheritableStepKeys`
  (`internal/flow/include.go:182-187`). The orchestrator does not read the `context`
  field — it reads only `id`, `interactive`, `interaction`, and `resume_after`. A
  template that supplies `context` is safe: every flow that extends it inherits a
  consistent per-step context override, which is the correct and desired behavior.
- The reflection-driven merge in `mergeTemplates` (`internal/flow/include.go:259`)
  requires no code change for the new field: the merge iterates over declared YAML
  keys, and `context` is a standard top-level step key that overrides the template's
  `context` when the extending step declares its own.
- A template MAY declare `context` (it is not in `nonInheritableStepKeys`); an
  extending step that declares its own `context` key overrides the template's entirely
  (shallow merge, consistent with all other step fields).

Resolution of `step.Context` happens in `runStep` (`internal/flow/service.go:461`),
which passes the resolved `contextfile.StepContext` into agent construction (`NewAgent`
call at `service.go:513`) alongside the existing `agentID`, `outputSchema`, `step.ID`,
`step.Interactive`, and `boundPeers` arguments. The `${flow.id}` and `${flow.step}`
template tokens are populated explicitly from `f.ID` and `step.ID` at that call site —
`NewAgent` is context-free (`factory.go:207-208` discards its ctx), and the
`FlowIDContextKey` / `FlowStepIDContextKey` ctx values are set only later
(`service.go:654-656`) on the Run context for telemetry, so they cannot be the source
of the template tokens.

#### Scenario: A step inherits a template
- **WHEN** a flow includes a file defining `.resolve-team` with `agent` and `prompt`, and a step declares `extends: [".resolve-team"]` and no `agent` or `prompt`
- **THEN** the step runs with the template's agent and prompt, indistinguishable at execution from having declared them inline

#### Scenario: The step overrides an inherited key
- **WHEN** the template sets `maxTurns: 15` and the extending step sets `maxTurns: 20`
- **THEN** the step runs with `maxTurns: 20`, and every key it does not set keeps the template's value

#### Scenario: Overriding a block replaces it wholly
- **WHEN** the template defines an `output` block and the extending step defines its own `output`
- **THEN** the step's `output` is used unmerged, and no field of the template's block survives

#### Scenario: Several templates apply in order
- **WHEN** a step declares `extends: [".base", ".override"]` and both set `agent`
- **THEN** `.override`'s agent wins over `.base`'s, and a key set only by `.base` is still inherited

#### Scenario: A template declaring an orchestrator-read key is rejected
- **WHEN** a template declares `interactive: true`, `interaction`, `resume_after` or `id`
- **THEN** loading the flow fails with an error naming that key, because the orchestrator parses the same file without resolving templates and would see the key as absent

#### Scenario: A merged step is validated like an inline one
- **WHEN** a template supplies a `rules` entry naming a step the extending flow does not define
- **THEN** the flow fails validation with the existing unknown-rule-target error, because merging precedes validation

#### Scenario: An unknown template name is an error
- **WHEN** a step declares `extends: [".missing"]` and no included file defines it
- **THEN** loading fails naming the template, rather than running a step with an empty prompt

#### Scenario: A template may not nest composition
- **WHEN** an included file's template declares `include` or `extends`
- **THEN** loading fails, keeping the merge order one level deep and cycle-free

#### Scenario: Flows without include are unaffected
- **WHEN** a flow declares neither `include` nor `extends`
- **THEN** it loads and executes exactly as before, with no keys required by the include mechanism itself

#### Scenario: A declared prompt source is not merged with an inherited one
- **WHEN** a step declares `prompt` and its `extends` template declares `langfusePromptPath` (or the reverse)
- **THEN** the step inherits neither the competing key nor `langfusePromptLabel`, so the step's declared source replaces the template's rather than colliding on the exactly-one-source rule
- **AND WHEN** a step declares only `langfusePromptLabel`
- **THEN** it still inherits `langfusePromptPath` from its template, so a flow can run a shared prompt against a different label

#### Scenario: An include path is the same from any flow depth
- **WHEN** the same `include: - local: .agents/steps/x.yaml` line appears in a flow under `.agents/flows/` and in a team-hosted flow under `<team>/flows/`
- **THEN** both resolve to the same file, and a `$ref` inside that template resolves next to the TEMPLATE, not next to either flow

#### Scenario: An included file is subject to the size limit
- **WHEN** an included file exceeds the per-file size limit
- **THEN** loading fails naming that file, so `include` is not a way around the cap

#### Scenario: A misspelled template key is rejected
- **WHEN** a template declares a key that is not a step field, such as `promt:`
- **THEN** loading fails naming the key and listing the real fields, rather than dropping it silently as an inline step would

#### Scenario: A template supplies `context` and the extending step inherits it

- **WHEN** a step template declares `context: { paths: ["AGENTS.runtime.md"], mode: replace }`
  and a flow step `extends` that template without declaring its own `context`
- **THEN** the step runs with `AGENTS.runtime.md` as its only context file, identical
  to declaring `context` inline

#### Scenario: The extending step overrides the template's `context`

- **WHEN** the template declares `context: { paths: ["AGENTS.runtime.md"], mode: replace }`
  and the extending step declares `context: { paths: ["AGENTS.custom.md"], mode: append }`
- **THEN** the step runs with `AGENTS.custom.md` in `append` mode and the template's
  `context` is wholly replaced (shallow merge, consistent with `output`, `rules`, etc.)

#### Scenario: Flows without step `context` are unaffected

- **WHEN** a flow declares neither `context` on any step nor any `include` templates
  with `context`
- **THEN** it loads, validates, and executes exactly as before this change — no new
  required keys, no behavior change
