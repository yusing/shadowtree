# Shadowtree Spec

Shadowtree is a small project-local development task runner for repeatable
checks, builds, generation, cleanup, install workflows, and other project
commands that benefit from one inspectable recipe interface. Sandboxed recipes
run in a disposable workspace so commands do not mutate the host checkout unless
the recipe or invocation explicitly asks for that.

The recipe `sandboxed` field accepts omitted/`true` for the existing disposable
workspace, `false` for direct host execution, and `"system"` for the explicit
system-container backend. No other string or type is accepted. System mode is
sandboxed for sync-out and lifecycle purposes and never falls back to either
other mode. Static help, completion, config loading, and print output do not
probe an engine; plans report `runtime: <not probed>`.

System execution and host-capability checks probe Docker, Podman, then nerdctl
in stable order through direct argument vectors. Presence alone is insufficient:
the selected client must reach its engine and expose the required image, build,
labelled-volume, nested/read-only mount, UID/GID, attached-start, signalling,
and forced-removal operations. Probes are bounded and state-free, report progress on
stderr, continue after an unusable installed candidate, and aggregate all
candidate failures when none is usable.

System images form five deterministic immutable stages above a pinned external
profile base: base metadata, exact tooling, normalized system packages, exact
recipe packages, and locked project dependencies. Tags and full ownership/key
labels are validated before reuse, and collisions fail without overwrite.
`system.base_image` is a literal non-`latest` override valid only in system
mode. Locked preparation uses manifest-only contexts and disables Node/Bun
lifecycle scripts; unlocked projects skip automatic dependency preparation.
One ephemeral read-only-root container runs the complete resolved lifecycle
against a private workspace mounted at the canonical checkout path. Nested
references reuse that lifecycle, cancellation preserves `post`, and successful
sync-out consumes the private workspace through the existing confinement rules.

This document describes the behavior currently implemented by the project.

## Goals

- Run common development tasks through a simple recipe interface.
- Keep command writes isolated from the host checkout unless explicitly synced.
- Avoid triggering editor/LSP reindexing for generated or temporary files.
- Provide useful defaults for Go, Node, and Rust projects.
- Keep configuration small and exact, using shell strings for commands and
  typed arguments plus placeholders for validated defaults and CLI forwarding.
- Support dynamic shell completion from resolved recipes.
- Provide editor-facing schema and syntax support for TOML configuration.
- Let the project use Shadowtree for its own development tasks.

## Non-Goals

- Shadowtree is not a complete untrusted-code security sandbox.
- Shadowtree does not require reflinks.
- Shadowtree does not currently provide Docker, remote execution, matrix jobs,
  watch mode, or persistent named sessions.
- Shadowtree does not try to cover every language-specific workflow. Supported
  profiles provide focused defaults, and projects can add more dynamic argument
  completion with recipe `values` commands.
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
--profile PROFILE   use a profile; supported profiles are go, node, and rust
--all               run the recipe's profile-defined aggregate plan
--sync-out PATH     copy path back after success; repeatable or comma-separated
--sync-out-all      copy the entire workspace back after success
--print             print the resolved plan without running
--expanded          with --print, include expanded scripts and resolved values
--check             validate the resolved recipe without running
--shell             with --check, parse expanded shell scripts
--verbose           show workspace and compact command boundaries
--help              show basic CLI help
--version           print the version
```

Global flags are parsed before the command or recipe name. Arguments after the
recipe name are passed to the recipe's main command.

`--all` is valid only for a recipe that declares aggregate support. Its target
domain and execution strategy are recipe-specific. It cannot be combined with
the recipe's explicit primary target. A post-recipe `--all` remains a
recipe/tool argument. Before the passthrough delimiter, a bare token is treated
as an explicit target and rejected. Tool flags with separate values therefore
use `--`, for example `shadowtree --all test -- -run TestName`; single-token
flags such as `-count=1` do not require the delimiter.

## Config Discovery

Shadowtree discovers config upward from the current directory until the outer
Git repository boundary or filesystem root. Registered submodules continue into
their superprojects when they contain no nearer config. Independent nested
repositories and linked worktrees remain boundaries.

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
include = ["./common.shadowtree.toml"]
profile = "go"
shell = "sh"
shell_prelude = '''
shared_function() {
	echo ok
}
'''

[env]
KEY = "value"

[vars]
NAME = "static value"

[var_commands]
DYNAMIC_NAME = "cmd arg"

[recipes.<name>]
help = "Short recipe help text."
sandboxed = true
for_each = "cmd arg"
workdir = "{item}"
pre = ["cmd arg"]
cmd = "cmd {placeholders}"
post = ["cmd arg"]
sync_out = ["path/from/project/root"]
log = "logs/{run_id}.log"
log_stages = ["pre", "cmd", "post"]
log_tee = true

[recipes.<name>.requires]
commands = ["go"]
optional_commands = ["h2load"]
go_commands = { stringer = "golang.org/x/tools/cmd/stringer@latest" }
node_commands = { eslint = "eslint@^9" }

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
values = "cmd arg"
```

