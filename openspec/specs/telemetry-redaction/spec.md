# telemetry-redaction Specification

## Purpose
Prevents credentials and other sensitive values from leaving the process inside telemetry spans. Every content-bearing telemetry payload — tool input/output, LLM request/response, trace input/output, span metadata and error messages — is scanned against a built-in detector set plus operator-supplied rules, and matched values are replaced before the span is exported.

## Requirements

### Requirement: Every content-bearing telemetry payload is redacted before export

The system SHALL apply secret redaction to every telemetry attribute that carries caller- or environment-derived content, specifically: tool call input, tool call output, LLM request payload, LLM response payload, trace-level input, trace-level output, span metadata values, and span error/status messages. No content-bearing attribute SHALL reach the telemetry backend without passing through redaction. Attributes that carry only structural or diagnostic signal — span and tool names, timings, token usage, cost, model name, session and user identifiers, and observation type — SHALL NOT be modified.

#### Scenario: Tool output carrying a credential

- **WHEN** a tool's output contains a value matching an enabled detector and tool output capture is permitted
- **THEN** the exported span's output attribute contains the replacement marker in place of the value
- **AND** the surrounding output text is preserved unchanged

#### Scenario: Tool input carrying a credential

- **WHEN** a tool's input contains a value matching an enabled detector and tool input capture is permitted
- **THEN** the exported span's input attribute contains the replacement marker in place of the value

#### Scenario: LLM request and response payloads

- **WHEN** generation capture is permitted and the request payload or the response payload contains a value matching an enabled detector
- **THEN** the exported generation span's corresponding attribute contains the replacement marker in place of the value

#### Scenario: Trace-level input and output

- **WHEN** a trace-level input or output contains a value matching an enabled detector
- **THEN** the exported trace attribute contains the replacement marker in place of the value

#### Scenario: Error messages are redacted

- **WHEN** a tool or generation fails with an error message containing a value matching an enabled detector
- **THEN** the exported span's status message contains the replacement marker in place of the value
- **AND** the span is still marked as errored with the remainder of the message intact

#### Scenario: Metadata values are redacted

- **WHEN** span or trace metadata contains a value matching an enabled detector
- **THEN** the exported metadata attribute contains the replacement marker in place of the value

#### Scenario: Diagnostic attributes are untouched

- **WHEN** a span is exported with redaction enabled
- **THEN** its name, tool name, timings, token usage, cost, model name, session id, user id and observation type are byte-identical to what they would be with redaction disabled

### Requirement: Redaction is applied before truncation

The system SHALL redact a payload before applying any size cap to it. A payload that exceeds its cap SHALL be redacted in full first and truncated afterwards, so that no partial credential is emitted at the truncation boundary.

#### Scenario: Credential positioned near the truncation boundary

- **WHEN** a payload exceeds its size cap and contains a credential that spans the cap boundary
- **THEN** the exported attribute contains no fragment of that credential
- **AND** the attribute still respects the size cap

#### Scenario: Redaction never increases payload size beyond the cap

- **WHEN** a payload containing credentials is redacted and truncated
- **THEN** the exported attribute is no larger than the configured cap

### Requirement: Built-in detectors cover the observed credential shapes

The system SHALL ship a built-in detector set, enabled by default, that detects at minimum: GitLab personal access tokens, Slack tokens, AWS access key identifiers, Langfuse public and secret keys, Anthropic API keys, GitHub tokens, JSON Web Tokens, PEM-encoded private key blocks, credentials embedded in a URL's userinfo component, credentials in an HTTP authorization header, and values assigned to an environment-variable-style name denoting a secret. The set SHALL cover every credential class enumerated by the production scan that motivated this capability. Detection SHALL NOT depend on the credential appearing alone on a line or at a payload boundary; a credential embedded in JSON, in shell output, in a URL, or in a quoted and escaped string SHALL be detected. Each detector SHALL have a stable name that appears in its replacement marker.

#### Scenario: Bare credentials of each built-in shape

- **WHEN** a payload contains a bare credential of any built-in shape
- **THEN** that value is replaced in the exported attribute

#### Scenario: Credential inside a URL

- **WHEN** a payload contains a URL whose userinfo component carries a credential, such as a fetched repository URL
- **THEN** the credential portion is replaced and the scheme, host and path are preserved

