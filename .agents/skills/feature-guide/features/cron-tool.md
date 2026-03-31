# Feature: Cron Tool

## Overview
Add a cron-based scheduling tool that allows agents to create recurring tasks.

## Goals
- Agents can schedule commands to run on a cron schedule
- Schedules persist across sessions
- Users can list and cancel scheduled tasks

## Architecture
- Store schedules in SQLite via sqlc
- Use `robfig/cron` for schedule parsing and execution
- Expose via `cron-create`, `cron-list`, `cron-delete` tools

## Implementation Steps
1. Add migration for `cron_schedules` table
2. Create `internal/cron/service.go` with CRUD operations
3. Register tools in the tool registry
4. Add TUI display for active schedules

## Acceptance Criteria
- [ ] Agent can create a cron schedule with a valid expression
- [ ] Schedules survive process restart
- [ ] `cron-list` shows all active schedules with next run time
- [ ] `cron-delete` removes a schedule by ID
