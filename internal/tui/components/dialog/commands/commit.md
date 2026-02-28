commit and push using conventional commits specification

# Commit format
The commit message should be structured as follows:
```
<type>[optional scope]: <description>

[optional body]

[optional footer(s)]
```

The commit contains the following structural elements, to communicate intent to the consumers of your library:
- fix: a commit of the type fix patches a bug in your codebase (this correlates with PATCH in Semantic Versioning).
- feat: a commit of the type feat introduces a new feature to the codebase (this correlates with MINOR in Semantic Versioning).
- BREAKING CHANGE: a commit that has a footer BREAKING CHANGE:, or appends a ! after the type/scope, introduces a breaking API change (correlating with MAJOR in Semantic Versioning). A BREAKING CHANGE can be part of commits of any type.
- types other than fix: and feat: are allowed, for example @commitlint/config-conventional (based on the Angular convention) recommends build:, chore:, ci:, docs:, style:, refactor:, perf:, test:, and others.
- footers other than BREAKING CHANGE: <description> may be provided and follow a convention similar to git trailer format.
- A scope may be provided to a commit’s type, to provide additional contextual information and is contained within parenthesis, e.g., feat(parser): add ability to parse arrays.

Examples
Commit message with description and breaking change footer:
```
feat(api): allow provided config object to extend other configs

BREAKING CHANGE: `extends` key in config file is now used for extending other config files
```

Commit message with no body:
```
docs: correct spelling of CHANGELOG
```

Commit message with multi-paragraph body and multiple footers:
```
fix: prevent racing of requests

Introduce a request id and a reference to latest request. Dismiss
incoming responses other than from latest request.

Remove timeouts which were used to mitigate the racing issue but are
obsolete now.

Reviewed-by: Z
Refs: #123
```

# Recommendations
- prefer to explain WHY something was done from an end user perspective instead of WHAT was done
- do not do generic messages like "improved user experience" be very specific about what user facing changes were made
- if there are conflicts DO NOT FIX THEM. notify me and I will fix them
- if changes related only to 
   - markdown (.md .mdx) files, then always use `docs` commit type
   - .github workflows or .gitlab-ci or any other known ci tools, then use `ci` commit type
   - test files, then use `test` commit tyoe

## GIT DIFF

!`git diff`

## GIT DIFF --cached

!`git diff --cached`

## GIT STATUS --short

!`git status --short`
