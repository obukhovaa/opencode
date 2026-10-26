## ADDED Requirements

### Requirement: Every bash invocation uses configured permissions
Every bash invocation SHALL use the configured permission chain and syntax gate
before foreground execution or background task creation. Membership in a safe
or read-only command list SHALL NOT grant execution authority. Existing allow,
ask, rule-denial and rejected-prompt behavior SHALL remain intact.

#### Scenario: A wrapper cannot bypass deny-all
- **WHEN** a deny-all agent calls env, nice, time, timeout, go run or go test
- **THEN** the tool returns a rule denial before any command or task starts

#### Scenario: Plain safe-list entries cannot bypass deny-all
- **WHEN** a deny-all agent calls pwd, ls or echo
- **THEN** the tool returns a rule denial rather than exempting the command

#### Scenario: An unconfigured safe-list command asks
- **WHEN** no rule grants a pwd invocation
- **THEN** the normal permission prompt runs once before execution

#### Scenario: A wrapper is deliberately allowed
- **WHEN** the policy explicitly allows a harmless wrapper and its syntax passes
- **THEN** the wrapper can execute through the normal dispatch path
