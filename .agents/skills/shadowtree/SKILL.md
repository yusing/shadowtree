---
name: shadowtree
description: Use and configure the Shadowtree development recipe runner. Use when the user asks to run project recipes through shadowtree, isolate tests/builds/lints/codegen in a disposable workspace, inspect or write .shadowtree.toml config, initialize config, use sync-out safely, print resolved recipe plans, install completions, or explain sandboxed versus unsandboxed recipe behavior.
---

# Shadowtree

Use Shadowtree to run project tasks through named recipes while keeping command writes isolated by default. Read live state first; do not guess recipe names, resolved commands, profile, sandbox mode, or sync-out.

## Evidence First

Inspect before acting:

```sh
shadowtree config
shadowtree recipes
shadowtree help <recipe>
shadowtree --print <recipe> [args...]
```

Create starter config only when config should be added:

```sh
shadowtree init
shadowtree init path/to/shadowtree.toml
```

Global flags must appear before the command or recipe name. Later flags are recipe args:

```sh
shadowtree --verbose test ./...
shadowtree test -v ./...
```

Completion criterion: chosen command is based on Shadowtree output or an existing config file.

## Commands

- `shadowtree recipes`: list resolved recipes.
- `shadowtree help`: show CLI help plus resolved recipes.
- `shadowtree help <recipe> [color=false]`: show command summary plus applicable args, configured values, pre/post, for_each, workdir, unsandboxed marker, and sync-out for one recipe. Recipe help uses color by default; `color=false` disables it.
- `shadowtree config`: print config path, profile, and resolved recipe list.
- `shadowtree init [path]`: create `.shadowtree.toml` or the given path; fails if the file exists.
- `shadowtree completion bash`: emit bash completion script.
- `shadowtree completion fish`: emit fish completion script.
- `shadowtree completion zsh`: emit zsh completion script.
- `shadowtree exec -- <cmd> [args...]`: run an explicit command as a sandboxed ad hoc recipe.
- `shadowtree <recipe> [args...]`: run a resolved recipe.

Use `--print` before commands that may write, delete, install, publish, regenerate, sync out, or use unfamiliar args. Use `--verbose` to see workspace and phase commands.

Completion criterion: risky or unclear execution is inspected with `--print` before it runs.

## Config Discovery

Discovery walks upward from the current directory until the git root or filesystem root:

```text
.shadowtree.toml
```

An explicit `--config PATH` bypasses discovery. Supported profiles are `go` and `node`. When no config is loaded, Shadowtree detects the nearest upward marker: `package.json` selects `node`, `go.mod` or `go.work` selects `go`, and same-directory ties select `go`. A config that omits `profile` suppresses detected built-ins.

Completion criterion: config location and profile are known before editing or explaining behavior.

## Sandbox Contract

Recipes are sandboxed unless `sandboxed = false`.

- Sandboxed: commands run in a temporary workspace; host checkout changes only through sync-out after successful command completion.
- Linux: Shadowtree prefers namespace overlayfs and runs commands at the source checkout path inside the namespace.
- Fallback: when overlayfs is unavailable, Shadowtree warns and uses a copied workspace.
- Skipped while preparing workspaces: `.git`, `.shadowtree`, and `.shadowtree.*`.
- Unsandboxed: commands run directly in the host checkout.

Shadowtree ignores `sync_out`, `--sync-out`, and `--sync-out-all` for unsandboxed recipes because there is no sandbox to copy from.

Completion criterion: persistence expectations match sandbox mode.

## Execution Order

For a recipe:

1. `pre` commands run in order.
2. If all `pre` commands succeed, the resolved `cmd` runs once, or once per `for_each` value when set.
3. `post` commands run even when a `pre` or main command failed.
4. The first pre/main error wins unless only `post` failed.
5. For sandboxed recipes, sync-out runs only after all recipe commands finish successfully.

Command env is `os.Environ()` overlaid by top-level `env`, then recipe `env`.

