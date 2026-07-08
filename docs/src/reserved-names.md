# Reserved Names

Some names are reserved and cannot be used as recipe names.

```text
recipes
init
config
exec
completion
enum
glob
go-main-packages
go-modules
go-packages
help
lines
retry
vars
version
__complete
```

The argument-values builtins are reserved:

- `enum`
- `glob`
- `go-main-packages`
- `go-modules`
- `go-packages`
- `lines`
- `recipes`
- `vars`

Command helpers such as `retry` are also reserved. Future built-in `@` command
identifiers are reserved as well.

`run` is a valid recipe name. Use `shadowtree exec -- <cmd> [args...]` for the
explicit-command form.