`include` entries are TOML config file paths resolved relative to the config
file that contains them. Included files are merged before the including file;
later includes override earlier includes, and the including file overrides all
included files. Includes are global mixins: top-level `profile`, `shell`,
`shell_prelude`, `vars`, `var_commands`, `env`, and `recipes` all participate
in the effective config.

When multiple files define `shell_prelude`, Shadowtree concatenates included
preludes first, then the including file's prelude. If `a.shadowtree.toml`
includes `b.shadowtree.toml`, `a`'s prelude and all recipes can use shell
variables and functions created by `b`'s prelude. `b`'s prelude body cannot read
variables assigned later by `a` while the prelude is being evaluated.

Same-name recipes from includes are field-merged, with the including file
winning for fields it sets. There is no deletion syntax for inherited recipes,
arguments, vars, or env keys in this version. Use `@path:recipe` instead of
`include` when a recipe should stay isolated and run from another config's
directory.

## Recipe Fields

Commands are shell strings, recipe reference strings, or command-list entries
containing those strings. Shell strings are executed with the configured shell:

```toml
shell = "bash"

[recipes.example]
cmd = '''
set -euo pipefail
echo "hello"
'''
```

If `shell` is not set, Shadowtree uses `sh`.
Supported config shells are `sh` and `bash`; fish remains supported only as a
generated CLI completion shell.

Command strings run through the configured shell after placeholder expansion.
A string that is exactly `@recipe` or `@path:recipe` invokes another recipe;
other strings run in the shell. Defaults belong in typed `arguments`,
referenced from `cmd`.

Default `{name}` expansion is raw text when unquoted, before the shell parses
the script. In single-quoted or double-quoted shell context, `{name}` is escaped
for that quote context, so `"{name}"`, `'{name}'`, and `"https://{host}"`
remain one shell word. Escaping is context-aware, not type-aware.

Explicit placeholder modes are available in shell script strings. Prefer normal
shell quotes for free string or path values, for example `foo "{bar}"`.

- `{name:shell}` expands as one shell-escaped word and is valid only outside
  shell quotes. Use it when the value must be embedded in an unquoted shell
  word, for example `foo -xxx{name:shell}`.
- `{name:dq}` expands as double-quote-safe content and is valid only inside
  double quotes.
- `{name:raw}` expands raw text and documents intentional unsafe shell text or
  word splitting.

In non-shell fields such as `env`, `vars`, `workdir`, `sync_out`, and `log`,
`{name}` and `{name:raw}` use raw string substitution; shell-specific modes are
invalid. `{@}` is only special in `cmd`, where it splices leftover recipe CLI
args as separate shell-quoted words and must occupy a whole shell word.

Example:

```toml
[recipes.gen-swagger]
cmd = "go generate ./internal/api"

[recipes.test]
pre = ["@gen-swagger"]
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
required = true
values = "@go-packages"

[recipes.build.arguments.project]
values = "@list-build-targets"
```

The text after `@` is the referenced recipe name. Bracket-style arguments pass
CLI arguments to the referenced recipe:

```toml
pre = ["@build-api[service=public]"]
```

For `sh` and `bash` script commands, including `cmd`, `pre`, `post`,
`for_each`, argument `values`, and `shell_prelude`, a literal `@recipe` command
word also invokes the referenced recipe directly. This works anywhere a normal
shell command can run, including conditionals:

```toml
[recipes.test]
cmd = '''
if [ -f schema.json ]; then
	@generate mode=dev
fi
'''
```

Only literal command-position references dispatch recipes. Leading assignment
prefixes apply to that recipe command's environment. Assignment values,
expanded variables, quoted text, and ordinary command arguments do not dispatch
recipes:

```sh
FOO=bar @generate mode=dev  # recipe dispatch with FOO in @generate's environment
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
cmd = "@webui:gen-schema"
```

In command-list fields such as `pre` and `post`, only strings that are exactly
`@recipe` are direct recipe references:

```toml
pre = ["echo 123", "@gen-swagger"]
```

Non-reference strings in `pre` and `post` still run in the shell, so a literal
command-position `@recipe` inside them also dispatches a recipe:

```toml
post = ["if [ -f schema.json ]; then @publish-schema; fi"]
```

Recipe references can use bracket-style arguments. Comma separators split the
argument list, and surrounding whitespace is ignored:

```toml
pre = ["@build[component=godoxy, mode=dev]"]
cmd = "@test[package=./internal/recipe]"
post = ["@webui:gen-schema[mode=dev]"]
```