#### Scenario: Credential inside an authorization header

- **WHEN** a payload contains an HTTP authorization header with a bearer, basic or token credential
- **THEN** the credential is replaced and the header name and scheme are preserved

#### Scenario: Credential in an environment-style assignment

- **WHEN** a payload assigns a value to a name containing a secret-denoting word such as TOKEN, SECRET, PASSWORD, API_KEY, ACCESS_KEY, PRIVATE_KEY or CREDENTIAL
- **THEN** the assigned value is replaced and the name is preserved
- **AND** this holds for a credential whose own shape matches no other detector

#### Scenario: Credential preceded by non-ASCII text

- **WHEN** a payload contains a credential that is detected by its surrounding context, and any text earlier in the same payload contains a non-ASCII character
- **THEN** the credential is still replaced, regardless of where the non-ASCII character sits relative to it
- **AND** the non-ASCII text itself is exported unchanged

#### Scenario: Credential inside nested JSON and escaped strings

- **WHEN** a payload contains a credential inside a JSON string value, including one that is backslash-escaped inside another JSON string
- **THEN** the credential is replaced

#### Scenario: GitLab token of the shorter observed length

- **WHEN** a payload contains a GitLab personal access token whose body is 16 characters
- **THEN** it is replaced, as is one whose body is 35 characters

#### Scenario: Credential wrapped across a line break

- **WHEN** a payload contains a credential of a prefixed built-in shape split across a single line break, as produced by wrapped terminal output
- **THEN** the whole credential including its continuation is replaced, leaving no fragment
- **AND** a payload containing the same prefix followed by a short body and a run of blank lines is exported unchanged, with no paragraph break consumed

#### Scenario: JSON Web Token

- **WHEN** a payload contains a JSON Web Token
- **THEN** it is replaced
- **AND** ordinary text that merely begins with the same three characters followed by a dot-separated word is exported unchanged

#### Scenario: PEM private key block

- **WHEN** a payload contains a PEM-encoded private key block spanning multiple lines
- **THEN** the whole block from its BEGIN marker through its END marker is replaced
- **AND** a lone BEGIN marker appearing in prose with no matching END marker is exported unchanged

### Requirement: Ordinary text resembling a credential is not redacted

The system SHALL NOT redact strings that share a credential prefix but are ordinary text. A generic detector for an ambiguous prefix SHALL accept a candidate only when its body is at least 40 characters, or when its body contains an uppercase letter and contains no lowercase-alphabetic segment of two or more characters delimited by a hyphen or underscore. Prefix-specific detectors SHALL NOT be subject to this gate. The system SHALL NOT use a Shannon-entropy threshold as the sole or primary discriminator for an ambiguous prefix.

#### Scenario: Known false positives are preserved

- **WHEN** a payload contains a string matching an ambiguous credential prefix followed by hyphenated lowercase English words, such as a fragment of a longer word like "task-", "risk-" or "disk-"
- **THEN** the payload is exported unchanged, with no replacement marker

#### Scenario: A high-entropy key sharing the same prefix is still redacted

- **WHEN** a payload contains a credential using the same ambiguous prefix but whose body contains uppercase characters and no lowercase word segments
- **THEN** it is replaced

#### Scenario: A long body is accepted regardless of case

- **WHEN** a payload contains a credential using an ambiguous prefix whose body is at least 40 characters
- **THEN** it is replaced

#### Scenario: A prefix-specific detector is not weakened by the gate

- **WHEN** a payload contains an all-lowercase credential matched by a prefix-specific detector, such as a hexadecimal Langfuse key
- **THEN** it is replaced despite containing no uppercase characters

### Requirement: Overlapping detector matches resolve deterministically

When two or more detectors match overlapping regions of a payload, the system SHALL resolve them to a single replacement: the longest matching region SHALL win, and ties SHALL be broken by detector declaration order with built-in detectors ordered before operator-supplied rules. A replacement marker SHALL NOT be emitted inside another replacement marker, and the result SHALL be identical across runs for identical input and configuration.

#### Scenario: Two detectors match the same credential

- **WHEN** a payload contains a credential matched by both a shape detector and a positional detector at the same offset
- **THEN** exactly one replacement marker is emitted covering the credential

#### Scenario: Partially overlapping matches

