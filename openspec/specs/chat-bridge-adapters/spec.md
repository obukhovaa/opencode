# Chat Bridge Adapters

## Purpose

Defines the per-platform adapter contract for the chat bridge: a common `Adapter` interface implemented by Telegram (`go-telegram/bot`), Slack (`slack-go/slack` Socket Mode), and Mattermost (hand-rolled WebSocket + REST). Each adapter handles inbound normalization, outbound text/file delivery, echo prevention, and per-platform media-size limits. Adapters may optionally satisfy `InteractiveQuestionSender` for platform-native question UI; non-supporting adapters and runtime failures fall back to the bridge's numbered-options text rendering.

## Requirements

### Requirement: Adapter interface

The bridge SHALL define a single `Adapter` interface in `internal/bridge/adapter.go` that each platform implementation satisfies. The interface MUST support: connect/disconnect, inbound event subscription, outbound text message send, outbound file send, and per-identity health reporting. Adapters MUST run independently — failure or disconnection of one adapter MUST NOT affect others.

#### Scenario: Adapter implements common contract

- **WHEN** a new platform adapter is added under `internal/bridge/<platform>/`
- **THEN** it implements the `Adapter` interface and is constructed via a per-platform factory invoked from the orchestrator

### Requirement: Telegram adapter via go-telegram/bot

The Telegram adapter SHALL use `github.com/go-telegram/bot` for long-polling. The adapter MUST implement: private/public access mode per identity, mention extraction, media download into the bridge media store, outbound text chunking, file upload, and reply-to-thread. The adapter MUST NOT use webhook mode (no inbound HTTP exposure required).

#### Scenario: Long-poll loop

- **WHEN** the Telegram adapter starts for an identity with a valid token
- **THEN** it begins long-polling `getUpdates`; received messages are normalized to the bridge's `Inbound` type and forwarded to the orchestrator

#### Scenario: Pairing-code flow

- **WHEN** a peer sends a pairing code matching the `pairingCodeHash` configured under `router.channels.telegram.bots[].pairingCodeHash`
- **THEN** the peer is added to `bridge_allowlist` for that identity

#### Scenario: Inbound media

- **WHEN** an inbound Telegram message contains a photo or document
- **THEN** the adapter downloads the file to `<config.Data.Directory>/bridge/media/` and the orchestrator passes the path to the agent as an attachment

### Requirement: Slack adapter via slack-go/slack

The Slack adapter SHALL use `github.com/slack-go/slack` Socket Mode. The adapter MUST handle: `app_mention`, `message.im`, file uploads. Outbound text via `chat.postMessage`, files via `files.upload`. Socket Mode handshake retries and reconnects MUST rely on the library's built-in behavior rather than re-implementing in-process retry logic.

#### Scenario: Bot mentioned in channel

- **WHEN** the Slack `app_mention` event arrives for the configured bot identity
- **THEN** the adapter normalizes the event to an `Inbound` (stripping the bot mention) and forwards it to the orchestrator

#### Scenario: File attached to inbound message

- **WHEN** a Slack message includes a file attachment
- **THEN** the adapter downloads the file to the bridge media store and the agent receives the local path as an attachment

### Requirement: Mattermost adapter

The Mattermost adapter SHALL connect to a Mattermost server via WebSocket and the REST API v4. The adapter MUST subscribe to `posted`, `post_edited`, and `post_deleted` events. Outbound text via REST `POST /api/v4/posts`; file attach via multipart upload to `POST /api/v4/files` followed by `POST /api/v4/posts` with `file_ids`. The adapter MUST implement an exponential-backoff reconnect loop with **1s → 30s, 20 attempts max**, matching the existing TS bridge behavior.

The implementation MAY use either `github.com/mattermost/mattermost/server/public/model` (the official driver) or a hand-rolled HTTP + WebSocket client. The Go port chooses the hand-rolled path because the official lib pulls ~272 transitive dependencies (grpc, protobuf, mlog, msgpack, etc.) which busts the +15 MB binary growth budget for what amounts to a thin JSON-over-WebSocket protocol — the TS bridge already hand-rolls the equivalent contract.

#### Scenario: DM routing

- **WHEN** an inbound `posted` event arrives on a direct-message channel
- **THEN** the adapter forwards it to the orchestrator without requiring a bot mention

#### Scenario: Group DM honors per-identity groupsEnabled

- **WHEN** an inbound `posted` event arrives on a group-DM channel and the identity has `groupsEnabled: false`
- **THEN** the adapter ignores the message; **WHEN** the identity has `groupsEnabled: true` it forwards the message

#### Scenario: Channel message requires mention

- **WHEN** an inbound `posted` event arrives on a regular channel without an @mention of the bot
- **THEN** the adapter ignores the message

#### Scenario: WebSocket disconnect triggers backoff reconnect

