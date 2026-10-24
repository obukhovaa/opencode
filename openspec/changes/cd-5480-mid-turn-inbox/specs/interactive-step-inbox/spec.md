# Spec Delta

## Purpose

Defines how reviewer input that arrives while an interactive flow step's agent is working reaches that agent mid-turn, in order and without loss inside the step, and how the bridge reports which input was delivered.

## ADDED Requirements

### Requirement: Reviewer input on an interactive session SHALL wait in a per-session inbox

An inbound for a session marked interactive that does not answer a pending question SHALL be appended to that session's inbox in arrival order. A batched inbound SHALL be appended as one unit, so that no other input interleaves with it. The inbox SHALL keep its existing capacity and audible drop-oldest overflow.

#### Scenario: Batch appended atomically
- **WHEN** an inbound carrying messages M1 and M2 arrives for interactive session S with no question pending
- **THEN** S's inbox holds M1 then M2, with nothing between them

#### Scenario: Non-interactive session unaffected
- **WHEN** an inbound arrives for a session that no interactive step owns
- **THEN** it is dispatched exactly as before and the inbox stays empty

### Requirement: The agent loop SHALL drain the inbox after tool results and before the next model call

After a turn's tool results are stored and before the next model call, the agent loop SHALL remove every item from the session's inbox and store each item as its own user message, in order. The loop SHALL NOT interrupt a model call or a tool call that is in progress.

#### Scenario: Two messages arrive during a tool call
- **WHEN** M1 and M2 arrive while the agent's tool call T runs
- **THEN** the next model request ends with T's result followed by user message M1 and user message M2, with no assistant message or tool call between M1 and M2

#### Scenario: Slack reviewer on an interactive step
- **WHEN** a Slack-bound reviewer of interactive session S sends M1 while the agent's tool call T runs
- **THEN** the next model request ends with T's result followed by user message M1, exactly as for any other channel

#### Scenario: Message arrives during streaming
- **WHEN** M1 arrives while the model is streaming a response
- **THEN** the stream completes undisturbed and M1 is drained at the next tool boundary or turn end

### Requirement: A run SHALL NOT end while its inbox holds input

When the model ends a turn without a tool call, or a `struct_output` is accepted, and the session's inbox is not empty, the run SHALL drain the inbox as above and run another cycle instead of finishing. An accepted `struct_output` stays captured unless the model emits a new one.

#### Scenario: Input waits at a natural end turn
- **WHEN** the model ends its turn with text and the inbox holds M1
- **THEN** M1 is stored as a user message and the model is called again in the same run

#### Scenario: Input waits after struct_output
- **WHEN** `struct_output` is accepted and the inbox holds M1
- **THEN** M1 is stored as a user message, the model is called again, and if it ends without a new `struct_output` the earlier one is the run's output

### Requirement: A pending question SHALL take only the first inbox item as its answer

When a question is pending, or is asked while the inbox holds input, the first item SHALL answer it and every other item SHALL stay in the inbox. Those items are drained right after the question's tool result.

#### Scenario: Batch arrives while a question is pending
- **WHEN** a batch of M1, M2 and M3 arrives while question Q is pending
- **THEN** M1 is Q's answer and the next model request ends with Q's result followed by user messages M2 and M3

#### Scenario: Question asked with buffered input
- **WHEN** the agent asks question Q while the inbox holds M1 and M2
- **THEN** Q is answered with M1 without being shown to the peers, and M2 follows Q's result as a user message

### Requirement: Inbox drains SHALL count against the turn budget only when they add a model call

A drain at a tool boundary SHALL NOT consume turn budget of its own. A cycle that runs only because the inbox held input at a run-end point SHALL count against `max_turns` like any other cycle. The forced max-turns wrap-up turn SHALL NOT drain the inbox.

#### Scenario: Wrap-up turn leaves input waiting
- **WHEN** the run reaches `max_turns` and the inbox holds M1
- **THEN** the wrap-up turn runs without M1, M1 is not acknowledged as delivered, and it is cleared with the session's other pending input when the step ends

### Requirement: The bridge SHALL acknowledge delivered input that carries an id

When inbox items that carry an id become part of the session, either as a question answer or as drained user messages, the bridge SHALL report their ids, in order, to the session's bound peers through any adapter that supports delivery acknowledgements. Input cleared without delivery SHALL NOT be acknowledged.

#### Scenario: Drained batch acknowledged
- **WHEN** M1 (id a) and M2 (id b) are drained into the session
- **THEN** the session's external peer receives one acknowledgement listing a then b

#### Scenario: Cleared input not acknowledged
- **WHEN** the step ends while the inbox still holds M1 (id a)
- **THEN** no acknowledgement names a

### Requirement: The external adapter SHALL relay delivery acknowledgements as a `delivered` frame

The `external` adapter SHALL send an acknowledgement as a relay frame with `kind: "delivered"`, the session and job ids, a `delivered` object carrying a non-empty `messageIds` array, and no `text`. A non-202 response SHALL be a failed send, recorded on the adapter's status. Chat adapters SHALL treat an acknowledgement as a no-op.

#### Scenario: Frame shape
- **WHEN** the adapter acknowledges ids a and b for session S
- **THEN** the POSTed frame has `kind: "delivered"`, S's session id, the job id and `delivered` equal to `{messageIds: ["a", "b"]}`