A scalar command that is exactly `@recipe` invokes another resolved Shadowtree
recipe directly in the current workspace. Use `@path:recipe` to load
`path/.shadowtree.toml` relative to the referencing config directory and run the
target recipe from that path. Recipe references do not spawn another
`shadowtree` process, start a nested sandbox, or run the referenced recipe's
sync-out. Pass args with bracket syntax, e.g. `@build[mode=dev]`. Recursive
references fail with a cycle error.

In `sh` and `bash` script commands, including `cmd`, `pre`, `post`, scalar
`for_each`, argument `values`, and `shell_prelude`, a literal
command-position `@recipe` also invokes the referenced recipe directly,
including inside conditionals.
For example, `if @generate; then ...; fi` dispatches `generate`, and
`@build mode=dev` passes `mode=dev` as a recipe CLI arg. Assignments and
expanded variables do not dispatch recipes: `FOO="@build"` is just a shell
assignment, and `$FOO` uses normal shell command lookup.

In command-list fields such as `pre` and `post`, a string command that is
exactly `@recipe` is also a recipe reference. Other string commands remain shell
scripts, so `pre = ["echo 123", "@foo"]` runs `echo 123` as a script and then
invokes recipe `foo`. A literal command-position `@recipe` inside those script
strings also dispatches directly, e.g. `post = ["if @check; then @publish; fi"]`.

Structured `pre` and `post` tables support one stage command with execution
controls:

```toml
[recipes.benchmark.pre]
cmd = "benchmark_prepare"
timeout = "120s"
```

`timeout` is a positive Go duration for that stage command. Timeout failure
follows normal stage ordering: failing `pre` skips `cmd`, and `post` still runs.

`@retry` is a sh/bash command-position helper for flaky readiness checks:
`pre = "@retry[count=30,delay=1s] benchmark_prepare"`. `count` is max attempts,
`delay` is wait between failures, defaults are `count=3` and `delay=1s`, and it
can wrap external commands or literal recipe references.

Completion criterion: cleanup or reporting that must run after failure belongs in `post`; generated outputs are copied back only on success.

## Top-Level Fields

Use these fields at config root:

- `include`: TOML config files merged before this config; paths are relative to the including config file.
- `profile`: `go` or `node`.
- `shell`: shell for script commands; defaults to `sh`.
- `shell_prelude`: shell code prepended to every script command.
- `sync_out`: sandboxed paths copied back after successful runs for every recipe unless unsandboxed.
- `env`: environment variables for recipe commands and top-level `var_commands`.
- `vars`: static placeholders usable as `{NAME}`.
- `var_commands`: commands evaluated from the source checkout to produce placeholder values.
- `recipes`: map of named recipes.

Includes are global mixins, not isolated imports. Later includes override
earlier includes, and the including file overrides all included files. Included
recipes appear in help, `recipes`, shell completion, and LSP completion.
Top-level `vars`, `var_commands`, `env`, `sync_out`, `profile`, `shell`, and
`shell_prelude` also merge into the effective config. Included preludes run
before the including file's prelude.

Valid `vars` and `var_commands` keys match `[A-Za-z_][A-Za-z0-9_]*`.

Completion criterion: every top-level setting affects more than one recipe or intentionally defines shared defaults.

## Recipe Fields

Use these fields under `[recipes.<name>]`:

