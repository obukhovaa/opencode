## ADDED Requirements

### Requirement: External channel command allowlist

On the `external` channel the bridge SHALL run only the chat commands on that channel's allowlist: `router.channels.external.commands`, or `stop` and `reset` when it is unset or empty. An alias SHALL be allowed exactly when the command it stands for is. A known command not on the list SHALL get a refusal reply and change nothing. Other channels SHALL be unaffected.

#### Scenario: Default allowlist refuses session and process commands

- **WHEN** `router.channels.external.commands` is unset and an `external` peer sends `/sessions`, `/session abcd1234`, `/agent coder`, `/model gpt-5` or `/help`
- **THEN** each gets a reply saying the command is not available on this channel, no session is listed or bound, and the active agent and model are unchanged

#### Scenario: Default allowlist runs stop and reset

- **WHEN** `router.channels.external.commands` is unset and an `external` peer sends `/stop`, `/cancel`, `/abort`, `/reset` or `/new`
- **THEN** the command runs as it does on a chat channel

#### Scenario: Configured allowlist

- **WHEN** `router.channels.external.commands` is `["abort", "help"]` and an `external` peer sends `/stop`, then `/help`, then `/reset`
- **THEN** `/stop` runs, `/help` lists only `/abort`, `/stop`, `/cancel` and `/help`, and `/reset` is refused

#### Scenario: Text that is not a command still reaches the agent

- **WHEN** an `external` peer sends `/etc/hosts looks wrong`
- **THEN** no command is refused and the text is dispatched to the agent as a prompt

#### Scenario: Chat channels are unaffected

- **WHEN** a Slack peer sends `/model gpt-5` and `router.channels.external.commands` is unset
- **THEN** the model is switched as before

### Requirement: External callers address only their own session

When the caller is an `external` peer, the session commands SHALL address only the session bound to that peer, whatever the allowlist and `router.isolateExternalSessions` say: `/sessions` SHALL list only it, `/session <id-prefix>` SHALL match only it, so no other session is shown, named in an ambiguous listing, or bound, and `/crons` SHALL show only its jobs.

#### Scenario: Listing shows only the caller's session

- **WHEN** the external allowlist includes `sessions` and an `external` peer bound to `E1` sends `/sessions` while chat sessions and other external conversations exist
- **THEN** the listing shows `E1` marked ★ and no other session's id or title

#### Scenario: Binding another session is refused

- **WHEN** the external allowlist includes `session` and an `external` peer bound to `E1` sends `/session <prefix>` whose only match is another session
- **THEN** the bridge replies with the ordinary "No session matches prefix" text and the peer stays bound to `E1`

#### Scenario: Ambiguous prefix names no other session

- **WHEN** the external allowlist includes `session` and a prefix matches `E1` and two other sessions
- **THEN** the reply says the peer is already on `E1` and names neither other session

#### Scenario: Cron jobs of other sessions are hidden

- **WHEN** the external allowlist includes `crons` and an `external` peer bound to `E1` sends `/crons` while other sessions have active jobs
- **THEN** only `E1`'s jobs are listed

## RENAMED Requirements

- FROM: `### Requirement: Opt-in isolation of external sessions from chat session commands`
- TO: `### Requirement: Isolation of external sessions from chat session commands`

## MODIFIED Requirements

### Requirement: Isolation of external sessions from chat session commands

Unless `router.isolateExternalSessions` is `false`, the chat session commands SHALL NOT expose a session of an external conversation's run to a chat peer, unless it is that peer's current session. Such a session holds at least one binding on the `external` channel, or has the same root as a session that does; a session's root is its `root_session_id`, or its own id when that is unset, so the step sessions of one flow run share a root. Concretely, `/sessions` SHALL leave it out of the listing, `/session <id-prefix>` SHALL leave it out of prefix matching (so it is neither shown, switched into, nor named in an ambiguous-prefix listing), and `/crons` SHALL leave out its jobs. The set of such sessions SHALL be read with one store query per command, and a failed query SHALL hide every session except the caller's current one and log a WARN. When the setting is `false`, the commands SHALL behave as without it.

#### Scenario: Listing hides an external conversation

- **WHEN** isolation is on and a Slack peer bound to `S1` sends `/sessions` while session `E1` is bound to an `external` peer
- **THEN** the listing shows `S1` and other sessions, and neither `E1`'s id nor its title

#### Scenario: Switching into an external conversation is refused

- **WHEN** isolation is on and a Slack peer sends `/session <prefix>` whose only match is `E1`
- **THEN** the bridge replies with the ordinary "No session matches prefix" text and the peer's binding still points at its current session

#### Scenario: Ambiguous prefix does not name it

- **WHEN** isolation is on and a prefix matches `E1` and two chat sessions
- **THEN** the ambiguous-prefix reply lists the two chat sessions only

#### Scenario: A sibling step of an external flow run is hidden

- **WHEN** isolation is on, step session `F1` of a flow run is bound to an `external` peer, and its sibling step `F2` (same root, no binding) has a cron job
- **THEN** `/sessions` lists neither `F1` nor `F2`, `/session <F2 prefix>` gets the "No session matches prefix" reply, `/crons` leaves out `F2`'s job, and a chat session that is its own root stays listed

#### Scenario: The current session stays visible

- **WHEN** isolation is on and the caller's current session is itself bound to an `external` peer
- **THEN** `/sessions` still lists it marked ★, and `/session <its prefix>` replies that the peer is already on it

#### Scenario: Lookup failure fails closed

- **WHEN** isolation is on and reading the external bindings fails
- **THEN** the commands show only the caller's current session and a WARN is logged

#### Scenario: Isolation is on by default

- **WHEN** `router.isolateExternalSessions` is unset and a Slack peer sends `/sessions` while `E1` is bound to an `external` peer
- **THEN** the listing leaves `E1` out, as with the setting `true`

#### Scenario: Setting off

- **WHEN** `router.isolateExternalSessions` is `false`
- **THEN** `/sessions` lists `E1` and its run's sibling steps, and `/session <prefix>` switches into them, as before
