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
yields a job with nobody bound to answer; `resume_after` is read by the orchestrator
and is not modelled by the engine at all. Any key added to the flow schema later is
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

#### Scenario: An include path is the same from any flow depth
- **WHEN** the same `include: - local: .agents/steps/x.yaml` line appears in a flow under `.agents/flows/` and in a team-hosted flow under `<team>/flows/`
- **THEN** both resolve to the same file, and a `$ref` inside that template resolves next to the TEMPLATE, not next to either flow

#### Scenario: An included file is subject to the size limit
- **WHEN** an included file exceeds the per-file size limit
- **THEN** loading fails naming that file, so `include` is not a way around the cap

#### Scenario: A misspelled template key is rejected
- **WHEN** a template declares a key that is not a step field, such as `promt:`
- **THEN** loading fails naming the key and listing the real fields, rather than dropping it silently as an inline step would
