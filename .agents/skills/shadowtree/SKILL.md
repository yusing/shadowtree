---
name: shadowtree
description: Use and configure the Shadowtree development recipe runner. Use when the user asks to run project recipes through shadowtree, isolate tests/builds/lints/codegen in a disposable workspace, inspect or write .shadowtree.toml config, initialize config, use sync-out safely, print resolved recipe plans, install completions, or explain sandboxed versus unsandboxed recipe behavior.
---

# Shadowtree

Use Shadowtree for named project recipes with isolated writes by default. Read live state first; do not guess recipe names, commands, profile, sandbox mode, or sync-out.

## Evidence First

Inspect before acting:

```sh
shadowtree config
shadowtree recipes
shadowtree help <recipe>
shadowtree --print <recipe> [args...]
shadowtree --print --expanded <recipe> [args...]
shadowtree --check <recipe> [args...]
shadowtree --check --shell <recipe> [args...]
```

Create starter config only when config should exist:

```sh
shadowtree init
shadowtree init path/to/shadowtree.toml
```

Global flags go before command/recipe name; later flags are recipe args:

```sh
shadowtree --verbose test ./...
shadowtree test -v ./...
```

Completion criterion: chosen command comes from Shadowtree output or existing config.

## Commands

- `shadowtree recipes`: list resolved recipes.
- `shadowtree help`: CLI help plus resolved recipes.
- `shadowtree help <recipe> [color=false]`: recipe summary, args, configured values, stages, for_each, workdir, unsandboxed marker, sync-out; color default on.
- `shadowtree config`: config path, profile, resolved recipe list.
- `shadowtree init [path]`: create `.shadowtree.toml` or path; fail if exists.
- `shadowtree completion bash|fish|zsh`: emit shell completion.
- `shadowtree exec -- <cmd> [args...]`: sandboxed ad hoc recipe.
- `shadowtree <recipe> [args...]`: run resolved recipe.

Use `--print` before writes, deletes, installs, publishes, regeneration, sync-out, or unfamiliar args. It prints resolved plan without running commands or validating nested references. Use `--print --expanded` for expanded `pre`, `cmd`, `post`, `for_each`, config path, profile, sandbox, workdir, sync-out, logging, typed args, preset, values, computed vars, recipe env.

Use `--check <recipe> [args...]` to validate resolved command forms, nested `@recipe` / `@path:recipe`, cycles, workdir, and log paths without running. It skips `requires` host tool availability; execution checks that before sandbox setup. Add `--shell` to parse expanded sh/bash after placeholder expansion and shell prelude insertion.

Use `--verbose` to see workspace and phase commands during execution.

Completion criterion: risky/unclear execution inspected with `--print` first.

## Config Discovery

Discovery walks upward from cwd to the outer Git repository boundary or
filesystem root. Registered submodules continue into their superprojects when
they contain no nearer config. Independent nested repositories and linked
worktrees remain boundaries:

```text
.shadowtree.toml
```

`--config PATH` bypasses discovery. Profiles: `go`, `node`. With no config, nearest upward marker picks profile: `package.json` -> `node`; `go.mod` or `go.work` -> `go`; same-directory tie -> `go`. Config without `profile` suppresses detected built-ins.

Completion criterion: config path and profile known before editing or explaining behavior.

## Sandbox Contract

Recipes are sandboxed unless `sandboxed = false`.

- Sandboxed: commands run in temp workspace; host checkout changes only through sync-out after success.
- Linux: prefer namespace overlayfs; commands run at workspace path inside namespace.
- Fallback: if overlayfs unavailable, warn and copy workspace.
- Workspace prep skips `.git`, `.shadowtree`, and `.shadowtree.*`.
- Unsandboxed: commands run directly in host checkout.

Unsandboxed recipes ignore `sync_out`, `--sync-out`, and `--sync-out-all`; no sandbox exists to copy from.

Completion criterion: persistence expectations match sandbox mode.

## Execution Order

For recipe:

1. `pre` commands run in order.
2. If all `pre` commands succeed, resolved `cmd` runs once, or once per `for_each` value.
3. `post` commands run even after `pre` or main failure, including cancellation such as SIGINT.
4. First pre/main error wins unless only `post` failed.
5. Sandboxed sync-out runs only after all recipe commands succeed.

