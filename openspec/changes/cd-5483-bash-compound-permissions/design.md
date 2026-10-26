## Context

The existing safe-list excludes compound commands, but a non-exempt command is
still passed as one string to Registry.EvaluatePermission. A prefix allow glob
therefore swallows executable suffixes. The common gate precedes foreground
execution, wait interception, and background task creation.

## Goals / Non-Goals

Goals: every statically present executable part must pass existing rules before
any part starts; preserve deny/ask semantics and quoted literal data.

Non-goals: an OS sandbox; resolving variable values, aliases, functions carried
from earlier persistent-shell calls, or the behavior of an allowed script or
interpreter; changing the existing simple-command safe list; deployment.

## Decisions

1. Use mvdan.cc/sh/v3's Bash parser, not delimiter splitting. Walk the syntax
   tree so nested substitutions, heredocs, process substitutions and compound
   containers are recognized without executing or expanding the input.
2. Reuse Registry.EvaluatePermission through a callback. Keep its agent/global
   precedence and deny-list/allow-list checks. Retain the original invocation's
   verdict, then combine every executable statement's verdict: deny > ask >
   allow. A default/explicit whole-command deny is not weakened just because
   its individual parts would allow.
3. Evaluate executable source slices, excluding structural containers,
   negation and separator tokens. Traverse structural statements, including
   unreachable branches and function bodies; do not predict runtime routing.
4. Unsupported/malformed Bash is a rule denial. An allow glob must not become a
   fallback for syntax the policy layer could not inspect. Other configured
   shells retain execution, but require input compatible with this parser.
5. Keep the check at the existing common gate. Rejections happen before the
   first foreground command or creation of a detached task. Keep one full-call
   prompt for ask. Do not apply the safe-list exemption separately to parts.

## Risks / Trade-offs

- Extra denials/prompts for compound-only rules or unsupported dialect syntax
  → Accepted for this security fix; document exact behavior and preserve the
  original verdict instead of weakening existing restrictions.
- Parser dependency and toolchain drift → pin v3.12.0; the latest parser
  requires Go 1.26, whereas this pin supports the project's Go 1.25.8.
- Confusing command matching with containment → document the non-goals above;
  no claim that this prevents an allowed interpreter/script from acting with
  its existing privileges.
- Structural-wrapper rejection hiding a missing recursive check → include an
  allow-by-default regression corpus with a narrowly denied nested command.

## Migration Plan

Merge and release the engine after review; operators update their engine pin.
No automatic workspace policy edits or deployment are part of this change.
Keep prefix allow rules narrow; when a compound invocation needs an additional
command, that part needs its own allow rule as well. Rollback restores the
known permission exposure, so prefer correcting a legitimate rule or dialect
restriction over reverting the gate.

## Open Questions

None. The ticket delegates this design to the engine owner. Human review is
still required before merging/releasing the security fix.
