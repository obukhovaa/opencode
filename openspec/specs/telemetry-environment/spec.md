# Telemetry Environment

## Purpose

Defines the Langfuse trace environment a deployment's traces are filed under, so deployments that share one Langfuse project (dev and prod, say) can be told apart: how `telemetry.langfuse.environment` resolves, the resource attributes that carry it, and how an invalid value is dropped without failing startup or disabling tracing.

## Requirements

### Requirement: The environment resolves like the Langfuse keys

The system SHALL resolve `telemetry.langfuse.environment` the way it resolves the Langfuse keys: a literal value, an `env:VAR_NAME` reference, or, when the field is empty, the `LANGFUSE_TRACING_ENVIRONMENT` environment variable.

#### Scenario: Environment from a reference

- **WHEN** `telemetry.langfuse.environment` is `env:AGENT_ENV` and `AGENT_ENV` is `prod`
- **THEN** the resolved environment is `prod`

#### Scenario: Environment from the fallback variable

- **WHEN** `telemetry.langfuse.environment` is empty and `LANGFUSE_TRACING_ENVIRONMENT` is `prod`
- **THEN** the resolved environment is `prod`

### Requirement: Traces carry the resolved environment

When the resolved environment is valid, the system SHALL set it on the OpenTelemetry resource of every exported span under both `langfuse.environment` and `deployment.environment.name`. When it is empty, the system SHALL add no environment attribute, so the resource it builds carries only `service.name` and `service.version` as before. The OpenTelemetry SDK still merges `OTEL_RESOURCE_ATTRIBUTES` in, unvalidated; the system's own keys win.

#### Scenario: Environment set

- **WHEN** `telemetry.langfuse.environment` is `staging`
- **THEN** every exported span's resource carries `langfuse.environment = staging` and `deployment.environment.name = staging`

#### Scenario: Environment unset

- **WHEN** neither the field nor `LANGFUSE_TRACING_ENVIRONMENT` yields a value, and `OTEL_RESOURCE_ATTRIBUTES` sets no environment
- **THEN** the exported resource carries no environment attribute and Langfuse files the traces under `default`

### Requirement: An invalid environment is dropped, never fatal

A valid environment SHALL consist of lowercase letters, digits, `-` and `_` only, be at most 40 characters long, and not start with `langfuse`, as Langfuse requires. The Langfuse client SHALL be the one place that checks it; the configuration load SHALL NOT reject the value. An invalid value SHALL be logged as a warning naming the value and the reason, and dropped, with tracing kept enabled.

#### Scenario: Invalid environment

- **WHEN** the resolved environment is `Prod`, longer than 40 characters, or starts with `langfuse`
- **THEN** a warning names the value and the reason, the value is not set on the resource, and spans are still exported

#### Scenario: Unexpanded placeholder

- **WHEN** `telemetry.langfuse.environment` is `$AGENT_ENV` because the deployment did not fill the placeholder
- **THEN** the config loads, the warning says the value looks like an unexpanded placeholder, the value is not set on the resource, and tracing stays enabled
