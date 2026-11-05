## Context

The existing flow runtime already persists step sessions and flow_states, with crash recovery, bounded iterations and question tools. The TUI currently dispatches only ordinary turns.

## Goals / Non-Goals

Goals: reuse the runtime, isolate dynamic policy, keep chat responsive and preserve root identity across restarts. Non-goals: silently install executables or mutate completed step definitions.

## Decisions

- Add optional run-definition/root/boundary hooks to existing runtime. Defaults retain standalone behavior.
- A primary agent builds the definition using embedded flow-creator guidance, then calls opt-in `flow`; the controller starts execution asynchronously after submission. Missing OpenSpec produces installation guidance before any planning side effects.
- Persist one atomic private JSON manifest per root session under the profile dynamic flows directory. It contains the validated definition and pinned agent; actual step histories remain in the database.
- Size presets XS/S/M/L/XL/XXL cap steps, parallel agents, turns and iterations. Explicit configuration wins; otherwise builder chooses. All steps use the pinned primary or a configured subagent; model overrides and recursive dynamic flows are forbidden.
- Sequential topology is preferred; review fanout uses task subagents. Boundary steering is delivered before the next step. Executed definitions cannot be rewritten.
- TUI page lists current states and exposes cancel/resume/terminate. Session lists show roots; standalone first-step roots remain visible.

## Risks / Trade-offs

- Process interruption → persist definition before scheduling; resume through existing RecoverRunning semantics.
- Concurrent edits → immutable per-run definitions and mutex-protected coordinator state.
- Wrong root ownership → validate session association before controls and scope persistence by profile.
- External side effects are not rolled back by cancellation; UI reports cancellation honestly.

## Migration Plan

Opt-in only; old profiles and standalone definitions need no migration. Disabling dynamicFlow prevents new plans without deleting existing manifests.
