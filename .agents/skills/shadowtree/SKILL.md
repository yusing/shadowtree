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
- `shadowtree help <recipe>`: show command summary, args, values, pre/post, sandbox marker, and sync-out for one recipe.
- `shadowtree config`: print config path, profile, and resolved recipe list.
- `shadowtree init [path]`: create `.shadowtree.toml` or the given path; fails if the file exists.
- `shadowtree completion fish`: emit fish completion script.
- `shadowtree run -- <cmd> [args...]`: run an explicit command as a sandboxed ad hoc recipe.
- `shadowtree <recipe> [args...]`: run a resolved recipe.

Use `--print` before commands that may write, delete, install, publish, regenerate, sync out, or use unfamiliar args. Use `--verbose` to see workspace and phase commands.

Completion criterion: risky or unclear execution is inspected with `--print` before it runs.

## Config Discovery

Discovery walks upward from the current directory until the git root or filesystem root:

```text
.shadowtree.toml
```

An explicit `--config PATH` bypasses discovery. Only `go` is a supported profile. If no profile is configured, Shadowtree detects `go` from `go.mod` or `go.work`.

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
2. If all `pre` commands succeed, `cmd + args + selected_args` runs.
3. `post` commands run even when a `pre` or main command failed.
4. The first pre/main error wins unless only `post` failed.
5. Sync-out runs only after all recipe commands finish successfully.

Command env is `os.Environ()` overlaid by top-level `env`, then recipe `env`.

Completion criterion: cleanup or reporting that must run after failure belongs in `post`; generated outputs are copied back only on success.

## Top-Level Fields

Use these fields at config root:

- `profile`: currently `go`.
- `shell`: shell for script commands; defaults to `sh`.
- `shell_prelude`: shell code prepended to every script command and every `["sh", "-c", "..."]` command.
- `sync_out`: sandboxed paths copied back after successful runs for every recipe unless unsandboxed.
- `env`: environment variables for recipe commands and top-level `var_commands`.
- `vars`: static placeholders usable as `{NAME}`.
- `var_commands`: commands evaluated from the source checkout to produce placeholder values.
- `recipes`: map of named recipes.

Valid `vars` and `var_commands` keys match `[A-Za-z_][A-Za-z0-9_]*`.

Completion criterion: every top-level setting affects more than one recipe or intentionally defines shared defaults.

## Recipe Fields

Use these fields under `[recipes.<name>]`:

- `help`: short text shown by `help`, `recipes`, and shell completion.
- `cmd`: required main command; argv array or script string.
- `args`: fixed args always appended after `cmd`.
- `default_args`: selected only when the user provides no recipe CLI args, or used as placeholder-bearing selected args when typed arguments exist.
- `pre`: list of commands before `cmd`.
- `post`: list of commands after `cmd`.
- `sandboxed`: boolean; defaults to `true`.
- `sync_out`: sandboxed paths copied back after successful recipe completion.
- `env`: recipe environment overrides.
- `vars`: recipe placeholder values overriding top-level `vars`.
- `shell`: recipe shell override.
- `shell_prelude`: recipe shell code appended after the top-level prelude.
- `arguments`: typed argument definitions.

Reserved recipe names: `run`, `recipes`, `init`, `config`, `completion`, `help`, `version`, `__complete`.

Completion criterion: each recipe has `help` and `cmd`, and uses `args` for fixed args versus `default_args` for caller-replaceable args.

## Command Forms

Use argv arrays for exact execution:

```toml
[recipes.test]
cmd = ["go", "test"]
default_args = ["./..."]
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

Script strings run as `<shell> -c <script> shadowtree`; `$0` is `shadowtree`. `shell_prelude` is joined before script bodies and before `sh -c` array commands. Empty commands and empty script bodies are invalid.

Completion criterion: argv arrays are used unless shell syntax is actually needed.

## Placeholders And Vars

`{NAME}` placeholders expand in `cmd`, `args`, `default_args`, `pre`, `post`, and `sync_out`.

Value sources:

1. top-level `vars`;
2. top-level `var_commands` output, trimmed;
3. recipe `vars`, overriding top-level values;
4. typed argument values, overriding vars with the same name.

Shell parameter expansion is preserved: `${NAME}` is not treated as a Shadowtree placeholder.

`var_commands` use top-level `env`, configured `shell`, and top-level `shell_prelude`. They are not evaluated during shell completion.

Completion criterion: placeholders have a value at resolve time, or `--print`/run will fail with a missing value error.

## Typed Arguments

Use fields under `[recipes.<name>.arguments.<arg>]`:

- `help`: shown by `help <recipe>` and completion.
- `type`: `string`, `int`, `float`, `bool`, `path`, or `rel_path`; default is `string`.
- `path_kind`: optional completion filter for `path` and `rel_path`; `any`, `file`, `dir`, or `executable`; default is `any`.
- `position`: 1-based positional slot.
- `required`: true when user must supply the argument.
- `default`: string, integer, number, or boolean default; converted to string then type-validated.
- `values`: command that prints completion/help candidates, one per line as `value` or `value<TAB>help`.

Call forms:

```sh
shadowtree build ./cmd/tool
shadowtree build project=./cmd/tool
shadowtree 'build[project=./cmd/tool,binary=tool-dev]'
```

Resolution rules:

- With no typed `arguments`, CLI args replace `default_args`.
- With typed `arguments`, defaults load first; `key=value` args set named values; positional tokens fill arguments by increasing `position`; `default_args` remain selected args and can contain placeholders.
- Unknown named args, unexpected positional args, missing required args, and invalid typed values fail.
- Bool completion suggests `true` and `false`.
- `path` accepts absolute paths, relative paths, and `~/`; `rel_path` rejects absolute and `~` paths.
- Path completion lists filesystem candidates. `path_kind=file` and `path_kind=executable` still include directories as traversal candidates; `path_kind=dir` lists directories only.
- `values` commands for help/completion run with top-level `env` overlaid by recipe `env`; output help text is split on a tab.

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
cmd = ["go", "generate", "./..."]
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
test       go test ./...
test-race  go test -race ./...
vet        go vet ./...
lint       golangci-lint run ./... or go vet ./...
build      go build ./...
generate   go generate ./...
tidy       go mod tidy
```

`tidy` is unsandboxed by default. Project config can override any built-in recipe field; partial overrides preserve unspecified built-in fields.

Completion criterion: use `shadowtree recipes` or `shadowtree --print <recipe>` to confirm the built-in or overridden behavior before relying on it.

## Completion

Only fish completion is implemented:

```sh
shadowtree completion fish > ~/.config/fish/completions/shadowtree.fish
```

Completion includes commands, resolved recipes, `--profile go`, typed argument names, bool values, path/rel_path filesystem candidates, and dynamic `values` output. Completion reads `--config` and `--profile` when they appear before the command.

Completion criterion: after config changes affecting recipes or arguments, regenerate or re-source fish completion before testing interactive behavior.
