# Placeholders

Placeholders insert recipe arguments, vars, and built-in values into recipe
fields.

```toml
[recipes.build]
cmd = 'go build -o "bin/{binary}" "{pkg}" {@}'
```

## Shell Command Expansion

Default `{name}` expansion is raw text when unquoted, before the shell parses
the script. Inside single-quoted or double-quoted shell context, `{name}` is
escaped for that quote context:

```toml
cmd = 'printf "%s\n" "{name}"'
cmd = "printf '%s\n' '{name}'"
```

Both forms keep the placeholder value as one shell word even when the value
contains quote characters.

## Explicit Modes

- `{name:shell}` expands as one shell-escaped word and is valid only outside
  shell quotes. Use it when a value must be embedded in an unquoted shell word,
  such as `foo -xxx{name:shell}`.
- `{name:dq}` expands as double-quote-safe content and is valid only inside
  double quotes.
- `{name:raw}` expands raw text and documents intentional unsafe shell text or
  word splitting.

Prefer normal shell quotes around free string or path values, such as
`foo "{bar}"`.

## Non-Shell Fields

Fields such as `env`, `vars`, `workdir`, `sync_out`, and `log` use raw string
placeholder expansion. Only `{name}` and `{name:raw}` are valid in those
fields.

## Built-In Placeholders

`{run_id}` is generated once for each top-level invocation. It is a
filesystem-safe lowercase hex value reused through `pre`, `cmd`, `post`,
`for_each`, and nested recipe references.

`cmd` commands can use `{status:pre}`. `post` commands can use `{status:pre}`
and `{status:cmd}`. Status placeholders expand to:

- `0` on success.
- the failing exit code when available.
- `1` for non-exit failures such as timeouts.
- an empty string when that stage did not run.

For leftover CLI args, see [Variadic Args](variadic-args.md).
