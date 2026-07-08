# Shadowtree

Shadowtree is a project-local development task runner for repeatable checks,
builds, generation, cleanup, and install workflows. It gives teams one small
recipe interface for commands that should be inspectable, composable,
editor-completable, and sandboxed unless they intentionally edit the checkout.

On Linux, Shadowtree uses overlayfs in a user and mount namespace by default.
When namespace overlayfs is unavailable, it warns and falls back to a copied
workspace with the same isolation contract.

## Install

```sh
go install github.com/yusing/shadowtree/cmd/shadowtree@latest
```

Bash completion:

```sh
command -v shadowtree >/dev/null 2>&1 && eval "$(shadowtree completion bash)"
```

The `install` recipe appends the same guarded eval line to `~/.bashrc`.

Fish completion:

```sh
shadowtree completion fish > ~/.config/fish/completions/shadowtree.fish
```

Zsh completion:

```sh
command -v shadowtree >/dev/null 2>&1 && eval "$(shadowtree completion zsh)"
```

Completion is dynamic: it uses configured recipes plus recipes from the selected
profile. Without a config file, Shadowtree detects the nearest Go or Node
project marker upward from the current directory and exposes matching built-ins.

## Quick Start

Create a default TOML config in a project:

```sh
shadowtree init
```

In a project with Shadowtree config:

```sh
shadowtree config
shadowtree recipes
shadowtree help test
shadowtree help test color=false
shadowtree test
shadowtree build
shadowtree lint
```

Run one-off commands without adding a recipe:

```sh
shadowtree exec -- go test ./...
shadowtree exec -- npm test
```

Inspect the resolved plan before running a recipe:

```sh
shadowtree --print test
shadowtree --print --expanded test
shadowtree --check --shell test
shadowtree --verbose build
```

`--verbose` prints compact stage boundaries such as `== cmd: @build ==` before
commands run. Multiline scripts are shown as `<script>`, so verbose headers do
not dump script bodies.

Built-in Go workflow recipes run once per discovered Go module:

```sh
shadowtree --print test
```

prints the module fan-out:

```text
for_each: @go-modules
workdir: {item}
main: go test ./... {@}
```

## Sandboxed By Default

Sandboxed recipe writes stay inside the temporary workspace. The host checkout
is unchanged unless sync-out is requested.

Recipes that intentionally edit the checkout can opt out:

```toml
[recipes.tidy]
sandboxed = false
for_each = "@go-modules"
workdir = "{item}"
cmd = "go mod tidy"
post = ["if test -f go.work; then go work sync; fi"]
```

Use sync-out when a sandboxed recipe should copy selected results back:

```sh
shadowtree --sync-out internal/generated generate
shadowtree --sync-out dist --sync-out schema.json build-assets
```

Recipe-level sync-out:

```toml
[recipes.generate]
cmd = "go generate ./..."
sync_out = ["internal/generated"]
```

A selected path missing from the sandbox is mirrored as a deletion on the host.
Prefer narrow `--sync-out PATH` or recipe `sync_out` over `--sync-out-all`.

## Configure Recipes

Shadowtree discovers config upward from the current directory:

```text
.shadowtree.toml
```

Discovery stops at the git root when the current directory is inside a Git
repository.

Shadowtree config is TOML:

```toml
include = ["./common.shadowtree.toml"]
profile = "go"
shell = "sh"

shell_prelude = '''
require_tool() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "$1 is required" >&2
    exit 1
  }
}
'''

[vars]
go_ldflags = "-buildvcs=false"

[recipes.test]
help = "Run tests after regenerating code."
pre = ["@generate"]
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
position = 1
required = true
values = "@go-packages"

[recipes.build]
help = "Build a Go package."
cmd = 'go build {go_ldflags:raw} "{project}" {@}'

[recipes.build.requires]
commands = ["go"]
go_commands = { stringer = "golang.org/x/tools/cmd/stringer@latest" }

[recipes.build.arguments.project]
help = "Go main package to build."
type = "string"
position = 1
default = "./cmd/shadowtree"
values = "@go-main-packages"

[recipes.tidy]
help = "Tidy Go module files."
sandboxed = false
for_each = "@go-modules"
workdir = "{item}"
cmd = "go mod tidy"
post = ["if test -f go.work; then go work sync; fi"]
```

