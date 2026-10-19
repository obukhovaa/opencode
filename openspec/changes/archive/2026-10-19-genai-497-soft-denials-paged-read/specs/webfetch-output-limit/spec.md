## MODIFIED Requirements

### Requirement: Oversized webfetch content is spilled to a file with a head+tail preview

When a `webfetch` call's content exceeds the resolved cap, the system SHALL write the full content to a temp file in the process scratch directory and return, in place of the full content, a header followed by a byte-aligned head+tail excerpt of approximately one cap's worth of bytes. The header SHALL state the total byte size, name the temp file path, and instruct the agent to explore the file with the grep/read tools or sed in bash rather than re-fetching the URL. Content at or below the cap SHALL be returned unchanged with no file written. The returned response SHALL NOT be marked as an error. Each spill SHALL be logged with the URL, total size, resolved cap, and file path.

#### Scenario: Content under the cap is unchanged

- **WHEN** a fetched page's formatted content is no larger than the resolved cap
- **THEN** the tool result contains the content verbatim and no temp file is written

#### Scenario: Content over the cap is previewed and saved

- **WHEN** a fetched page's formatted content is larger than the resolved cap
- **THEN** the full content is written to a temp file
- **AND** the tool result contains a header naming the total byte size and the file path plus guidance to grep/read/sed it
- **AND** the tool result contains a head fragment and a tail fragment separated by an elided-bytes marker
- **AND** the tool result is smaller than the original content and is not marked as an error

#### Scenario: Spill file is reachable by the agent's own tools

- **WHEN** a `webfetch` call spills its content to a temp file
- **THEN** the named path is absolute and the `grep` tool finds content in it that the preview omitted, with no further configuration
- **AND** a spill within the `read` tool's whole-file ceiling opens with the `read` tool, and a larger one opens with a paged `read` (offset/limit)

#### Scenario: Recovery guidance matches what the tools can do

- **WHEN** the overflow header names a spill file
- **THEN** it directs the agent to tools that work at any size (the `grep` tool, the `read` tool's paged reads with offset/limit, and `sed`) and states the size limit above which the `read` tool declines a whole-file read
