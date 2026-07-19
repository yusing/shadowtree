# CLI Inspection

Shadowtree exposes recipe behavior before execution. Use inspection commands
before running unfamiliar recipes, writes, deletes, installs, regeneration, or
sync-out.

## Help

`shadowtree help` prints CLI usage, active config/profile, and resolved recipes
with their `help` text.

```sh
shadowtree help
shadowtree help test
shadowtree help test color=false
```

`shadowtree help <recipe>` prints these fields when present or applicable:

- recipe name and help text
- `--all` support and target domain
- command
- explicit `false` or `system` sandbox marker
- tool requirements
- `pre` and `post`
- `for_each`
- `workdir`
- typed arguments with help, info, and configured values
- sync-out paths for sandboxed recipes

## Recipe Listing

`shadowtree recipes` prints resolved recipe names and help text. If a recipe
has no `help`, Shadowtree falls back to a compact command summary.

```sh
shadowtree recipes
```

## Plan Printing

`--print` prints the resolved execution plan without running it:

```sh
shadowtree --print test ./internal/runner
shadowtree --print --expanded test ./internal/runner
shadowtree --all --print test
```

The plan includes fields such as recipe name, profile, config path,
unsandboxed marker, declared requirements, stages, `for_each`, `workdir`, main
command, post commands, and sync-out paths. Static system plans also report
`runtime: <not probed>`.
Aggregate plans also print `scope`, `target_domain`, and `target_source` so a
native workspace command and a per-target plan remain distinguishable.

`--print --expanded` also prints normalized defaults for absent fields,
expanded script bodies, the selected preset, resolved typed arguments, leftover
variadic args, computed vars, recipe-local env, expanded log settings, and
expanded sync-out paths.

## Checks

`--check` validates the selected resolved recipe without running commands:

```sh
shadowtree --check test
shadowtree --check --shell test
```

It validates nested `@recipe` and `@path:recipe` references, reports missing
references, rejects reference cycles, and validates resolved log and workdir
paths.

`--check` does not check host tool availability declared in `requires`; those
are checked only before real execution.

For `sandboxed = "system"`, `--check` first performs the bounded host-capability
probe used by execution: Docker, Podman, then nerdctl. Detection progress and
candidate failures use stderr. Static `--print` remains runtime-free.

`--check --shell` additionally parses expanded `sh` and `bash` script bodies
after placeholder expansion and shell prelude insertion.