- `help`: short text shown by `help`, `recipes`, and shell completion.
- `cmd`: required main command; prefer a shell string.
- `for_each`: optional value-provider command; when set, runs `cmd` once per candidate.
- `workdir`: optional relative working directory for the main command; with `for_each`, expands per item.
- `pre`: list of commands before `cmd`, or a structured table with `cmd` and optional `timeout`.
- `post`: list of commands after `cmd`, or a structured table with `cmd` and optional `timeout`.
- `sandboxed`: boolean; defaults to `true`.
- `sync_out`: sandboxed paths copied back after successful recipe completion.
- `log`: optional recipe log file path; supports placeholders including `{run_id}`.
- `log_stages`: optional stages logged to `log`; values are `pre`, `cmd`, `post`; omitted means all three; selected commands get compact `== stage: command ==` boundaries.
- `log_tee`: boolean; defaults to `true`; when `false`, selected stage output goes only to the log.
- `requires`: recipe-local tool requirements checked before sandbox setup and before `pre`.
- `env`: recipe environment overrides.
- `vars`: recipe placeholder values overriding top-level `vars`.
- `shell`: recipe shell override.
- `shell_prelude`: recipe shell code appended after the top-level prelude.
- `arguments`: typed argument definitions.
- `profiles`: recipe-local argument default sets selected by `profile=<name>`.

Reserved recipe names: `recipes`, `init`, `config`, `exec`, `completion`, `enum`, `glob`, `go-main-packages`, `go-modules`, `go-packages`, `help`, `lines`, `retry`, `vars`, `version`, `__complete`, plus future built-in `@` command identifiers. `run` is a valid recipe name; use `shadowtree exec -- <cmd> [args...]` for the explicit-command form.

Completion criterion: each recipe has `help` and `cmd`, and uses typed `arguments` plus placeholders in `cmd` instead of extra argument lists.

## Tool Requirements

Use `[recipes.<name>.requires]` when a recipe needs tools to exist before any
commands run:

```toml
[recipes.benchmark.requires]
commands = ["docker", "openssl", "go"]
optional_commands = ["h2load"]
go_commands = { stringer = "golang.org/x/tools/cmd/stringer@latest" }
node_commands = { eslint = "eslint@^9" }
```

`commands`, `go_commands`, and `node_commands` are required executable checks.
`commands` and `optional_commands` use executable basenames, not paths. Missing
required tools fail before sandbox setup and before `pre`.
`optional_commands` prints one warning and continues. Shadowtree does not
install tools. Missing Go tools show `go install <module>@<version>` guidance.
Missing Node tools use detected `npm`/`pnpm`/`yarn`/`bun` to suggest installing
the CLI globally, such as `pnpm add --global eslint@^9`. `--print` shows
declared requirements without checking the host.

Requirement names are static, not placeholders. Included or overriding recipes
that specify `requires` replace the inherited `requires` block as a whole.

Completion criterion: run `shadowtree --print <recipe>` to inspect declared requirements before running recipes that depend on host tools.

## Command Forms

Use shell strings for recipe commands:

```toml
[recipes.test]
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
required = true
```

Command strings run through the configured shell after placeholder expansion.
A string that is exactly `@recipe` or `@path:recipe` invokes another recipe;
other strings run in the shell. Put typed `arguments` in placeholders when a
recipe needs validated inputs.
Unquoted `{arg}` is raw shell text. In normal cases, put free string/path
arguments in shell quotes, such as `foo "{bar}"`. Use `{arg:shell}` only when
the value must be embedded in an unquoted shell word, such as
`foo -xxx{bar:shell}`, and use `{arg:raw}` only for intentional raw shell
syntax or word splitting.

Use recipe references from `cmd`, `pre`, `post`, or argument `values`:

```toml
[recipes.test]
pre = ["@generate"]
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
required = true
```

Use bracket-style syntax to pass args to referenced recipes, e.g.
`@build-api[service=public]`.
Use `@webui:gen-schema` to invoke `gen-schema` from
`webui/.shadowtree.toml`; relative paths are resolved from the referencing
config directory and execution starts in `webui/`.
Use `@{NAME}` only when the recipe name must come from a placeholder; static
`@recipe` references are easier for LSP diagnostics and completion to validate.
Use bracket-style arguments when passing args:

```toml
[recipes.test]
pre = ["@build[component=godoxy, mode=dev]"]
cmd = "go test"
```

Use script strings for shell workflows:

