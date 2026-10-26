## ADDED Requirements

### Requirement: Writeback preserves unresolved configuration
Configuration edits SHALL retain unchanged environment placeholders, unknown JSON fields, and map-key spelling on disk, including placeholders in typed numeric and boolean fields. Resolved environment values SHALL NOT replace unchanged placeholders. Existing atomic replacement and file-mode policy SHALL remain in effect.

#### Scenario: Theme edit with environment secrets
- **WHEN** a theme is changed in a config containing provider secrets, URLs, typed placeholders, and unknown fields
- **THEN** only the edited values change and all unchanged placeholders and unknown fields remain on disk without resolved secrets

#### Scenario: Intentional referenced field update
- **WHEN** an edit changes a field whose current value is an environment reference
- **THEN** that field stores the new literal value and other referenced fields remain unresolved
