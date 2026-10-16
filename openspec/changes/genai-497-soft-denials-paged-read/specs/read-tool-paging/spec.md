## ADDED Requirements

### Requirement: The size ceiling applies to whole-file reads only

The `read` tool SHALL decline a call with neither `offset` nor `limit` for a file larger than `MaxReadSize` (250KB), with a message that tells the agent to pass `offset`/`limit` or to use the `grep` tool. A call with `offset` or `limit` SHALL be served for a file of any size.

#### Scenario: Whole read of a large file

- **WHEN** `read` is called on a 400KB file without offset or limit
- **THEN** an error result names offset and limit

#### Scenario: Paged read of a large file

- **WHEN** `read` is called on a 400KB file with offset 3000 and limit 5
- **THEN** lines 3001–3005 are returned with their line numbers and a note giving the total line count and the offset to continue from

### Requirement: A paged read is bounded per call and streams the file

A read SHALL return at most `DefaultReadLimit` (2000) lines and at most `MaxReadSize` bytes of file text. When the byte bound stops it early, the note SHALL say the call's output limit was reached and give the offset to continue from. The tool SHALL read the file as a stream, holding only the returned window; a line longer than any read buffer SHALL be cut to `MaxLineLength` with a `...` marker rather than fail the call. An offset at or past the end of the file SHALL return an error naming the file's line count. A paged read SHALL record the file as read for the edit tools' read-before-edit check. A file over `MaxReadSize` SHALL NOT be opened in the LSP.

#### Scenario: Long lines

- **WHEN** a page of 2000 lines would hold 2MB of text
- **THEN** the result stays within about 250KB and its note says the output limit was reached

#### Scenario: Minified file

- **WHEN** a file holds a 1MB line
- **THEN** the line is returned cut to 2000 characters plus `...`, and the following lines keep their numbers
