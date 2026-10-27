## ADDED Requirements

### Requirement: Reply text prefers an exact option label over an index
When the bridge parses reply text into a question answer (a chat reply, a button click, a buffered message or a request-bound text reply), a token that equals an option's label, ignoring case, SHALL select that option before the token is read as a 1-based index. For a Multiple prompt, text that equals one option's label as a whole SHALL select that option and SHALL NOT be split on commas.

#### Scenario: A numeric-looking label is clicked
- **WHEN** a prompt's options are `3`, `6` and `12` and the reply text is `3`
- **THEN** the answer is `3`, not the third option `12`

#### Scenario: A number that matches no label
- **WHEN** a prompt's options are `3`, `6` and `12` and the reply text is `2`
- **THEN** the answer is the second option, `6`

#### Scenario: A label containing a comma on a Multiple prompt
- **WHEN** a Multiple prompt has an option `Yes, ship it` and the reply text is `Yes, ship it`
- **THEN** the answer is that one option, not the tokens `Yes` and `ship it`
