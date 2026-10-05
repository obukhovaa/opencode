# webfetch-output-limit (delta)

Delta spec for the `webfetch-output-limit` change. This capability is new, so every
requirement below is added.

## Purpose

Bounds the context-window footprint of a single `webfetch` call: a configurable byte cap on the tool's returned content, with oversized results spilled to a temp file and replaced by a head+tail preview the agent can explore with its existing file tools. Also makes the 5MB response-body limit visible to the agent instead of silently returning a truncated document.

## ADDED Requirements

### Requirement: The webfetch output cap is configurable

The system SHALL expose a top-level configuration object `webFetch` with an integer field `maxOutputBytes` that caps the size, in bytes, of a single `webfetch` call's content kept in the model context. A positive value SHALL be used as the cap; a negative value SHALL disable the cap entirely (unbounded output); zero or an omitted value SHALL fall back to the built-in default of 50KB (51200 bytes). The field SHALL appear in the generated configuration JSON schema.

#### Scenario: Default cap when unset

- **WHEN** the configuration does not set `webFetch.maxOutputBytes`
- **THEN** `webfetch` calls are capped at the built-in default of 51200 bytes

#### Scenario: Positive override

- **WHEN** the configuration sets `webFetch.maxOutputBytes` to a positive value `N`
- **THEN** `webfetch` calls are capped at `N` bytes instead of the default

#### Scenario: Negative value disables the cap

- **WHEN** the configuration sets `webFetch.maxOutputBytes` to a negative value
- **THEN** `webfetch` content is not capped and is returned in full (subject only to the global tool-response backstop)

#### Scenario: Field present in schema

- **WHEN** the configuration JSON schema is generated
- **THEN** `webFetch.maxOutputBytes` is present with an integer type and a description

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
- **AND** a spill within the `read` tool's own size ceiling opens with the `read` tool

#### Scenario: Recovery guidance matches what the tools can do

- **WHEN** the overflow header names a spill file
- **THEN** it directs the agent to tools that work at that file's size (the `grep` tool and `sed`, neither of which has a size ceiling) and states the size limit above which the `read` tool declines a file

### Requirement: The cap is measured and spilled after format conversion

The system SHALL apply the cap to the content in its final returned form — after HTML-to-markdown conversion, HTML-to-text extraction, or fenced-block wrapping — for every supported `format` value, and the spilled file SHALL contain that same converted content, so a search over the file and a search over the preview see identical text.

#### Scenario: Markdown conversion is measured post-conversion

- **WHEN** an HTML page is fetched with `format: markdown` and its converted markdown exceeds the cap
- **THEN** the cap is applied to the markdown and the spill file contains the markdown (not the source HTML)

#### Scenario: Every format is capped

- **WHEN** a page whose content exceeds the cap is fetched with `format` set to `text`, `markdown`, or `html`
- **THEN** the returned content is capped and spilled in each case

### Requirement: Response-body truncation at the 5MB limit is reported

When a response body exceeds the tool's 5MB read limit and is therefore truncated before conversion, the system SHALL state in the returned content that the response exceeded the limit and was truncated. The result SHALL remain a non-error response carrying the truncated content.

#### Scenario: Oversized body is flagged

- **WHEN** a response body larger than 5MB is fetched without a `Content-Length` header
- **THEN** the returned content states that the response was truncated at the 5MB limit
- **AND** the truncated content is still returned as a non-error response

#### Scenario: Body within the limit is not flagged

- **WHEN** a response body at or below 5MB is fetched
- **THEN** the returned content carries no truncation notice
