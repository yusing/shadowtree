---
name: using-shadowtree
description: Run and inspect existing Shadowtree recipes without changing .shadowtree.toml. Use when a project already exposes Shadowtree workflows and the task is to list, understand, validate, or execute a recipe; pass recipe arguments; inspect a resolved plan; Not for designing or editing recipes.
---

# Using Shadowtree

**Shadowtree is Make, but with args.**

Use this skill only for existing recipes. When `.shadowtree.toml` must change or
a recipe feature must be chosen, use `authoring-shadowtree-recipes` instead.

## Use One CLI Shape

Run a recipe directly:

```sh
shadowtree [global flags] <recipe> [recipe args...]
```

```sh
shadowtree test
shadowtree test ./internal/recipe -run=TestResolve
shadowtree --verbose build
shadowtree --profile go test
shadowtree --all test
```

Follow these rules:

- Invoke the recipe directly. Do **not** use `shadowtree run <recipe>` as a
  dispatcher. `run` has no dispatch meaning; it is merely a valid recipe name,
  including a Go-profile recipe for `go run`.
- Put global flags before the recipe name. Treat everything after the recipe
  name as input to that recipe.
- Use `--all` before a recipe only when help reports aggregate support. Its
  target domain is recipe-specific; do not combine it with an explicit primary
  target. A post-recipe `--all` is a recipe/tool argument instead. Under
  `--all`, put `--` before passthrough tool flags that take separate bare
  values, for example `shadowtree --all test -- -run TestName`.
- Pass positional values and `key=value` arguments directly after the recipe.
- Omit `--` for ordinary recipe arguments and single-token passthrough flags.
- Use `shadowtree exec -- <cmd> [args...]` when a command require sandboxing
  and is not a recipe:

  ```sh
  shadowtree exec -- go test ./...
  ```

- Use `--` after typed recipe arguments only to deliberately forward every
  following token through the recipe's `{@}` placeholder. This is uncommon and
  useful when a literal token such as `NAME=value` would otherwise look like a
  named recipe argument:

  ```sh
  shadowtree test pkg=./internal/recipe -- --cookie NAME=value
  ```

  Do not turn `shadowtree test ./...` into `shadowtree test -- ./...`.

## Resolve Only the Current Uncertainty

Do not guess recipe names or arguments:

```sh
shadowtree config
shadowtree recipes
shadowtree help <recipe> [color=false]
```

- Use `config` when the config path or selected profile is unknown.
- Use `recipes` only to discover or confirm recipe names.
- Use `help <recipe>` when its arguments, stages, requirements, sandbox mode,
  or sync-out paths are unclear.
- Skip discovery when project instructions or established context already
  identify the exact recipe and interface.

Inspect an unfamiliar or persistent operation before running it:

```sh
shadowtree --print <recipe> [args...]
shadowtree --print --expanded <recipe> [args...]
shadowtree --check <recipe> [args...]
shadowtree --check --shell <recipe> [args...]
```

- Start with `--print`; add `--expanded` only when the compact plan omits a
  relevant detail.
- Use `--check` to validate resolution and references without executing recipe
  commands; add `--shell` only when expanded `sh` or `bash` syntax matters.
- Use `--verbose` during execution when workspace paths and stage boundaries
  are useful diagnostics.

## Respect Persistence

Recipes are sandboxed unless their resolved definition says otherwise.

- Expect ordinary sandbox writes to disappear after the run.
- Trust recipe-local `sync_out` declared by the existing recipe.
- Add invocation-local sync-out only when the requested output must persist:

  ```sh
  shadowtree --sync-out internal/generated generate
  ```

- Prefer narrow paths over `--sync-out-all`.
- Treat a missing selected path as a host deletion during sync-out.
- Expect sync-out only after all recipe stages succeed.
- Expect configured `post` cleanup to run after failure or initial
  cancellation; do not assume cancellation skips cleanup.

Before execution, ensure the chosen recipe, arguments, sandbox behavior, and
persistence all come from project context, `help`, or the resolved plan—not
from an invented wrapper or remembered profile detail.
