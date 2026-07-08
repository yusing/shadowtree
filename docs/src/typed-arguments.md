# Typed Arguments

Typed arguments define validated recipe inputs under
`[recipes.<name>.arguments.<arg-name>]`.

```toml
[recipes.build]
cmd = 'go build -o "bin/{binary}" "{pkg}" {@}'

[recipes.build.arguments.pkg]
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

## Fields

- `help`: short help text shown by `shadowtree help <recipe>` and shell
  completion.
- `type`: argument type. Defaults to `string`.
- `path_kind`: completion filter for `path` and `rel_path` arguments.
- `position`: 1-based positional index.
- `required`: whether the user must supply a value. Defaults to `false`.
- `default`: default value, type-checked before use.
- `min`: inclusive lower bound for numeric and duration arguments.
- `max`: inclusive upper bound for numeric and duration arguments.
- `values`: command or builtin that produces completion candidates.

## Types

Supported `type` values:

- `string`
- `int`
- `float`
- `bool`
- `path`
- `rel_path`
- `duration`
- `duration:seconds`

`path` accepts absolute and relative paths. `rel_path` accepts relative paths
only and rejects absolute paths and `~` home paths.

`duration` accepts Go duration strings such as `10s`, `1500ms`, and `1m30s`.
`duration:seconds` accepts the same format only when it is an exact whole
number of seconds, and expands as base-10 integer seconds.

`path_kind` can be `any`, `file`, `dir`, or `executable`. The default is
`any`. `file` and `executable` still include directories as traversal
candidates.

## Passing Arguments

Arguments can be provided positionally:

```sh
shadowtree build ./cmd/shadowtree
```

Arguments can be provided by name:

```sh
shadowtree build pkg=./cmd/shadowtree binary=shadowtree-dev
```

Arguments can also be provided with bracket-style syntax:

```sh
shadowtree 'build[pkg=./cmd/shadowtree,binary=shadowtree-dev]'
```

Bracket-style syntax is especially useful for recipe references and shell
completion.

## Validation

Resolved argument values are type-checked, range-checked, and checked against
safely checkable `values` builtins before any recipe command runs.

Argument values are exposed through placeholders:

```toml
cmd = 'go test "{pkg}"'
```

Related topics:

- [Value Providers](value-providers.md)
- [Variadic Args](variadic-args.md)
- [Presets](presets.md)