```toml
shell = "sh"

[recipes.install]
sandboxed = false
cmd = '''
set -eu
install -d "$HOME/.local/bin"
install -m 0755 bin/tool "$HOME/.local/bin/tool"
'''
```

Script strings run as `<shell> -c <script> shadowtree`; `$0` is `shadowtree`. `shell_prelude` is joined before script bodies. Empty commands and empty script bodies are invalid.
In `sh` and `bash` script strings, including `shell_prelude`, a literal
command-position `@recipe` invokes that recipe directly without a Shadowtree
subprocess or nested sandbox:

```toml
[recipes.test]
cmd = '''
if [ -f schema.json ]; then
	@generate mode=dev
fi
'''
```

Completion criterion: command examples use scalar shell strings. Do not write
TOML argv arrays for command fields; they are invalid in config.

Editor support: Shadowtree LSP provides shell highlighting for script-valued
`cmd`, `pre`, `post`, `for_each`, `shell_prelude`, and scalar argument
`values`, plus the same recipe-reference completion and diagnostics for literal
command-position `@recipe` in those `sh`/`bash` strings as it provides for
scalar `values` recipe references.

## Placeholders And Vars

`{NAME}` placeholders expand in `cmd`, `pre`, `post`, `for_each`, `shell_prelude`, `workdir`, `sync_out`, and `log`.

Value sources:

1. top-level `vars`;
2. top-level `var_commands` output, trimmed;
3. recipe `vars`, overriding top-level values;
4. typed argument values, overriding vars with the same name.

Shell parameter expansion is preserved: `${NAME}` is not treated as a Shadowtree placeholder.

`{run_id}` is built in. Shadowtree generates one lowercase hex run ID per
top-level invocation and reuses it through `pre`, `cmd`, `post`, `for_each`, and
nested `@recipe` calls. `run_id` is reserved and cannot be declared in
top-level `vars`, top-level `var_commands`, recipe `vars`, or recipe
`arguments`.

`var_commands` use top-level `env`, configured `shell`, and top-level `shell_prelude`. They are not evaluated during shell completion.

Fan-out placeholders exist only when a recipe has `for_each`:

- `{item}`: current candidate value.
- `{item_help}`: current candidate help text, if any.
- `{item_index}`: zero-based candidate index.

`for_each` accepts the same value-provider forms as argument `values`, including
`@enum`, `@lines`, `@glob`, `@go-modules`, `@go-packages`,
`@go-main-packages`, `@recipes`, `@vars`, command output, and recipe references. `pre` runs once before the loop;
`post` runs once after it; the first failing item stops later items. `workdir`
can also be used without `for_each`; it must resolve to a relative workspace path.

Recipe logging opens/truncates one file before commands start. `log` paths must
be relative and stay under the active config directory, or the source checkout
when no config path exists. `cmd` logging includes each main `for_each` item but
not the value-provider command. `post` output is still logged after a failing
`pre` or `cmd`. A selected parent stage captures nested recipe output; nested
recipe `log` settings do not open another log during the reference.

Completion criterion: placeholders have a value at resolve time, or `--print`/run will fail with a missing value error.

## Typed Arguments

Use fields under `[recipes.<name>.arguments.<arg>]`:

