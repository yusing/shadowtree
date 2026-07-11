# Value Providers

Value providers produce completion candidates for typed arguments and
`for_each`.

```toml
[recipes.test.arguments.pkg]
type = "rel_path"
values = "@go-packages"
```

Each candidate is a value, optionally followed by a tab and help text:

```text
api	API service
worker	Background worker
```

## Builtins

`@enum`
: Literal values from arguments.

```toml
values = '@enum api worker "admin ui"'
values = "@enum api='API service' worker='Worker service'"
```

`@enum_set`
: Values from a named top-level enum set. Definitions reuse `@enum` syntax and
may be shared by argument `values` and `for_each`.

```toml
[enum_sets]
service = "@enum api='API service' worker='Worker service'"

[recipes.deploy.arguments.target]
values = "@enum_set service"
```

Enum sets are merged with includes: the including config replaces a same-named
included set. Definitions must be one non-empty `@enum` command; enum sets do
not nest.

`@lines`
: Reads candidates from a text file using the same `value<TAB>help` format.

```toml
values = "@lines examples/all-features-values.txt"
```

`@glob`
: Returns filesystem matches.

```toml
values = '@glob "cmd/*"'
```

`@go-modules`
: Returns directories containing `go.mod`, with `.` representing the config
directory module and help from the module directive.

`@go-packages`
: Runs `go list` in the config-directory module and in `go.work` modules when
present. It returns package arguments such as `./internal/recipe`, with help
from import paths.

`@go-main-packages`
: Returns package arguments for directories containing non-test Go files with
`package main`, with help from package comments when available.

`@recipes`
: Returns resolved recipe names.

`@vars`
: Returns recipe placeholder and argument names.

## Composition

Multiple builtin value commands can be separated with `;`; their candidates are
concatenated without running a shell:

```toml
values = "@go-modules; @enum all='all modules'"
for_each = "@go-modules; @enum all='all modules'"
```

## Resolution Paths

Relative `@lines` paths, `@glob` patterns, and Go discovery walks resolve from
the config file directory when available.

## Validation

When `values` is safely checkable without running arbitrary commands or walking
the filesystem, explicit CLI and recipe-reference argument values must match
one of its candidates before Shadowtree runs the recipe.

Safely checkable examples include:

- `@enum`
- `@recipes`
- `@vars`

Filesystem discovery and command-backed `values` remain completion and help
providers and are not run as part of argument validation.
