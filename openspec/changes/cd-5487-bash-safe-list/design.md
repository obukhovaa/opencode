## Context

Run computes IsSafeReadOnlyCommand before its policy block and entirely skips
that block for a matching prefix. Some entries are arbitrary-command wrappers.
The name also misleads callers: go run/test and env are not read-only authority.

## Goals / Non-Goals

Goal: no safe-list entry can evade a configured allow/ask/deny rule.
Non-goals: changing parallel scheduling, inspecting an allowed interpreter's
program, or OS containment. Keep CD-5483's compound/expansion/dialect gate.

## Decisions

1. Remove the safe-list conditional around the common gate. Every invocation
   evaluates policy before foreground execution, wait interception or detached
   task creation. Do not maintain a second, inevitably incomplete wrapper list.
2. Keep IsSafeReadOnlyCommand only as the existing scheduling hint. Update its
   comments/tests so it is not described as a permission exemption.
3. Use existing rule and prompt semantics without a new config flag. An
   unconfigured pwd now asks, rather than silently bypassing the default ask.
4. Stack this focused PR on #110; merge/rebase/retarget after #110 is approved.
   Keep the two security findings independently reviewable.

## Risks / Trade-offs

- Extra prompts for formerly exempt commands → Accepted security tightening;
  operators use their existing rules to grant intended actions explicitly.
- Allowed wrappers remain powerful → Accepted: this enforces the chosen rule,
  not the behavior of a deliberately allowed program.
- Incomplete tests accidentally remove the whole tool → Cover deny-all in
  both modes, a single rejected ask, and an explicitly allowed harmless wrapper.
- Same guard touched by #110 → Stack and make the dependency explicit; no
  independent release or merge before the base change.

## Migration Plan

Review #110 first, then this follow-up. Human merge/release/deployment follow.
No workspace policy is edited automatically and no deny rule is relaxed.

## Open Questions

None. The ticket delegates the design to the engine owner.
