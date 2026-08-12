# mcp-tool-output-limit Specification

## ADDED Requirements

### Requirement: Per-server MCP tool-call output cap is configurable

The system SHALL expose a per-server MCP configuration field `callToolMaxOutputBytes` (integer, on each `mcpServers.<name>` entry) that caps the size, in bytes, of a single tool call's output kept in the model context for that server. A positive value SHALL be used as the cap; a negative value SHALL disable the cap entirely (unbounded output); zero or an omitted value SHALL fall back to the built-in default of 50KB (51200 bytes). The field SHALL appear in the generated configuration JSON schema.

#### Scenario: Default cap when unset

- **WHEN** an MCP server entry does not set `callToolMaxOutputBytes`
- **THEN** its tool calls are capped at the built-in default of 51200 bytes

#### Scenario: Positive override

- **WHEN** an MCP server sets `callToolMaxOutputBytes` to a positive value `N`
- **THEN** its tool calls are capped at `N` bytes instead of the default

#### Scenario: Negative value disables the cap

- **WHEN** an MCP server sets `callToolMaxOutputBytes` to a negative value
- **THEN** its tool-call output is not capped and is returned in full (subject only to the global tool-response backstop)

#### Scenario: Field present in schema

- **WHEN** the configuration JSON schema is generated
- **THEN** `callToolMaxOutputBytes` is present under an `mcpServers` entry with an integer type and a description

### Requirement: Oversized MCP tool output is spilled to a file with a head+tail preview

When an MCP tool call's combined output exceeds the resolved cap, the system SHALL write the full output to a temp file in the process scratch directory and return, in place of the full output, a compact preview consisting of a header followed by a byte-aligned head+tail excerpt (approximately one cap's worth of bytes total). The header SHALL state the total byte size, name the temp file path, and instruct the agent to explore the file with the grep/read tools or sed in bash rather than re-running the tool. Output at or below the cap SHALL be returned unchanged with no file written. The returned response SHALL NOT be marked as an error. The spill file SHALL be created directly inside the scratch directory even when the tool name contains path separators or other unsafe filename characters (the server-supplied name is sanitized to a single safe path component), concurrent spills SHALL never overwrite one another (unique file names), and each spill SHALL be logged with the tool name, total size, resolved cap, and file path.

#### Scenario: Output under the cap is unchanged

- **WHEN** a tool returns output no larger than the resolved cap
- **THEN** the tool result contains the output verbatim and no temp file is written

#### Scenario: Output over the cap is previewed and saved

- **WHEN** a tool returns output larger than the resolved cap
- **THEN** the full output is written to a temp file
- **AND** the tool result contains a header naming the total byte size and the file path plus guidance to grep/read/sed it
- **AND** the tool result contains a head fragment and a tail fragment of the output separated by an elided-bytes marker
- **AND** the tool result is smaller than the original output and is not marked as an error

#### Scenario: Single-line payload is still bounded

- **WHEN** a tool returns a large payload containing no newlines (e.g. minified JSON) that exceeds the cap
- **THEN** the returned preview is smaller than the original output (a byte-based cut is applied even without line boundaries)

#### Scenario: Preview preserves UTF-8

- **WHEN** the cap falls inside a multi-byte UTF-8 character while building the preview
- **THEN** the returned preview is valid UTF-8 (cut points are snapped to rune boundaries; no character is split)

#### Scenario: Hostile tool name cannot escape the scratch directory

- **WHEN** a tool whose server-supplied name contains path separators (e.g. `a/../../evil`) returns output larger than the cap
- **THEN** the full output is written to a file directly inside the process scratch directory (the name is sanitized; no path traversal)

#### Scenario: Concurrent spills do not collide

- **WHEN** two tool calls with the same tool name spill oversized output concurrently
- **THEN** each call's output is written to a distinct temp file (neither overwrites the other)

### Requirement: MCP tool output combines all content blocks before capping

When an MCP tool call returns multiple content blocks, the system SHALL concatenate all blocks into the tool output before applying the size cap, so no block is silently dropped and the cap is measured against the full result.

#### Scenario: Multi-block result is concatenated

- **WHEN** a tool returns two or more text content blocks and the cap is not exceeded
- **THEN** the tool result contains the concatenation of all blocks in order
