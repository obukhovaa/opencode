# Agent Skills Guide

## Overview

Agent skills are reusable instruction sets that provide specialized knowledge and step-by-step guidance for specific tasks. Skills are defined in markdown files with YAML frontmatter and are loaded on-demand by agents via the native `skill` tool.

## Quick Start

### Creating Your First Skill

1. **Create a skill directory:**

```bash
mkdir -p .opencode/skills/git-release
```

2. **Create the skill file** `.opencode/skills/git-release/SKILL.md`:

```markdown
---
name: git-release
description: Create consistent releases and changelogs
license: MIT
compatibility: opencode
metadata:
  audience: maintainers
  workflow: github
---

## What I do

- Draft release notes from merged PRs
- Propose a version bump
- Provide a copy-pasteable `gh release create` command

## When to use me

Use this when you are preparing a tagged release.
Ask clarifying questions if the target versioning scheme is unclear.

## Steps

1. Review recent commits since last tag using `git log`
2. Categorize changes (features, fixes, breaking changes)
3. Suggest semantic version bump based on changes
4. Generate changelog in Keep a Changelog format
5. Provide GitHub CLI command for release creation

## Example

```bash
gh release create v1.2.0 --title "v1.2.0" --notes "$(cat CHANGELOG.md)"
```
```

3. **Test the skill:**

Start OpenCode and ask: "Help me create a release"

The agent will see the skill in its available tools and can load it when appropriate.

## Skill File Structure

### Required Frontmatter Fields

```yaml
---
name: skill-name          # Required: 1-64 chars, lowercase, hyphens only
description: Brief desc   # Required: 1-1024 chars, shown to agent
---
```

### Optional Frontmatter Fields

```yaml
---
name: skill-name
description: Brief description
license: MIT              # Optional: License information
compatibility: opencode   # Optional: Compatibility marker
metadata:                 # Optional: String-to-string map
  author: team-name
  version: "1.0"
  category: git
---
```

### Content Section

After the frontmatter, write your instructions in markdown:

```markdown
## What I do

Bullet points describing the skill's purpose.

## When to use me

Guidance on when this skill is appropriate.

## Steps

1. Detailed step-by-step instructions
2. Include commands, examples, best practices
3. Provide context and reasoning

## Examples

Code blocks, commands, or sample outputs.

## Notes

Additional tips, warnings, or edge cases.
```

## Discovery Locations

OpenCode searches for skills in multiple locations, in priority order:

### Project-Level Skills

Skills specific to your project:

```
.opencode/skills/<name>/SKILL.md
.opencode/skill/<name>/SKILL.md
.agents/skills/<name>/SKILL.md
.claude/skills/<name>/SKILL.md
```

OpenCode walks up from your current directory to the git worktree root, discovering skills along the way.

### Global Skills

Skills available across all projects:

```
~/.config/opencode/skills/<name>/SKILL.md
~/.config/opencode/skill/<name>/SKILL.md
~/.agents/skills/<name>/SKILL.md
~/.claude/skills/<name>/SKILL.md
```

### Custom Paths

Add custom skill directories in your configuration:

```json
{
  "skills": {
    "paths": [
      "~/my-company-skills",
      "./team-skills",
      "/absolute/path/to/skills"
    ]
  }
}
```

**Path resolution:**
- `~/path` → Expands to home directory
- `./path` → Relative to working directory
- `/path` → Absolute path

## Naming Rules

Skill names must follow strict rules:

### Valid Names

- `git-release` ✓
- `docker-build` ✓
- `pr-review` ✓
- `test-123` ✓
- `my-cool-skill` ✓

### Invalid Names

- `Git-Release` ✗ (uppercase)
- `git_release` ✗ (underscore)
- `git release` ✗ (space)
- `-git-release` ✗ (starts with hyphen)
- `git-release-` ✗ (ends with hyphen)
- `git--release` ✗ (consecutive hyphens)

### Validation Regex

```regex
^[a-z0-9]+(-[a-z0-9]+)*$
```

### Directory Name Matching

The skill name in frontmatter **must match** the directory name:

```
✓ .opencode/skills/git-release/SKILL.md (name: git-release)
✗ .opencode/skills/git-release/SKILL.md (name: different-name)
```

## Permissions

### Global Permissions

Control skill access globally:

```json
{
  "permission": {
    "skill": {
      "*": "allow",
      "internal-*": "deny",
      "experimental-*": "ask"
    }
  }
}
```

### Permission Actions

