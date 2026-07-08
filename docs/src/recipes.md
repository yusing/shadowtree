# Recipe Fields

Recipes define named project workflows under `[recipes.<name>]`.

```toml
[recipes.generate]
help = "Regenerate checked-in code."
pre = ["@clean-generated"]
cmd = "go generate ./..."
post = ["git diff --stat"]
sync_out = ["internal/generated"]
```

## Core Fields

- `help`: short text shown by `shadowtree recipes`, `shadowtree help`, and
  shell completion.
- `cmd`: required main command. Use a shell command string or a recipe
  reference such as `@build`.
- `pre`: commands that run before the main command.
- `post`: commands that run after `pre` or the main command, including after
  failures.
- `sandboxed`: `true` by default. Set `false` only for recipes that should edit
  the host checkout directly.
- `sync_out`: sandboxed paths copied back to the host checkout after success.
- `for_each`: value provider that runs the main command once per candidate.
- `workdir`: relative working directory for the main command.
- `log`, `log_stages`, `log_tee`: recipe log output.
- `requires`: host tool checks performed before sandbox setup and `pre`.
- `env`: recipe-specific environment overrides.
- `vars`: recipe-specific placeholder values.
- `shell`: recipe-specific shell override.
- `shell_prelude`: recipe-specific shell code appended after top-level prelude.
- `arguments`: typed argument definitions.
- `presets`: recipe-local argument default sets selected with `preset=<name>`.

## One Workflow Per Recipe

Keep a recipe focused on one workflow. Use `pre` and `post` for setup and
cleanup that belongs to that workflow. Use [Recipe References](recipe-references.md)
to compose separate workflows without hiding them in a large shell script.

For execution order and failure behavior, see [Recipe Lifecycle](recipe-lifecycle.md).
