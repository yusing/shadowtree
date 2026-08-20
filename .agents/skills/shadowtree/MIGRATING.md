# Migrating to Shadowtree

Converting existing automation into recipes and removing the obsolete surface.
Invocation, inspection, lifecycle, configuration form, and persistence live in
[`SKILL.md`](SKILL.md).

Migrate **behavior**, not files or syntax: preserve the useful workflow contract,
replace script mechanics with Shadowtree concepts, update every active caller,
and delete what is left.

## 1. Capture the legacy contract

Read every affected automation file and trace its callers before editing. Record:

- **Entry points**: targets, scripts, package commands, CI steps, documented invocations.
- **Inputs**: positional arguments, flags, environment variables, config files, defaults, allowed values, precedence.
- **Execution**: working directory, required tools, setup, main work, retries, teardown, nested tasks, fan-out.
- **Effects**: checkout writes, generated outputs, external services, installs, caches, logs, deletions.
- **UX**: progress, stdout/stderr shape, interactive versus redirected behavior, waiting, cancellation, failure diagnostics, exit status.
- **Compatibility**: callers or users still requiring the old name, path, or interface.

DO separate observable behavior from incidental implementation: manual CLI
parsers, temporary pass-through variables, usage banners, traps, wrappers.

## 2. Inspect the target project

Read the active config and affected includes in full.

- DO reuse a profile built-in or existing recipe when it already provides the
  required contract, overriding only the behavior gap.
- DON'T assume one legacy target or script must become one same-named recipe.
- DO consolidate duplicate entry points when their callers can move to one
  clearer interface without losing required behavior.

## 3. Map the contract

Record every legacy surface's destination before implementing:

| Legacy behavior | Destination |
| --- | --- |
| Reusable target or command | Existing built-in, existing recipe, or one new recipe |
| Positional, flag, or environment input | Typed argument, explicit default, constant, or deliberate removal |
| Repeated default combinations | Recipe preset |
| Setup command | `pre` |
| Main action | `cmd` |
| Trap or finally-style cleanup | Idempotent `post` |
| Nested target or script call | `@recipe` or `@path:recipe` |
| Same command repeated per module or item | `for_each`, with `workdir` when needed |
| Required executable check | `requires` |
| Selected generated output | Recipe-local `sync_out` |
| Intentional direct checkout or host mutation | `sandboxed = false` |
| Shared literal or environment setting | `vars` or `env` |
| Persistent run output | Recipe logging |

DON'T copy this table mechanically: keep only what the captured contract needs.

## 4. Implement the smallest equivalent

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

- DO turn legacy inputs into typed arguments and consume `{arg}` placeholders
  directly, keeping shell variables only for values truly computed at runtime.
  DON'T preserve shell-variable names merely to resemble the old implementation.
- DO make `post` cleanup safe when `pre` only partially succeeds: prefer an
  idempotent command, and add a marker only when cleanup cannot otherwise tell
  whether setup created the resource.
- DON'T move a legacy `trap` into `cmd` or `shell_prelude`; cleanup belongs in
  `post`.
- DO use scalar strings or lists for ordinary `pre` and `post` commands, and a
  structured stage table only for controls such as `timeout`.
- DO add `{@}` only when undeclared trailing argv must be forwarded.
- DO choose sandboxing from actual persistence and host-side effects. DON'T mark
  a recipe unsandboxed merely because the old implementation used Docker, a
  network, or a long-running process.
- DO preserve required progress and diagnostics. DON'T hide a long wait inside a
  silent replacement recipe.

## 5. Cut over every caller

Update CI, docs, README snippets, package scripts, Make targets, developer
instructions, and other recipes to the direct invocation form.

- DO delete the old script, target, parser, helper, usage text, environment
  names, and temporary files once nothing supported refers to them.
- DO keep a compatibility adapter only when an explicit supported contract still
  requires the old entry point, and make it thin, documented, and covered by
  migration guidance.

## 6. Prove the migration

Validate definition and shell syntax with `--check`, `--check --shell`, and
`--print`, without executing host-mutating work. Then compare old against new:

- Default invocation and each retained input form.
- Named, positional, boundary, and forwarded arguments where applicable.
- Invalid, missing, malformed, and unknown or future values.
- Success, setup failure, main failure, cleanup failure, and cancellation.
- Interactive and redirected output when the old workflow distinguishes them.
- Expected writes, outputs, logs, external side effects, and deletions.
- Unrelated recipe-name collisions, especially a recipe literally named `run`.

DO run the migrated recipe only when doing so is safe, in scope, and reasonably
fast; print and review expensive benchmarks, installs, publishing, live services,
or destructive sync-out instead of executing them speculatively.

DO finish with scoped searches for every removed script path, target, flag,
environment variable, usage string, helper name, and old invocation. Remaining
matches must be supported compatibility, migration, changelog, historical, or
security evidence, never stale active guidance.