- **WHEN** two detectors match overlapping but unequal regions
- **THEN** the longer region is replaced as one marker and no fragment of the shorter match remains

#### Scenario: Result is stable

- **WHEN** the same payload is redacted twice under the same configuration
- **THEN** both results are byte-identical

### Requirement: Redaction is re-entrant and never re-wraps an existing marker

The system SHALL treat a replacement marker already present in a payload as protected: no detector match overlapping an existing marker SHALL produce a replacement. Redacting an already-redacted payload SHALL return it unchanged, so that a marker produced elsewhere — by a subagent whose output is nested into a parent span, or by a retried call re-exporting a payload — reaches the backend with its original detector name and fingerprint intact.

#### Scenario: Redacting twice changes nothing

- **WHEN** a payload that has already been redacted is redacted again under the same configuration
- **THEN** the result is byte-identical to the first result

#### Scenario: A marker sitting where a detector would match

- **WHEN** an existing marker occupies a position a detector would otherwise match, such as the userinfo component of a URL whose credential was already replaced
- **THEN** the existing marker is preserved with its original detector name and fingerprint
- **AND** no marker is emitted around or inside it

#### Scenario: Nested subagent output

- **WHEN** a subagent's redacted output is embedded in a parent span's payload
- **THEN** the markers it carries are exported unchanged

### Requirement: Replacement markers identify the detector and distinguish distinct secrets

The system SHALL replace a detected value with a marker naming the detector that matched. In the default mode the marker SHALL also carry a fingerprint derived from a cryptographic hash of the matched value, such that two occurrences of the same secret produce the same fingerprint and two different secrets produce different fingerprints with high probability. The marker SHALL NOT contain any substring of the original value. The system SHALL support a mode that omits the fingerprint and a mode that removes the value entirely.

#### Scenario: Same secret in two spans

- **WHEN** the same credential appears in two different exported spans under the default mode
- **THEN** both markers carry the same detector name and the same fingerprint

#### Scenario: Two different secrets

- **WHEN** two different credentials of the same shape are redacted under the default mode
- **THEN** their markers carry different fingerprints

#### Scenario: No plaintext survives in the marker

- **WHEN** any credential is redacted in any mode
- **THEN** the marker is composed only of a fixed literal, the detector name, and a cryptographic hash digest, with no part copied from the matched value
- **AND** two credentials sharing a long common prefix produce markers that do not share that prefix

#### Scenario: Fingerprint can be suppressed

- **WHEN** redaction mode is set to omit fingerprints
- **THEN** markers name the detector only and carry no fingerprint

### Requirement: Redaction is configurable and enabled by default

The system SHALL expose a `telemetry.redaction` configuration object with: an `enabled` boolean defaulting to **true**; a `mode` selecting the replacement marker form; a `disableBuiltins` list naming built-in detectors to switch off; a `pii` toggle enabling the personal-data detector group, defaulting to **false**; a `rules` array of operator-supplied detectors, each with a name, a regular-expression pattern, an optional capture-group index and an optional replacement; and an `allowlist` of literal values never to redact. Operator rules SHALL be expressed as an array of objects, never as an object keyed by operator-supplied names, so that configuration loading cannot alter a rule name or pattern. The configuration object SHALL appear in the generated configuration JSON schema. Configuration loading SHALL preserve the case of every rule name and pattern.

#### Scenario: Enabled by default

- **WHEN** the configuration does not mention `telemetry.redaction`
- **THEN** built-in secret detectors are active on all content-bearing payloads

#### Scenario: Redaction can be switched off

- **WHEN** `telemetry.redaction.enabled` is set to false
- **THEN** payloads are exported without redaction, byte-identical to the pre-change behavior

#### Scenario: A built-in detector can be disabled

- **WHEN** `telemetry.redaction.disableBuiltins` names a built-in detector
- **THEN** that detector no longer matches and the remaining built-ins still apply

#### Scenario: An operator rule adds a shape

- **WHEN** `telemetry.redaction.rules` declares a pattern
- **THEN** values matching it are replaced using the rule's name in the marker
- **AND** when the rule names a capture group, only that group is replaced

#### Scenario: Rule names and patterns survive configuration loading

