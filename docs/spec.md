# Shadowtree Spec

Shadowtree is a small development recipe runner that executes commands in a
disposable sandbox workspace for the current project. Its primary goal is to let
codegen, tests, builds, linting, and cleanup run without mutating the host
checkout by default.

This document describes the behavior currently implemented by the project.

## Goals

- Run common development tasks through a simple recipe interface.
- Keep command writes isolated from the host checkout unless explicitly synced.
- Avoid triggering editor/LSP reindexing for generated or temporary files.
- Provide useful defaults for Go projects.
- Keep configuration small and exact, using argv arrays for process execution
  and shell script strings for workflows that benefit from shared shell logic.
- Support dynamic shell completion from resolved recipes.
- Provide editor-facing schema and syntax support for TOML configuration.
- Let the project use Shadowtree for its own development tasks.

## Non-Goals

- Shadowtree is not a complete untrusted-code security sandbox.
- Shadowtree does not require reflinks.
- Shadowtree does not currently provide Docker, remote execution, matrix jobs,
  watch mode, or persistent named sessions.
- Shadowtree does not provide built-in language-aware argument completion.
  Projects can opt into dynamic argument completion with recipe `values`
  commands.
- Shadowtree's editor integrations do not replace runtime config validation.
  The CLI loader remains authoritative.

## Isolation Model

For each sandboxed run, Shadowtree creates a temporary workspace:

```text
/tmp/shadowtree-*/workspace
```

On Linux, Shadowtree uses overlayfs inside a user and mount namespace by
default. Commands run at the source checkout path inside that namespace, so
tools such as `go test` see a stable working directory while writes land in the
overlay upperdir instead of the host checkout. Shadowtree hides metadata entries
from the lower tree. When namespace overlayfs is unavailable, Shadowtree warns
and falls back to copying the current source directory into the temporary
workspace and running commands there. On filesystems that support it, fallback
copy may use reflinks as an optimization.

By default:

- Files written by commands stay in the sandbox workspace.
- The temporary workspace is removed after the run.
- The host checkout is not changed.

Exceptions for sandboxed runs:

- `--sync-out PATH` mirrors selected paths back after a successful recipe.
- Recipe-level `sync_out` mirrors selected paths back after a successful recipe.
- `--sync-out-all` copies the whole workspace back after a successful recipe.

Unsandboxed recipes set `sandboxed = false` and run directly in the host
checkout. `--sync-out`, `sync_out`, and `--sync-out-all` only apply to sandboxed
execution.

Shadowtree intentionally skips `.git`, `.shadowtree`, and `.shadowtree.*` while
preparing sandboxed workspaces. Because `.git` is skipped, Go build recipes that
require VCS stamping should use `-buildvcs=false`.

## CLI

```sh
shadowtree [flags] <recipe> [args...]
shadowtree [flags] exec -- <cmd> [args...]
shadowtree help [recipe [color=false]]
shadowtree recipes
shadowtree config
shadowtree init [path]
shadowtree completion bash|fish|zsh
shadowtree __complete fish <words...>
shadowtree __complete bash <cursor> <line> [current]
shadowtree __complete zsh <words...>
```

`__complete` is internal and used by generated shell completion.

## Global Flags

```text
--config PATH       use an explicit config file
--profile PROFILE   use a profile, initially only go is supported
--sync-out PATH     copy path back after success; repeatable or comma-separated
--sync-out-all      copy the entire workspace back after success
--print             print the resolved plan without running
--verbose           show workspace and command details
--help              show basic CLI help
--version           print the version
```

Global flags are parsed before the command or recipe name. Arguments after the
recipe name are passed to the recipe's main command.

## Config Discovery

Shadowtree discovers config upward from the current directory until the git root
or filesystem root.

Discovery order:

```text
.shadowtree.toml
```

An explicit `--config PATH` bypasses discovery.

`shadowtree init` writes `.shadowtree.toml` by default. A custom path can be
provided:

```sh
shadowtree init ./ci/shadowtree.toml
```

## Config Schema

Top-level fields:

```toml
profile = "go"
shell = "sh"
shell_prelude = '''
shared_function() {
	echo ok
}
'''
sync_out = ["path/from/project/root"]

[env]
KEY = "value"

[vars]
NAME = "static value"

[var_commands]
DYNAMIC_NAME = ["cmd", "arg"]

[recipes.<name>]
help = "Short recipe help text."
sandboxed = true
pre = [["cmd", "arg"]]
cmd = ["cmd", "arg"]
args = ["fixed", "args"]
default_args = ["args", "with", "{placeholders}"]
post = [["cmd", "arg"]]
sync_out = ["path/from/project/root"]

[recipes.<name>.vars]
NAME = "recipe override"

[recipes.<name>.env]
KEY = "value"

[recipes.<name>.arguments.<arg-name>]
help = "Short argument help text."
type = "string"
position = 1
required = false
default = "value"
values = ["cmd", "arg"]
```

## Recipe Fields

Commands can be argv arrays, recipe references, or shell script strings. Script
strings are executed with the configured shell:

```toml
shell = "bash"

[recipes.example]
cmd = '''
set -euo pipefail
echo "hello"
'''
```

If `shell` is not set, Shadowtree uses `sh`.

An argv array whose first item starts with `@` invokes another Shadowtree recipe
directly:

```toml
[recipes.gen-swagger]
cmd = ["go", "generate", "./internal/api"]

[recipes.test]
pre = [["@gen-swagger"]]
cmd = ["go", "test"]
default_args = ["./..."]

[recipes.build.arguments.project]
values = ["@list-build-targets"]
```

The text after `@` is the referenced recipe name. Remaining argv items are
passed to that recipe as CLI arguments:

```toml
pre = [["@build-api", "service=public"]]
```

For `sh` and `bash` script commands, including `cmd`, `pre`, `post`, argument
`values`, and `shell_prelude`, a literal `@recipe` command word also invokes
the referenced recipe directly. This works anywhere a normal shell command can
run, including conditionals:

```toml
[recipes.test]
cmd = '''
if [ -f schema.json ]; then
	@generate mode=dev
fi
'''
```

Only literal command-position references dispatch recipes. Assignments,
expanded variables, quoted text, and ordinary command arguments do not:

```sh
FOO="@generate"   # assignment only
$FOO              # normal shell command lookup, not a recipe dispatch
echo @generate    # normal argument text
```

Use `@path:recipe` to invoke a recipe from another Shadowtree config. The path
is resolved relative to the directory containing the referencing
`.shadowtree.toml`, and the target config is loaded from
`path/.shadowtree.toml`. The referenced recipe runs from that target directory:

```toml
[recipes.gen-schema]
cmd = ["@webui:gen-schema"]
```

In command-list fields such as `pre` and `post`, a string command that is
exactly `@recipe` is also a recipe reference. Other string commands are shell
scripts:

```toml
pre = ["echo 123", "@gen-swagger"]
```

Because non-reference strings in `pre` and `post` are shell scripts, a literal
command-position `@recipe` inside those strings also dispatches a recipe:

```toml
post = ["if [ -f schema.json ]; then @publish-schema; fi"]
```

String and argv recipe references can use bracket-style arguments. Comma
separators split the argument list, and surrounding whitespace is ignored:

```toml
pre = ["@build[component=godoxy, mode=dev]"]
cmd = ["@test[package=./internal/recipe]"]
post = ["@webui:gen-schema[mode=dev]"]
```

Placeholders can be used in recipe references. Static references such as
`@gen-swagger` and `@webui:gen-schema` can be validated by the editor; dynamic
references such as `@{target}` and `@{target_path}:{target_recipe}` are
resolved at run time after placeholder expansion.

Top-level `shell_prelude` is prepended to every script command and every
`["sh", "-c", "..."]` command. It is intended for shared shell functions and
variables:

```toml
shell_prelude = '''
require_tool() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "$1 is required" >&2
		exit 1
	}
}
'''
```

Top-level `vars` are static placeholder values shared by every recipe.
`var_commands` are evaluated from the source checkout when recipes are resolved
for execution, printing, or help; surrounding whitespace is trimmed from stdout
and the result becomes a shared placeholder value. Recipe-level `vars` override
top-level vars.

`help`
: Short human-facing help text. Used by `shadowtree help`, `shadowtree recipes`,
and shell completion.

`sandboxed`
: Whether to run the recipe in a temporary workspace. Defaults to `true`.
`false` runs the recipe directly in the source checkout and skips sync-out.