Command env: `os.Environ()` overlaid by top-level `env`, then recipe `env`.

Scalar command exactly `@recipe` invokes another resolved Shadowtree recipe in current workspace. `@path:recipe` loads `path/.shadowtree.toml` relative to referencing config dir and runs target from that path. Recipe references do not spawn another `shadowtree`, start nested sandbox, or run referenced sync-out. Pass args with brackets, e.g. `@build[mode=dev]`. Cycles fail.

In `sh` and `bash` script commands (`cmd`, `pre`, `post`, scalar `for_each`, argument `values`, `shell_prelude`), literal command-position `@recipe` dispatches directly, including conditionals. `if @generate; then ...; fi` dispatches `generate`; `@build mode=dev` passes recipe CLI arg. Assignments/variables do not dispatch: `FOO="@build"` is assignment, `$FOO` uses normal shell lookup.

In `pre`/`post` lists, string exactly `@recipe` is recipe reference. Other strings remain shell scripts; literal command-position `@recipe` inside them also dispatches, e.g. `post = ["if @check; then @publish; fi"]`.

Structured `pre`/`post` tables support one stage command plus controls:

```toml
[recipes.benchmark.pre]
cmd = "benchmark_prepare"
timeout = "120s"
```

`timeout` is positive Go duration. Timeout follows normal stage order: failed `pre` skips `cmd`; `post` still runs.

`@retry` wraps flaky readiness checks in sh/bash command position: `pre = "@retry[count=30,delay=1s] benchmark_prepare"`. Defaults: `count=3`, `delay=1s`. Wraps external commands, shell functions, or literal recipe references; it composes with normal shell `&&`/`||` operators. Under `set -e`, retried shell functions must return failures explicitly, e.g. `cleanup_step || return $?`, because shells suppress `errexit` while command status is tested.

Completion criterion: failure cleanup/reporting belongs in `post`; generated outputs copy back only on success.

## Top-Level Fields

Config root fields:

- `include`: TOML configs merged before this config; paths relative to including config.
- `profile`: `go` or `node`.
- `shell`: script shell; default `sh`.
- `shell_prelude`: shell code before every script command.
- `env`: env for recipe commands and top-level `var_commands`.
- `vars`: static placeholders as `{NAME}`.
- `var_commands`: workspace commands producing placeholder values.
- `recipes`: named recipe map.

Includes are global mixins. Later includes override earlier; including file overrides all. Included recipes appear in help, `recipes`, completion, LSP. Top-level `vars`, `var_commands`, `env`, `profile`, `shell`, `shell_prelude` merge; included preludes run before including prelude.

Valid `vars` / `var_commands` keys: `[A-Za-z_][A-Za-z0-9_]*`.

Completion criterion: top-level settings affect multiple recipes or intentional shared defaults.

## Recipe Fields

Fields under `[recipes.<name>]`:

- `help`: short text for `help`, `recipes`, shell completion.
- `cmd`: required main command; prefer shell string.
- `for_each`: optional value-provider command; runs `cmd` once per candidate.
- `workdir`: optional relative main-command cwd; expands per `for_each` item.
- `pre`: command list before `cmd`, or structured table with `cmd` and optional `timeout`.
- `post`: command list after `cmd`, or structured table with `cmd` and optional `timeout`.
- `sandboxed`: bool; default `true`.
- `sync_out`: sandboxed paths copied back after successful completion.
- `log`: optional recipe log path; supports placeholders including `{run_id}`.
- `log_stages`: stages logged to `log`: `pre`, `cmd`, `post`; omitted means all; selected commands get compact `== stage: command ==` boundaries.
- `log_tee`: bool; default `true`; `false` sends selected stage output only to log.
- `requires`: recipe-local tool checks before sandbox setup and `pre`.
- `env`: recipe env overrides.
- `vars`: recipe placeholder values overriding top-level `vars`.
- `shell`: recipe shell override.
- `shell_prelude`: recipe shell code appended after top-level prelude.
- `arguments`: typed argument definitions.
- `presets`: recipe-local argument default sets selected by `preset=<name>`.