`pre` and `post` may also be structured stage command tables when a command
needs execution controls:

```toml
[recipes.benchmark.pre]
cmd = "benchmark_prepare"
timeout = "120s"
```

`timeout` is parsed as a Go duration and must be greater than zero. It limits
that one stage command. Timeout failure follows the same stage-order rules as
other command failures: a failing `pre` skips the main command, and `post`
commands still run after `pre` or main failure.

In `sh` and `bash` script commands, `@retry` is a built-in command helper for
flaky readiness checks:

```toml
pre = "@retry[count=30,delay=1s] benchmark_prepare"
```

`count` is the maximum number of attempts and `delay` is the duration to sleep
between failed attempts. Defaults are `count=3` and `delay=1s`. The helper runs
the remaining command words again until they succeed or attempts are exhausted.
It can wrap external commands, shell functions, or literal recipe references
such as `@retry[count=5] @prepare`. Because `@retry`, `@recipe`, and built-in
recipe references remain normal shell commands inside script strings, they can
be composed with operators such as `&&` and `||`.

When retrying a shell function under `set -e`, the function must return
failures explicitly, for example `cleanup_step || return $?`. This follows shell
semantics: `errexit` is suppressed while command status is being tested by
retry, `if`, `&&`, or `||`.

Placeholders can be used in recipe references. Static references such as
`@gen-swagger` and `@webui:gen-schema` can be validated by the editor; dynamic
references such as `@{target}` and `@{target_path}:{target_recipe}` are
resolved at run time after placeholder expansion.

Top-level `shell_prelude` is prepended to every script command. It is intended
for shared shell functions and variables:

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
top-level vars. Static vars may reference other static or dynamic vars with the
same `{name}` placeholder syntax.
Top-level and recipe-specific `env` values are expanded with the same
placeholders when a recipe is resolved. `shell_prelude` is expanded with the
same shell-aware placeholder escaping before it is prepended to script
commands.

`help`
: Short human-facing help text. Used by `shadowtree help`, `shadowtree recipes`,
and shell completion.

`sandboxed`
: Whether to run the recipe in a temporary workspace. Defaults to `true`.
`false` runs the recipe directly in the source checkout and skips sync-out.

`cmd`
: Required shell string or `@recipe` reference for the main command.

`for_each`
: Optional value-provider command. When set, `cmd` runs once per candidate
value. It accepts the same value-provider forms as argument `values`, including
`@enum`, `@lines`, `@glob`, `@go-modules`, `@go-packages`,
`@go-main-packages`, `@recipes`, `@vars`, command output, and recipe
references.

`workdir`
: Optional relative workspace path used as the working directory for the main
command. With `for_each`, it is expanded per item and can use `{item}`,
`{item_help}`, and `{item_index}`.

`pre`
: Commands run before the main command, in order. May be an array of command
  strings or one structured table with `cmd` and optional `timeout`.

`post`
: Commands run after the main command, in order. May be an array of command
  strings or one structured table with `cmd` and optional `timeout`.

`env`
: Recipe-specific environment overrides.

`sync_out`
: Paths mirrored back to the host checkout after a successful sandboxed recipe.
If a selected path is deleted in the sandbox, it is deleted from the host
checkout. Ignored when `sandboxed = false`.

`log`
: Optional recipe log file path. The path is expanded with recipe placeholders,
including `{run_id}`. It must be a relative local path under the active config
file directory, or under the source checkout when no config path exists. Parent
directories are created with mode `0755`, and the log file is opened/truncated
with mode `0644` before recipe commands start.

`log_stages`
: Optional list of stages to write to `log`. Valid values are `pre`, `cmd`, and
`post`; omitting the field logs all three. The `cmd` stage includes every
`for_each` main command item. The `for_each` value-provider command itself is
not `cmd` stage output.

`log_tee`
: Optional boolean. Defaults to `true`, which preserves terminal stdout/stderr
while also writing selected stage output to the log. When `false`, selected
stage stdout/stderr are written only to the log.

Each selected logged command is preceded by a boundary line of the form
`== stage: command ==`, for example `== pre[0]: <script> ==`,
`== cmd: @build ==`, or `== post[0]: <script> ==`. Long one-line commands are
truncated in the boundary. Multiline scripts are shown as `<script>`; full
multiline script bodies are never dumped into boundary lines.

`requires`
: Optional recipe-local tool requirements checked before sandbox setup and
before any `pre` command. Requirements are static declarations and are not
placeholder-expanded. An included or overriding recipe that specifies
`requires` replaces the previous `requires` block as a whole.

`requires.commands`
: Required executable names, not paths. Missing entries fail the recipe in one
grouped error such as `recipe "benchmark" missing required tools: docker,
openssl`.

