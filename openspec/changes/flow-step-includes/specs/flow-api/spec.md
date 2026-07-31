# Spec delta: Flow API

## ADDED Requirements

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

Only step keys consumed by the flow engine itself SHALL be inheritable:
`agent`, `prompt`, `session`, `output`, `rules`, `fallback`, `maxTurns`,
`maxIterations`, `timeout`, `compact`. A template declaring any other key — in
particular `id`, `interactive`, `interaction` or `resume_after` — MUST be a load
error naming the offending key. This MUST be enforced as an allow-list rather than
a deny-list: the flow file is also parsed by the orchestrator, which is a separate
program that never resolves templates and which reads a different subset of the
schema, so a key it reads must not be inheritable, and a key added to the schema
later must be non-inheritable until deliberately admitted.

A template MUST NOT itself declare `include` or `extends`. Include paths MUST
resolve relative to the directory of the file declaring them, matching output-schema
`$ref` resolution, and a `$ref` inside a template MUST resolve relative to the
template file. The per-file size limit MUST apply to each included file.

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
- **THEN** it loads and executes exactly as before, with no new required keys
