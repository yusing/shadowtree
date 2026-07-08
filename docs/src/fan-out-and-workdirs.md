# Fan-Out and Workdirs

`for_each` runs one recipe main command once per value candidate.

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

See [Value Providers](value-providers.md) for provider details.

## Item Placeholders

Per iteration, these placeholders are available to `cmd` and `workdir`:

- `{item}`: candidate value.
- `{item_help}`: candidate help text, or empty string.
- `{item_index}`: zero-based item index.

## Workdir

`workdir` can be used with or without `for_each`. It makes the main command run
from a relative path under the recipe workspace.

With `for_each`, `workdir` is expanded for each item:

```toml
for_each = "@go-modules"
workdir = "{item}"
```

## Execution Order

`pre` commands run once before candidate resolution. `post` commands run once
after the loop, even if `pre`, candidate resolution, or an item command fails.
Items run sequentially; the first failing item stops later items.

For sandboxed recipes, `sync_out` runs once after all items and `post` commands
succeed. `sync_out` does not accept `{item}` placeholders.
