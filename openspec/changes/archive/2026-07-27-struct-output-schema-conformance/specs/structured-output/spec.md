## ADDED Requirements

### Requirement: struct_output materializes declared schema defaults for omitted fields

When the output schema is an object with `properties`, the `struct_output` tool MUST, before returning its result, fill in any property the caller omitted that declares a `default` in the schema, using a deep copy of that default value. It MUST descend recursively into properties that are present and are themselves objects, materializing their nested defaults. It MUST NOT overwrite a value the caller supplied, even if the schema also declares a default for that property.

This keeps the returned JSON (which flow routing derives its args from) complete, so a routing rule that references a defaulted field evaluates against a present key rather than matching no rule.

#### Scenario: Omitted field with a default is filled

- **WHEN** `struct_output` is called with a payload that omits a property whose schema declares `"default": []`
- **THEN** the returned JSON MUST contain that property set to the declared default (`[]`)
- **AND** the result MUST NOT be an error

#### Scenario: Provided value is not overridden by a default

- **WHEN** a payload supplies a value for a property that also declares a default
- **THEN** the returned JSON MUST preserve the caller-supplied value unchanged

#### Scenario: Nested default is materialized inside a present object

- **WHEN** a payload includes a nested object that omits one of its own defaulted sub-properties
- **THEN** the returned JSON MUST fill that sub-property with its declared default

#### Scenario: Materialized default does not alias the schema

- **WHEN** two `struct_output` calls on the same tool instance each materialize the same default and one call's result is subsequently mutated
- **THEN** the other call's materialized default MUST be unaffected

### Requirement: struct_output rejects a missing required field that has no default

When the output schema is an object with `properties`, the `struct_output` tool MUST, after applying defaults, reject the call if any field listed in the schema's top-level `required` is still absent. The rejection MUST be an error tool response naming the missing field(s), so the agent loop re-enters and the model retries with a complete payload rather than persisting an incomplete output. A required field that declares a default is satisfied by the materialized default and MUST NOT trigger a rejection.

Non-object schemas, and object schemas without a `properties` map (the wrapped single-`output` fallback), are exempt: they pass through with the pre-existing JSON-validity check only.

#### Scenario: Required field with no default is missing

- **WHEN** `struct_output` is called with a payload that omits a `required` property that declares no default
- **THEN** the tool MUST return an error response naming the missing property
- **AND** the error MUST NOT name fields that were present

#### Scenario: Required field with a default is omitted

- **WHEN** a payload omits a property that is both `required` and declares a default
- **THEN** the tool MUST fill the default and MUST NOT return an error

#### Scenario: Multiple required fields missing

- **WHEN** several `required` properties without defaults are all absent
- **THEN** the error response MUST list every missing property in a deterministic (sorted) order

#### Scenario: Syntactically invalid JSON

- **WHEN** the tool input is not valid JSON
- **THEN** the tool MUST return the existing `Invalid JSON` error and MUST NOT attempt default/required processing