`requires.optional_commands`
: Optional executable names. Missing entries print one stderr warning and the
recipe continues, for example `shadowtree: recipe "benchmark" optional tools
not found: h2load`.

`requires.go_commands`
: Required Go-installable tools keyed by executable name with package strings
as values, for example `stringer =
"golang.org/x/tools/cmd/stringer@latest"`. Shadowtree checks only for the
executable on `PATH`; it does not run `go install`. Missing entries fail with
guidance such as `stringer (go install
golang.org/x/tools/cmd/stringer@latest)`.

`requires.node_commands`
: Required Node-installable CLI tools keyed by executable name with package
strings as values, for example `eslint = "eslint@^9"`. Shadowtree checks only
for the executable on `PATH`; it does not install packages. Missing entries use
the detected package manager to suggest installing the CLI, such as `npm
install -g eslint@^9`, `pnpm add --global eslint@^9`, `yarn global add
eslint@^9`, or `bun add --global eslint@^9`.

Requirement command names must be non-empty executable basenames without path
separators or surrounding whitespace. `commands` and `optional_commands` cannot
contain duplicates within each list. `go_commands` and `node_commands` keys use
the same executable basename rules, excluding `run_id`, and package values must
be non-empty strings without surrounding whitespace. Duplicate executable names
across `commands`, `go_commands`, and `node_commands` are rejected. Overlap
between required and optional names is rejected.

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
`rel_path`, `duration`, and `duration:seconds`. The default is `string`. `path`
accepts absolute and relative paths. `rel_path` accepts relative paths only and
rejects absolute paths and `~` home paths. `duration` accepts Go duration
strings parsed by `time.ParseDuration`, such as `10s`, `1500ms`, and `1m30s`,
and preserves the configured or CLI text when expanded. `duration:seconds`
accepts the same duration format only when it is an exact whole number of
seconds, and expands as base-10 integer seconds.

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

`min`
: Optional inclusive lower bound for `int`, `float`, `duration`, and
`duration:seconds` arguments. Values are converted and type-checked the same
way as argument values. Duration bounds use Go duration strings.

`max`
: Optional inclusive upper bound for `int`, `float`, `duration`, and
`duration:seconds` arguments. `max` must be greater than or equal to `min` when
both are set.

`values`
: Optional command that produces completion candidates for this argument. Each
output line is a value, optionally followed by a tab and help text. The
command is a shell string. It can be an `@recipe` reference, or an
argument-values builtin:

```toml
values = '@enum api worker "admin ui"'
values = "@enum api='API service' worker='Worker service'"
values = "@enum_set service"
values = '@lines config/targets.txt'
values = '@glob "cmd/*"'
values = "@go-modules; @enum all='all modules'"
values = '@go-packages'
values = '@go-main-packages'
values = '@recipes'
values = '@vars'
```

`@enum` returns literal values from its arguments. Enum arguments in
`value=help text` form attach help when the help side contains whitespace;
quote the help side, for example `@enum all='all modules'`.
Single-token values such as `GOOS=linux` remain
literal values. `@lines` reads candidates from a text file, using the same
`value<TAB>help` line format. `@glob` returns filesystem matches. `@go-modules`
returns directories containing `go.mod`, with `.` representing the config
directory module and help from the module directive. `@go-packages` runs
`go list` in the config-directory module and, when `go.work` is present, in
modules listed by the workspace. It returns package arguments such as
`./internal/recipe`, with help from import paths. `@go-main-packages` returns
package arguments for directories containing non-test Go files with
`package main`, with help from package comments when available. Multiple builtin
value commands in a `values` field can be separated with `;`; their candidates
are concatenated without running a shell. `@recipes` returns resolved recipe
names.
`@vars` returns recipe placeholder and argument names. Relative `@lines` paths,
`@glob` patterns, and Go discovery walks resolve from the config file directory
when available.
When `values` is a builtin that can be checked without running arbitrary
commands or walking the filesystem, such as `@enum`, `@recipes`, or `@vars`,
explicit CLI and recipe-reference argument values must match one of its
candidates. Filesystem discovery and command-backed `values` remain completion
and help providers and are not run as part of argument validation.

Example:

