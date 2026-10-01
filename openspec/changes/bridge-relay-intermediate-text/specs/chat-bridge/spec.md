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

The relay SHALL cover only the bridge-dispatched run itself: parts published before
the run started — by another actor's run that held the session while the bridge
waited for it — SHALL NOT be relayed. A part SHALL be checked against the run that
received it, even when it is handled after that run ended.

Each assistant message SHALL be posted at most once per run, whichever of the
terminal path, the intermediate path or the question flush reaches it first. The
terminal reply SHALL carry no header. Before posting it, the bridge SHALL wait for
intermediate posts of the run that are still in flight, for at most 5 seconds.

One intermediate post SHALL be bounded to 10 seconds, or 60 seconds when it carries
attachments, so a chat API that does not answer cannot stall the session's tool
updates.

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

#### Scenario: Another actor's run held the session

- **WHEN** the bridge retried `ErrSessionBusy` while another actor's run on the session completed a `tool_use` message with text
- **THEN** that message is not relayed, and the bridge's own run's messages are

#### Scenario: Late part of a finished run

- **WHEN** a part of a run is handled after that run ended, whether or not the next run has started
- **THEN** it is checked against the run that received it: a message that run already posted is not posted again, and one it had not posted yet is posted

#### Scenario: Final reply after an in-flight intermediate post

- **WHEN** the terminal reply is ready while an intermediate post of the same run is still being sent
- **THEN** the terminal reply is posted after that post returns, or after 5 seconds

### Requirement: Question widget follows its text

Before posting a `question` prompt for a session with a bridge-dispatched run in
flight, the bridge SHALL make sure the text of the session's latest assistant message
has been relayed. If no path has posted it yet, the question router SHALL claim it and
send it without blocking the router on the send. Whether the router sends it or
another path is already sending it, the router SHALL wait for that post to finish, for
at most 5 seconds, then post the widget. A send still running at that point SHALL NOT
be cancelled; the text is still delivered, once. The router SHALL NOT create a
dispatcher to do so.

#### Scenario: Text then widget

- **WHEN** the agent writes text and then calls `question`
- **THEN** the chat shows `⌛ question` with the text, and then the question widget

#### Scenario: Slow chat API

- **WHEN** the send of the pre-question text takes longer than 5 seconds
- **THEN** the widget is posted after 5 seconds and the text is still delivered, once

## MODIFIED Requirements

### Requirement: Per-session typing/reporting indicators

The bridge SHALL emit platform-appropriate typing indicators while a run is in flight for a session, and SHALL surface tool-transition status (`[tool] pending|running|completed`) to the chat surface when `cfg.Router.ToolUpdatesEnabled` is true. Indicator emission MUST NOT block the inbound dispatch loop.

#### Scenario: Tool updates enabled

- **WHEN** `cfg.Router.ToolUpdatesEnabled == true` and a tool transitions from `pending` to `running`
- **THEN** the bridge sends a short status message to the chat surface reflecting the transition

#### Scenario: Tool updates disabled

- **WHEN** `cfg.Router.ToolUpdatesEnabled == false`
- **THEN** the bridge suppresses per-tool transition messages but still emits typing indicators, the intermediate assistant text of a bridge-dispatched run (see "Intermediate assistant text relay") and the final agent reply
