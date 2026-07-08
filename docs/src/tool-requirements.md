# Tool Requirements

Declare recipe-local tool requirements when a command should fail before any
recipe phase runs.

```toml
[recipes.benchmark]
cmd = "go test -bench ."

[recipes.benchmark.requires]
commands = ["docker", "openssl", "go"]
optional_commands = ["h2load"]
go_commands = { stringer = "golang.org/x/tools/cmd/stringer@latest" }
node_commands = { eslint = "eslint@^9", playwright = "@playwright/test@latest" }
```

Requirements are checked before sandbox setup and before `pre`. They are static
declarations and are not placeholder-expanded. An included or overriding recipe
that specifies `requires` replaces the previous `requires` block as a whole.

## Required Commands

`commands` lists required executable names, not paths. Missing entries fail the
recipe in one grouped error.

```text
recipe "benchmark" missing required tools: docker, openssl
```

## Optional Commands

`optional_commands` lists executable names that should warn but not fail.

```text
shadowtree: recipe "benchmark" optional tools not found: h2load
```

## Go Commands

`go_commands` maps executable names to Go package install strings. Shadowtree
checks only for the executable on `PATH`; it does not run `go install`.

Missing tools include guidance such as:

```text
stringer (go install golang.org/x/tools/cmd/stringer@latest)
```

## Node Commands

`node_commands` maps executable names to Node package install strings.
Shadowtree checks only for the executable on `PATH`; it does not install
packages.

Missing tools use the detected package manager to suggest installing the CLI,
for example:

```text
npm install -g eslint@^9
pnpm add --global eslint@^9
yarn global add eslint@^9
bun add --global eslint@^9
```

## Validation Rules

Requirement command names must be non-empty executable basenames without path
separators or surrounding whitespace. Required lists cannot duplicate names.
Duplicate executable names across `commands`, `go_commands`, and
`node_commands` are rejected. Overlap between required and optional names is
rejected.