`cmd`
: Required argv prefix, `@recipe` reference, or shell script for the main
command.

`args`
: Fixed arguments always inserted after `cmd`.

`default_args`
: Arguments used only when the user does not provide recipe CLI args.

`pre`
: Commands run before the main command, in order.

`post`
: Commands run after the main command, in order.

`env`
: Recipe-specific environment overrides.

`sync_out`
: Paths mirrored back to the host checkout after a successful sandboxed recipe.
If a selected path is deleted in the sandbox, it is deleted from the host
checkout. Ignored when `sandboxed = false`.

## Recipe Arguments

Recipes can define typed arguments under:

```toml
[recipes.<name>.arguments.<arg-name>]
```

Argument fields:

`help`
: Short help text used by `shadowtree help <recipe>` and shell completion.

`type`
: Optional type. Supported values are `string`, `int`, `float`, `bool`, `path`,
and `rel_path`. The default is `string`. `path` accepts absolute and relative
paths. `rel_path` accepts relative paths only and rejects absolute paths and
`~` home paths.

`path_kind`
: Optional completion filter for `path` and `rel_path` arguments. Supported
values are `any`, `file`, `dir`, and `executable`. The default is `any`.
`file` and `executable` still include directories as traversal candidates.

`position`
: Optional 1-based positional index. Arguments with a position can be supplied
positionally.

`required`
: Whether the argument must be supplied by the user. Defaults to `false`.

`default`
: Optional default value. Defaults are type-checked.

`values`
: Optional command that produces completion candidates for this argument. Each
output line is a value, optionally followed by a tab and help text. The
command can be an `@recipe` reference, or an argument-values builtin:

```toml
values = '@enum api worker "admin ui"'
values = '@lines config/targets.txt'
values = '@glob "cmd/*"'
values = '@recipes'
values = '@vars'
```

`@enum` returns literal values from its arguments. `@lines` reads candidates
from a text file, using the same `value<TAB>help` line format. `@glob` returns
filesystem matches. `@recipes` returns resolved recipe names. `@vars` returns
recipe placeholder and argument names. Relative `@lines` paths and `@glob`
patterns resolve from the config file directory when available.

Example:

```toml
[recipes.build]
help = "Build a Go package."
cmd = ["go", "build"]
args = ["-o", "bin/{binary}"]
default_args = ["{project}"]
sync_out = ["bin/{binary}"]

[recipes.build.arguments.project]
help = "Go package to build."
type = "string"
position = 1
default = "./cmd/shadowtree"
values = '''
go list -f '{{.ImportPath}}{{"\t"}}{{.Doc}}' ./cmd/...
'''

[recipes.build.arguments.binary]
help = "Output binary name under bin/."
type = "string"
default = "shadowtree"
```

Arguments can be provided positionally:

```sh
shadowtree build ./cmd/shadowtree
```

Arguments can be provided by name:

```sh
shadowtree build project=./cmd/shadowtree binary=shadowtree-dev
```

Arguments can also be provided with bracket-style syntax:

```sh
shadowtree 'build[project=./cmd/shadowtree,binary=shadowtree-dev]'
```

Bracket-style syntax is preferred for shell completion, especially in fish.

Argument values are exposed to recipe commands through `{name}` placeholders.
Shared vars are exposed through the same placeholder syntax. Placeholders are
expanded in `cmd`, `args`, `default_args`, `pre`, `post`, and `sync_out`.
Shell parameter expansion such as `${HOME}` is not treated as a Shadowtree
placeholder.

## Recipe Resolution

Recipe resolution order:

```text
built-in recipes for the selected profile, or for a detected profile when no
config is loaded
then config recipe overrides
then CLI flags
then trailing recipe args
```

Config recipes with the same name as a built-in recipe override only specified
fields. Unspecified built-in fields remain intact.

Example:

```toml
[recipes.test]
help = "Run generated-code tests."
pre = [["go", "generate", "./..."]]
args = ["-count=1"]
```

The built-in `test` command and defaults remain:

```text
cmd = ["go", "test"]
default_args = ["./..."]
```

Resolved without CLI args:

```sh
go generate ./...
go test -count=1 ./...
```

Resolved with CLI args:

```sh
shadowtree test ./internal/recipe
```

runs:

```sh
go generate ./...
go test -count=1 ./internal/recipe
```

