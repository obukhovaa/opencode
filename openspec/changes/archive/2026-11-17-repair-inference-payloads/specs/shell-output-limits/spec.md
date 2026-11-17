## Purpose

Bound the model-visible Bash output excerpt while retaining oversized command output for deliberate retrieval.

## ADDED Requirements

### Requirement: Bash previews obey byte and line bounds
Bash stdout and stderr excerpts SHALL independently obey the existing byte and line limits regardless of line shape, including single-line JSON and Unicode. A bounded truncation notice and retrieval path MAY be added outside each excerpt budget. Output within both limits SHALL remain unchanged.

#### Scenario: Oversized single line
- **WHEN** command output exceeds the byte limit in a single line
- **THEN** the excerpt is byte-bounded rather than returning the entire line

#### Scenario: Many lines or multibyte text
- **WHEN** command output exceeds either limit and contains multibyte characters or many short lines
- **THEN** both excerpt limits hold and valid UTF-8 input remains valid UTF-8 in the excerpt

### Requirement: Truncated output remains recoverable
Oversized command output SHALL retain the existing spill-file behavior and exit metadata. Retrieval instructions SHALL accurately describe the saved data, including the existing persistence cap or a failure to save it.

#### Scenario: Saved original
- **WHEN** output is clipped and persistence succeeds within its cap
- **THEN** the advertised file contains the original output and the response identifies that file

#### Scenario: Persistence is capped or unavailable
- **WHEN** output cannot be fully persisted
- **THEN** the preview remains bounded and the response does not falsely claim that the full original is available
