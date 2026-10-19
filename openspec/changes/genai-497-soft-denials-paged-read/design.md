# Design — Soft Permission Denials, Paged Reads, Overflow Recovery, No Headless Titles

## 1. Telling a rule denial from a rejected prompt

Every tool already separates the two cases in code: the `case permission.ActionDeny:` branch after `EvaluatePermission` is the rule, the `if !p {…}` after `permissions.Request` is the human (or the bridge's `permissionMode: deny` acting for one). Both returned the same sentinel, so the agent loop could not tell them apart.

The rule branch now returns `permission.DeniedByRule(tool, input)`, a `*RuleDeniedError` whose `Is` matches `ErrorPermissionDenied`. Callers that only ask "was this denied?" (`errors.Is`) keep working; the agent loop checks `permission.AsRuleDenied` first and takes the soft path. The prompt branch is untouched, so a rejected prompt still ends the run exactly as before, cancelling the turn's other calls.

The read-category tools (`read`, `view_image`, `grep`, `glob`, `ls`) turn a denial into a `Permission denied` text result themselves, for both cases; main's canonical-path checks (CD-5247) made that their contract. They now match with `errors.Is(err, ErrorPermissionDenied)`, so a `*RuleDeniedError` keeps that behaviour: their denials stay text results, never reach the agent loop and do not count towards the breaker.

The denial message is written for a model that has to keep working: it names the tool and the denied input (cut at 512 bytes: the model holds the full input in its own call), says a retry will be denied again, and says the rule forbids the action itself, so reaching the same effect through another command, tool or script is not allowed either. "Choose another approach" alone invites exactly that workaround (`rm` denied → `find -delete`). The matched rule pattern is not named: `agentregistry.Registry.EvaluatePermission` returns only an `Action`, and exposing the pattern means changing the evaluation API for every caller.

PreToolUse hook blocks and `skill` permission denials were already error results and are unchanged; they do not count towards the breaker.

## 2. The circuit breaker

A model that keeps hitting denied calls should not burn the whole turn budget. The breaker lives on the run's `callTracker` (created per `processGeneration`), observed once per turn after all its tool calls have settled:

- **3 consecutive turns with no progress, starting from a rule denial.** Counted per turn, not per call: one turn of parallel calls that all hit the same rule was sent before the model saw any denial, and ending the run there would bring back the original defect. Progress means a call that ran and was not rule-denied; it resets the streak. A call answered without running (loop detected, tool not found, blocked by a PreToolUse hook) is not progress: loop detection fires on the third identical call, *before* the tool runs, so counting it as progress would let a model repeating the same denied command reset the streak forever and run to `maxTurns` — worse than the old behaviour, which stopped at the first try.
- **10 rule-denied calls per run**, however they interleave with other calls, so a model alternating denied and allowed calls is still bounded.

When either trips, the assistant message is finished with `FinishReasonPermissionDenied`, which ends the loop the way a rejected prompt does — except on a turn that delivered an accepted `struct_output`: the run finishes there anyway, and `processGeneration` captures `struct_output` only from a turn still finished with `tool_use`, so tripping would drop the step's output. The bounds are constants (`maxConsecutiveRuleDeniedTurns`, `maxRuleDenialsPerRun`): nothing asks for them to vary per workspace yet, and adding a config field later is cheap.

## 3. Paged reads

The ceiling exists to bound what one call puts in the context. For a whole-file read that is the file size; for a paged read it is the window. So:

- `offset == 0 && limit <= 0` on a file over `MaxReadSize` is declined, with a message naming `offset`/`limit` and `grep`.
- Any other read streams the file through a `bufio.Reader`, keeping only the requested window. `ReadLine` hands over a line longer than the buffer in chunks, which are cut to `MaxLineLength` as they arrive, so a multi-megabyte minified line is neither held whole nor an error (`bufio.Scanner`'s 64KB token limit made it a hard tool error before). The remaining lines are still scanned for the total count, which the continuation note reports.
- The returned text is capped at `MaxReadSize` bytes as well as at 2000 lines: the worst case is otherwise 2000 × 2000 bytes ≈ 4MB, which is the context-budget concern the `webfetch-output-limit` design deferred this over. A capped page says so and gives the offset to continue from.
- A file over the ceiling skips `NotifyOpenFile` and `FormatDiagnostics`: the LSP client's `didOpen` reads and sends the whole file.
- `recordFileRead` runs for paged reads as before, so read-before-edit tracking is unchanged.
- The stream reads through the call's context: counting lines reads a file of any size to its end, which a cancelled turn, or a call the tool watchdog abandoned, must not keep doing.
- Paging changes nothing before the size check: `authorizeRead` (CD-5247's canonical-path check) and `openForRead`'s verified open apply to every read, paged or whole.

## 4. Overflow recovery

The in-loop compaction block (compact, reload from the summary, re-inject the schema envelope and the deferred-tools delta, carry the task budget, rebuild `msgHistory` from the preserved tail / user turn / synthetic tail, re-count) moves into a closure, `compactInLoop`, shared by the threshold path and the recovery.

The recovery runs only on the main model call, when all of these hold:

- the error came from the provider's response stream (`modelCallError`, set where `streamAndHandleEvents` processes provider events). An error after the turn's tools ran — saving their results — is excluded, because resending that turn would make the model re-issue calls whose side effects already happened;
- the error is not a cancellation, and the run's context is not done: a flow step past its timeout fails its call with `DeadlineExceeded`, which near the window would otherwise classify as an overflow and run the compaction on a dead context (recording a backoff failure for the session);
- `autoCompact` is on;
- no recovery has run yet in this `processGeneration` (one per run: a history no compaction can shrink fails instead of looping);
- `contextOverflowCause` classifies it: `provider_error` when the error text carries a known overflow marker (Anthropic/Vertex `prompt is too long`, `exceed context limit`; Bedrock `Input is too long`; OpenAI `context_length_exceeded`, `maximum context length`, the Responses API's `exceeds the context window`; Gemini `exceeds the maximum number of tokens`; Moonshot/Kimi `exceeded model token limit`; LiteLLM's `ContextWindowExceededError`, which it raises for every provider it maps, with wrapped text that need not carry any other marker), else `near_window` when the context count was at least 90% of the window, the same test `warnIfLikelyContextOverflow` uses for proxies that reset the stream instead of returning the 400;
- `compactionAllowed` agrees, so the backoff in `compaction_backoff.go` is respected.

The failed call's message is finished as an error and its tool calls (if any streamed) get error results before compacting, so the history stays well-formed; both sit before the new summary and drop out on reload. The retry is sent inline rather than through `continue`, so it does not consume a turn and the threshold check at the top of the loop cannot run a second compaction first. The call's result only replaces `agentMessage`/`toolResults` once it succeeds, because the rebuild re-appends the previous cycle's pair from them. If the compaction fails, or the retry fails for any reason other than cancellation, the original error is returned and the retry's error is logged.

`near_window` can turn a transient failure close to the window (a 529, a rate limit after retries) into a compaction. That compaction was close to due anyway (the default threshold is 95%), and the recovery is once per run and gated by the backoff; the cause is logged so such cases are visible.

The forced wrap-up call at max turns keeps its current handling: it already degrades to the previous message instead of failing the run.

## 5. Title generation

The permission service already marks the sessions nobody watches: `MarkUnattendedSession` is set by the flow runner for every step, by `opencode -p` and by the cron scheduler for unwatched jobs, and resolves through `LinkSession`, which the task tool calls before a subagent's first run. Interactive flow steps carry the unattended mark too, plus `MarkInteractiveSession`. Title generation now runs only when the session is not unattended, or is interactive. The agent keeps a reference to the permission service for this; with none (tests building the struct directly), titles are generated as before.

A flow step session keeps the title it was created with; a cron or `-p` session likewise.

## Risks

- **A workspace relied on `deny` ending the run.** The step now continues. The denial message tells the model to report the block, the breaker bounds a stubborn model, and a hook or a rejected `ask` still ends the run outright.
- **More context per paged read than before.** Bounded by `MaxReadSize` per call, which is what a whole-file read of a file at the ceiling already returned.
- **A lossy compaction after a transient error near the window.** See §4.
- **An oversized tool result in the preserved tail.** The rebuild re-appends the previous cycle's call and result after the summary. When that result is what overflows the window, the retry overflows again and the run fails with the original error, as before this change. Truncating a preserved result is out of scope.
- **No "strictly smaller" gate before the retry.** Comparing the recount with the pre-call count was considered and rejected: the pre-call count is the number that proved wrong (the call overflowed although it was under the threshold), so gating on it can block a retry that would succeed. The once-per-run cap bounds the cost of a useless retry to one call.
