# Session Provider Feature

**Date**: 2026-02-04
**Status**: Implemented
**Author**: AI-assisted

## Overview

This feature enables OpenCode to support multiple database backends for session storage, allowing users to choose between local SQLite (default) or remote MySQL database. This provides flexibility for different deployment scenarios, from single-user local development to multi-user team environments with centralized session storage.

Sessions are scoped by **project** to ensure that when using a shared MySQL database, users only see sessions relevant to their current project. Project identification is based on Git repository origin or working directory.

## Motivation

- **Local Development**: SQLite provides zero-configuration, file-based storage ideal for individual developers
- **Team Collaboration**: MySQL enables centralized session storage for teams sharing session history across multiple machines
- **Project Isolation**: Sessions are automatically scoped to projects, preventing session pollution in shared databases
- **Scalability**: MySQL supports concurrent access and better performance for high-volume usage
- **Flexibility**: Users can choose the appropriate backend based on their deployment requirements

## Architecture

### Session Provider Interface

The session provider abstraction will be implemented at the database connection level, maintaining the existing `db.Querier` interface while supporting different underlying database engines.

### Project Identification

Sessions are scoped to projects using a `project_id` field. The project ID is determined by:

1. **Git Repository**: If the current working directory is within a Git repository, use the Git remote origin URL as the project ID
   - Example: `https://github.com/opencode-ai/opencode.git` → `github.com/opencode-ai/opencode`
   - Normalize by removing protocol, `.git` suffix, and trailing slashes
   
2. **Directory Name**: If no Git repository exists, use the base name of the current working directory
   - Example: `/Users/john/projects/my-app` → `my-app`

This ensures that:
- Teams working on the same Git repository share sessions (when using MySQL)
- Different projects remain isolated even in shared databases
- Local non-Git projects are scoped to their directory name

### Supported Providers

1. **SQLite Provider** (default)
   - File-based local storage
   - Zero configuration required
   - Suitable for single-user scenarios
   - Current default behavior
   - Sessions still scoped by project for consistency

2. **MySQL Provider**
   - Remote database connection
   - Requires connection configuration
   - Supports concurrent access
   - Suitable for team/multi-user scenarios
   - Sessions automatically filtered by project

## Configuration

### Config Structure

Add new `SessionProvider` configuration section to `internal/config/config.go`:

```json
{
  "sessionProvider": {
    "type": "sqlite",  // or "mysql"
    "mysql": {
      "host": "localhost",
      "port": 3306,
      "database": "opencode",
      "username": "opencode_user",
      "password": "secure_password",
      "maxConnections": 10,
      "maxIdleConnections": 5,
      "connectionTimeout": 30
    }
  }
}
```

### Environment Variables

Support environment variable overrides for sensitive data:

- `OPENCODE_SESSION_PROVIDER_TYPE`: Provider type (sqlite/mysql)
- `OPENCODE_MYSQL_DSN`: Complete MySQL DSN (overrides individual settings)

### Default Behavior

- If no `sessionProvider` configuration exists, default to SQLite
- SQLite uses existing `data.directory` configuration for database file location
- MySQL requires explicit configuration; application fails fast with clear error if MySQL is selected but not properly configured

## Implementation Plan

### Phase 1: Configuration & Abstraction

1. **Update Config Package** (`internal/config/config.go`)
   - Add `SessionProvider` struct with type and MySQL configuration
   - Add validation for MySQL configuration when type is "mysql"
   - Add environment variable support for MySQL credentials

2. **Create Provider Interface** (`internal/db/provider.go`)
   - Define `Provider` interface with `Connect() (*sql.DB, error)` method
   - Define `ProviderType` enum (SQLite, MySQL)

### Phase 2: Provider Implementations

3. **SQLite Provider** (`internal/db/sqlite_provider.go`)
   - Extract existing SQLite connection logic from `connect.go`
   - Implement `Provider` interface
   - Maintain existing SQLite-specific optimizations (pragmas, WAL mode)

4. **MySQL Provider** (`internal/db/mysql_provider.go`)
   - Implement MySQL connection with DSN building
   - Add connection pooling configuration
   - Implement connection timeout and retry logic
   - Add MySQL-specific optimizations

### Phase 3: Schema Updates & Migration Support

5. **Add Project ID to Sessions** (`internal/db/migrations/`)
   - Create new migration to add `project_id` column to sessions table
   - Add index on `project_id` for efficient filtering
   - Add composite index on `(project_id, created_at)` for listing queries
   - Update session queries to filter by project_id

6. **Update Migration System**
   - Create MySQL-compatible versions of existing migrations
   - Use conditional migration files based on provider type
   - Ensure schema compatibility between SQLite and MySQL
   - Handle dialect differences (e.g., `strftime` vs `UNIX_TIMESTAMP`)

7. **Migration Strategy**
   - SQLite migrations: `migrations/sqlite/`
   - MySQL migrations: `migrations/mysql/`
   - Shared schema definitions where possible
   - Provider-specific SQL for timestamps, triggers, and functions

