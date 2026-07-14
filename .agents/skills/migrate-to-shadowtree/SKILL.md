---
name: migrate-to-shadowtree
description: Migrate existing project automation into Shadowtree recipes. Use when converting Makefiles and Make targets, task-runner commands, build/test/benchmark/dev/codegen/install workflows, scripts, or environment- and flag-driven automation into simpler .shadowtree.toml configuration with typed arguments, builtins, placeholders, lifecycle stages, sandboxing, sync_out, and Shadowtree validation.
---

# Migrate To Shadowtree

Use this skill to turn script-shaped automation into simpler Shadowtree configuration while preserving user-visible behavior: comparable arguments, features, defaults, side effects, and output. Do not preserve incidental script structure or require a one-to-one recipe mapping.

## Workflow

1. Capture the old contract before editing.
   Completion criterion: know every entrypoint, argument, flag, environment variable, default, output, side effect, caller/reference, cleanup path, and compatibility requirement. Distinguish observable behavior from script-only implementation mechanics.

2. Inspect the current Shadowtree shape.
   Completion criterion: know the active profile, builtin recipes, local overrides, config layout, and naming style from `shadowtree config`, `shadowtree recipes`, and `shadowtree help <recipe> color=false`.

3. Choose the recipe boundary.
   Completion criterion: reuse a builtin when it provides comparable behavior. Consolidate or reshape old entrypoints when the resulting interface stays similarly capable and becomes simpler. Otherwise choose the smallest new recipe or override, justified by a behavior gap.

4. Map script interface to typed arguments.
   Completion criterion: each old flag/env/positional input is either represented as a typed Shadowtree argument, folded into a constant/default, or deliberately removed with references updated.

5. Move shell logic into recipe stages.
   Completion criterion: shared shell helpers are in `shell_prelude`, setup is in `pre`, the main action is in `cmd`, teardown is in `post`, and cleanup that depends on successful setup uses a marker or idempotent checks.

6. Remove stale script surface.
   Completion criterion: the old script is deleted or left only as an explicitly requested compatibility wrapper, and searches for the old path, old env names, old flags, old usage text, and removed helper names are clean or intentionally documented.

7. Validate the migrated recipe.
   Completion criterion: `shadowtree config`, `shadowtree help <recipe> color=false`, and `shadowtree --print <recipe> ...` pass; large shell blocks pass shell syntax checks; safe dry/fast paths run; stale-term searches are clean.

## Recipe Design Rules

- Do not recreate or override a builtin recipe unless a behavior requirement cannot be met by it, or the replacement is demonstrably better. Better means a simpler implementation or interface with comparable observable behavior, not merely less configuration. Inspect builtin help, expanded plan, arguments, forwarded args, fan-out, sandboxing, and outputs first. State the behavior gap; override only the smallest necessary field. If no gap remains, delete the custom recipe.
- Preserve comparable arguments, features, defaults, side effects, and output by default. Preserve exact output paths or formats when users, docs, or callers depend on them. Simplify recipe boundaries and remove script-only artifacts, cleanup, and intermediate files; migration need not be one-to-one.
- Prefer `.shadowtree/<domain>.toml` for substantial migrations and include it from root Shadowtree config when that is the local convention.
- Use typed arguments for user-controlled values. Pick the narrowest type that matches behavior: `bool`, `int`, `float`, `string`, `path`, `rel_path`, `duration`, or `duration:seconds`; use `default` and allowed `values` when appropriate.
- Use placeholders directly in commands: `"{arg}"` for free string/path shell words, direct `{arg}` for type-safe numeric/bool/duration/enum values, `{arg:shell}` only when embedding in an unquoted shell word such as `-Dname={arg:shell}`, `{@}` for leftover args, and recipe references such as `@recipe` or `@path:recipe`.
- Avoid placeholder-to-variable boilerplate. Do not write `host="{host}"`, `runs="{runs}"`, or similar unless the shell variable holds computed state after additional logic.
- Keep shell variables only for values that are actually computed at runtime: selected tools, derived paths, arrays, parsed output, process ids, temporary files, or profile-dependent defaults.
- Use `shell_prelude` for reusable functions, traps, validators, or helper commands used by multiple stages.
- Split behavior with `pre`, `cmd`, and `post`. `pre` prepares certificates, dependencies, generated inputs, and services; `cmd` performs the main task; `post` cleans up even when `cmd` fails.
- Prefer idempotent cleanup. When cleanup should only run after setup, create a marker in `pre` and check it in `post`.
- Use `sandboxed = false` for recipes that need Docker, host services, local tool installation, live processes, benchmarks, host networking, or other non-isolated side effects. Keep the sandbox default for pure builds, checks, and generators.
- Use `sync_out` for generated files that must leave a sandboxed Shadowtree workspace.
- Use recipe references instead of shelling out to nested `shadowtree` commands inside recipe bodies.
- Preserve quoting, `set -euo pipefail`-style guarantees, traps, and error messages when moving Bash into TOML strings.

