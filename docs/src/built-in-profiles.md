# Profile Selection

Profiles provide built-in recipes. Supported profiles are `go` and `node`.

Profile selection precedence is:

1. explicit `--profile`
2. config `profile`
3. marker detection only when no config is loaded

```sh
shadowtree --profile go recipes
shadowtree --profile node --print build
```

```toml
profile = "go"
```

## Marker Detection

When no config file is loaded, Shadowtree walks upward from the current
directory and compares the nearest profile markers:

- `package.json` selects `node`.
- `go.mod` or `go.work` selects `go`.
- If Go and Node markers are in the same directory, Go wins.

Configs that omit `profile` suppress detected built-ins. This preserves exact
configured recipe sets unless a config opts into a profile.

## Inspecting Profiles

Use inspection commands to see exact built-ins for the current checkout:

```sh
shadowtree recipes
shadowtree --print test
shadowtree --print --expanded test
shadowtree --check --shell test
```

Profile-specific behavior:

- [Go Profile](go-profile.md)
- [Node Profile](node-profile.md)