- **WHEN** the WebSocket connection drops
- **THEN** the adapter waits 1s, attempts reconnect, doubles the delay (cap 30s) on failure, and gives up after 20 attempts marking the identity disabled

### Requirement: from_bot/from_webhook filtering

All adapters SHALL filter out inbound messages flagged as originating from bots, webhooks, or the bridge's own bot identity. Echo prevention is the adapter's responsibility, not the orchestrator's.

#### Scenario: Mattermost from_webhook filtered

- **WHEN** a Mattermost `posted` event arrives with `props.from_webhook == "true"`
- **THEN** the adapter ignores the event

#### Scenario: Self-message filtered

- **WHEN** an inbound event's author matches the bridge's own bot user for that identity
- **THEN** the adapter ignores the event

### Requirement: Outbound media size limits

For outbound file delivery each adapter MUST enforce the platform's media size limit at upload time and return an error to the agent rather than attempt the upload: Telegram 50 MB, Slack 1 GB, Mattermost configurable (the adapter SHALL query the server config at startup or accept a configured override).

#### Scenario: Oversize file rejected pre-upload

- **WHEN** the agent emits a `FILE:<path>` token pointing at a 100 MB file via the Telegram adapter
- **THEN** the adapter does not attempt the upload, returns an error message to the chat surface, and surfaces the error to the agent via the FILE: protocol's error path

### Requirement: Adapter test parity with TS bridge

Each adapter MUST be covered by a Go test suite that mirrors the scenario coverage of the corresponding TS test file: Mattermost (`mattermost.test.js` 1377 LOC), Slack (`slack.test.js` 273 LOC), Telegram (`telegram.test.js` 204 LOC). Port success per adapter is binary: every existing TS test scenario MUST have a passing Go equivalent. Tests use Go std `testing` + `gorilla/websocket` for WebSocket mocking.

#### Scenario: Mattermost test suite green

- **WHEN** `go test ./internal/bridge/mattermost/...` runs
- **THEN** every scenario from the TS `mattermost.test.js` has a passing equivalent, covering WebSocket lifecycle, post dispatch, group DMs, and file flow

### Requirement: Optional InteractiveQuestionSender per-adapter capability

Each adapter MAY satisfy an optional `bridge.InteractiveQuestionSender` interface to render question-tool prompts using platform-native interactive UI. The interface is opt-in — adapters that do not satisfy it fall back to the bridge's numbered-options text rendering automatically. Adapters that DO satisfy it but fail at send time (missing scope, deprecated feature, network error) MUST return an error so the bridge's question router can retry with the text fallback for that peer.

The interface signature is:

```go
type InteractiveQuestionSender interface {
    SendInteractiveQuestion(ctx context.Context, peer PeerRef, prompt string, choices []QuestionChoice) error
}

type QuestionChoice struct {
    Label string
    Value string
}
```

Click callbacks (Slack `block_actions`, Telegram `callback_query`) MUST be normalized into the same `bridge.Inbound` shape as text replies, with `Inbound.Text == QuestionChoice.Value` for the clicked button. This keeps the inbound reply parser path identical between interactive and text-fallback flows.

Per-adapter support status in v1:

- **Slack**: satisfies the interface via `chat.postMessage` + actions block (one button per choice).
- **Telegram**: satisfies the interface via `sendMessage` + `reply_markup.inline_keyboard` (one row per choice).
- **Mattermost**: does NOT satisfy the interface. Mattermost interactive attachments (`actions[].integration.url`) require a Mattermost-callable webhook URL that the bridge does not host without additional infrastructure. Mattermost peers always use the numbered-options text fallback.

#### Scenario: Slack adapter implements InteractiveQuestionSender

- **WHEN** the bridge type-asserts a Slack adapter to `bridge.InteractiveQuestionSender`
- **THEN** the assertion succeeds; calling `SendInteractiveQuestion` produces a `chat.postMessage` with a single actions block whose buttons match the supplied choices in order

#### Scenario: Telegram adapter implements InteractiveQuestionSender

- **WHEN** the bridge type-asserts a Telegram adapter to `bridge.InteractiveQuestionSender`
- **THEN** the assertion succeeds; calling `SendInteractiveQuestion` produces a `sendMessage` with `reply_markup.inline_keyboard` populated with one row per choice; clicking a button delivers a `callback_query` that the adapter normalizes into a `bridge.Inbound` whose `Text` is the clicked choice's `Value`

#### Scenario: Mattermost adapter does NOT implement InteractiveQuestionSender

- **WHEN** the bridge type-asserts a Mattermost adapter to `bridge.InteractiveQuestionSender`
- **THEN** the assertion fails; the bridge's question router uses the numbered-options text fallback for every Mattermost peer regardless of `cfg.Router.QuestionMode`
