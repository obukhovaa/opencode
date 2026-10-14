## ADDED Requirements

### Requirement: The agent loop bounds every tool call

The agent loop SHALL bound each tool call, on the parallel and the sequential path, by `toolCallTimeoutSeconds`: the agent's `.opencode.json` value, else its markdown frontmatter value, else the top-level value, else 1200 seconds; 0 SHALL disable the bound. `task`, `question` and `bash` SHALL be exempt. For an MCP tool the bound SHALL be at least the server's per-call timeout plus 60 seconds. Time the call spends waiting on a permission prompt SHALL NOT count towards the bound. On expiry the loop SHALL cancel the call's context, stop waiting for it and record an error result naming the tool and the bound, and the turn SHALL continue. A cancelled run SHALL NOT wait for a bounded tool that ignores its context.

#### Scenario: Tool ignores its context

- **WHEN** a non-exempt tool never returns and ignores cancellation
- **THEN** after the bound the model receives an error result for that call and the turn continues

#### Scenario: Slow MCP server

- **WHEN** an MCP server sets `callToolTimeoutSeconds` to 1800 and the loop bound is 1200
- **THEN** the loop bound for that server's tools is 1860 seconds

#### Scenario: Permission prompt outlasts the bound

- **WHEN** a non-exempt tool waits on a permission prompt for longer than the bound and then finishes
- **THEN** the call completes normally and the wait is not counted
