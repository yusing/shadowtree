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
profile-owned `--all` plan; unsupported aggregate use fails before execution.

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
