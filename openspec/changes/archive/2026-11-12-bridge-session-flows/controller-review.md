# Controller implementation review

Implemented generated planning/direct definition submission, immutable named runs,
status/list/control, distinct invocation identities and conversation ancestry,
backend ownership, legacy import and private versioned exports. Named definitions
retain their authored agents/models/graph and within-run forks; authored top-level
session prefixes are deliberately namespaced by invocation so conversations
cannot share step sessions accidentally.

Review corrections include:

- Copying the resolved project ID into both sessions and flow storage at App.New.
- Fenced flow-state and transcript writes, pre-tool/model ownership guards,
  atomic terminal result/outbox writes and atomic input-delivery receipts.
- Durable question identity and late-answer recovery, including an accepted answer
  that races with a persisted cancellation result.
- Durable interaction targets when steering interrupts a blocking question; exact
  session injection survives a crash before the live question is closed. Visible
  question identity outranks ID sorting; stale hints never select hidden requests.
  A controller question-start hook consumes input accepted before Ask without
  publishing an obsolete widget.
- Generated prompt shell markup is rejected; authored named markup uses the
  caller's bash policy, ownership guard and bounded shell without rescanning
  substituted values or command output.
- Ordinary submitting-turn questions are not mistaken for controller planner
  questions. Root transcript steering is inserted only after a safe tool/turn
  boundary and is deduplicated by stable message identity.
- Accepted input stays accepted if live question wakeup subsequently fails; the
  controller pauses and recovery handles the durable target.
- Turn admission prevents an ordinary root run from competing with an active
  durable flow, including after process restart.
- Fixed-size planning obeys that size's limits. Automatic planning uses an M
  ceiling before the builder selects the execution size.
- Named runtime arguments cannot be replaced on recovery; only redacted
  credentials are rehydrated from trusted admission. Remote prompts are pinned
  once and still receive supplemental steering.
- Conversation deletion coordinates local cancellation and atomic metadata/tree
  deletion; deleting an individual durable step is refused.
- `argsJson` keeps the model-facing flow tool compatible with Gemini, while HTTP
  callers retain object-valued `args`.

The focused race suite passed for app/controller, flow runner, tools, agents,
provider serialization and relevant bridge tests. Tests include real SQLite and
flow-engine execution, named runs without OpenSpec, sequential runs and retries,
profile/filesystem-independent recovery, source removal, remote prompt pinning,
credential redaction, exact question recovery and cancellation races, input
acknowledgement, root/tool-result ordering, typed origin admission, session deletion,
legacy ownership refusal, expired-deadline termination, constructor project scope,
and planning resource limits. See `internal/app/flow_sessions_test.go`,
`internal/llm/provider/flow_tool_gemini_test.go` and the existing runtime suites.

Upgrade boundaries: legacy MySQL runs still marked running/waiting have no
historical lease and must be reconciled through their original orchestrator before
adoption. External effects already sent before ownership loss cannot be undone.
Local profile exports are best-effort; the configured session backend is the
recovery source of truth. Background task crash recovery retains the existing
parent-step/task-session behavior; this change does not introduce a nested task
scheduler.
