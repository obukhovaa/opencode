## Context
Chat already caches message renders, but tool-state recomputation performs a quadratic call/result join. Background-task registry and persisted child messages supply activity details. /agents already lists agent types.

## Goals / Non-Goals
Goals: responsive long sessions and discoverable session activities.
Non-goals: changing task completion notification or persistence semantics.

## Decisions
- Use /subagents for live subagents, preserving /agents configuration browser.
- Reuse registry cancellation and message renderer; include synchronous descendant sessions where available, including tasks under flow steps.
- Load details asynchronously and bound output-file previews. Poll only while viewing activity pages; chat counts refresh on a bounded tick.
- Replace quadratic tool/result matching with indexed lookup. Avoid re-rendering unchanged chat history during ordinary editor input.
- Use an above-editor activity strip; Up on an empty editor or vim NORMAL k focuses it, Enter opens activities; Esc/Down restores typing.

## Risks / Trade-offs
- Live task state changes during reads → immutable snapshots and registry atomic getters.
- Large transcripts/output files → bounded previews and existing message rendering.
- Navigation conflicts → focus only from empty editor or vim NORMAL; completion dialogs retain priority.
