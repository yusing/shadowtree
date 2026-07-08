# Variadic Args

`{@}` forwards leftover recipe CLI args to the recipe `cmd`.

```toml
[recipes.test]
cmd = 'go test "{pkg}" {@}'

[recipes.test.arguments.pkg]
type = "rel_path"
position = 1
required = true
values = "@go-packages"
```

Run it with:

```sh
shadowtree test ./internal/recipe -run=TestResolve -count=1
```

The typed positional value supplies `{pkg}`. The leftover flags are spliced at
`{@}` as separate shell-quoted words.

## Rules

- `{@}` is supported only in `cmd`.
- In shell command strings, it must occupy a whole shell word.
- It is not available in `pre`, `post`, or `sync_out`.
- For typed recipes, positional values and known `key=value` values are
  consumed by typed arguments and excluded from `{@}`.
- Unknown identifier `key=value` tokens remain errors.
- Command flags such as `-run=TestName` pass through.

## Literal Pass-Through

Use `--` after typed recipe arguments to pass the following argv literally to
`{@}`, including option values that contain `=`:

```sh
shadowtree test pkg=./internal/recipe -- --flag NAME=value
```