CLI args replace `default_args`; they do not append to them.

For recipes with typed `arguments`, CLI args are parsed as argument values
instead. In that case, `default_args` stays active and can contain placeholders
such as `{project}`.

## Execution Semantics

For a sandboxed recipe:

1. Create a temporary workspace with namespace overlayfs, or copy the source
   tree if namespace overlayfs is unavailable.
2. Run `pre` commands in order.
3. Run the resolved main command.
4. Run `post` commands in order.
5. If all phases succeeded, sync configured/requested paths back.
6. Remove the temporary workspace.

When a command is an `@recipe` or `@path:recipe` reference, Shadowtree runs the
referenced recipe's `pre`, main command, and `post` directly in the current
workspace. It does not start another Shadowtree process, create a nested
sandbox, or perform the referenced recipe's sync-out. Cross-config references
load `path/.shadowtree.toml` relative to the referencing config and run from the
target path inside the current host checkout or current sandbox. Sync-out is
performed only for the top-level invoked recipe after all phases succeed.
Recursive recipe references fail with a cycle error.

Failure behavior:

- If a `pre` command fails, the main command is skipped.
- `post` commands still run after a `pre` or main command failure.
- Sync-out does not run after failure.
- The process exits with the first failing command's exit code when available.

With namespace overlayfs, commands run from the source checkout path inside the
namespace. With copy fallback, commands run from the copied temporary workspace
root.

For an unsandboxed recipe, Shadowtree skips the temporary workspace and runs
`pre`, main, and `post` commands directly from the source checkout. Sync-out is
not performed because command writes already target the host checkout.

## Reserved Recipe Names

The following names are reserved and cannot be used as recipes:

```text
recipes
init
config
exec
completion
enum
glob
help
lines
vars
version
__complete
```

The argument-values builtins (`enum`, `glob`, `lines`, `recipes`, and `vars`)
are reserved as recipe names. Future built-in `@` command identifiers are also
reserved.

## Built-In Go Profile

The Go profile is selected when:

- `--profile go` is provided, or
- config has `profile = "go"`, or
- no config is loaded and Shadowtree detects `go.mod` or `go.work` upward from
  the current directory.

Only the `go` profile is supported currently.

Built-in Go recipes:

```text
build      go build ./...
check      @vet && @test ./...
fmt        gofmt -w .
generate   go generate ./...
lint       golangci-lint run ./... if available, otherwise go vet ./...
run        go run {command}
test       go test ./...
test-race  go test -race ./...
tidy       go mod tidy
vet        go vet ./...
```

Built-in `fmt` and `tidy` are unsandboxed by default, so `gofmt -w` and
`go mod tidy` update the host checkout directly. Other built-in Go recipes are
sandboxed unless project config overrides them. Built-in `run` has a required
positional `command` argument with `rel_path` type.

## Help

`shadowtree help` prints CLI usage, active config/profile, and resolved recipes
with their `help` text.

`shadowtree help <recipe>` prints a sectioned recipe page with ANSI color by
default. Pass `color=false` after the recipe name to disable color.

It prints these fields when present or applicable:

- recipe name
- recipe help text
- command section
- sandboxed section for unsandboxed recipes
- pre command section
- post command section
- argument section with `name - help`, `info:`, and configured `values:`
- sync-out section for sandboxed recipes

Multi-line command arguments are summarized as `<script>` in help and completion
output.

## Recipe Listing

`shadowtree recipes` prints resolved recipe names and help text. If a recipe has
no `help`, Shadowtree falls back to a compact command summary.

## Plan Printing

`--print` prints the resolved execution plan without running it:

```sh
shadowtree --print test ./internal/runner
```

The plan includes these fields when present or applicable:

- recipe name
- profile
- config path
- `sandboxed: false` for unsandboxed recipes
- pre commands
- main command
- post commands
- sync-out paths for sandboxed recipes

## Shell Completion

Shadowtree can generate bash, fish, and zsh completion:

```sh
command -v shadowtree >/dev/null 2>&1 && eval "$(shadowtree completion bash)"
```

The repository `install` recipe appends the same guarded eval line to
`~/.bashrc`.

```sh
shadowtree completion fish > ~/.config/fish/completions/shadowtree.fish
```

```sh
command -v shadowtree >/dev/null 2>&1 && eval "$(shadowtree completion zsh)"
```