- `help`: shown by `help <recipe>` and completion.
- `type`: `string`, `int`, `float`, `bool`, `path`, `rel_path`, `duration`, or `duration:seconds`; default is `string`.
- `path_kind`: optional completion filter for `path` and `rel_path`; `any`, `file`, `dir`, or `executable`; default is `any`.
- `position`: 1-based positional slot.
- `required`: true when user must supply the argument.
- `default`: string, integer, number, or boolean default; converted to string then type-validated.
- `min` / `max`: optional inclusive bounds for `int`, `float`, `duration`, and `duration:seconds`; duration bounds use Go duration strings.
- `values`: shell string command that prints completion/help candidates, one per line as `value` or `value<TAB>help`; TOML arrays are invalid here, including `values = []`.
  Use argument-values builtins for common static/contextual sources: `@enum a b "c d"`, `@enum api='API service'`, `@lines config/targets.txt`, `@glob "cmd/*"`, `@go-modules`, `@go-packages`, `@go-main-packages`, `@recipes`, and `@vars`.
  `@enum` attaches help for `value=help text` entries when the help side contains whitespace; quote the help side, for example `@enum all='all modules'`. Single-token values such as `GOOS=linux` remain literal values.
  `@go-modules` returns directories containing `go.mod`, using `.` for the config-directory module and module paths as help. `@go-packages` runs `go list` in the config-directory module and, when `go.work` is present, in modules listed by the workspace. It returns package arguments such as `./internal/recipe`, with import paths as help. `@go-main-packages` returns package arguments for directories containing non-test Go files with `package main`, using package comments as help when available. Go discovery builtins are filesystem-backed and skip common generated/vendor directories where applicable.
  A scalar value that is exactly `@recipe` or `@path:recipe` invokes that recipe directly. Pass args with bracket syntax, for example `values = "@targets[kind=go]"`.
  Other scalar `values` commands run once through the configured recipe shell, for example `values = "printf 'api\tAPI service\n'"`.
  Builtin `values` can concatenate multiple simple builtin commands with semicolons without running a shell, for example `values = "@go-modules; @enum all='all modules'"`.

Call forms:

```sh
shadowtree build ./cmd/tool
shadowtree build project=./cmd/tool
shadowtree 'build[project=./cmd/tool,binary=tool-dev]'
```

Resolution rules:

- With no typed `arguments`, recipe CLI args are accepted only when `cmd` includes `{@}`.
- With typed `arguments`, defaults load first; selected recipe profile defaults apply next; `key=value` args set named values; positional tokens fill arguments by increasing `position`; leftover args are forwarded only when `cmd` includes `{@}`.
- Recipe profiles live under `[recipes.<name>.profiles.<profile>.arguments]`. Select with `profile=<profile>` after the recipe name. Profile argument keys must match typed arguments, values use the same scalar conversion and validation as `default`, and explicit CLI args override profile values.
- Unknown named args, unexpected positional args, missing required args, and invalid typed values fail.
- Use `--` after typed recipe arguments to pass following argv literally to `{@}`, especially option values that contain `=` such as `-- --cookie NAME=value`.
- Bool completion suggests `true` and `false`.
- `path` accepts absolute paths, relative paths, and `~/`; `rel_path` rejects absolute and `~` paths.
- `duration` accepts Go duration strings such as `10s`, `1500ms`, and `1m30s`; `duration:seconds` accepts exact whole-second Go durations and expands them as base-10 integer seconds.
- `min` and `max` validate defaults, profile argument values, explicit CLI args, and recipe-reference args for rangeable argument types.
- Path completion lists filesystem candidates. `path_kind=file` and `path_kind=executable` still include directories as traversal candidates; `path_kind=dir` lists directories only.
- Command-backed scalar `values` for help/completion run with top-level `env` overlaid by recipe `env` and use the configured recipe shell; output help text is split on a tab. LSP completion and diagnostics do not run command-backed `values`; use builtins such as `@enum`, `@glob`, `@lines`, `@recipes`, `@vars`, `@go-modules`, `@go-packages`, and `@go-main-packages` for editor-safe completions.

Completion criterion: typed args are used when the recipe needs named, validated, defaulted, positional, or completable values.

## Sync Out

Use sync-out only when sandbox outputs should persist.

Command-level:

```sh
shadowtree --sync-out generated/file.go generate
shadowtree --sync-out dist --sync-out schema.json build-assets
shadowtree --sync-out-all <recipe>
```

Recipe-level:

```toml
[recipes.generate]
cmd = "go generate ./..."
sync_out = ["internal/generated"]
```

Rules:

