## ADDED Requirements

### Requirement: In dry-run, external writes SHALL be recorded and not executed

When the job args carry `dry_run: true`, a call to a tool on its MCP server's `dryRun.write` list SHALL NOT reach the server. It SHALL be recorded and answered with a success response whose metadata carries `dryRun: true`.

#### Scenario: Create MR

- **WHEN** a dry-run run calls `gitlab_create_merge_request`, listed under the GitLab server's `dryRun.write`
- **THEN** the GitLab MCP server receives no call, the model gets an object with a fake `iid` and `web_url`, and `writes.jsonl` has one line for the call

#### Scenario: Read

- **WHEN** the same run calls `gitlab_get_merge_request` for a real MR, listed under `dryRun.read`
- **THEN** the call reaches the server and the real MR is returned

### Requirement: In dry-run, an unclassified tool SHALL be refused

A tool that is neither a local built-in nor on its server's `dryRun.read` or `dryRun.write` list SHALL NOT run in dry-run. The model SHALL get an error naming the tool as not classified for dry-run.

#### Scenario: New server tool

- **WHEN** the TeamCity server gains `teamcity_pin_build`, nobody adds it to either list, and a dry-run run calls it
- **THEN** the server receives no call and the model gets the not-classified error

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

### Requirement: The runtime SHALL confirm dry-run to its caller

`opencode --dry-run` SHALL start a run in dry-run. A `POST /flow` request whose args carry `dry_run: true` SHALL be answered with `dryRun: true` once the run is accepted in dry-run.

#### Scenario: Pool start

- **WHEN** the orchestrator posts a flow with `"args": {"dry_run": true}`
- **THEN** the response carries `dryRun: true`

### Requirement: Dry-run state SHALL NOT outlive its run

A pool pod that serves a dry-run run and then a live run SHALL run the live run's tools for real. A dry-run run after a live one SHALL intercept from its first call.

#### Scenario: Dry, live, dry

- **WHEN** one pool pod serves a dry-run run, a live run and a dry-run run in that order
- **THEN** only the live run's `gitlab_create_merge_request` reaches GitLab
