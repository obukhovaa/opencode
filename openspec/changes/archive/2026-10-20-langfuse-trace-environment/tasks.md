## 1. Config and schema

- [x] 1.1 `LangfuseConfig.Environment` (`telemetry.langfuse.environment`), carried verbatim by config
- [x] 1.2 `cmd/schema/main.go` declares the field as a string without `pattern` / `maxLength`; `opencode-schema.json` regenerated

## 2. Client

- [x] 2.1 `newResource` resolves the value (`env:VAR_NAME`, else `LANGFUSE_TRACING_ENVIRONMENT`) and adds `langfuse.environment` and `deployment.environment.name` when valid
- [x] 2.2 `validateEnvironment` holds Langfuse's rule, citing the Langfuse docs; an invalid value warns and is dropped, a `$` is reported as an unexpanded placeholder
- [x] 2.3 `langfuse.New` / `langfuse.Init` take the environment; `cmd/root.go`, `cmd/serve.go`, `cmd/redaction-e2e` and the agent test helper pass it

## 3. Tests

- [x] 3.1 `newResource` table: set, unset, `env:VAR_NAME`, fallback, config over fallback, invalid values (uppercase, over 40 characters, `langfuse` prefix, `$` placeholders, space, dot)
- [x] 3.2 `validateEnvironment` table, including the placeholder wording; the logged warning names the value and the reason
- [x] 3.3 Through `New` against an OTLP endpoint: the attributes on the exported resource, and an invalid value leaves the client enabled
- [x] 3.4 Config load round-trip for `telemetry.langfuse.environment`

## 4. Docs and verification

- [x] 4.1 `docs/telemetry.md` (field table, Trace Environment section) and the README environment variable table
- [x] 4.2 `gofmt -l`, `go vet`, `go test ./internal/langfuse/... ./internal/config/... ./cmd/...`, `make schema-check`, `scripts/test/redaction.sh`
- [x] 4.3 `make test`'s test step by hand (`go test -race -short ./...`; `make test` itself stops at `go generate` without `mockgen` on PATH): every failure also fails on a clean `main`