8. **Backfill Migration for Existing Sessions**
   - Create data migration to populate `project_id` for existing sessions
   - Run on application startup if `project_id` is NULL for any sessions
   - Use Git repository detection or directory name based on `data.directory` location
   - Log migration progress for transparency

### Phase 4: Session Service Updates

9. **Update Session Service** (`internal/session/session.go`)
   - Add `ProjectID` field to `Session` struct
   - Update `Create()` to accept and store project ID
   - Update `List()` to filter by project ID
   - Add `GetProjectID()` helper function to determine current project ID
   - Update all session creation points to include project ID

10. **Update Session Queries** (`internal/db/sql/sessions.sql`)
    - Modify `ListSessions` to filter by `project_id`
    - Add `ListSessionsByProject` query
    - Update `CreateSession` to include `project_id`
    - Ensure all queries respect project scoping

### Phase 5: Connection Management

11. **Update Connect Function** (`internal/db/connect.go`)
    - Refactor to use provider pattern
    - Select provider based on configuration
    - Initialize appropriate provider
    - Run provider-specific migrations
    - Execute backfill migration for existing sessions

12. **Update SQLC Configuration** (`sqlc.yaml`)
    - Support multiple engine configurations
    - Generate code for both SQLite and MySQL
    - Handle dialect-specific query differences

### Phase 6: UI Updates

13. **Update TUI** (`internal/tui/`)
    - Display current project ID in the info/status section
    - Show session provider type indicator:
      - "local" for SQLite
      - "remote" for MySQL
    - Update session list to show only current project's sessions
    - Add visual indicator for provider type (icon or label)

14. **Update Status Display** (`internal/tui/components/core/status.go`)
    - Add project ID display
    - Add provider type indicator
    - Ensure layout accommodates new information

### Phase 7: Testing & Documentation

15. **Testing**
    - Unit tests for each provider implementation
    - Unit tests for project ID detection (Git and directory-based)
    - Integration tests with real SQLite and MySQL databases
    - Test migration compatibility and backfill logic
    - Test connection pooling and error handling
    - Test configuration validation
    - Test project scoping in both providers
    - Test TUI displays correct project and provider info

16. **Documentation**
    - Update README with session provider configuration examples
    - Add MySQL setup guide
    - Document project ID detection logic
    - Document migration process including backfill
    - Add troubleshooting section
    - Document TUI changes

## Project ID Detection

### Git Repository Detection

```go
// Pseudo-code for Git origin detection
func getProjectIDFromGit(workingDir string) (string, error) {
    // Check if .git directory exists
    // Run: git config --get remote.origin.url
    // Parse URL and normalize:
    //   - Remove protocol (https://, git@, etc.)
    //   - Remove .git suffix
    //   - Remove trailing slashes
    //   - Convert git@github.com:user/repo to github.com/user/repo
    // Return normalized path
}
```

**Examples:**
- `https://github.com/opencode-ai/opencode.git` → `github.com/opencode-ai/opencode`
- `git@github.com:opencode-ai/opencode.git` → `github.com/opencode-ai/opencode`
- `https://gitlab.com/myteam/myproject` → `gitlab.com/myteam/myproject`

### Directory-Based Fallback

```go
// Pseudo-code for directory-based project ID
func getProjectIDFromDirectory(workingDir string) string {
    // Return base name of working directory
    return filepath.Base(workingDir)
}
```

**Examples:**
- `/Users/john/projects/my-app` → `my-app`
- `/home/dev/work/client-project` → `client-project`

### Combined Logic

```go
func GetProjectID(workingDir string) string {
    // Try Git first
    if projectID, err := getProjectIDFromGit(workingDir); err == nil {
        return projectID
    }
    // Fallback to directory name
    return getProjectIDFromDirectory(workingDir)
}
```

## Schema Changes

### Sessions Table Update

**New Schema:**
```sql
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,  -- NEW FIELD
    parent_session_id TEXT,
    title TEXT NOT NULL,
    message_count INTEGER NOT NULL DEFAULT 0,
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cost REAL NOT NULL DEFAULT 0.0,
    updated_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

-- NEW INDEXES
CREATE INDEX idx_sessions_project_id ON sessions(project_id);
CREATE INDEX idx_sessions_project_created ON sessions(project_id, created_at DESC);
```

### Migration for Existing Data

**SQLite Migration:**
```sql
-- Add project_id column (nullable initially)
ALTER TABLE sessions ADD COLUMN project_id TEXT;

-- Backfill will be done in application code on startup
-- Application will detect NULL project_id and populate based on data.directory location
```

**Application Backfill Logic:**
```go
// On startup, check for sessions with NULL project_id
// For each session:
//   - Determine project ID based on data.directory location
//   - Update session with project_id
// This ensures existing sessions get proper project scoping
```

## SQL Compatibility Considerations

### Timestamp Handling

**SQLite:**
```sql
strftime('%s', 'now')  -- Unix timestamp
```