## Migration Patterns

Turn shell CLI/env inputs into typed args:

```sh
# Before
HOST="${HOST:-127.0.0.1}"
PORT="${PORT:-8080}"
RUNS="${RUNS:-3}"
go test ./internal/bench -run '^$' -bench . -count "$RUNS" -args -host "$HOST" -port "$PORT"
```

Avoid re-creating shell assignment wrappers:

```toml
# Avoid
[recipes.benchmark]
cmd = '''
host="{host}"
port="{port}"
runs="{runs}"
go test ./internal/bench -run '^$' -bench . -count "$runs" -args -host "$host" -port "$port"
'''
```

Prefer placeholders in place:

```toml
[recipes.benchmark]
shell = "bash"
sandboxed = false
cmd = 'go test ./internal/bench -run "^$" -bench . -count {runs} -args -host "{host}" -port {port}'

[recipes.benchmark.arguments.host]
type = "string"
default = "127.0.0.1"

[recipes.benchmark.arguments.port]
type = "int"
default = 8080

[recipes.benchmark.arguments.runs]
type = "int"
default = 3
```

Use stages when the script has setup and cleanup:

```toml
[recipes.benchmark]
shell = "bash"
sandboxed = false
shell_prelude = '''
marker=".shadowtree-benchmark-server"

start_server() {
  ./godoxy serve --config "{config}" &
  echo "$!" > "$marker"
}

stop_server() {
  [[ -f "$marker" ]] || return 0
  kill "$(cat "$marker")" 2>/dev/null || true
  rm -f "$marker"
}
'''
pre = "start_server"
cmd = 'go test ./internal/bench -run "^$" -bench "{bench}" -count {runs}'
post = "stop_server"
```

If a value needs logic, compute only that value:

```toml
[recipes.check]
shell = "bash"
shell_prelude = '''
resolve_pkg() {
  if [[ {all} == true ]]; then
    printf './...'
  else
    printf '%s' '{pkg}'
  fi
}
'''
cmd = "go test \"$(resolve_pkg)\""
```

## Do

- Search before adding helpers, validators, recipe patterns, or overrides; reuse builtin recipes and the repo's existing Shadowtree conventions.
- Preserve observable behavior while simplifying the interface and deleting script-only scaffolding that Shadowtree replaces.
- Update docs, README snippets, CI commands, package scripts, Make targets, and references from `scripts/name.sh` or env-based invocation to `shadowtree <recipe> arg=value`.
- Delete old usage banners, argument parsers, env-default blocks, trap scaffolding, temporary variable pass-throughs, and wrapper functions that no longer serve the recipe.
- Validate with `shadowtree --print <recipe> ...` before running recipes with expensive or host-mutating behavior.
- Run scoped searches for stale terms after removal, including old script names, old environment variables, old flags, and anti-patterns like `="[{]` or `="{`.

## Don't

- Do not assume every script or Make target needs a same-named recipe. Preserve comparable workflows and capabilities, not files or targets one-for-one.
- Do not replace a more capable builtin with a narrower custom recipe. Keep builtin package selection, argument forwarding, fan-out, sandboxing, and completion unless preserving behavior requires an override.
- Do not keep the shell script after migration unless the user explicitly asks for compatibility.
- Do not replace a script with a new wrapper that only calls `shadowtree <recipe>`.
- Do not move untyped environment-variable configuration into TOML unchanged; convert it to typed arguments or constants.
- Do not use `arg="{arg}"` or `FOO="{foo}"` just to preserve the old script's variable names.
- Do not make every script line a single huge `cmd` when `shell_prelude`, `pre`, and `post` would make lifecycle and cleanup clearer.
- Do not duplicate CLI parsing, `usage()` text, mode dispatch, or supported-value checks that typed arguments now provide.
- Do not use TOML command arrays unless the local Shadowtree docs/config prove they are supported.
- Do not run full benchmarks, Docker mutations, live server recipes, or local installation recipes before printing and reviewing the resolved recipe plan.

## Verification Checklist

- `shadowtree config`
- `shadowtree recipes`
- `shadowtree help <recipe> color=false`, including comparison with any builtin being overridden
- `shadowtree --print <recipe> ...` with representative typed args
- Old-vs-new contract comparison for retained workflows: arguments, defaults, features, side effects, output paths/formats, and failure behavior
- Shell syntax check for large Bash bodies after extraction or by copying the resolved command into the appropriate shell checker
- Scoped stale searches for old script path, old env vars, old flags, old usage text, deleted helper names, and placeholder-to-variable assignments
- Safe recipe execution only when it is fast, local, and consistent with the user's request
