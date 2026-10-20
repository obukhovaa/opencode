## Why

The Langfuse client builds its OpenTelemetry resource with only `service.name` and `service.version`, so every trace lands in Langfuse's `default` environment. Deployments that share one Langfuse project (a dev and a prod install, say) cannot tell their traces apart or filter on them, and the environment that Langfuse's own SDKs set from `LANGFUSE_TRACING_ENVIRONMENT` is ignored.

## What Changes

- New optional config field `telemetry.langfuse.environment`. Like `publicKey`, `secretKey` and `baseURL` it supports `env:VAR_NAME` and, when empty, falls back to an environment variable: `LANGFUSE_TRACING_ENVIRONMENT`.
- A valid value is set on the resource of every exported span as `langfuse.environment` and `deployment.environment.name`. The deprecated `deployment.environment` key is not set, and the semconv import is unchanged.
- Langfuse's naming rule (lowercase letters, digits, `-`, `_`; at most 40 characters; no `langfuse` prefix) is checked once, in the langfuse package. An invalid value is logged as a warning that names it and the reason, then dropped; tracing stays on and startup never fails. A value containing `$` is reported as an unexpanded placeholder.
- `langfuse.New` and `langfuse.Init` take the environment as a fourth argument; every caller passes it.
- Schema: the field is a plain string with no `pattern` or `maxLength`, because a config may hold a `$VAR` placeholder that the deployment fills before opencode reads it.
- Out of scope: `resource.WithFromEnv`, and any handling of `OTEL_RESOURCE_ATTRIBUTES`. The OpenTelemetry SDK already merges `OTEL_RESOURCE_ATTRIBUTES` and `OTEL_SERVICE_NAME` underneath the resource opencode builds, with opencode's keys winning. So when `telemetry.langfuse.environment` is unset or dropped as invalid, an environment set there still reaches Langfuse; opencode does not validate it, and this change does not strip it.

## Capabilities

### New Capabilities

- `telemetry-environment`: the Langfuse trace environment a deployment's traces are filed under.

## Impact

- `internal/config` (`LangfuseConfig.Environment`), `internal/langfuse/client.go` (`New`, `Init`, `newResource`, `validateEnvironment`), `cmd/root.go`, `cmd/serve.go`, `cmd/redaction-e2e`, the agent test helper that installs a client, `cmd/schema` + `opencode-schema.json`, `docs/telemetry.md`, `README.md`.
- `.opencode.json` public contract: one additive optional field.
- No behaviour change when neither the field nor `LANGFUSE_TRACING_ENVIRONMENT` is set: the resource is the same as before.