Use `include` to share config across projects or subprojects. Include entries
are TOML file paths resolved relative to the file that contains them. Included
files are merged first, then the current config overrides them:

```toml
include = ["./tools.shadowtree.toml", "./ci.shadowtree.toml"]
```

Includes are mixins, not isolated imports. Included recipes appear in
`shadowtree help`, `shadowtree recipes`, shell completion, and editor
completion as if they were defined in the current config. Top-level `vars`,
`var_commands`, `env`, `profile`, `shell`, and `shell_prelude` also merge into
the effective config. Included preludes run before the current file's prelude.

Use shell strings for process execution:

```toml
[recipes.test]
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
required = true
values = "@go-packages"
```

Command strings run through the configured shell after placeholder expansion.
A string that is exactly `@recipe` or `@path:recipe` invokes another recipe;
other strings run in the shell. Put defaults directly in `cmd` through typed
`arguments`.
Default `{name}` expansion is raw when unquoted. Inside single or double
quotes, `{name}` is escaped for that quote context, so `"{name}"`, `'{name}'`,
and `"https://{host}"` stay one shell word even when values contain quote
characters. Escaping is context-aware, not type-aware.
Normally, put free string or path placeholders in shell quotes, such as
`foo "{bar}"`. Use `{name:shell}` only when the value must be embedded in an
unquoted shell word, such as `foo -xxx{name:shell}`. Use `{name:dq}` only
inside double quotes for content that must be combined with literal quoted
text. Use `{name:raw}` as the explicit unsafe escape hatch when raw shell text
or word splitting is intended.
`{@}` is special: in `cmd`, it splices leftover recipe CLI args as separate
shell-quoted words and must be a whole shell word.
Environment values under `[env]` and `[recipes.<name>.env]`, along with
`vars`, `workdir`, `sync_out`, and `log`, use raw string placeholder expansion;
only `{name}` and `{name:raw}` are valid there. `shell_prelude` uses shell
placeholder expansion before it is prepended to script commands. Shadowtree
also provides a built-in `{run_id}` placeholder: one lowercase hex ID is
generated for each top-level run and stays the same through `pre`, `cmd`,
`post`, `for_each`, and nested recipe references. `run_id` is reserved and
cannot be declared in `vars`, `var_commands`, recipe `vars`, or recipe
`arguments`.
`cmd` commands can use `{status:pre}`, and `post` commands can use
`{status:pre}` and `{status:cmd}` to inspect prior stage status. They expand to
`0` on success, the failing exit code when available, `1` for non-exit failures
such as timeouts, and an empty string when that stage did not run.

Use a recipe reference from `cmd`, `pre`, or `post`:

```toml
[recipes.gen-swagger]
help = "Regenerate Swagger files."
cmd = "go generate ./internal/api"

[recipes.test]
help = "Regenerate API files, then test."
pre = ["@gen-swagger"]
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
required = true
values = "@go-packages"
```

Referenced recipes run in the current workspace. They do not start a nested
sandbox or run their own sync-out.

In command lists, only strings that are exactly `@recipe` are direct recipe
references:

```toml
pre = ["echo 123", "@gen-swagger"]
```

Use bracket-style arguments to pass named or positional arguments:

```toml
pre = ["@build[component=godoxy, mode=dev]"]
```

Use a structured `pre` or `post` table when a stage command needs execution
controls:

```toml
[recipes.benchmark.pre]
cmd = "benchmark_prepare"
timeout = "120s"
```

`timeout` is a Go duration such as `30s`, `2m`, or `1m30s`. If it expires,
Shadowtree stops that stage command, skips later main work when the failure is
in `pre`, and still runs `post` commands.

Use `@retry` in a shell command position to retry flaky setup or readiness
checks:

```toml
pre = "@retry[count=30,delay=1s] benchmark_prepare"
```

`count` is the maximum number of attempts and `delay` is the wait between
failed attempts. Omitted values default to `count=3` and `delay=1s`.

Use shell script strings when a workflow needs shared shell logic, conditionals,
pipes, or multiple statements.

Use `for_each` to fan out one recipe command across values from the same value
providers used by argument `values`. `pre` runs once before all items, `cmd`
runs once per item, `post` runs once after the loop, and the first failing item
stops later items:

```toml
[recipes.lint]
help = "Run golangci-lint in every Go module."
for_each = "@go-modules"
workdir = "{item}"
cmd = "golangci-lint run ./..."
```

`workdir` is optional for any recipe and makes the main command run from a
relative workspace path. With `for_each`, `workdir` is expanded per item:
`{item}` is the candidate value, `{item_help}` is its help text when present,
and `{item_index}` is the zero-based index.

Use recipe logging when a run should keep a copy of selected stage output:

```toml
[recipes.test]
cmd = "go test ./..."
log = "logs/test-{run_id}.log"
log_stages = ["pre", "cmd", "post"]
log_tee = true
```

`log` is expanded with normal recipe placeholders plus `{run_id}`. Log paths
must be relative and stay under the active config file directory, or the source
checkout when no config path exists. If `log_stages` is omitted, Shadowtree logs
`pre`, `cmd`, and `post`; `cmd` includes every `for_each` item but not the
value-provider command used to collect items. `log_tee` defaults to `true`,
preserving terminal output while writing to the log. Set `log_tee = false` to
send selected stages only to the log. Each selected command is preceded by a
compact boundary such as `== pre[0]: <script> ==`, `== cmd: @build ==`, or
`== post[0]: <script> ==`; long one-line commands are truncated, and multiline
script bodies are not written into boundaries. A selected parent stage also
captures output from nested `@recipe` calls; nested recipe `log` settings do not
open a second log during that reference.

Declare recipe-local tool requirements when a command should fail before any
recipe phase runs:

```toml
[recipes.benchmark]
cmd = "go test -bench ."

[recipes.benchmark.requires]
commands = ["docker", "openssl", "go"]
optional_commands = ["h2load"]
go_commands = { stringer = "golang.org/x/tools/cmd/stringer@latest" }
node_commands = { eslint = "eslint@^9", playwright = "@playwright/test@latest" }
```

`commands` are required executable names, not paths. Missing required commands
fail before sandbox setup and before `pre` commands. `optional_commands` print
one warning to stderr and continue. `go_commands` and `node_commands` are required
executable names with package guidance; Shadowtree checks only for the
executable on `PATH` and does not install packages. Missing Go tools report
`go install <module>@<version>` guidance. Missing Node tools use the detected
package manager to suggest installing the CLI, such as `npm install -g
eslint@^9`, `pnpm add --global eslint@^9`, `yarn global add eslint@^9`, or
`bun add --global eslint@^9`.

## Typed Arguments

Recipes can define typed arguments. Arguments can be passed positionally, by
name, or with bracket-style syntax:

```sh
shadowtree build ./cmd/shadowtree
shadowtree build project=./cmd/shadowtree
shadowtree 'build[project=./cmd/shadowtree]'
```

Supported argument types are `string`, `int`, `float`, `bool`, `path`,
`rel_path`, `duration`, and `duration:seconds`. `path` accepts absolute and
relative paths and provides fish-style path completion for relative paths,
absolute paths, and `~/`. `rel_path` accepts relative paths only and completes
relative checkout paths. `duration` accepts Go duration strings such as `10s`,
`1500ms`, and `1m30s`. `duration:seconds` accepts Go duration strings that are
exact whole seconds and expands them as base-10 integer seconds. Path arguments
can set `path_kind` to `any`, `file`, `dir`, or `executable` to filter
completion candidates. `int`, `float`, `duration`, and `duration:seconds`
arguments can set inclusive `min` and `max` bounds; duration bounds use Go
duration strings such as `100ms` or `2m`.

Use `{@}` in `cmd` to splice leftover recipe CLI args:

```toml
[recipes.test]
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
position = 1
required = true
values = "@go-packages"
```

For typed recipes, positional values and known `key=value` argument values are
consumed by typed arguments and excluded from `{@}`. Unknown identifier
`key=value` tokens remain errors; command flags such as `-run=TestName` pass
through. Use `--` after typed recipe arguments to pass the following argv
literally to `{@}`, including option values that contain `=`:

```sh
shadowtree test pkg=./internal/recipe -- --flag NAME=value
```

Recipe-local presets can set several argument defaults at once. Select them
with `preset=<name>` after the recipe name; explicit CLI arguments still win:

```toml
[recipes.benchmark]
cmd = "run-benchmark --connections {connections} --requests {requests} --runs {runs}"

[recipes.benchmark.arguments.connections]
type = "int"
default = 32

[recipes.benchmark.arguments.requests]
type = "int"
default = 1000

[recipes.benchmark.arguments.runs]
type = "int"
default = 1

[recipes.benchmark.presets.stable.arguments]
connections = 64
requests = 20000
runs = 5
```

```sh
shadowtree benchmark preset=stable runs=3
```

## Built-In Profiles

Supported profiles are `go` and `node`. A profile is selected by explicit
`--profile`, then config `profile`, then marker detection only when no config
file is loaded. Detection walks upward from the current directory:
`package.json` selects `node`, and `go.mod` or `go.work` selects `go`. The
nearest marker wins; if Go and Node markers are in the same directory, Go wins.

Configs that omit `profile` do not receive detected built-ins. This keeps local
config recipes exact unless the config opts into a profile.

Use `shadowtree recipes`, `shadowtree --print <recipe>`, or
`shadowtree --print --expanded <recipe>` to inspect exact built-in behavior for
the current checkout. Use `shadowtree --check <recipe>` to validate references
and resolved command forms without running commands; add `--shell` to parse
expanded shell scripts. See `docs/spec.md` for the full Go and Node built-in
recipe lists, inference rules, and override semantics.

## Editor Support

Shadowtree includes a shared JSON Schema for Shadowtree TOML config files plus
editor integration files for Zed and VS Code under `editors/`.

The Zed extension provides a dedicated `Shadowtree TOML` language, syntax
highlighting, Shadowtree-specific highlighting, shell semantic highlighting for
script-valued fields, and LSP completion, diagnostics, and semantic tokens.

The VS Code extension binds the shared schema to Shadowtree TOML files through
Even Better TOML. Completion, hover, and validation come from that extension.

Install the Zed language server with:

```sh
go install github.com/yusing/shadowtree/cmd/shadowtree-lsp@latest
```

See `docs/spec.md` and the editor integration READMEs for implementation
details.

## VS Code Config

```json
"files.associations": {
  ".shadowtree.toml": "toml",
  "**/.shadowtree.toml": "toml",
  "*.shadowtree.toml": "toml",
  "**/*.shadowtree.toml": "toml",
  ".shadowtree/*.toml": "toml",
  "**/.shadowtree/*.toml": "toml"
},
"evenBetterToml.schema.associations": {
  "^file://.*/[^/]*\\.shadowtree\\.toml$": "https://raw.githubusercontent.com/yusing/shadowtree/main/schemas/shadowtree.schema.json",
  "^file://.*/\\.shadowtree/.*\\.toml$": "https://raw.githubusercontent.com/yusing/shadowtree/main/schemas/shadowtree.schema.json"
}
```

### Zed Config

```json
 "file_types": {
    "Shadowtree TOML": [
      ".shadowtree.toml",
      "**/.shadowtree.toml",
      "*.shadowtree.toml",
      "**/*.shadowtree.toml",
      ".shadowtree/*.toml",
      "**/.shadowtree/*.toml"
    ]
  },
  "languages": {
    "TOML": {
      "language_servers": ["shadowtree-lsp", "..."]
    }
  }
```

## Development

This project uses Shadowtree for its own development tasks. Before installing a
`shadowtree` binary, run the local CLI with `go run`:

```sh
go run ./cmd/shadowtree recipes
go run ./cmd/shadowtree test
go run ./cmd/shadowtree check
go run ./cmd/shadowtree build
go run ./cmd/shadowtree install
```

After installing or building `shadowtree`, use the shorter form:

```sh
shadowtree test
shadowtree check
shadowtree build
shadowtree install
shadowtree fmt
shadowtree tidy
shadowtree install-skill
```

Recipes that intentionally change the host checkout set `sandboxed = false` in
`.shadowtree.toml`.

The `install` recipe uses default `go install`, honors `FISH_CONFIG_DIR` and
`FISH_COMPLETIONS_DIR`, generates completion from `shadowtree` on `PATH`,
installs fish completion when `fish` is available, and appends single guarded
eval lines to `~/.bashrc` and `~/.zshrc` when those shells are available.

The `install-skill` recipe installs every local agent skill from
`.agents/skills/` to `${AGENTS_SKILLS_DIR:-$HOME/.agents/skills}`.