**MySQL:**
```sql
UNIX_TIMESTAMP()  -- Unix timestamp
```

### Auto-increment IDs

Both SQLite and MySQL support auto-increment, but OpenCode uses UUIDs for IDs, so no changes needed.

### Triggers

Triggers need to be rewritten for MySQL syntax:

**SQLite:**
```sql
CREATE TRIGGER update_sessions_updated_at
AFTER UPDATE ON sessions
BEGIN
  UPDATE sessions SET updated_at = strftime('%s', 'now')
  WHERE id = new.id;
END;
```

**MySQL:**
```sql
CREATE TRIGGER update_sessions_updated_at
BEFORE UPDATE ON sessions
FOR EACH ROW
SET NEW.updated_at = UNIX_TIMESTAMP();
```

### Data Types

- SQLite `TEXT` → MySQL `VARCHAR(255)` or `TEXT`
- SQLite `INTEGER` → MySQL `BIGINT`
- SQLite `REAL` → MySQL `DOUBLE`
- `project_id`: VARCHAR(512) in MySQL (to accommodate long Git URLs)

## Error Handling

### Connection Errors

- Clear error messages for missing MySQL configuration
- Retry logic for transient MySQL connection failures
- Graceful fallback messaging (but no automatic fallback to SQLite)

### Migration Errors

- Validate schema compatibility before running migrations
- Provide rollback capability for failed migrations
- Clear error messages for migration failures

## Security Considerations

1. **Credential Storage**
   - Never store MySQL passwords in plain text in config files
   - Prefer environment variables for sensitive data
   - Support external secret management systems (future enhancement)

2. **Connection Security**
   - Support TLS/SSL for MySQL connections (future enhancement)
   - Validate certificate chains (future enhancement)
   - Support SSH tunneling (future enhancement)

## Performance Considerations

1. **Connection Pooling**
   - Configure appropriate pool sizes for MySQL
   - Implement connection health checks
   - Handle connection timeouts gracefully

2. **Query Optimization**
   - Ensure indexes are created for both providers
   - Test query performance on both backends
   - Use prepared statements (already implemented via sqlc)

## Migration Path for Existing Users

### Existing SQLite Users

Existing users with SQLite databases will continue to work with automatic migration:

1. **Automatic Schema Update**: On first run after upgrade, the migration adds `project_id` column
2. **Automatic Backfill**: Application detects NULL `project_id` values and populates them:
   - Attempts to detect Git repository from `data.directory` location
   - Falls back to directory name if no Git repository found
3. **No User Action Required**: Migration is transparent and automatic

### Migrating to MySQL

To switch from SQLite to MySQL:

1. Set up MySQL database
2. Update configuration to use MySQL provider
3. Run OpenCode (migrations will create schema automatically)
4. Existing SQLite sessions remain accessible if you switch back
5. No automatic data migration between providers (manual export/import if needed)

### Project ID Consistency

**Important**: The project ID for existing sessions is determined by the location of the SQLite database file (`data.directory`), not the current working directory. This means:

- If you have a SQLite database in `~/.opencode/` and work on project A, existing sessions will be tagged with the project ID based on `~/.opencode/` location
- New sessions will use the correct project ID based on the current working directory
- This is acceptable as it maintains backward compatibility while new sessions get proper scoping

## Future Enhancements

- PostgreSQL provider support
- Automatic data migration tool (SQLite → MySQL)
- Session sharing/synchronization across multiple instances
- Read replicas support for MySQL
- Database backup/restore utilities

## UI/UX Changes

### TUI Status Bar

The TUI status/info section will display:

```
┌─────────────────────────────────────────────────────────────┐
│ Project: github.com/opencode-ai/opencode | Provider: local  │
│ Session: Implement session provider feature                 │
└─────────────────────────────────────────────────────────────┘
```

**Elements:**
- **Project**: Current project ID (Git origin or directory name)
- **Provider**: "local" (SQLite) or "remote" (MySQL)
- **Session**: Current session title (existing)

### Session List

Sessions in the sidebar will only show sessions for the current project, preventing clutter when using shared MySQL databases.

## Success Criteria

- [ ] Users can configure MySQL as session provider via config file
- [ ] Users can configure MySQL via environment variables
- [ ] SQLite remains the default with zero configuration
- [ ] All existing functionality works with both providers
- [ ] Sessions are properly scoped by project ID
- [ ] Project ID detection works for Git repositories and directories
- [ ] Migrations run successfully on both providers
- [ ] Existing sessions are automatically backfilled with project IDs
- [ ] Connection errors are handled gracefully with clear messages
- [ ] Performance is acceptable on both providers
- [ ] TUI displays project ID and provider type
- [ ] Session list shows only current project's sessions
- [ ] Documentation is complete and clear
- [ ] Tests cover both provider implementations and project scoping

## Non-Goals

- Automatic migration of data between providers
- Support for other databases (PostgreSQL, MongoDB, etc.) in this phase
- Distributed session management
- Real-time session synchronization across instances