- **WHEN** a rule is declared with an uppercase-bearing name and a case-sensitive pattern and the configuration is loaded through the full loader
- **THEN** the compiled rule's name and pattern are character-for-character what was written in the file

#### Scenario: An allowlisted value is preserved

- **WHEN** a payload contains a literal listed in `telemetry.redaction.allowlist`
- **THEN** it is exported unchanged even though a detector matches it

#### Scenario: PII detectors are off unless enabled

- **WHEN** the configuration does not enable the `pii` group and a payload contains an email address or an IP address
- **THEN** the payload is exported unchanged
- **AND** when `pii` is enabled, those values are replaced

#### Scenario: Configuration object is present in the schema

- **WHEN** the configuration JSON schema is generated
- **THEN** `telemetry.redaction` is present with its fields typed and described, and its `mode` enum matches the values the loader accepts

### Requirement: An invalid operator rule fails configuration loading

The system SHALL validate operator-supplied redaction rules when configuration is loaded. A rule whose pattern is not a valid regular expression, whose name is empty, or whose capture-group index does not exist in its pattern SHALL cause configuration loading to fail with an error naming the offending rule. The system SHALL NOT silently ignore an invalid rule or fall back to running without it.

#### Scenario: Malformed pattern

- **WHEN** a redaction rule declares a pattern that does not compile
- **THEN** configuration loading fails with an error naming that rule

#### Scenario: Pattern uses an unsupported construct

- **WHEN** a redaction rule declares a pattern using lookahead or lookbehind, which the regular-expression engine does not support
- **THEN** configuration loading fails with an error naming that rule and identifying the unsupported construct, rather than reporting a raw parser error

#### Scenario: Capture group out of range

- **WHEN** a rule names a capture-group index its pattern does not define
- **THEN** configuration loading fails with an error naming that rule

#### Scenario: Valid rules load

- **WHEN** every declared rule is valid
- **THEN** configuration loading succeeds and each rule is active

### Requirement: Redaction correctness is verified against a committed corpus

The system SHALL carry a committed test corpus derived from the credential shapes and false positives observed in the production incident, partitioned into values that MUST be redacted and values that MUST NOT be redacted. The test suite SHALL assert both directions and SHALL fail if any must-redact value survives or any must-not-redact value is altered. The corpus SHALL contain no real credential.

Every must-redact value SHALL be unmistakably non-live while preserving the length, character class and internal structure of the shape it stands for, so that detector coverage is unaffected. Where preserving a shape would still cause an external secret scanner to reject the corpus, the value SHALL be altered further until it does not, and the deviation SHALL be recorded alongside it; the overriding constraint is that the detector's own matching semantics remain exercised. Where the shape's character class permits alphabetic text, the value SHALL embed a literal marker identifying it as an example; where it does not — a hexadecimal key, for instance — the value SHALL use a recognisably constant filler instead. Must-not-redact values SHALL be committed verbatim: they carry no credential, and their exact characters are what the false-positive regression tests.

#### Scenario: All must-redact cases are caught

- **WHEN** the corpus's must-redact section is run through redaction with default configuration
- **THEN** every entry is replaced, both in bare form and in its embedded-context form

#### Scenario: No must-not-redact case regresses

- **WHEN** the corpus's must-not-redact section is run through redaction with default configuration
- **THEN** every entry is returned unchanged

#### Scenario: Corpus is safe to commit

- **WHEN** the corpus is inspected
- **THEN** it contains only synthetic values that are structurally valid but carry no access to any real system
- **AND** each must-redact value is recognisable as an example by inspection, without reference to the surrounding documentation

#### Scenario: Defusing a value does not weaken detection

- **WHEN** a must-redact value is replaced by its non-live equivalent
- **THEN** its length, character class and internal segment structure are unchanged
- **AND** the detector that matched the original still matches it, including any acceptance gate that depends on letter case or segment shape

#### Scenario: A value an external scanner still rejects is altered further

- **WHEN** a defused value retains a shape that an external secret scanner treats as a live credential
- **THEN** the value is altered until the scanner accepts it, even where that changes its length or segment shape
- **AND** the detector under test still matches the altered value
- **AND** the reason for the deviation is recorded next to it in the corpus

#### Scenario: False-positive values are unmodified

- **WHEN** the must-not-redact section is compared against the strings observed in production
- **THEN** they are character-for-character identical
