# Presets

Recipe-local presets set multiple typed argument defaults at once.

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

Select a preset with `preset=<name>` after the recipe name:

```sh
shadowtree benchmark preset=stable runs=3
```

Explicit CLI arguments still win, so the example above uses `connections=64`,
`requests=20000`, and `runs=3`.

## Rules

Presets are declared under:

```toml
[recipes.<name>.presets.<preset-name>.arguments]
```

Preset names use identifier syntax:

```text
[A-Za-z_][A-Za-z0-9_]*
```

Preset argument keys must name typed arguments declared by the same recipe.
Preset values are converted and type-checked the same way as argument defaults.

A recipe with `presets` reserves the `preset` recipe argument name for preset
selection.

## Resolution Order

Argument values resolve in this order:

1. typed argument defaults
2. selected recipe preset defaults
3. explicit positional or `key=value` CLI arguments

The `preset=<name>` selector is consumed like a typed argument and is excluded
from `{@}`. Tokens after `--` are not preset selectors.
