# Spec Delta

## MODIFIED Requirements

### Requirement: The shell environment SHALL disable interactive prompting

The persistent shell's environment SHALL be configured so that tools which would
otherwise prompt for input fail immediately with a diagnostic instead of waiting. At
minimum, git's terminal prompting SHALL be disabled and git's editor SHALL remain
non-interactive.

The system MUST NOT configure any askpass helper that could cause a credential prompt to
be answered silently or incorrectly; the required outcome is a fast, explicit failure.

The shell's inherited environment SHALL be opencode's environment filtered by the tool
environment deny list (see the `tool-process-environment` capability).

#### Scenario: Git operation that would prompt for credentials
- **WHEN** a command run through the persistent shell performs a git operation against a
  remote requiring credentials that are not already available
- **THEN** git fails immediately with a diagnostic rather than waiting for input

#### Scenario: Caller-supplied environment is preserved
- **GIVEN** the process environment contains a variable that neither the prompting policy
  nor the tool environment deny list names
- **WHEN** the persistent shell is started
- **THEN** that variable is visible to commands run through the shell, unchanged

#### Scenario: Denied variable is not visible
- **GIVEN** the process environment contains `OPENCODE_SERVER_PASSWORD`
- **WHEN** the persistent shell is started
- **THEN** `OPENCODE_SERVER_PASSWORD` is not set for commands run through the shell
