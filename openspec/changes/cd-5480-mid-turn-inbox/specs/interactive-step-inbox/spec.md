# Spec Delta

## Purpose

Defines how reviewer input that arrives while an interactive flow step's agent is working reaches that agent mid-turn, in order, without loss and without repeats, and how the bridge reports which input was delivered and which was not.

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

After a turn's tool results are stored and before the next model call, the agent loop SHALL remove every item from the session's inbox and store each item as its own user message, in order. This applies to every channel. The loop SHALL NOT interrupt a model call or a tool call that is in progress.

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

When the model ends a turn without a tool call, or a `struct_output` is accepted, and the session's inbox is not empty, the run SHALL drain the inbox as above and run another cycle instead of finishing. An accepted `struct_output` stays captured unless the model emits a new one. `max_turns` is the only bound.

#### Scenario: Input waits at a natural end turn
- **WHEN** the model ends its turn with text and the inbox holds M1
- **THEN** M1 is stored as a user message and the model is called again in the same run

#### Scenario: Input waits after struct_output
- **WHEN** `struct_output` is accepted and the inbox holds M1
- **THEN** M1 is stored as a user message, the model is called again, and if it ends without a new `struct_output` the earlier one is the run's output

### Requirement: Queued input SHALL never answer a question

Input that carries a message id SHALL never become a question's answer. When such input arrives while a question is pending, or a question is asked while such input waits, the bridge SHALL close the question as unanswered. The result tells the agent the user has not answered and has sent messages, and the input follows that result as user messages. Input with only already delivered ids SHALL NOT close a question.

#### Scenario: Queued batch arrives while a question is pending
- **WHEN** a batch of M1 and M2 (with ids) arrives while question Q is pending
- **THEN** Q's tool result states that the user has not answered and sent messages, followed by user messages M1 and M2
- **AND** neither M1 nor M2 is recorded as Q's answer

#### Scenario: Question asked while queued input waits
- **WHEN** the agent asks question Q while the inbox holds M1 (with an id)
- **THEN** Q is closed as unanswered without being shown to the peers, and M1 follows Q's result as a user message

#### Scenario: Only repeats arrive while a question is pending
- **WHEN** a batch whose ids were all delivered earlier in the run arrives while Q is pending
- **THEN** Q stays pending and the ids are acknowledged again

### Requirement: Plain-text input SHALL answer a pending question as before

An inbound without message ids (a typed message, a button click or a suggestion chip sent as plain text) that arrives while a question is pending SHALL answer it exactly as today.

#### Scenario: Typed answer
- **WHEN** a plain-text inbound "Approve" arrives while question Q is pending
- **THEN** "Approve" is Q's answer

### Requirement: Each message id SHALL reach the agent at most once per run

The bridge SHALL remember, per bound run peer, the message ids it has delivered to the agent, across the run's steps. An id delivered before SHALL be skipped and still acknowledged as delivered. The memory MAY be lost on a pod restart.

#### Scenario: Re-sent batch after a lost acknowledgement
- **GIVEN** M1 (id a) was delivered in step S1 and its acknowledgement was lost
- **WHEN** the sender re-sends M1 during step S2 of the same run
- **THEN** M1 is not stored again and id a is acknowledged as delivered

#### Scenario: Retry while persistence is in progress
- **WHEN** a sender re-sends M1 after it is drained but before persistence and delivery bookkeeping finish
- **THEN** the retry is not queued again and no acknowledgement is sent before persistence succeeds
- **AND** if persistence fails, M1 remains unacknowledged and a later retry can be queued

#### Scenario: Independent peers use the same id
- **WHEN** two bound peers each send a distinct message with id a to the same session
- **THEN** both messages are queued and delivered in order, with deduplication scoped to each originating peer

### Requirement: Inbox drains SHALL count against the turn budget only when they add a model call

A drain at a tool boundary SHALL NOT consume turn budget of its own. A cycle that runs only because the inbox held input at a run-end point SHALL count against `max_turns` like any other cycle. The forced max-turns wrap-up turn SHALL NOT drain the inbox.

#### Scenario: Wrap-up turn leaves input waiting
- **WHEN** the run reaches `max_turns` and the inbox holds M1
- **THEN** the wrap-up turn runs without M1, M1 is not acknowledged as delivered, and it is cleared with the session's other pending input when the step ends

### Requirement: The bridge SHALL acknowledge delivered input that carries an id

When inbox items that carry an id become part of the session, the bridge SHALL report their ids, in order, to the session's bound peers through any adapter that supports delivery acknowledgements. Input cleared without delivery SHALL NOT be acknowledged.

#### Scenario: Drained batch acknowledged
- **WHEN** M1 (id a) and M2 (id b) are drained into the session
- **THEN** the session's external peer receives one acknowledgement listing a then b

#### Scenario: Cleared input not acknowledged
- **WHEN** the step ends while the inbox still holds M1 (id a)
- **THEN** no acknowledgement names a

### Requirement: Chat-platform input left at step end SHALL be reported as not delivered

When an interactive step ends while its inbox still holds input without a message id (a Slack or Mattermost message), the bridge SHALL reply to that message's peer, in its thread, that it was not delivered and should be resent, before the step's bindings are removed. Input with a message id gets no such reply.

#### Scenario: Slack message left at step end
- **GIVEN** a Slack reviewer's message M1 still in the inbox
- **WHEN** the interactive step ends
- **THEN** the reviewer gets a short reply in the thread saying M1 was not delivered

### Requirement: The external adapter SHALL relay delivery acknowledgements as a `delivered` frame

The `external` adapter SHALL send an acknowledgement as a relay frame with `kind: "delivered"`, the session and job ids, a `delivered` object carrying a non-empty `messageIds` array, and no `text`. A non-202 response SHALL be a failed send, recorded on the adapter's status. Chat adapters SHALL treat an acknowledgement as a no-op.

#### Scenario: Frame shape
- **WHEN** the adapter acknowledges ids a and b for session S
- **THEN** the POSTed frame has `kind: "delivered"`, S's session id, the job id and `delivered` equal to `{messageIds: ["a", "b"]}`