```toml
[recipes.build]
help = "Build a Go package."
cmd = 'go build -o "bin/{binary}" "{project}" {@}'
sync_out = ["bin/{binary}"]

[recipes.build.arguments.project]
help = "Go main package to build."
type = "string"
position = 1
default = "./cmd/shadowtree"
values = "@go-main-packages"

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

Recipe-local presets can set multiple typed argument defaults. They are
declared under:

```toml
[recipes.<name>.presets.<preset-name>.arguments]
<arg-name> = <scalar>
```

Preset names use identifier syntax (`[A-Za-z_][A-Za-z0-9_]*`). Preset
argument keys must name typed arguments declared by the same recipe, and values
are converted and type-checked the same way as argument `default` values. A
recipe with `presets` reserves the `preset` recipe argument name for preset
selection.

Select a preset with `preset=<preset-name>` after the recipe name:

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

Argument values are resolved in this order: typed argument defaults, selected
recipe preset defaults, then explicit positional or `key=value` CLI arguments.
The `preset=<name>` selector is consumed like a typed argument and is excluded
from `{@}`. Tokens after `--` are not preset selectors.
Resolved argument values are type-checked, range-checked, and, for safely
checkable `values` builtins, checked against the accepted candidate set before
any recipe command runs.

Argument values are exposed to recipe commands through `{name}` placeholders.
Shared vars are exposed through the same placeholder syntax. Placeholders are
expanded in `vars`, `env`, `cmd`, `pre`, `post`, `for_each`, `shell_prelude`,
`workdir`, `sync_out`, and `log`.
Shell parameter expansion such as `${HOME}` is not treated as a Shadowtree
placeholder.
In shell command strings, placeholders inside single or double quotes are
escaped for that quote context, so `"{name}"` and `'{name}'` stay one shell word
even when the value contains quote characters.

`{run_id}` is built in. Shadowtree generates one filesystem-safe lowercase hex
run ID for each top-level invocation and reuses it for `pre`, `cmd`, `post`,
`for_each` expansion, and nested `@recipe` calls. The name `run_id` is reserved
and cannot be declared in top-level `vars`, top-level `var_commands`, recipe
`vars`, or recipe `arguments`.

`cmd` commands can use `{status:pre}`, and `post` commands can use
`{status:pre}` and `{status:cmd}` to inspect prior stage status. They expand to
`0` when the stage succeeds, the failing exit code when available, `1` for
non-exit failures such as timeouts, and an empty string when that stage did not
run.

`{@}` is a variadic placeholder for leftover recipe CLI args. It must be a
whole argument item in argv-style `cmd` values, or a whole shell word in script
`cmd` values; Shadowtree splices each leftover CLI arg at that position. It is
supported only in `cmd`, not in `pre`, `post`, or `sync_out`. For recipes with typed
`arguments`, positional argument values and known `key=value` argument values
are consumed by those arguments and excluded from `{@}`. Unknown identifier
`key=value` tokens remain errors; command flags such as `-run=TestName` pass
through. Use `--` after typed recipe arguments to pass the following argv
literally to `{@}`, including option values that contain `=`:

```toml
[recipes.test]
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
position = 1
required = true
values = "@go-packages"
```

```sh
shadowtree test ./internal/recipe -run=TestResolve -count=1
shadowtree test pkg=./internal/recipe -- --flag NAME=value
```

## Fan-Out Recipes

`for_each` runs a recipe's main command once per value candidate:

```toml
[recipes.lint]
help = "Run golangci-lint in every Go module."
for_each = "@go-modules"
workdir = "{item}"
cmd = "golangci-lint run ./..."
```

`for_each` uses the same candidate format and builtins as argument `values`.
Builtin providers can be composed with semicolons without running a shell:

```toml
for_each = "@go-modules; @enum all='all modules'"
```

Per iteration, these placeholders are available to `cmd` and `workdir`:

- `{item}`: candidate value.
- `{item_help}`: candidate help text, or empty string.
- `{item_index}`: zero-based item index.

`workdir` can be used without `for_each`; it makes the main command run from a
relative path under the recipe workspace. With `for_each`, `workdir` is
expanded for each item. `pre` commands run once before candidate resolution.
`post` commands run once after the loop, even if `pre`, candidate resolution,
or an item command fails. Items run sequentially; the first failing item stops
later items. For sandboxed recipes, `sync_out` runs once after all items and
`post` commands succeed. `sync_out` does not accept `{item}` placeholders.

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
fields, except `for_each` and `workdir`. Those scheduling fields are not
inherited. Any project override also removes the built-in profile-owned
aggregate plan, so `--all` fails unless a future supported configuration
surface explicitly supplies one.

Example:

```toml
[recipes.test]
help = "Run generated-code tests."
workdir = "."
pre = ['go generate "{pkg}"']
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
position = 1
required = true
values = "@go-packages"
```

The built-in `test` recipe normally executes once from the recipe workspace:

```text
cmd = "go test ./... {@}"
```

`shadowtree --all test` selects a distinct aggregate plan. After `pre`, it
discovers package batches in the active workspace and runs once from each
owning module with:

```sh
go test ./...
```

Aggregate `build` and `install` main-package discovery uses the active Go
toolchain with the resolved command environment, so `GOOS`, `GOARCH`,
`GOFLAGS`, and other build constraints determine which main packages are
selected. Forwarded Go build-context flags such as `-tags` are also applied to
discovery before the same arguments reach the recipe command.

With the override above, the recipe runs once from the root workdir and CLI
args are parsed by the custom typed argument:

```sh
shadowtree test ./internal/recipe
```

runs:

```sh
go generate ./internal/recipe
go test ./internal/recipe
```

For recipes with typed `arguments`, CLI args are parsed as argument values
instead. Placeholders in `cmd` expand from argument defaults and supplied
values. Use `{@}` in `cmd` when a typed recipe should also forward leftover CLI
args after its typed argument values.

## Execution Semantics

For a sandboxed recipe:

1. Create a temporary workspace with namespace overlayfs, or copy the source
   tree if namespace overlayfs is unavailable.
2. Open/truncate the configured log file, when `log` is set.
3. Run `pre` commands in order.
4. Run the resolved main command, or once per `for_each` candidate when set.
5. Run `post` commands in order.
6. If all phases succeeded, sync configured/requested paths back.
7. Remove the temporary workspace.

When a command is an `@recipe` or `@path:recipe` reference, Shadowtree runs the
referenced recipe's `pre`, main command, and `post` directly in the current
workspace. It does not start another Shadowtree process, create a nested
sandbox, or perform the referenced recipe's sync-out. Cross-config references
load `path/.shadowtree.toml` relative to the referencing config and run from the
target path inside the current host checkout or current sandbox. Sync-out is
performed only for the top-level invoked recipe after all phases succeed.
When a selected parent log stage invokes a nested recipe, all nested output is
written through the parent stage writers. Nested recipe `log` settings do not
open another file during a recipe reference.
Recursive recipe references fail with a cycle error.

Failure behavior:

- If a `pre` command fails, the main command is skipped.
- `post` commands still run after a `pre` or main command failure.
- `post` commands run as cleanup after the run context is canceled, such as by
  SIGINT.
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
go-main-packages
go-modules
go-packages
help
lines
retry
vars
version
__complete
```

