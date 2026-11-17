## Context

Flow output currently bypasses rich rendering and verbosity, posting raw event bodies. Durable flow events and fenced delivery receipts already exist. Native adapters implement text editing, but their existing tool-card caches are ephemeral. See proposal.md for motivation.

## Goals / Non-Goals

Goals: reusable native flow cards, recoverable delivery, bounded output and unchanged run execution. Non-goals: new callback actions, a new verbosity option, orchestrator rendering changes, or changes to LLM prompts/tools.

## Decisions

- Introduce a bridge-owned presentation model and optional native flow send/edit interface. Keep the external relay path unchanged; it has a different acknowledgement protocol.
- Persist message token, applied event sequence and bounded presentation state per run and recipient under the existing delivery lease. Reconstruct progress from that run's event journal, never the conversation's current run. A restart can then continue editing the same message.
- Coalesce nonterminal updates and suppress identical rendered states. Flush terminal/failure/waiting updates promptly. Do not acknowledge platform errors, including errors returned inside SendResult rather than as Go errors.
- Add optional presentation metadata to stored events: flow identity/order, agent, structured-output flag and timestamps. Old events retain a readable fallback. No protocol-version change for additive fields.
- Compact mode contains statuses, duration and a short terminal result/failure. Full mode adds bounded folded details. Disabled tool updates keep significant lifecycle outcomes visible. User question widgets stay separate.
- Slack: native plan/task_card and rich_text_preformatted JSON, with an independent unsupported-block fallback. Mattermost: props.mm_blocks collapsible sections plus a useful message fallback. Telegram: HTML expandable blockquotes with escaped, indented JSON; do not nest pre/code inside spoilers or quotations.
- Bound the aggregate card, not only individual outputs. Preserve state counts and active/error steps when a long plan needs windowing; visibly mark omitted content.

References: https://docs.slack.dev/reference/block-kit/blocks/plan-block/ ; https://docs.slack.dev/reference/block-kit/blocks/task-card-block/ ; https://docs.mattermost.com/developers/integrate/reference/mm-blocks ; https://core.telegram.org/bots/api#formatting-options

## Risks / Trade-offs

- Remote send can succeed immediately before local persistence fails → retain existing at-least-once delivery semantics; do not claim impossible exactly-once cross-system delivery.
- Deleted messages → replace only on a definite missing-message error; transient errors retry the stored token.
- Older Mattermost servers may ignore mm_blocks → compact text fallback is always present.
- Telegram cannot combine code formatting and spoilers arbitrarily → expandable escaped quotations preserve JSON structure without invalid entity nesting.
- Rich platform limits differ → adapter contract tests cover aggregate size, Unicode and markup escaping.

## Migration Plan

Add an additive SQLite/MySQL table for card state, scoped by run and recipient and removed with the run. Existing rows and receipts remain valid. Ship as v0.31.2 after full checks, isolated adapter/runtime tests and review. Older binaries ignore the extra table; execution state is unchanged.