Reserved recipe names: `recipes`, `init`, `config`, `exec`, `completion`, `enum`, `glob`, `go-main-packages`, `go-modules`, `go-packages`, `help`, `lines`, `retry`, `vars`, `version`, `__complete`, plus future built-in `@` command identifiers. `run` is valid; use `shadowtree exec -- <cmd> [args...]` for explicit-command form.

Completion criterion: each recipe has `help` and `cmd`; use typed `arguments` plus placeholders in `cmd` instead of extra argument lists.

## Tool Requirements

Use `[recipes.<name>.requires]` for tools needed before commands run:

```toml
[recipes.benchmark.requires]
commands = ["docker", "openssl", "go"]
optional_commands = ["h2load"]
go_commands = { stringer = "golang.org/x/tools/cmd/stringer@latest" }
node_commands = { eslint = "eslint@^9" }
```

`commands`, `go_commands`, `node_commands` are required checks. `commands` and `optional_commands` use executable basenames, not paths. Missing required tools fail before sandbox setup and `pre`. `optional_commands` warns once and continues. Shadowtree does not install tools. Missing Go tools show `go install <module>@<version>`; missing Node tools suggest global install with detected `npm`/`pnpm`/`yarn`/`bun`, e.g. `pnpm add --global eslint@^9`. `--print`/`--check` show/validate plan without host tool checks; execution checks requirements before sandbox setup.

Requirement names are static, not placeholders. Included/overriding recipes replace inherited `requires` block as a whole.

Completion criterion: use `shadowtree --print --expanded <recipe>` before host-tool-dependent recipes; use `shadowtree --check --shell <recipe>` for references and expanded shell syntax.

## Command Forms

Use shell strings for recipe commands:

```toml
[recipes.test]
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
required = true
```

Command strings run through configured shell after placeholder expansion. String exactly `@recipe` or `@path:recipe` invokes recipe; other strings run in shell. Put typed `arguments` in placeholders for validated inputs. Unquoted `{arg}` is raw shell text. Usually quote free string/path args, e.g. `foo "{bar}"`. Use `{arg:shell}` only inside unquoted shell words like `foo -xxx{bar:shell}`; use `{arg:raw}` only for intentional raw shell syntax or word splitting.

Use recipe references from `cmd`, `pre`, `post`, or argument `values`:

```toml
[recipes.test]
pre = ["@generate"]
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
required = true
```

Pass referenced-recipe args with brackets, e.g. `@build-api[service=public]`. `@webui:gen-schema` invokes `gen-schema` from `webui/.shadowtree.toml`; relative paths resolve from referencing config dir and execution starts in `webui/`. Use `@{NAME}` only when recipe name must come from placeholder; editor diagnostics/completion work best with static `@recipe`. Bracket args:

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

Scripts run as `<shell> -c <script> shadowtree`; `$0` is `shadowtree`. `shell_prelude` joins before scripts. Empty commands/scripts invalid. In `sh`/`bash` scripts, including `shell_prelude`, literal command-position `@recipe` invokes recipe directly:

```toml
[recipes.test]
cmd = '''
if [ -f schema.json ]; then
	@generate mode=dev
fi
'''
```

Completion criterion: command examples use scalar shell strings. Do not write TOML argv arrays for command fields; invalid config.

Editor support: script-valued `cmd`, `pre`, `post`, `for_each`, `shell_prelude`, and scalar argument `values` can get shell highlighting. Command-position `@recipe` in `sh`/`bash` strings gets the same recipe-reference completion/diagnostics as scalar `values` references.

## Placeholders And Vars

`{NAME}` expands in `cmd`, `pre`, `post`, `for_each`, `shell_prelude`, `workdir`, `sync_out`, and `log`.

Value sources:

1. top-level `vars`;
2. top-level `var_commands` output, trimmed;
3. recipe `vars`, overriding top-level values;
4. typed argument values, overriding vars with same name.

Shell parameter expansion preserved: `${NAME}` is not Shadowtree placeholder.

`{run_id}` is built in: one lowercase hex run ID per top-level invocation, reused through `pre`, `cmd`, `post`, `for_each`, nested `@recipe`. `run_id` reserved; cannot be declared in top-level `vars`, top-level `var_commands`, recipe `vars`, or recipe `arguments`.

