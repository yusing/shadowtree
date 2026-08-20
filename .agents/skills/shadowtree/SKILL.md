---
name: shadowtree
description: Run, inspect, design, review, or migrate to Shadowtree recipes in .shadowtree.toml. Use when executing a recipe, passing recipe arguments, or inspecting a resolved plan; when adding or changing recipes, typed arguments, lifecycle stages, recipe references, fan-out, profiles, includes, vars, env, requirements, logging, presets, value providers, sandbox policy, or sync-out; or when replacing Makefiles, package scripts, task-runner configuration, shell scripts, or CI-only command sequences with .shadowtree.toml.
---

# Shadowtree

Shadowtree is the project's workflow boundary, not a prefix for every shell
command. A recipe defines how an operation runs; it does not expand the task's
authorization.

Each reference below is self-sufficient. Read the one that owns the work, and
only that one.

| Work | Reference |
| --- | --- |
| Run, execute, or inspect an existing recipe | [`RUNNING.md`](RUNNING.md) |
| Add, change, review, or explain a recipe or config field | [`AUTHORING.md`](AUTHORING.md) |
| Replace a Make target, script, package command, or CI sequence | [`MIGRATING.md`](MIGRATING.md) |

## Invocation

```sh
shadowtree [global flags] <recipe> [recipe args...]
shadowtree --profile go test ./internal/recipe -run=TestResolve
```

- DO put global flags before the recipe name; every later token is recipe input.
- DO pass positional and `key=value` arguments straight after the recipe name.
- DON'T write `shadowtree run <recipe>`. `run` is not a dispatcher but a recipe
  name, including the Go-profile recipe for `go run`.
- DON'T add `--` before ordinary arguments or single-token passthrough flags.

## Inspection

Use the smallest command that answers the open question. Skip it when the user,
project instructions, or established context already answers it.

| Command | Use only when |
| --- | --- |
| `shadowtree config` | the config path or selected profile is unknown |
| `shadowtree recipes` | the recipe name is unknown; run it once |
| `shadowtree help <recipe> color=false` | first invoking an unfamiliar *custom* recipe, and the task must choose among unknown argument names, types, bounds, presets, or values |
| `shadowtree --print <recipe> [args...]` | the exact invocation's resolved stages, sandbox mode, workdir, requirements, or sync-out matter |
| `shadowtree --print --expanded ...` | a compact plan hides a script or resolved value the decision needs |
| `shadowtree --check <recipe> [args...]` | resolution and recipe references need validating without running commands |
| `shadowtree --check --shell ...` | expanded `sh` or `bash` syntax is the uncertainty |
| `shadowtree --verbose <recipe>` | workspace paths or stage boundaries are useful during execution |

- DO prefer `--print` over `help` once the invocation syntax is known.
- DON'T call `help` for a profile built-in, for an override that keeps built-in
  usage, or after an `unknown argument` error. Help resolves dynamic values and
  may run command-backed providers, so it is neither cheap nor quiet.
- DON'T run `help` before every recipe or chain it across known recipes.

## Lifecycle

1. `pre` runs in order; a failure there skips `cmd`.
2. `cmd` runs once, or once per `for_each` value.
3. `post` runs after success, failure, and initial cancellation. Cancellation
   never skips cleanup.
4. The first `pre` or `cmd` failure is preserved unless only `post` fails.
5. Sync-out happens only after every stage succeeds.

## Configuration form

- DO write command fields as shell strings; never TOML argv arrays.
- DO quote placeholders in shell text: `command "{path}"`.
- DO compose with `@recipe` or `@path:recipe`; never a nested `shadowtree`
  process.
- Argument types are `string`, `int`, `float`, `bool`, `path`, `rel_path`,
  `duration`, and `duration:seconds`.

## Persistence

The sandbox is disposable: writes vanish after the run unless their exact paths
are synced out.

- DO treat an unsandboxed recipe, recipe `sync_out`, or invocation `--sync-out`
  as a host write. Existing configuration supplies the mechanism, not
  authorization for further paths.
- DO prefer exact sync-out paths. Use `--sync-out-all` only when applying the
  whole sandbox is the request.
- DO account for deletion: a selected path missing in the sandbox is mirrored as
  a host deletion.
- DO `--print` the exact invocation before running an unfamiliar recipe that is
  unsandboxed, persists output, installs dependencies, uses privileges, controls
  processes, or writes externally; add `--expanded` when a script's effects are
  unknown. Stop if any stage exceeds the authorized operation.

Execute only once the recipe, arguments, lifecycle stages, sandbox behavior, and
persistence all match the requested operation.
