# flow-api (delta)

Delta spec for the `scoped-context-files` change. Restates only the requirements
that change; unchanged requirements are not repeated here. For the full specification
see `openspec/specs/flow-api/spec.md`.

## MODIFIED Requirements

### Requirement: Flow files compose shared step definitions via include and extends

*(Existing requirement heading preserved. Text is unchanged; a new field clarification
is appended.)*

The `Step` struct gains a `context` field (`context: { paths, mode, nested }`) parallel
to the `AgentContext` type on `config.Agent`. This field is **inheritable** via
`extends`/`include`. Concretely:

- `context` MUST NOT be listed in `nonInheritableStepKeys`
  (`internal/flow/include.go:182-187`). The orchestrator does not read the `context`
  field — it reads only `id`, `interactive`, `interaction`, and `resume_after`. A
  template that supplies `context` is safe: every flow that extends it inherits a
  consistent per-step context override, which is the correct and desired behavior.
- The reflection-driven merge in `mergeTemplates` (`internal/flow/include.go:244`)
  requires no code change for the new field: the merge iterates over declared YAML
  keys, and `context` is a standard top-level step key that overrides the template's
  `context` when the extending step declares its own.
- A template MAY declare `context` (it is not in `nonInheritableStepKeys`); an
  extending step that declares its own `context` key overrides the template's entirely
  (shallow merge, consistent with all other step fields).

Resolution of `step.Context` happens in `runStep` (`internal/flow/service.go:396`)
where `FlowIDContextKey` and `FlowStepIDContextKey` are already set in the context
(service.go:558-561). The resolved context object is passed into agent construction
alongside the existing `agentID`, `outputSchema`, `step.ID`, `step.Interactive`, and
`boundPeers` arguments.

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
