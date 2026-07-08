# Configuration Files

Shadowtree config is TOML. By default, Shadowtree discovers the nearest
`.shadowtree.toml` by walking upward from the current directory.

Discovery stops at the git root when the current directory is inside a Git
repository. An explicit `--config PATH` bypasses discovery.

```sh
shadowtree --config ./ci.shadowtree.toml recipes
```

## Minimal Config

```toml
[recipes.test]
help = "Run tests."
cmd = "go test ./..."
```

Run it with:

```sh
shadowtree test
```

## Top-Level Fields

```toml
include = ["./common.shadowtree.toml"]
profile = "go"
shell = "sh"
shell_prelude = '''
shared_function() {
	echo ok
}
'''

[env]
KEY = "value"

[vars]
NAME = "static value"

[var_commands]
DYNAMIC_NAME = "printf dynamic"
```

Field summary:

- `include`: merge shared Shadowtree TOML files before the current config.
- `profile`: opt into built-in recipes for `go` or `node`.
- `shell`: script shell for shell command strings; supported values are `sh`
  and `bash`.
- `shell_prelude`: shell code prepended to every script command.
- `env`: environment values applied to recipe commands and top-level
  `var_commands`.
- `vars`: static placeholder values.
- `var_commands`: commands that compute placeholder values during recipe
  resolution.
- `recipes`: named workflow definitions.

## Recipe Tables

Recipes live under `[recipes.<name>]`:

```toml
[recipes.build]
help = "Build the CLI."
cmd = 'go build -o "bin/{binary}" "{pkg}" {@}'
sync_out = ["bin/{binary}"]

[recipes.build.arguments.binary]
type = "string"
default = "shadowtree"

[recipes.build.arguments.pkg]
type = "string"
default = "./cmd/shadowtree"
values = "@go-main-packages"
```

Use [Recipe Fields](recipes.md) for recipe field details and
[Typed Arguments](typed-arguments.md) for argument definitions.

## Related Topics

- [Includes](includes.md)
- [Variables and Environment](variables-and-env.md)
- [Shell Commands](shell-commands.md)
- [Placeholders](placeholders.md)
- [Recipe References](recipe-references.md)
- [Recipe Resolution](recipe-resolution.md)