The argument-values builtins (`enum`, `glob`, `go-main-packages`, `go-modules`,
`go-packages`, `lines`, `recipes`, and `vars`) and command helpers such as
`retry` are reserved as recipe names. Future built-in `@` command identifiers
are also reserved.

## Built-In Profiles

Supported profiles are `go`, `node`, and `rust`. Profile selection precedence is:

1. explicit `--profile`;
2. config `profile`;
3. marker detection only when no config is loaded.

Configs that omit `profile` suppress detected built-ins. This preserves exact
configured recipe sets unless a config opts into a profile.

When marker detection is active, Shadowtree walks upward from the current
directory and compares the nearest profile markers:

- `package.json` selects `node`.
- `go.mod` or `go.work` selects `go`.
- `Cargo.toml` selects `rust`.
- Same-directory precedence is Go, then Node, then Rust.

## Built-In Go Profile

The Go profile is selected when:

- `--profile go` is provided, or
- config has `profile = "go"`, or
- no config is loaded and Shadowtree detects `go.mod` or `go.work` upward from
  the current directory.

Built-in Go recipes:

```text
build      go build {pkg}; --all targets main packages
check      @vet && @test
fix        go fix {pkg} when go > 1.26; --all targets packages
fmt        go fmt {target}; --all targets packages
generate   go generate {pkg}; --all targets packages
install    go install -ldflags="-s -w" {pkg}; --all targets main packages
lint       lint {pkg}; --all targets packages
run        go -C {cwd} run {command}
test       go test {pkg}; --all targets packages
test-race  go test -race {pkg}; --all targets packages
tidy       go mod tidy; --all targets modules, then syncs go.work
vet        go vet {pkg}; --all targets packages
```

Built-in `fix`, `fmt`, and `tidy` are unsandboxed by default, so `go fix`,
`go fmt`, `go mod tidy`, and `go work sync` update the host checkout directly.
Other built-in Go recipes are sandboxed unless project config overrides them.
Normal built-ins are leaf recipes and preserve explicit targets from the
recipe workspace. `--all` selects a recipe-specific aggregate target domain:
package recipes batch `./...` per module, `build` and `install` execute each
main package from its owning module, and `tidy` executes each module. `run`
rejects `--all` because multi-process supervision is undefined. Discovery runs
after `pre` in the active workspace, and an empty target set fails. Built-in
`build` exposes an optional positional `pkg` argument with shell completion from
`@go-main-packages`; `install` exposes a named `ldflags` argument defaulting to
`-s -w` plus an optional positional `pkg` completed from `@go-main-packages`;
other package-style Go built-ins expose `pkg` completion
from `@go-packages`; `fix` is available when the most common
`go.mod` directive is greater than `1.26`. `fmt` exposes an optional positional `target`
from `@go-packages` plus `@glob "*.go"`. Built-in `run` has a named `cwd`
argument defaulting to `.` and a required positional `command` argument with
`rel_path` type. `cwd` completes from `@go-modules`; `command` completes from
`@go-main-packages` plus `@glob "*.go"`. `command` is interpreted by `go run`
after `go -C {cwd}`, so non-default `cwd` values use paths relative to that
directory.

