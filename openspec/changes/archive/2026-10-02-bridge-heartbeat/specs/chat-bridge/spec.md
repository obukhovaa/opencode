# chat-bridge (delta)

Delta spec for the `bridge-heartbeat` change. Restates only the requirements of
`openspec/specs/chat-bridge/spec.md` that heartbeat turns make an exception to; the
heartbeat's own behaviour is in the `bridge-heartbeat` capability.

The queued acknowledgement and the per-run progress card are not yet in the main spec
(they are in the unarchived `bridge-queue-visibility-and-loss-paths` and
`bridge-run-progress-card` changes). Their heartbeat exceptions are stated in the
`bridge-heartbeat` capability's "Heartbeat turns are quiet unless there is something to
say" requirement. `bridge-run-progress-card` also modifies "Per-session
typing/reporting indicators": whichever of the two changes is archived second must
carry both modifications.

## MODIFIED Requirements

### Requirement: Per-session typing/reporting indicators

The bridge SHALL emit platform-appropriate typing indicators while a run is in flight for a session, and SHALL surface tool-transition status (`[tool] pending|running|completed`) to the chat surface when `cfg.Router.ToolUpdatesEnabled` is true. Indicator emission MUST NOT block the inbound dispatch loop.

A heartbeat turn (see the `bridge-heartbeat` capability) is the exception: the bridge SHALL NOT surface tool-transition status for it, whatever `cfg.Router.ToolUpdatesEnabled` and the verbosity are. Whether a part event belongs to a heartbeat turn SHALL be decided by the run that received it, so a heartbeat run's late event stays quiet and a human run's late event handled while a beat runs is surfaced.

#### Scenario: Tool updates enabled

- **WHEN** `cfg.Router.ToolUpdatesEnabled == true` and a tool transitions from `pending` to `running`
- **THEN** the bridge sends a short status message to the chat surface reflecting the transition

#### Scenario: Tool updates disabled

- **WHEN** `cfg.Router.ToolUpdatesEnabled == false`
- **THEN** the bridge suppresses per-tool transition messages but still emits typing indicators, the intermediate assistant text of a bridge-dispatched run (see "Intermediate assistant text relay") and the final agent reply

#### Scenario: Heartbeat turn

- **WHEN** `cfg.Router.ToolUpdatesEnabled == true` and a tool of a heartbeat turn transitions
- **THEN** nothing is sent for the transition

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

A heartbeat turn (see the `bridge-heartbeat` capability) SHALL NOT have its
intermediate text relayed: only its final reply, under the heartbeat header, or its
one-line failure reaches the chat. As above, a part SHALL be checked against the run
that received it.

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

- **WHEN** a part was not received by a bridge-dispatched run (e.g. a self-started turn), or a question arrives while no bridge-dispatched run is in flight
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

#### Scenario: Heartbeat turn

- **WHEN** an assistant message of a heartbeat turn ends in `tool_use` with text
- **THEN** nothing is posted for it
