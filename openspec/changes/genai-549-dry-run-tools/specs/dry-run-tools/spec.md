## ADDED Requirements

### Requirement: In dry-run, external writes SHALL be recorded and not executed

In a run started with `--dry-run-root`, a call to a tool on its MCP server's `dryRun.write` list SHALL NOT reach the server. It SHALL be recorded and answered with a success response whose metadata carries `dryRun: true`.

#### Scenario: Create MR

- **WHEN** a dry-run run calls `gitlab_create_merge_request`, listed under the GitLab server's `dryRun.write`
- **THEN** the GitLab MCP server receives no call, the model gets an object with a fake `iid` and `web_url`, and the call's trace span carries `dryRun: true` in its response metadata

#### Scenario: Read

- **WHEN** the same run calls `gitlab_get_merge_request` for a real MR, listed under `dryRun.read`
- **THEN** the call reaches the server and the real MR is returned

### Requirement: In dry-run, an unclassified tool SHALL be refused

A tool that is neither a local built-in nor on its server's `dryRun.read`, `dryRun.write` or `dryRun.passthrough` list SHALL NOT run in dry-run, and the model SHALL get an error naming the tool as not classified for dry-run. A tool on `dryRun.passthrough` SHALL run.

#### Scenario: New server tool

- **WHEN** the TeamCity server gains `teamcity_pin_build`, nobody adds it to any list, and a dry-run run calls it
- **THEN** the server receives no call and the model gets the not-classified error

#### Scenario: Child job

- **WHEN** a dry-run run calls the orchestrator's `createJob`, listed under its `dryRun.passthrough`
- **THEN** the call reaches the orchestrator, which creates the child as a dry-run job

### Requirement: In dry-run, bash SHALL NOT run a script that contains a write command

A bash script that contains `git push`, a writing `glab` subcommand, or `curl`/`wget` with a non-GET method or a body SHALL NOT run, matched by command basename anywhere in the script. It SHALL be recorded and answered with a success that names the faked command. A script with none of these SHALL run.

#### Scenario: Absolute path in a chain

- **WHEN** a dry-run run executes `go test ./... && /usr/bin/git push origin HEAD`
- **THEN** neither command runs, the response names `git push`, and the full script is recorded

#### Scenario: Local commit

- **WHEN** a dry-run run executes `git commit -am "fix"`
- **THEN** the commit is made in the pod's checkout

### Requirement: In dry-run, paths outside the tool dispatcher SHALL follow the same rules

Bridge and flow chat posts SHALL NOT be sent. Each command in `` !`cmd` `` prompt markup and each hook command SHALL go through the bash classifier. Webfetch SHALL allow only GET.

#### Scenario: Step chat message

- **WHEN** a dry-run flow step would post its summary to the bridge thread
- **THEN** nothing is sent and the text is logged

### Requirement: A read of an object faked in the same run SHALL be answered from the record

A read whose input names a fake id issued in this run SHALL return the object the faked write returned. A read naming a fake id that this run never issued SHALL fail with a dry-run error, not with the server's 404.

#### Scenario: Comment on a faked MR, then read it

- **WHEN** a dry-run run creates an MR (fake `iid` 9000000001), posts a note on it, then calls `gitlab_get_merge_request` with `iid` 9000000001
- **THEN** the note is faked, and the read returns the faked MR without calling GitLab

### Requirement: Dry-run SHALL be set for the whole process by `--dry-run-root`

`opencode --dry-run-root <root>` SHALL run the process in dry-run, for every step, subagent, background shell and resumed session in it. A root that does not match `[A-Za-z0-9-]+` SHALL end the process with an error before any tool call. An unknown CLI flag SHALL end the process with a non-zero exit before any tool call.

#### Scenario: Subagent

- **WHEN** a run started with `--dry-run-root` spawns an async subagent that calls `gitlab_create_merge_request`
- **THEN** the call is recorded and faked

#### Scenario: Bad root

- **WHEN** opencode is started with `--dry-run-root "a/b"`
- **THEN** it exits non-zero and no tool runs

### Requirement: Dry-run flow state SHALL use its own session prefix

In dry-run, the session prefix of a flow run SHALL start with `dry-run-<root>-`, whether it was resolved from the flow's `session.prefix` or given by the caller.

#### Scenario: Live rows on the same issue

- **WHEN** a live `implement` run on `TPWEBAPP-12345` left `flow_states` rows, and a dry-run `implement` run on the same issue starts with `--dry-run-root 3f2a`
- **THEN** the dry-run run writes and reads rows under `dry-run-3f2a-TPWEBAPP-12345`, and the live rows are unchanged
