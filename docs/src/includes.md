# Includes

Use `include` to share Shadowtree config across projects or subprojects.

```toml
include = ["./tools.shadowtree.toml", "./ci.shadowtree.toml"]
```

Include paths are resolved relative to the config file that contains them.
Included files are merged before the including file; later includes override
earlier includes, and the including file overrides all included files.

## What Merges

Includes are global mixins. These surfaces participate in the effective config:

- `profile`
- `shell`
- `shell_prelude`
- `vars`
- `var_commands`
- `enum_sets`
- `env`
- `recipes`

Included recipes appear in `shadowtree help`, `shadowtree recipes`, shell
completion, and editor completion as if they were defined in the current config.

## Shell Prelude Order

When multiple files define `shell_prelude`, Shadowtree concatenates included
preludes first, then the including file's prelude.

If `app.shadowtree.toml` includes `tools.shadowtree.toml`, recipes in both
files can use shell functions from `tools.shadowtree.toml`. The included
prelude cannot read variables that are assigned later by the including prelude
while the prelude is being evaluated.

## Recipe Overrides

Same-name recipes from includes are field-merged, with the including file
winning for fields it sets. There is no deletion syntax for inherited recipes,
arguments, vars, or env keys.

Use [Recipe References](recipe-references.md) with `@path:recipe` instead of
`include` when a recipe should stay isolated and run from another config's
directory.