The generated shell scripts call back into Shadowtree:

```sh
shadowtree __complete fish <words...>
shadowtree __complete bash <cursor> <line> [current]
shadowtree __complete zsh <words...>
```

Completion is dynamic and uses:

- configured recipes
- built-in recipes from the selected profile, or from a detected profile when no
  config is loaded
- recipe `help` text

Supported completion behavior:

- `shadowtree <TAB>` completes core commands and resolved recipes.
- `shadowtree te<TAB>` completes matching recipe names such as `test`.
- `shadowtree help <TAB>` completes recipe names.
- `shadowtree help test <TAB>` completes `color=false`.
- `shadowtree --profile <TAB>` completes `go`.
- `shadowtree build <TAB>` completes configured recipe arguments such as
  `project=`.
- `shadowtree build[<TAB>` completes bracket-style arguments such as
  `build[project=`.
- `shadowtree test race=<TAB>` completes `true` and `false` for bool
  arguments.
- `path` arguments complete relative paths, absolute paths, and `~/` paths.
  `rel_path` arguments complete relative paths only. `path_kind` filters path
  candidates to files, directories, or executable files.
- Arguments with `values` complete dynamic values produced by the configured
  command.

Completion parses config, checks profile markers, and runs only argument
`values` commands needed for the active argument.

## Editor Support

Shadowtree ships editor integration files, but the CLI does not depend on them.

Shared schema:

```text
schemas/shadowtree.schema.json
```

Zed support:

```text
editors/zed-shadowtree/
```

The Zed extension defines a `Shadowtree TOML` language backed by the pinned
`tree-sitter-toml` grammar. Its queries provide:

- base TOML highlighting
- Shadowtree-specific key, recipe, argument, and variable highlighting
- semantic shell highlighting for script-valued `cmd`, `pre`, `post`,
  `shell_prelude`, and `values` strings
- recipe-reference completion, diagnostics, and semantic tokens for literal
  command-position `@recipe` in `sh`/`bash` script strings, including
  `shell_prelude` and `pre`/`post`

Shell semantic highlighting supports `shell = "bash"`, `shell = "sh"`, and
`shell = "fish"` without hardcoding a single Zed shell language name.

Zed completion, diagnostics, and semantic tokens are provided by
`shadowtree-lsp`.
The Zed extension starts `shadowtree-lsp` from `PATH`; when developing inside
the Shadowtree checkout, it runs `go run ./cmd/shadowtree-lsp` so local LSP
changes take effect before an installed binary on `PATH`.

VS Code support:

```text
editors/vscode-shadowtree/
```

The VS Code companion manifest contributes a `tomlValidation` rule for
`.shadowtree.toml` and `shadowtree.toml`. Even Better TOML consumes that rule
and provides schema-backed validation, hover, and completion.

## Install Recipe Convention

This repository's own `.shadowtree.toml` includes an `install` recipe.

It:

- installs the binaries with default `go install`
- honors `FISH_CONFIG_DIR`
- honors `FISH_COMPLETIONS_DIR`
- generates completion from `shadowtree` on `PATH`
- installs fish completion when `fish` is available
- appends one guarded bash completion eval line to `~/.bashrc`
- appends one guarded zsh completion eval line to `~/.zshrc` when `zsh` is
  available

## Current Project Recipes

Shadowtree currently uses itself for development through `.shadowtree.toml`.

```text
build          Build the shadowtree binary into bin/shadowtree.
check          Run vet and tests.
fmt            Format Go source files.
generate       Run go generate.
install        Install the Shadowtree CLI and language server.
install-skill  Install the Shadowtree agent skill.
lint           Run Go lint checks.
run            Run a Go command.
test           Run the test suite.
test-race      Run Go tests with the race detector.
tidy           Tidy module dependencies.
vet            Run go vet.
```

Recipes that intentionally mutate the host checkout set `sandboxed = false`:

```text
fmt
tidy
```

## Known Limits

- Workspace isolation uses namespace overlayfs only when the host supports it.
- Large repositories may be slower when Shadowtree falls back to copying files.
- Commands can still intentionally read or write absolute host paths.
- Shell script strings are supported for recipes that need shell workflows;
  argv arrays remain preferred for direct process execution.
- Go is the only language profile currently implemented.
