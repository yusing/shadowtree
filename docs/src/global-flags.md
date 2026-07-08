# Global Flags

Global flags are parsed before the command or recipe name.

```sh
shadowtree --profile go --print test
shadowtree --config ./ci.shadowtree.toml recipes
shadowtree --sync-out internal/generated generate
```

Arguments after the recipe name are passed to the recipe's main command or
parsed as typed recipe arguments.

## Flags

`--config PATH`
: Use an explicit config file instead of discovery.

`--profile PROFILE`
: Use a profile. Supported profiles are `go` and `node`.

`--sync-out PATH`
: Copy a path back after a successful sandboxed recipe. Repeat the flag or use
comma-separated paths.

`--sync-out-all`
: Copy the entire sandbox workspace back after success.

`--print`
: Print the resolved plan without running it.

`--expanded`
: With `--print`, include expanded scripts and resolved values.

`--check`
: Validate the resolved recipe without running commands.

`--shell`
: With `--check`, parse expanded shell scripts.

`--verbose`
: Show workspace information and compact command boundaries during execution.

`--help`
: Show basic CLI help.

`--version`
: Print the version.

## Position Matters

Put global flags before the command or recipe name:

```sh
shadowtree --verbose test ./...
```

Flags after the recipe name are recipe args:

```sh
shadowtree test -v ./...
```

Related topics:

- [Profile Selection](built-in-profiles.md)
- [Sandboxing and Sync-Out](sandboxing-and-sync-out.md)
- [CLI Inspection](cli-inspection.md)