- `--sync-out` may repeat or contain comma-separated paths.
- Top-level `sync_out`, recipe `sync_out`, and command-level `--sync-out` combine for sandboxed runs.
- Missing selected paths in the sandbox are mirrored as deletions on the host.
- Sync-out paths must stay under the workspace.
- Prefer narrow paths over `--sync-out-all`.

Completion criterion: sync-out paths are narrow and deletion semantics are intentional.

## Go Profile

When profile is `go`, built-ins are:

```text
build      for each @go-modules: go build ./...
check      @vet && @test
fix        for each @go-modules when go > 1.26: go fix ./...
fmt        for each @go-modules: go fmt ./...
generate   for each @go-modules: go generate ./...
lint       for each @go-modules: golangci-lint run ./... or go vet ./...
run        go -C {cwd} run {command}
test       for each @go-modules: go test ./...
test-race  for each @go-modules: go test -race ./...
tidy       for each @go-modules: go mod tidy; if go.work exists, go work sync
vet        for each @go-modules: go vet ./...
```

`fix`, `fmt`, and `tidy` are unsandboxed by default. Built-in `tidy` runs `go work sync` after module tidying when `go.work` exists. Module-wide Go built-ins use `for_each = "@go-modules"` and `workdir = "{item}"`; the `./...` package pattern is evaluated inside each module directory, not at the repo root. Built-in `build` exposes an optional positional `pkg` argument with shell completion from `@go-main-packages`; other package-style Go built-ins expose `pkg` completion from `@go-packages`; `fix` is available when the most common `go.mod` directive is greater than `1.26`; `fmt` exposes an optional positional `target` from `@go-packages` plus `@glob "*.go"`. `run` has a named `cwd` argument defaulting to `.` plus a required positional `command` argument with `rel_path` type; `cwd` completes from `@go-modules`, and `command` completes from `@go-main-packages` plus `@glob "*.go"`. `command` is interpreted by `go run` after `go -C {cwd}`, so non-default `cwd` values use paths relative to that directory. Project config can override built-in recipe fields. Partial overrides preserve unspecified fields except built-in `for_each` and `workdir`, which must be set explicitly when custom behavior should keep module fan-out.

Completion criterion: use `shadowtree recipes` or `shadowtree --print <recipe>` to confirm the built-in or overridden behavior before relying on it.

## Node Profile

When profile is `node`, Shadowtree loads the nearest upward `package.json`, detects the package manager from `packageManager` then lockfiles then `npm`, and runs generated shell commands from that package directory. Node built-ins are unsandboxed by default.

Built-ins include `install`, script/framework-aware `dev`/`build`/`start`, script/tool-aware `test`/`lint`/`fmt`/`typecheck`, and `check` composed from available `lint`, `typecheck`, and `test` recipes. Package scripts fill gaps after normalizing names such as `lint:fix` to `lint-fix`; the generated command still runs the original script key.

Completion criterion: inspect `shadowtree --print <recipe>` before Node recipes that install dependencies, update lockfiles, build assets, or run framework commands.

## Completion

Bash, fish, and zsh completion are implemented:

```sh
command -v shadowtree >/dev/null 2>&1 && eval "$(shadowtree completion bash)"
```

The repository `install` recipe uses default `go install`, generates completion from `shadowtree` on `PATH`, installs fish completion when `fish` is available, and appends one guarded completion eval line to `~/.bashrc` and `~/.zshrc` when those shells are available.

```sh
shadowtree completion fish > ~/.config/fish/completions/shadowtree.fish
```

```sh
command -v shadowtree >/dev/null 2>&1 && eval "$(shadowtree completion zsh)"
```

Completion includes commands, resolved recipes, `--profile go` and `--profile node`, typed argument names, bool values, path/rel_path filesystem candidates, dynamic `values` output, and argument-values builtins including Go module/package/main-package discovery. Completion reads `--config` and `--profile` when they appear before the command.

Completion criterion: after config changes affecting recipes or arguments, regenerate or re-source shell completion before testing interactive behavior.
