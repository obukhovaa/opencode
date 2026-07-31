## 1. Schema and loader

- [ ] 1.1 Add `Extends []string \`yaml:"extends,omitempty"\`` to `Step` in `internal/flow/flow.go`, and a `stepTemplate` type for the inheritable subset. Model the template as its own type rather than reusing `Step`: reusing `Step` would make every future step field inheritable by default, which is the exact failure D4's allow-list exists to prevent.
- [ ] 1.2 Add `Include []IncludeEntry \`yaml:"include,omitempty"\`` to `flowFile`, with `IncludeEntry{Local string}`. Decode unknown include kinds (`remote`, `project`) as an error rather than an empty entry — an ignored include leaves `extends` unresolvable and the error would otherwise point at the wrong line.
- [ ] 1.3 In `parseFlowFile`, before `$ref` resolution and `validateFlow`: read each include, apply the per-file size cap, decode its templates, resolve each template's `output.schema` `$ref` against the TEMPLATE's dir, then merge each step's `extends`. Order matters and is asserted by 2.6 — merging after validation would let a template smuggle an invalid step through.
- [ ] 1.3b Name the mechanism, because the obvious implementation cannot work: the loader does ONE typed `yaml.Unmarshal` (`registry.go:341`), so a step's *raw* keys — which 1.7 needs to decide what overrides — are not available on that path. Either re-parse the flow bytes into a map, merge at map level, and re-decode; or give `Step` a custom `UnmarshalYAML`. Precedent for the first: `validateFlowSessionKeys` (`:352`) already re-parses the original bytes, and does so from `data`, so it is unaffected by a merge applied later.
- [ ] 1.4 Enforce the allow-list: a template key outside `agent|prompt|session|output|rules|fallback|maxTurns|maxIterations|timeout|compact` is a load error naming the key. Decode templates into a raw map first so an unknown key is *visible* — a typed decode silently drops it, which is how `flow.session` typos used to pass and why `validateFlowSessionKeys` exists.
- [ ] 1.5 Reject `include` or `extends` inside a template file (D6), and an `extends` naming a template no include provides.
- [ ] 1.6 Resolve include paths relative to the including file's directory, and a template's output-schema `$ref` relative to the template file's directory. Reuse `format.ResolveSchemaRef` with the template's base dir rather than the flow's.
- [ ] 1.7 Implement the merge: shallow per key, templates left to right, then the step's own keys. Guard the zero-value trap — on a typed struct an explicit zero is indistinguishable from unset, so the step's *raw* keys must decide what overrides, not the decoded values. The affected inheritable fields are `maxTurns`, `maxIterations`, `timeout: ""`, `agent: ""`, `prompt: ""`, and — least obvious — **`session`**, because `Session StepSession` is a value type (`flow.go:57`) so `session: {fork: false}` reads as unset. `compact` is safe (`*StepCompact`, nil is observable). NOT `interactive`: the allow-list forbids it in a template, so it can never be inherited.

## 2. Tests

- [ ] 2.1 A step inheriting agent + prompt from a template executes identically to the inline equivalent.
- [ ] 2.2 Per-key override, including the zero-value case from 1.7: a step setting `maxTurns: 0` and a step omitting `maxTurns` entirely must be distinguishable, and `session: {fork: false}` must override a template's `fork: true`. If the merge cannot tell an explicit zero from unset, 1.7 is not done.
- [ ] 2.3 Overriding `output` / `rules` replaces the block wholly — assert a field of the template's block is *absent*, not merely that the step's is present.
- [ ] 2.4 Several `extends` apply in declaration order, and a key set only by the first template survives the second.
- [ ] 2.5 Each of `id`, `interactive`, `interaction`, `resume_after` in a template is a load error naming the key. Table-driven, so a future field added to the allow-list has an obvious place to be argued for.
- [ ] 2.6 A template's `rules` naming an unknown step fails with the existing validation error — the assertion that merging precedes validation.
- [ ] 2.7 Unknown template name, `remote:`/`project:` include kind, and `include`/`extends` nested in a template each fail with their own message.
- [ ] 2.8 Include path resolution: relative to the flow's dir, absolute honoured, and a template's `$ref` resolved against the TEMPLATE's dir — the last one being the mistake 1.6 exists to avoid.
- [ ] 2.9 A per-included-file size breach is rejected, so `include` is not a way around `OPENCODE_MAX_FLOW_FILE_SIZE`.
- [ ] 2.10 Regression: an existing flow with neither key loads byte-identically. Run the full suite and `make check`.

## 3. Documentation

- [ ] 3.1 Document `include` / `extends` in the flow section of `AGENTS.md` (or the flow doc it points at), including the allow-list and *why* it is an allow-list — a reader who does not know a second parser exists cannot infer the constraint from the code.
- [ ] 3.2 NOT NEEDED, resolved by review: `opencode-schema.json` describes the opencode *config*, not flow YAML, and no flow-YAML schema exists in either repo. Worth recording the consequence rather than just closing the task — nothing anywhere will ever flag `include`/`extends` as an unknown key, which is exactly why an older runtime accepts a flow it cannot resolve (see the workspace change's deploy-ordering risk).

## 4. Release and hand-off

- [ ] 4.1 Cut an opencode tag carrying this, and record it here. The Piano agent image clones opencode at a pinned tag (`build/agent.dockerfile`), so the workspace migration cannot begin until the image is rebuilt on that tag — this is the real ordering constraint between the two MRs, not their merge order.
- [ ] 4.2 Verify against the migration's actual shape before it is merged: load the workspace's seven flows with the shared `resolve-team` template and confirm each resolves to the same step it had inline. The workspace change (piano-developer) owns the diffing; this task owns confirming the engine loads them.
- [ ] 4.3 NOT DOING, recorded so it is not re-litigated: implementing the resolver in c2-agent, or extracting a shared flow-schema module. See design D4 for why the allow-list is the answer at this size.
