# OpenCode Development Guide

## Build/Test Commands
- **Build**: `./scripts/snapshot` (uses goreleaser)
- **Test**: `go test ./...` (all packages) or `go test ./internal/llm/agent` (single package)
- **Generate schema**: `go run cmd/schema/main.go > opencode-schema.json`
- **Database migrations**: Uses sqlc for SQL code generation from `internal/db/sql/`
- **Security check**: `./scripts/check_hidden_chars.sh` (detects hidden Unicode)

## Code Style Guidelines

### Imports
- Three groups: stdlib, external, internal (separated by blank lines)
- Sort alphabetically within groups
- Internal imports: `github.com/opencode-ai/opencode/internal/...`

### Naming
- Variables: camelCase (`filePath`, `contextWindow`)
- Functions: PascalCase exported, camelCase unexported
- Types/Interfaces: PascalCase, interfaces often end with "Service"
- Packages: lowercase single word (`agent`, `config`)

### Error Handling
- Named error variables: `var ErrRequestCancelled = errors.New(...)`
- Early returns: `if err != nil { return nil, err }`
- Error wrapping: `fmt.Errorf("context: %w", err)`

### Testing
- Table-driven tests with anonymous structs
- Subtests with `t.Run(name, func(t *testing.T) {...})`
- Test naming: `Test<FunctionName>`