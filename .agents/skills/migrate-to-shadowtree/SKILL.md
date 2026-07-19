---
name: migrate-to-shadowtree
description: Convert existing project automation into smaller Shadowtree recipes while preserving its observable contract and removing obsolete entrypoints. Use when replacing Makefiles or Make targets, package scripts, task-runner configuration, shell scripts, CI-only command sequences, or environment- and flag-driven workflows with .shadowtree.toml. Use authoring-shadowtree-recipes for Shadowtree feature selection; do not use this skill for greenfield recipe authoring or merely running existing recipes.
---

# Migrate to Shadowtree

Migrate **behavior**, not files or syntax. Preserve the useful workflow contract,
replace script mechanics with Shadowtree-owned concepts, update every active
caller, and remove the obsolete surface.

Use `authoring-shadowtree-recipes` for the configuration feature catalog and
selection rules. This skill owns only the legacy-to-Shadowtree migration process.

## 1. Capture the Legacy Contract

Read every affected automation file and trace its callers before editing. Record:

- Entry points: targets, scripts, package commands, CI steps, and documented
  invocations.
- Inputs: positional arguments, flags, environment variables, config files,
  defaults, allowed values, and precedence.
- Execution: working directory, required tools, setup, main work, retries,
  teardown, nested tasks, and fan-out.
- Effects: checkout writes, generated outputs, external services, installs,
  caches, logs, and deletions.
- UX: progress, stdout/stderr shape, interactive versus redirected behavior,
  waiting, cancellation, failure diagnostics, and exit status.
- Compatibility: callers or users that still require the old name, path, or
  interface.

Separate observable behavior from incidental implementation such as manual CLI
parsers, temporary pass-through variables, usage banners, traps, and wrapper
functions.

## 2. Inspect the Target Project

```sh
shadowtree config
shadowtree recipes
shadowtree help <recipe>
shadowtree --print <recipe> [args...]
```

Use only what resolves a current uncertainty. Read the active config and affected
includes in full. Reuse a profile builtin or existing recipe when it already
provides the required contract; override only the behavior gap.

Do not assume one legacy target or script must become one same-named recipe.
Consolidate duplicate entry points when their callers can move to one clearer
interface without losing required behavior.

## 3. Map the Contract

For every legacy surface, record its destination before implementation:

| Legacy behavior | Shadowtree destination |
| --- | --- |
| Reusable target or command | Existing builtin, existing recipe, or one new recipe |
| Positional, flag, or environment input | Typed argument, explicit default, constant, or deliberate removal |
| Repeated default combinations | Recipe preset |
| Setup command | `pre` |
| Main action | `cmd` |
| Trap or finally-style cleanup | Idempotent `post` |
| Nested target or script call | `@recipe` or `@path:recipe` |
| Same command repeated for modules/items | `for_each` with `workdir` when needed |
| Required executable check | `requires` |
| Selected generated output | Recipe-local `sync_out` |
| Intentional direct checkout or host mutation | `sandboxed = false` |
| Explicit immutable system-container workflow | `sandboxed = "system"` |
| Shared literal or environment setting | `vars` or `env` |
| Persistent run output | Recipe logging |

Choose each Shadowtree feature through `authoring-shadowtree-recipes`. Do not
copy this table mechanically: keep only features required by the captured
contract.

## 4. Implement the Smallest Equivalent Recipe

Turn legacy inputs into typed arguments and consume placeholders directly. Do
not preserve shell-variable names merely to resemble the old implementation.

For example, migrate cleanup out of a trap:

```sh
# Before
pkg=${PKG:-./integration}
trap 'stop-test-service' EXIT INT TERM
start-test-service
go test "$pkg"
```

```toml
# After
[recipes.integration]
help = "Run integration tests."
pre = ["start-test-service"]
cmd = 'go test "{pkg}"'
post = ["stop-test-service"]

[recipes.integration.arguments.pkg]
type = "rel_path"
position = 1
default = "./integration"
```

Make `post` cleanup safe when `pre` only partially succeeds. Prefer an idempotent
cleanup command; use an explicit marker only when cleanup cannot otherwise tell
whether setup created the resource.

Apply these migration rules:

- Use shell strings, not TOML command arrays.
- Use scalar strings or lists for ordinary `pre` and `post` commands; use a
  structured stage table only when controls such as `timeout` are required.
- Never move a legacy `trap` into `cmd` or `shell_prelude`. Put lifecycle cleanup
  in `post`.
- Use `@recipe` references instead of nested `shadowtree` processes.
- Use `{arg}` placeholders directly; keep shell variables only for values truly
  computed at runtime.
- Add `{@}` only when undeclared trailing argv must be forwarded.
- Choose sandboxing from actual persistence and host-side effects. Do not mark a
  recipe unsandboxed merely because the old implementation used Docker, a
  network, or a long-running process.
- Preserve required progress and diagnostics. Do not hide a long wait inside a
  silent replacement recipe.

## 5. Cut Over Every Caller

Update CI, docs, README snippets, package scripts, Make targets, developer
instructions, and other recipes to the direct form:

```sh
shadowtree <recipe> [recipe args...]
```

Do not write `shadowtree run <recipe>`. Do not add `--` before ordinary recipe
arguments.

Delete the old script, target, parser, helper, usage text, environment names, and
temporary files once nothing supported refers to them. Keep a compatibility
adapter only when an explicit supported contract still requires the old entry
point; make the adapter thin, documented, and covered by migration guidance.

## 6. Prove the Migration

Validate definition and shell syntax without executing host-mutating work:

```sh
shadowtree --check <recipe> [args...]
shadowtree --check --shell <recipe> [args...]
shadowtree --print <recipe> [args...]
```

Then compare old and new behavior for representative cases:

- Default invocation and each retained input form.
- Named, positional, boundary, and forwarded arguments where applicable.
- Invalid, missing, malformed, and unknown/future values.
- Success, setup failure, main failure, cleanup failure, and cancellation.
- Interactive and redirected output when the old workflow distinguishes them.
- Expected writes, outputs, logs, external side effects, and deletions.
- Unrelated recipe-name collisions, especially a recipe literally named `run`.

Run the migrated recipe only when doing so is safe, within scope, and reasonably
fast. Print and review expensive benchmarks, installs, publishing, live services,
or destructive sync-out instead of executing them speculatively.

Finish with scoped searches for every removed script path, target, flag,
environment variable, usage string, helper name, and old invocation. Remaining
matches must be supported compatibility, migration, changelog, historical, or
security evidence—not stale active guidance.