## Built-In Node Profile

The Node profile is selected when:

- `--profile node` is provided, or
- config has `profile = "node"`, or
- no config is loaded and Shadowtree detects the nearest `package.json` upward
  from the current directory.

Node built-ins resolve the nearest `package.json` directory and generate shell
commands that `cd` there before invoking the package manager or tool. This
makes subdirectory invocation run against the package root. The profile
currently rejects `--all` because npm, pnpm, Yarn, and Bun require
package-manager-specific workspace semantics. Every Node built-in
recipe has `sandboxed = false` by default because package-manager and framework
commands commonly mutate lockfiles, dependency state, caches, and generated
outputs.

Package manager detection:

1. `packageManager` prefix: `pnpm`, `yarn`, `bun`, or `npm`;
2. lockfiles in order: `pnpm-lock.yaml`, `yarn.lock`, `bun.lockb`, `bun.lock`,
   `package-lock.json`, `npm-shrinkwrap.json`;
3. default `npm`.

Command forms:

- Package scripts run `<pm> run <script> -- {@}`.
- Tool commands run `npm exec -- <bin> ... {@}`, `pnpm exec <bin> ... {@}`,
  `yarn exec <bin> ... {@}`, or `bunx <bin> ... {@}`.
- Bun projects without a test script use `bun test {@}` when Vitest is not
  installed.

Default Node recipes:

```text
install    npm|pnpm|yarn|bun install
dev        package script dev, or inferred framework dev command
build      package script build, or inferred framework build command
start      package script start, or inferred framework start/preview command
test       package script test, or Vitest/Jest/Playwright/Bun fallback
lint       package script lint, or ESLint/Oxlint/Biome
fmt        package script fmt/format, or Prettier/Oxfmt/Biome
typecheck  package script typecheck/type-check, or detected type checkers
check      available lint, typecheck, and test recipes in that order
```

Framework inference for `dev`, `build`, and `start`:

- `next`: `next dev`, `next build`, `next start`.
- `vite`: `vite`, `vite build`, `vite preview`.
- `nuxt`: `nuxt dev`, `nuxt build`, `nuxt preview`.
- `astro`: `astro dev`, `astro build`, `astro preview`.
- `@sveltejs/kit`: `vite`, `vite build`, `vite preview`.

Test inference:

- Script `test` wins.
- Bun projects use installed `vitest` first, otherwise `bun test`.
- Other projects use installed `vitest`, then `jest`, then `playwright test`
  when `@playwright/test` is installed.

Lint inference:

- Script `lint` wins.
- ESLint markers: `eslint` dependency, `eslint.config.*`, `.eslintrc*`, or
  package `eslintConfig`; command `eslint .`.
- Oxlint markers: `oxlint` dependency, `oxlint.config.*`, `.oxlintrc.json`, or
  `.oxlintrc.jsonc`; command `oxlint`.
- Biome markers: `@biomejs/biome` dependency, `biome.json`, or `biome.jsonc`;
  command `biome lint .`.

Format inference:

- Script `fmt` wins, then script `format`.
- Prettier markers: `prettier` dependency, `prettier.config.*`,
  `.prettierrc*`, or package `prettier`; command `prettier --write .`.
- Oxfmt markers: `oxfmt` dependency, `oxfmt.config.*`, `.oxfmtrc.json`, or
  `.oxfmtrc.jsonc`; command `oxfmt`.
- Biome markers: `@biomejs/biome` dependency, `biome.json`, or `biome.jsonc`;
  command `biome format --write .`.

Typecheck inference:

- Script `typecheck` wins, then script `type-check`.
- Otherwise Shadowtree runs every detected checker in stable order:
  `vue-tsc --noEmit`, `svelte-check`, then `tsc --noEmit`.
- `tsc --noEmit` is included when `typescript` is installed or `tsconfig.json`
  exists.

Package scripts fill gaps without overriding predefined Node recipe names.
Before a package script becomes a recipe name, Shadowtree replaces `:` and every
character outside `[A-Za-z0-9._-]` with `-`, collapses repeated `-`, trims
leading and trailing `-`, and skips empty or reserved names. If multiple scripts
normalize to the same recipe name, the script whose original name already equals
the normalized name wins; otherwise the lexicographically first original script
name wins. For example, package script `lint:fix` becomes recipe `lint-fix`, but
the generated command still runs the original script key `lint:fix`.

## Built-In Rust Profile

