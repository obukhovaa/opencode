# Spec Delta

## ADDED Requirements

### Requirement: POST /router/inbound SHALL accept a batch of messages with ids

`POST /router/inbound` SHALL accept an optional `messages` array of `{id, text}` objects as an alternative to `text`. Each `id` and `text` SHALL be non-empty. A body that carries both `messages` and `text` or attachments, or neither, SHALL be rejected with 400. A batch SHALL enter the session in one step, never split across sessions or interleaved with other input.

#### Scenario: Valid batch accepted
- **WHEN** an inbound with `messages: [{id: "a", text: "Make my discounts steeper"}, {id: "b", text: "except for Premium"}]` is posted for an owned interactive session
- **THEN** the response is 202 and both messages enter the session's inbox in order

#### Scenario: Mixed body rejected
- **WHEN** an inbound carries both `text` and `messages`
- **THEN** the response is 400 and nothing is enqueued

#### Scenario: Empty id rejected
- **WHEN** a `messages` entry has an empty `id`
- **THEN** the response is 400 and nothing is enqueued
