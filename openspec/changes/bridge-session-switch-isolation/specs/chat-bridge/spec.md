## ADDED Requirements

### Requirement: Opt-in isolation of external sessions from chat session commands

When `router.isolateExternalSessions` is `true`, the chat session commands SHALL NOT expose a session that holds at least one binding on the `external` channel to a chat peer, unless it is that peer's current session: `/sessions` SHALL leave it out of the listing, `/session <id-prefix>` SHALL leave it out of prefix matching (so it is neither shown, switched into, nor named in an ambiguous-prefix listing), and `/crons` SHALL leave out its jobs. The set of such sessions SHALL be read with one store query per command, and a failed query SHALL hide every session except the caller's current one and log a WARN. When the setting is unset or `false`, the commands SHALL behave as without it.

#### Scenario: Listing hides an external conversation

- **WHEN** isolation is on and a Slack peer bound to `S1` sends `/sessions` while session `E1` is bound to an `external` peer
- **THEN** the listing shows `S1` and other sessions, and neither `E1`'s id nor its title

#### Scenario: Switching into an external conversation is refused

- **WHEN** isolation is on and a Slack peer sends `/session <prefix>` whose only match is `E1`
- **THEN** the bridge replies with the ordinary "No session matches prefix" text and the peer's binding still points at its current session

#### Scenario: Ambiguous prefix does not name it

- **WHEN** isolation is on and a prefix matches `E1` and two chat sessions
- **THEN** the ambiguous-prefix reply lists the two chat sessions only

#### Scenario: The current session stays visible

- **WHEN** isolation is on and the caller's current session is itself bound to an `external` peer
- **THEN** `/sessions` still lists it marked ★, and `/session <its prefix>` replies that the peer is already on it

#### Scenario: Lookup failure fails closed

- **WHEN** isolation is on and reading the external bindings fails
- **THEN** the commands show only the caller's current session and a WARN is logged

#### Scenario: Setting off

- **WHEN** `router.isolateExternalSessions` is unset
- **THEN** `/sessions` lists `E1` and `/session <prefix>` switches into it, as before