The Rust profile is selected explicitly or from the nearest `Cargo.toml` when
no config is loaded. It provides `check`, `test`, `build`, `run`, `fmt`, and
`clippy`, forwarding trailing arguments to Cargo. Aggregate execution uses
Cargo workspace flags for every recipe except `run`, whose multiple-binary
policy must be selected explicitly.

Rust resolution has one owner for the canonical workspace root, root and member
manifests, optional lockfile, exact toolchain and provenance, host and selected
target triples, Cargo home caches, target directory, project cache key, and
exclusive target-cache concurrency. Toolchain precedence is the nearest
`rust-toolchain.toml`, then `rust-toolchain`, then exact default `1.96.0`.
Ambiguous or malformed declarations fail with their path and value. Shadowtree
does not install toolchains, rustfmt, or Clippy. A present lockfile enables
`cargo fetch --locked` as the dependency-preparation contract.

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
- requires section when recipe-local tool requirements are declared
- pre command section
- post command section
- for_each section
- workdir section
- argument section with `name - help`, `info:`, and configured `values:`
- sync-out section for sandboxed recipes

Multi-line command arguments are summarized as `<script>` in help, completion,
verbose boundary, and log boundary output.

## Recipe Listing

`shadowtree recipes` prints resolved recipe names and help text. If a recipe has
no `help`, Shadowtree falls back to a compact command summary.

## Plan Printing

`--print` prints the resolved execution plan without running it:

```sh
shadowtree --print test ./internal/runner
shadowtree --print --expanded test ./internal/runner
shadowtree --all --print test
```

The plan includes these fields when present or applicable:

- recipe name
- selected `scope`, aggregate `target_domain`, and `target_source` for `--all`
- profile
- config path
- `sandboxed: false` for unsandboxed recipes
- declared requirements without checking the host
- pre commands
- for_each command
- workdir
- main command
- post commands
- sync-out paths for sandboxed recipes

`--print --expanded` also prints normalized defaults for absent fields,
expanded script bodies for `pre`, `cmd`, `post`, and `for_each`, the selected
preset, resolved typed arguments, leftover variadic args, computed vars,
recipe-local env, expanded log settings, and expanded sync-out paths.

`--check` validates the selected resolved recipe without running commands. It
checks the same command forms that resolution would run, validates nested
`@recipe` and `@path:recipe` references, reports missing references, rejects
reference cycles, and validates resolved log and workdir paths. It does not
check host tool availability declared in `requires`; those are checked only
before real execution. `--check --shell` additionally parses expanded sh/bash
script bodies after placeholder expansion and shell prelude insertion.

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
- `shadowtree --profile <TAB>` completes `go`, `node`, and `rust`.
- `shadowtree build <TAB>` completes configured recipe arguments such as
  `project=`.
- `shadowtree benchmark preset=<TAB>` completes recipe-local preset names
  such as `stable` and `stress`.
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
  `for_each`, `shell_prelude`, and `values` strings
- recipe-reference completion, diagnostics, and semantic tokens for literal
  command-position `@recipe` in `sh`/`bash` script strings, including
  `shell_prelude`, `for_each`, and `pre`/`post`

Shell semantic highlighting supports `shell = "bash"` and `shell = "sh"`.

Zed completion, diagnostics, and semantic tokens are provided by
`shadowtree-lsp`.
The Zed extension starts `shadowtree-lsp` from `PATH`; when developing inside
the Shadowtree checkout, it runs `go run ./cmd/shadowtree-lsp` so local LSP
changes take effect before an installed binary on `PATH`.
The bundled Zed language association covers `.shadowtree.toml`; TOML files
under `.shadowtree/` require user `file_types` settings because extension
`path_suffixes` do not support glob patterns.

VS Code support:

```text
editors/vscode-shadowtree/
```

The VS Code companion manifest contributes `tomlValidation` rules for
`*.shadowtree.toml` files and TOML files under `.shadowtree/`. Even Better TOML
consumes those rules and provides schema-backed validation, hover, and
completion.

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
build          Build the shadowtree binary into bin/.
check          Run vet and tests.
fix            Update Go source with go fix.
fmt            Format Go source files.
generate       Run go generate.
install        Install the Shadowtree CLI and language server.
install-skill  Install local agent skills.
lint           Run Go lint checks.
run            Run a Go command.
test           Run Go tests.
test-race      Run Go tests with the race detector.
tidy           Tidy Go module files.
vet            Run go vet.
```

Recipes that intentionally mutate the host checkout set `sandboxed = false`:

```text
fix
fmt
tidy
```

## Known Limits

- Workspace isolation uses namespace overlayfs only when the host supports it.
- Large repositories may be slower when Shadowtree falls back to copying files.
- Commands can still intentionally read or write absolute host paths.
- Configured commands are shell strings; direct process argv arrays are only an
  internal representation used by built-in recipes and resolved execution.
- Built-in language profiles currently cover Go, Node, and Rust.
