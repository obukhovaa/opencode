# chat-bridge (delta)

Delta spec for the `bridge-relay-intermediate-text` change. Restates only the
requirements that change; for the full specification see
`openspec/specs/chat-bridge/spec.md`.

## ADDED Requirements

### Requirement: Intermediate assistant text relay

During a bridge-dispatched run, the bridge SHALL relay to every peer bound to the
session the text of each assistant message that ended in `tool_use`, not only the
run's terminal message. The relayed message SHALL start with a header line
`⌛ <tool names>`, listing the message's tool calls in call order with duplicate names
kept, followed by a newline and the message text. `FILE:` tokens SHALL be handled as
they are for the terminal reply.

The bridge SHALL read the message from the session store when it receives the
`ToolCall` part that carries the call's merged input (`Finished` and non-empty
`Input`), and SHALL send the text before it emits anything else for that call. Parts
from descendant (subagent) sessions, synthetic parts and streaming parts without
input SHALL NOT trigger the relay.

Each assistant message SHALL be posted at most once per run, whichever of the
terminal path, the intermediate path or the question flush reaches it first. The
terminal reply SHALL carry no header.

This relay SHALL run at every tool-update verbosity, including with tool updates
disabled. It SHALL NOT change how tool calls, tool results or the progress card are
rendered.

#### Scenario: Text before a tool call

- **WHEN** an assistant message with text ends in `tool_use` with one `bash` call
- **THEN** the bridge posts `⌛ bash` followed by the text, once

#### Scenario: Several tool calls

- **WHEN** the message calls `bash` and then `question`
- **THEN** the header reads `⌛ bash, question`

#### Scenario: No text

- **WHEN** the `tool_use` message has no text and no attachments
- **THEN** nothing extra is posted

#### Scenario: Final reply

- **WHEN** the run's terminal message ends the turn with text
- **THEN** it is posted once, with no header

#### Scenario: Terminal message already relayed

- **WHEN** the terminal event carries a message the intermediate path already posted, or the intermediate trigger arrives for a message the terminal path already posted
- **THEN** no second post is made

#### Scenario: Text above the full-verbosity call card

- **WHEN** the live verbosity is `full` and tool updates are enabled
- **THEN** the intermediate text is sent before the call's 🔧 card

#### Scenario: Subagent, synthetic and streaming parts

- **WHEN** the triggering part belongs to a subagent session, is synthetic, or is a streaming publish with empty input
- **THEN** nothing is posted and the store is not read

#### Scenario: No bridge run in flight

- **WHEN** no bridge-dispatched run is in flight for the session
- **THEN** nothing is posted

### Requirement: Question widget follows its text

Before posting a `question` prompt for a session with a bridge-dispatched run in
flight, the bridge SHALL make sure the text of the session's latest assistant message
has been relayed. If no path has posted it yet, the question router SHALL post it
itself, bounding that send to 5 seconds. If another path is posting it, the router SHALL
wait for that post to finish, for at most 5 seconds. The router SHALL NOT create a dispatcher to do so.

#### Scenario: Text then widget

- **WHEN** the agent writes text and then calls `question`
- **THEN** the chat shows `⌛ question` with the text, and then the question widget