`cmd` may use `{status:pre}`; `post` may use `{status:pre}` and `{status:cmd}`. Values: `0` success, failing exit code when available, `1` for non-exit failures like timeouts, empty when stage did not run.

`var_commands` use top-level `env`, configured `shell`, top-level `shell_prelude`; not evaluated during shell completion.

Fan-out placeholders exist only with `for_each`:

- `{item}`: current candidate value.
- `{item_help}`: current candidate help text, if any.
- `{item_index}`: zero-based candidate index.

`for_each` accepts same value-provider forms as argument `values`: `@enum`, `@lines`, `@glob`, `@go-modules`, `@go-packages`, `@go-main-packages`, `@recipes`, `@vars`, command output, recipe references. `pre` runs once before loop; `post` once after; first failing item stops later items. `workdir` also works without `for_each`; must resolve to relative workspace path.

Recipe logging opens/truncates one file before commands start. `log` paths must be relative and stay under active config dir, or workspace root when no config path. `cmd` logging includes each main `for_each` item but not value-provider command. `post` output still logged after failing `pre`/`cmd`. Selected parent stage captures nested recipe output; nested recipe `log` does not open another log during reference.

Completion criterion: placeholders have resolve-time value, or `--print`/run fails missing value.

## Typed Arguments

Fields under `[recipes.<name>.arguments.<arg>]`:

- `help`: shown by `help <recipe>` and completion.
- `type`: `string`, `int`, `float`, `bool`, `path`, `rel_path`, `duration`, or `duration:seconds`; default `string`.
- `path_kind`: completion filter for `path` / `rel_path`: `any`, `file`, `dir`, `executable`; default `any`.
- `position`: 1-based positional slot.
- `required`: user must supply arg.
- `default`: string, integer, number, or boolean; converted to string then type-validated.
- `min` / `max`: inclusive bounds for `int`, `float`, `duration`, `duration:seconds`; duration bounds use Go duration strings.
- `values`: shell string printing completion/help candidates, one per line as `value` or `value<TAB>help`; TOML arrays invalid, including `values = []`.
  Use builtins for static/contextual values: `@enum a b "c d"`, `@enum api='API service'`, `@enum_set service`, `@lines config/targets.txt`, `@glob "cmd/*"`, `@go-modules`, `@go-packages`, `@go-main-packages`, `@recipes`, `@vars`. Define reusable sets with `[enum_sets] service = "@enum ..."`; sets work in `values` and `for_each`.
  `@enum` attaches help for `value=help text` when help side has whitespace; quote help side, e.g. `@enum all='all modules'`. Single-token values like `GOOS=linux` stay literal.
  `@go-modules` returns dirs containing `go.mod`, using `.` for config-dir module and module paths as help. `@go-packages` runs `go list` in config-dir module and, with `go.work`, workspace modules; returns package args like `./internal/recipe` with import paths as help. `@go-main-packages` returns dirs containing non-test Go files with `package main`, using package comments as help when available. Go builtins are filesystem-backed and skip common generated/vendor dirs where applicable.
  Scalar value exactly `@recipe` or `@path:recipe` invokes recipe directly. Pass args with brackets, e.g. `values = "@targets[kind=go]"`.
  Other scalar `values` commands run once through configured recipe shell, e.g. `values = "printf 'api\tAPI service\n'"`.
  Builtin `values` can concatenate simple builtins with semicolons without shell, e.g. `values = "@go-modules; @enum all='all modules'"`.

Call forms:

```sh
shadowtree build ./cmd/tool
shadowtree build project=./cmd/tool
shadowtree 'build[project=./cmd/tool,binary=tool-dev]'
```

Resolution rules:

