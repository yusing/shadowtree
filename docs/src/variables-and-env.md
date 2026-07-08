# Variables and Environment

Shadowtree exposes reusable values through placeholders. Static values come
from `vars`; dynamic values come from `var_commands`.

```toml
[vars]
ldflags = "-buildvcs=false"

[var_commands]
git_sha = "git rev-parse --short HEAD"

[recipes.build]
cmd = 'go build -ldflags="{ldflags}" -o "bin/app-{git_sha}" ./cmd/app'
```

## Static Vars

Top-level `vars` are available to every recipe. Recipe-level `vars` override
top-level values for that recipe.

```toml
[vars]
mode = "dev"

[recipes.release.vars]
mode = "release"
```

Static vars may reference other static or dynamic vars with the same
`{name}` placeholder syntax.

## Dynamic Vars

`var_commands` run when recipes are resolved for execution, printing, or help.
They run from the source checkout. Surrounding whitespace is trimmed from
stdout and the result becomes a placeholder value.

```toml
[var_commands]
version = "git describe --tags --always"
```

Dynamic vars are useful for values such as versions, commit IDs, detected
paths, or generated labels that should be visible in `--print --expanded`.

## Environment

Top-level `env` applies to recipe commands and top-level `var_commands`.
Recipe-level `env` overrides top-level values for that recipe.

```toml
[env]
GOFLAGS = "-mod=readonly"

[recipes.test.env]
CGO_ENABLED = "0"
```

Environment values use raw placeholder expansion. Only `{name}` and
`{name:raw}` are valid there; shell-specific modes such as `{name:shell}` are
for shell command strings.

## Reserved Names

`run_id` is built in and cannot be declared in `vars`, `var_commands`, recipe
`vars`, or recipe `arguments`.
