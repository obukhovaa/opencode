# Development

Contributor-facing build, test, container and release notes. Code style, schema rules and the docs index for agents working in the repo are in [`AGENTS.md`](../AGENTS.md).

## Prerequisites

- Go 1.25.8 or higher (see `go.mod`)

## Building from source

```bash
git clone https://github.com/obukhovaa/opencode.git
cd opencode
make build
```

## Testing

```bash
go test ./...              # all packages
go test ./internal/config  # one package
make test                  # lint + full test suite; run before opening a PR
make test-e2e              # scripts/test/*.sh against a freshly built binary
```

Changing a field in `.opencode.json` means regenerating the schema: `make schema` (CI runs `make schema-check`).

## Docker

Build and run OpenCode in a container:

```bash
# Build the Docker image (cross-compiles a Linux binary automatically)
make docker-build
```

All CLI arguments are passed through directly:

```bash
# Non-interactive prompt
docker run opencode:latest -p "Explain context in Go" -f json -q

# Run a flow
docker run opencode:latest -F my-flow -A key1=value1 -A key2=value2

# With timeout
docker run opencode:latest -p "Refactor this module" -t 5m -q
```

Mount your configuration and workspace as volumes:

```bash
docker run -ti --rm \
  -e LOCAL_ENDPOINT_API_KEY="${LOCAL_ENDPOINT_API_KEY}" \
  -e LOCAL_ENDPOINT="${LOCAL_ENDPOINT}" \
  -e VERTEXAI_PROJECT="${VERTEXAI_PROJECT}" \
  -e VERTEXAI_LOCATION="${VERTEXAI_LOCATION:-global}" \
  -e VERTEXAI_LOCATION_COUNT="${VERTEXAI_LOCATION_COUNT:-us-east5}" \
  -v ~/.opencode.json:/workspace/.opencode.json \
  -v $(pwd):/workspace \
  --network opencode_default \
  opencode:latest  # you can pass args here, e.g. -p "Analyze this codebase"
```

To run non-interactively, pass any of the [command-line flags](../README.md#command-line-flags):

```bash
docker run --rm \
  -v ~/.opencode.json:/workspace/.opencode.json \
  -v $(pwd):/workspace \
  --network opencode_default \
  opencode:latest -p "Analyze this codebase" -q
```

The container uses `/workspace` as its working directory. Mount `.opencode.json` there to provide configuration — it is not baked into the image.

## Release

```bash
make release SCOPE=patch
# or
make release SCOPE=minor
```
