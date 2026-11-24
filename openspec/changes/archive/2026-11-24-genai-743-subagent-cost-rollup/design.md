# Task cost accounting

## Run boundaries

The task tool holds an in-process slot for the child through its run and final accounting. For a new task the creation result supplies its opening balance. On resume the tool reads the child row after claiming the slot. Each worker therefore starts from the same persisted source, even if another worker handled the previous invocation.

OpenCode sends a terminal event before finishing post-turn compaction. The task tool retains the event and waits for the run channel to close before taking the final cost snapshot. It credits the positive difference to the calling session before returning the result or publishing asynchronous completion. A bounded accounting context survives cancellation of the caller. The slot is removed on exit, including errors and recovered panics.

The slot coordinates calls within one process. Each run accounts for usage recorded during its lifetime. Flow tasks drain their own background work before answering. A task process that exits before recording completion has the same recovery limitation as other unpersisted completion side effects.

## Storage

Each cost writer uses an atomic increment on the session row. Model calls update last-call tokens, cumulative tokens and cost in one statement. Compaction also stores its summary message ID in that statement. General session saves omit the cost column, which prevents callers holding an older copy of the session from overwriting later charges.

SQLite returns the updated row from the write statement. MySQL reads it back after the update. If that read fails after a successful write, the error identifies the committed write and the retry path does not repeat the charge. Transient conflicts on the write itself use the database retry policy.

## Reporting

A flow step reads its session's inclusive cost. Summing the step rows gives the flow cost. Adding child session rows to that sum duplicates charges.