- Without typed `arguments`, recipe CLI args accepted only when `cmd` includes `{@}`.
- With typed `arguments`: defaults load first; selected preset defaults next; `key=value` sets named values; positional tokens fill args by increasing `position`; leftovers forwarded only when `cmd` includes `{@}`.
- Recipe presets live under `[recipes.<name>.presets.<preset>.arguments]`. Select with `preset=<preset>` after recipe name. Preset keys must match typed args; values use same scalar conversion/validation as `default`; explicit CLI args override presets.
- Unknown named args, unexpected positional args, missing required args, invalid typed values fail.
- Use `--` after typed recipe args to pass following argv literally to `{@}`, especially option values containing `=`, e.g. `-- --cookie NAME=value`.
- Bool completion suggests `true` and `false`.
- `path` accepts absolute, relative, `~/`; `rel_path` rejects absolute and `~`.
- `duration` accepts Go duration strings like `10s`, `1500ms`, `1m30s`; `duration:seconds` accepts exact whole-second Go durations and expands as base-10 integer seconds.
- `min` / `max` validate defaults, preset values, explicit CLI args, recipe-reference args for rangeable types.
- Keep validation in Shadowtree's typed argument resolution, runtime validation, and LSP diagnostics. Do not put accepted-value, range, preset, or recipe-reference validation in shell scripts; shell bodies should consume already-validated placeholders.
- Path completion lists filesystem candidates. `path_kind=file` and `path_kind=executable` include dirs as traversal candidates; `path_kind=dir` lists dirs only.
- Command-backed scalar `values` for help/completion run with top-level `env` overlaid by recipe `env` and configured recipe shell; help text splits on tab. Editor completion/diagnostics do not run command-backed `values`; use editor-safe builtins: `@enum`, `@glob`, `@lines`, `@recipes`, `@vars`, `@go-modules`, `@go-packages`, `@go-main-packages`.

Completion criterion: use typed args when recipe needs named, validated, defaulted, positional, or completable values.

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
- Recipe `sync_out` and command-level `--sync-out` combine for sandboxed runs.
- Missing selected paths in sandbox mirror as host deletions.
- Sync-out paths must stay under workspace.
- Prefer narrow paths over `--sync-out-all`.

Completion criterion: sync-out paths narrow; deletion semantics intentional.

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

`fix`, `fmt`, `tidy` unsandboxed by default. Module-wide Go built-ins use `for_each = "@go-modules"` and `workdir = "{item}"`; `./...` evaluates inside each module, not workspace root. `tidy` also runs `go work sync` when `go.work` exists. `build` exposes optional positional `pkg` from `@go-main-packages`; other package-style built-ins expose `pkg` from `@go-packages`; `fix` available when common `go.mod` directive > `1.26`; `fmt` exposes optional positional `target` from `@go-packages` plus `@glob "*.go"`. `run` has `cwd` default `.` plus required positional `command` with `rel_path`; `cwd` completes from `@go-modules`; `command` from `@go-main-packages` plus `@glob "*.go"`. Config can override built-in fields; set built-in `for_each` and `workdir` explicitly to keep module fan-out.

Completion criterion: confirm built-in/override with `shadowtree recipes` or `shadowtree --print <recipe>` before relying on it.

## Node Profile

When profile is `node`, Shadowtree loads nearest upward `package.json`, detects package manager from `packageManager`, then lockfiles, then `npm`, and runs generated shell commands from that package dir. Node built-ins unsandboxed by default.

Built-ins: `install`, script/framework-aware `dev`/`build`/`start`, script/tool-aware `test`/`lint`/`fmt`/`typecheck`, and `check` from available `lint`, `typecheck`, `test`. Package scripts fill gaps after normalizing names such as `lint:fix` to `lint-fix`; generated command still runs original script key.

Completion criterion: inspect `shadowtree --print <recipe>` before Node recipes that install deps, update lockfiles, build assets, or run framework commands.

## Completion

Bash, fish, zsh completion implemented:

```sh
command -v shadowtree >/dev/null 2>&1 && eval "$(shadowtree completion bash)"
```

```sh
shadowtree completion fish > ~/.config/fish/completions/shadowtree.fish
```

```sh
command -v shadowtree >/dev/null 2>&1 && eval "$(shadowtree completion zsh)"
```

Completion includes commands, resolved recipes, `--profile go` / `--profile node`, typed arg names, bool values, path/rel_path filesystem candidates, dynamic `values` output, and argument-values builtins including Go module/package/main-package discovery. Completion reads `--config` and `--profile` when they appear before command.

Completion criterion: after config changes affecting recipes/args, regenerate or re-source shell completion before interactive testing.
