---
name: using-shadowtree
description: Run and inspect existing Shadowtree recipes without changing .shadowtree.toml. Use when a project exposes Shadowtree workflows and the task is to list, understand, validate, or execute a recipe, pass recipe arguments, or inspect a resolved plan. For designing or editing recipes, use authoring-shadowtree-recipes instead.
---

# Using Shadowtree

Shadowtree is the project's workflow boundary, not a prefix for every shell
command. Use an existing recipe when it owns the requested operation. A recipe
defines how that operation runs; it does not expand the task's authorization.

## Prefer the Recipe Interface

Run a recipe directly:

```sh
shadowtree [global flags] <recipe> [recipe args...]
```

```sh
shadowtree test ./internal/recipe -run=TestResolve
shadowtree --verbose test
shadowtree --profile go test
shadowtree --all test
```

- Invoke the recipe directly. `shadowtree run <recipe>` is not a dispatcher;
  `run` is itself a recipe name, including the Go-profile recipe for `go run`.
- Put global flags before the recipe name. Everything after the recipe name is
  input to that recipe.
- Pass positional values and `key=value` arguments directly after the recipe.
- Omit `--` for ordinary recipe arguments and single-token passthrough flags.
- Use `--` after typed recipe arguments only to forward every following token
  through the recipe's `{@}` placeholder. This is uncommon and is useful when
  a literal token such as `NAME=value` would otherwise look like a named recipe
  argument:

  ```sh
  shadowtree test pkg=./internal/recipe -- --cookie NAME=value
  ```

  Do not turn `shadowtree test ./...` into `shadowtree test -- ./...`.
- Use `--all` only when the recipe declares aggregate support. Put it before
  the recipe and do not combine it with an explicit primary target. Under
  `--all`, put `--` before passthrough flags that take separate bare values:

  ```sh
  shadowtree --all test -- -run TestName
  ```

## Use Built-ins Without Discovery

Profile built-ins have established interfaces. Invoke a built-in directly when
its name and intended operation are known. Do the same for a project override
that retains the built-in interface; use `--print` if the override's behavior,
not its usage, needs inspection.

For the common Go built-ins:

- `fmt` formats source and persists the edits. Use it instead of
  `shadowtree exec -- gofmt -w ...`.
- `test` runs tests.
- `vet` runs `go vet` only.
- `check` runs `vet` and then `test`.
- `build` builds packages or an artifact. It is not the default way to check a
  change.

Do not call `recipes` or `help` merely to confirm one of these known shapes.
If an invocation fails because a project override changed the interface, then
inspect that concrete uncertainty.

## Resolve Only the Current Uncertainty

Use the smallest inspection that answers the open question:

```sh
shadowtree config
shadowtree recipes
shadowtree help <recipe> color=false
shadowtree --print <recipe> [args...]
shadowtree --print --expanded <recipe> [args...]
shadowtree --check <recipe> [args...]
shadowtree --check --shell <recipe> [args...]
```

- Use `config` only when the config path or selected profile is unknown.
- Use `recipes` once when the recipe name is unknown. Skip it when the user,
  project instructions, or established context already names the recipe.
- Use `help <recipe>` only when an unfamiliar custom recipe's argument names,
  types, bounds, presets, or available values are needed. Help resolves dynamic
  argument values and may run command-backed value providers, so it is not a
  cheap static preflight and can be large or noisy. Do not run help before every
  recipe or chain help calls for several known recipes.
- Use `--print` on the exact intended invocation to inspect its resolved stages,
  sandbox mode, workdir, requirements, and sync-out behavior without executing
  it. Prefer this over help when the invocation syntax is already known.
- Add `--expanded` only when a compact plan hides a script or resolved value
  relevant to the decision.
- Use `--check` to validate resolution and recipe references without executing
  recipe commands. Add `--shell` only when expanded `sh` or `bash` syntax is the
  uncertainty.
- Use `--verbose` during execution only when workspace paths or stage
  boundaries are useful diagnostics.

Before running an unfamiliar recipe that is unsandboxed, persists output,
installs dependencies, uses privileges, controls processes, or writes
externally, inspect the exact invocation with `--print`. Use `--expanded` before
execution when the compact plan shows a script whose effects are not already
known. Stop if any stage exceeds the authorized operation.

## Keep Validation Non-overlapping

Choose one recipe that covers each validation scope:

- Use `test` for a focused behavioral check.
- Use `vet` when static analysis alone is required.
- Use `check` when both vet and tests are required.
- Use `build` only when the requested outcome is a build artifact or a
  build-specific condition must be verified.

Because `check` already runs `vet` and `test`, do not run `vet`, `test`, and
`check` over the same scope. In particular, avoid sequences such as:

```sh
shadowtree vet && shadowtree check
shadowtree test && shadowtree check
shadowtree vet && shadowtree test && shadowtree check
```

A focused test during iteration followed by one required broader `check` is not
the same scope and is acceptable. Do not repeat unchanged coverage merely to
accumulate successful commands, and do not append `build` to routine validation.

## Use `exec` Only for Unowned Arbitrary Commands

Use `shadowtree exec -- <cmd> [args...]` only when no recipe owns the operation
and the arbitrary command specifically needs Shadowtree's sandbox:

```sh
shadowtree exec -- ./scripts/reproduce-bug.sh
```

Do not use `exec` to reproduce an existing recipe such as `fmt`, `test`, `vet`,
`check`, or `build`. The sandbox is disposable by default: ordinary writes
disappear after the run, so a formatter, generator, migration, or other editing
command under `exec` does not update the host checkout unless its exact outputs
are synced out. Prefer the existing persistent recipe. Add invocation-local
sync-out only when the requested output must persist and every selected path is
in scope:

```sh
shadowtree --sync-out internal/generated exec -- generate-command
```

## Respect Host Persistence

- Treat an unsandboxed recipe, recipe-local `sync_out`, or invocation-local
  sync-out as a host-writing operation. Existing recipe configuration defines
  the persistence mechanism; it does not authorize additional output paths.
- Expect sandbox writes without sync-out to disappear after the run.
- Prefer exact sync-out paths. Use `--sync-out-all` only when applying the whole
  sandbox is the requested operation.
- A missing selected path is mirrored as a host deletion. Account for that
  destructive effect before execution.
- Sync-out occurs only after all recipe stages succeed.
- Configured `post` cleanup runs after failure or initial cancellation; do not
  assume cancellation skips cleanup.

Before execution, ensure the recipe, arguments, lifecycle stages, sandbox
behavior, and persistence all match the requested operation. Run the selected
operation once, then run only the smallest non-overlapping validation needed.
