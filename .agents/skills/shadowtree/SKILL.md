---
name: shadowtree
description: Run, inspect, design, review, or migrate to Shadowtree recipes in .shadowtree.toml. Use when executing a recipe, passing recipe arguments, or inspecting a resolved plan; when adding or changing recipes, typed arguments, lifecycle stages, recipe references, fan-out, profiles, includes, vars, env, requirements, logging, presets, value providers, sandbox policy, or sync-out; or when replacing Makefiles, package scripts, task-runner configuration, shell scripts, or CI-only command sequences with .shadowtree.toml.
---

# Shadowtree

Shadowtree is the project's workflow boundary, not a prefix for every shell
command. A recipe defines how an operation runs; it does not expand the task's
authorization.

Reading exactly one reference is a hard gate, not optional discovery. Before
issuing any Shadowtree command or editing `.shadowtree.toml`, select and read
the single reference that most directly owns the requested outcome. Reading
this file alone or loading multiple references does not satisfy the gate. Each
reference is self-sufficient; do not load another during the same activation.

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

Invoke a known recipe directly. Inspect only to resolve a concrete unknown that
can change the invocation or whether it is safe to run. The user, project
instructions, the owning reference, an earlier result, and a conclusive error
all count as established context; reuse them instead of seeking reassurance
from another command.

| Command | Use only when |
| --- | --- |
| `shadowtree config` | the config path or selected profile is unknown |
| `shadowtree recipes` | the recipe name is unknown; run it once |
| `shadowtree help <recipe> color=false` | all of these hold: the recipe is custom and unfamiliar, an unresolved argument choice blocks the invocation, and no established context gives its name, type, bound, preset, or value |
| `shadowtree --print <recipe> [args...]` | an unresolved question about this exact invocation's stages, sandbox mode, workdir, requirements, or sync-out can change the decision to run it |
| `shadowtree --print --expanded ...` | a compact plan hides a script or resolved value the decision needs |
| `shadowtree --check <recipe> [args...]` | resolution and recipe references need validating without running commands |
| `shadowtree --check --shell ...` | expanded `sh` or `bash` syntax is the uncertainty |
| `shadowtree --verbose <recipe>` | workspace paths or stage boundaries are useful during execution |

- Treat `help` and `--print` as exceptional evidence-gathering commands, never
  routine preflight, validation, or proof of diligence. Run neither for a known
  profile built-in, a documented invocation, or a command already established
  by the owning reference or project instructions.
- Use `help` only for the unresolved custom-recipe argument case in the table.
  An `unknown argument` error is conclusive evidence that the recipe does not
  expose that token; correct the invocation or owning configuration instead of
  calling `help`. Help resolves dynamic values and may run command-backed
  providers, so it is neither cheap nor quiet.
- Use `--print` only for the unresolved execution-property case in the table or
  the unfamiliar persistent or privileged case under Persistence. Do not print
  a known test, check, format, or build recipe before running it.
- Inspect once per unresolved decision. Reuse that result unless the recipe,
  arguments, working directory, or configuration changes.

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