| Action  | Behavior                                      |
|---------|-----------------------------------------------|
| `allow` | Skill loads immediately without prompting     |
| `deny`  | Skill hidden from agent, access rejected      |
| `ask`   | User prompted for approval before loading     |

### Pattern Matching

Permissions support wildcard patterns:

| Pattern         | Matches                                    |
|-----------------|--------------------------------------------|
| `git-release`   | Exact match only                           |
| `internal-*`    | `internal-docs`, `internal-tools`, etc.    |
| `*-test`        | `unit-test`, `integration-test`, etc.      |
| `*`             | All skills (global wildcard)               |

### Priority Order

When multiple patterns match, the most specific wins:

1. **Exact match**: `git-release: allow`
2. **Specific wildcard**: `internal-*: deny`
3. **Global wildcard**: `*: ask`

**Example:**

```json
{
  "permission": {
    "skill": {
      "git-release": "allow",
      "internal-*": "deny",
      "*": "ask"
    }
  }
}
```

- `git-release` → allow (exact match)
- `internal-docs` → deny (specific wildcard)
- `docker-build` → ask (global wildcard)


> **Note:** The permission system is generic and supports granular patterns for all tools, not just skills. See the [Configuration guide](../README.md#agents) for details on `bash`, `edit`, `read`, and `task` permissions.
### Agent-Specific Permissions

Override global permissions for specific agents:

```json
{
  "permission": {
    "skill": {
      "internal-*": "deny"
    }
  },
  "agents": {
    "coder": {
      "model": "claude-4-5-sonnet[1m]",
      "permission": {
        "skill": {
          "internal-*": "allow"
        }
      }
    }
  }
}
```

**Result:**
- Coder agent can access `internal-*` skills (agent override)
- Other agents cannot access `internal-*` skills (global deny)

### Disabling Skills for Agents

Agent permissions can also control other tools with granular patterns:

```json
{
  "agents": {
    "coder": {
      "permission": {
        "skill": { "internal-*": "allow" },
        "bash": { "*": "ask", "git *": "allow" },
        "edit": { "*.env": "deny", "*": "allow" }
      }
    }
  }
}
```

Completely disable the skill tool for specific agents:

```json
{
  "agents": {
    "summarizer": {
      "model": "gemini-3.0-flash",
      "tools": {
        "skill": false
      }
    },
    "descriptor": {
      "model": "gemini-3.0-flash",
      "tools": {
        "skill": false
      }
    }
  }
}
```

When disabled, the agent won't see the `<available_skills>` section in its tool description.

## Best Practices

### When to Create a Skill

Create a skill when you have:

- **Repeatable workflows**: Release process, PR reviews, deployment steps
- **Domain knowledge**: Company-specific practices, team conventions
- **Complex procedures**: Multi-step processes that benefit from guidance
- **Best practices**: Coding standards, security checks, testing strategies

### Skill Organization

**Project-specific skills** (`.opencode/skills/`):
- Workflows specific to this repository
- Project conventions and standards
- Technology-specific guidance

**Global skills** (`~/.config/opencode/skills/`):
- Personal workflows and preferences
- General best practices
- Cross-project utilities

**Team skills** (custom paths):
- Company-wide standards
- Shared team workflows
- Organizational best practices

### Writing Effective Descriptions

The description is shown to agents to help them choose the right skill:

**Good descriptions:**
- ✓ "Create consistent releases and changelogs"
- ✓ "Review pull requests with security and quality checks"
- ✓ "Set up Docker development environment"

**Poor descriptions:**
- ✗ "Git stuff" (too vague)
- ✗ "A skill for releases" (not descriptive enough)
- ✗ "This skill helps with..." (unnecessary preamble)

### Content Guidelines

**Structure your skill content:**

1. **What I do**: Brief bullet points of capabilities
2. **When to use me**: Guidance on appropriate use cases
3. **Steps**: Detailed instructions
4. **Examples**: Code snippets, commands, sample outputs
5. **Notes**: Edge cases, warnings, tips

**Keep it focused:**
- One skill = one workflow or task
- Break complex workflows into multiple skills
- Reference other skills when appropriate

**Use clear language:**
- Write for the agent, not just humans
- Be specific and actionable
- Include commands and examples
- Explain the "why" not just the "what"

## Troubleshooting

### Skill Not Showing Up

**Check the filename:**
- Must be exactly `SKILL.md` (all caps)
- Located in a directory matching the skill name

**Verify frontmatter:**
```bash
# Check for required fields
grep -A 2 "^---" .opencode/skills/my-skill/SKILL.md
```

Must include `name` and `description`.

**Check skill name:**
```bash
# Validate name format
echo "my-skill" | grep -E '^[a-z0-9]+(-[a-z0-9]+)*$'
```

**Verify directory structure:**
```
✓ .opencode/skills/my-skill/SKILL.md
✗ .opencode/skills/SKILL.md (missing skill directory)
✗ .opencode/skills/my-skill/skill.md (wrong case)
```

### Permission Denied

**Check global permissions:**
```json
{
  "permission": {
    "skill": {
      "my-skill": "deny"  // ← Blocking access
    }
  }
}
```

**Check agent-specific permissions:**
```json
{
  "agents": {
    "coder": {
      "tools": {
        "skill": false  // ← Tool disabled for this agent
      }
    }
  }
}
```

**Check logs:**
```bash
# Enable debug mode to see permission evaluation
opencode --debug
```

### Duplicate Skill Names

If you have multiple skills with the same name, OpenCode uses the first one found:

**Priority order:**
1. Project skills (closest to working directory)
2. Project skills (walking up to git root)
3. Global skills
4. Custom path skills

**Check for duplicates:**
```bash
# Search for duplicate skill names
find . -name "SKILL.md" -exec grep -H "^name:" {} \; | sort
```

### Validation Errors

**Name too long:**
- Maximum 64 characters
- Shorten the name or use abbreviations

**Description too long:**
- Maximum 1024 characters
- Keep it concise and focused

**Invalid YAML:**
```bash
# Validate YAML syntax
python3 -c "import yaml; yaml.safe_load(open('.opencode/skills/my-skill/SKILL.md').read().split('---')[1])"
```

## Advanced Usage

### Skill Metadata

Use metadata for additional context:

```yaml
---
name: security-review
description: Review code for security vulnerabilities
metadata:
  category: security
  severity: high
  tools: semgrep,trivy
  audience: security-team
---
```

Metadata is stored but not currently used by the system. It's useful for documentation and future features.

### Organizing Skills

**By category:**
```
.opencode/skills/
├── git-release/
├── git-hotfix/
├── docker-build/
├── docker-deploy/
├── pr-review/
└── security-scan/
```

**By team:**
```
~/company-skills/
├── backend-deploy/
├── frontend-build/
├── database-migration/
└── incident-response/
```

### Skill Templates

Create a template for your team:

```bash
# Create skill template script
cat > create-skill.sh << 'EOF'
#!/bin/bash
SKILL_NAME=$1
SKILL_DESC=$2
SKILL_TAGS=$3

mkdir -p .opencode/skills/$SKILL_NAME
cat > .opencode/skills/$SKILL_NAME/SKILL.md << SKILL
---
name: $SKILL_NAME
description: $SKILL_DESC
license: MIT
metadata:
  tags: $SKILL_TAGS
---

## What I do

- TODO: Add capabilities

## When to use me

TODO: Add use case guidance

## Steps

1. TODO: Add steps
SKILL

echo "Created skill: $SKILL_NAME"
EOF

chmod +x create-skill.sh
./create-skill.sh my-skill "My skill description" "#tag1,#tag2"
```

## Examples

### Example 1: Git Release Workflow

```markdown
---
name: git-release
description: Create consistent releases and changelogs
---

## What I do

- Analyze commits since last release
- Generate changelog following Keep a Changelog format
- Suggest semantic version bump
- Create GitHub release with notes

## Steps

1. Find last release tag: `git describe --tags --abbrev=0`
2. Get commits since last tag: `git log <last-tag>..HEAD --oneline`
3. Categorize commits:
   - feat: → Added
   - fix: → Fixed
   - BREAKING: → Breaking Changes
4. Suggest version bump (major/minor/patch)
5. Generate changelog entry
6. Provide `gh release create` command
```

### Example 2: PR Review Checklist

```markdown
---
name: pr-review
description: Review pull requests with consistent criteria
---

## What I do

- Check code quality and style
- Verify tests are included
- Review security implications
- Ensure documentation is updated

## Review Checklist

### Code Quality
- [ ] Code follows project conventions
- [ ] No unnecessary complexity
- [ ] Error handling is appropriate
- [ ] No hardcoded secrets or credentials

### Testing
- [ ] Unit tests included for new functionality
- [ ] Tests cover edge cases
- [ ] All tests pass

### Documentation
- [ ] README updated if needed
- [ ] Code comments for complex logic
- [ ] API documentation updated

### Security
- [ ] No SQL injection vulnerabilities
- [ ] Input validation present
- [ ] Authentication/authorization checked
- [ ] Dependencies are up to date

## Questions to Ask

1. What problem does this PR solve?
2. Are there any breaking changes?
3. How was this tested?
4. Are there any deployment considerations?
```

### Example 3: Docker Build Optimization

```markdown
---
name: docker-optimize
description: Optimize Docker images for size and build speed
---

## What I do

- Analyze Dockerfile for optimization opportunities
- Suggest multi-stage builds
- Recommend layer caching strategies
- Identify unnecessary dependencies

## Optimization Checklist

### Build Speed
- Use specific base image tags (not `latest`)
- Order layers from least to most frequently changing
- Leverage build cache with `.dockerignore`
- Use multi-stage builds to separate build and runtime

### Image Size
- Use minimal base images (`alpine`, `distroless`)
- Remove build dependencies in same layer
- Combine RUN commands to reduce layers
- Clean up package manager cache

### Security
- Don't run as root
- Use specific versions for dependencies
- Scan for vulnerabilities with `trivy`
- Keep base images updated

## Example Multi-Stage Build

```dockerfile
# Build stage
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o app

# Runtime stage
FROM alpine:3.19
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/app /app
USER nobody
ENTRYPOINT ["/app"]
```
```

## Environment Variables

| Variable                          | Purpose                                    |
|-----------------------------------|--------------------------------------------|
| `OPENCODE_DISABLE_CLAUDE_SKILLS`  | Set to `true` to disable `.claude/skills/` discovery |

## FAQ

### Can skills call other skills?

Not currently. Each skill is loaded independently. However, you can reference other skills in your content:

```markdown
## Related Skills

For deployment after release, see the `deploy-production` skill.
```

### Can skills have parameters?

Not in the current version. Skills receive no parameters and provide static instructions. This is planned for a future release.

### How do I share skills with my team?

**Option 1: Git repository**
```bash
# Add skills to your project repo
git add .opencode/skills/
git commit -m "Add team skills"
```

**Option 2: Shared directory**
```json
{
  "skills": {
    "paths": [
      "/shared/team-skills"
    ]
  }
}
```

**Option 3: Dotfiles**
```bash
# Include in your dotfiles repo
~/.config/opencode/skills/
```

### Can I use skills from Claude Desktop?

Yes! OpenCode automatically discovers skills from `.claude/skills/` directories. This provides compatibility with Claude Desktop's skill format.

To disable Claude skill discovery:
```bash
export OPENCODE_DISABLE_CLAUDE_SKILLS=true
```

### How do I debug skill discovery?

Enable debug mode to see skill discovery logs:

```bash
opencode --debug
```

Look for log messages like:
```
Discovered skills count=3
Duplicate skill name found name=my-skill existing=/path/1 duplicate=/path/2
Failed to parse skill file path=/path/to/SKILL.md error=...
```

### What's the difference between skills and context files?

| Feature          | Skills                          | Context Files (AGENTS.md)        |
|------------------|---------------------------------|------------------------------------|
| **When loaded**  | On-demand by agent              | Always included in context         |
| **Purpose**      | Specialized workflows           | General project information        |
| **Discovery**    | Multiple locations, patterns    | Fixed filenames                    |
| **Permissions**  | Fine-grained control            | No permission system               |
| **Format**       | YAML frontmatter + markdown     | Plain markdown                     |

**Use skills for:** Specific workflows, procedures, checklists
**Use context files for:** Project overview, coding standards, build commands

## Skill Ideas

### Development Workflows
- `git-hotfix`: Emergency hotfix procedure
- `git-rebase`: Interactive rebase guidance
- `code-review`: Code review checklist
- `refactor-guide`: Refactoring best practices

### DevOps & Deployment
- `docker-build`: Docker optimization
- `k8s-deploy`: Kubernetes deployment
- `ci-setup`: CI/CD configuration
- `rollback-procedure`: Production rollback steps

### Testing & Quality
- `test-strategy`: Testing approach guidance
- `performance-test`: Performance testing checklist
- `security-scan`: Security review process
- `accessibility-check`: Accessibility testing

### Documentation
- `api-docs`: API documentation standards
- `readme-template`: README structure
- `changelog-format`: Changelog formatting
- `architecture-doc`: Architecture documentation guide

## See Also

- [Configuration](../README.md#configuration)
- [Custom Commands](custom-commands.md)
- [Session Providers](session-providers.md)
- [MCP Servers](../README.md#mcp-servers)
