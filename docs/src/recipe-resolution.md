# Recipe Resolution

Shadowtree resolves a recipe by combining built-ins, config, flags, and recipe
arguments.

Resolution order:

```text
built-in recipes for the selected profile, or for a detected profile when no
config is loaded
then config recipe overrides
then CLI flags
then trailing recipe args
```

## Profile Built-Ins

Built-in recipes come from the selected [Go Profile](go-profile.md) or
[Node Profile](node-profile.md). Profile selection is described in
[Profile Selection](built-in-profiles.md).

Configs that omit `profile` do not receive detected built-ins. This keeps local
config recipes exact unless the config opts into a profile.

## Overriding Built-Ins

Config recipes with the same name as a built-in recipe override only specified
fields, except `for_each` and `workdir`. Those scheduling fields are not
inherited. A project override also does not inherit the built-in recipe's
profile-owned `--all` plan unless it sets `all = true`; otherwise aggregate use
fails before execution.

```toml
profile = "go"

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

The Go built-in `test` normally runs once from the recipe workspace:

```text
cmd = "go test ./... {@}"
```

`shadowtree --all test` selects the separate package aggregate plan. It
discovers modules after `pre` and runs `go test ./...` from each module.

The override above runs once from the root workdir and parses CLI args as typed
arguments:

```sh
shadowtree test ./internal/recipe
```

That run executes:

```sh
go generate ./internal/recipe
go test ./internal/recipe
```

Use `{@}` when a typed recipe should forward leftover CLI args after typed
argument values.

## Keeping the `--all` Plan

Set `all = true` on an override to keep the built-in `--all` plan. The
override's `pre` runs once, `cmd` runs once per discovered target from the
target's module, and `post` runs once. In `cmd`, the built-in target argument,
such as `{pkg}`, is bound to each target; elsewhere it keeps its default,
because `--all` takes no explicit target.

```toml
profile = "go"

[recipes.test]
all = true
pre = ['go generate "{pkg}"']
cmd = 'go test -count=1 "{pkg}" {@}'
```

`shadowtree test ./internal/recipe` tests one package. `shadowtree --all test`
runs `go generate ./...` once, then `go test -count=1 ./...` from each module.

`all = true` cannot be combined with `for_each` or `workdir`, and it is
rejected where there is no plan to keep: on a recipe that overrides no profile
recipe, on a profile recipe that rejects `--all`, and on a profile recipe whose
plan rewrites its own command, such as the Rust built-ins.
