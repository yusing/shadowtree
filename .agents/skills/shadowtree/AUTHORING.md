# Authoring Recipes

Changing a recipe definition or configuration contract. Invocation, inspection,
lifecycle, configuration form, and persistence live in [`SKILL.md`](SKILL.md).

A recipe is one named project workflow, not a new task language. Prefer one
readable path from `pre` through `cmd` to `post`.

## Before editing

1. Read the active `.shadowtree.toml` and every affected included config in full.
2. Inspect a same-name profile recipe or override only when its inherited
   behavior matters.
3. Reuse the existing recipe, argument, var, enum set, or include that owns the
   behavior. DON'T introduce a parallel source of truth.
4. Choose the smallest feature set that makes the workflow explicit. DON'T add a
   field merely because it exists; every feature must answer a current
   requirement.

## Feature selection

| Need | Use | Selection rule |
| --- | --- | --- |
| Standard Go, Node, or Rust workflows | `profile` | Opt in when profile recipes belong in the config; omit it when the configured recipe set must stay exact. Rust supplies Cargo `check`, `test`, `build`, `run`, `fmt`, `clippy`; toolchains and components are never installed implicitly. |
| One reusable workflow | `[recipes.<name>]`, `help`, `cmd` | Start here: concise help, one shell-string main command. |
| Setup before the main command | `pre` | Ordered preparation only. |
| Cleanup or reporting afterward | `post` | Use instead of shell `trap`. |
| Compose existing workflows | `@recipe`, `@path:recipe` | Reuses the current workspace; no second process, no nested sandbox. |
| Validated user input | `arguments` plus `{name}` | Typed positional or named inputs instead of parsing raw shell arguments. |
| Forward leftover flags | `{@}` in `cmd` | Only when the main command intentionally accepts undeclared trailing argv. |
| Named bundles of defaults | `presets` | When users repeatedly pick the same related values; explicit arguments still win. |
| Bounded or discoverable values | `values`, `enum_sets` | Prefer builtins such as `@enum`, `@lines`, `@glob`, or Go providers over custom shell discovery. |
| Repeat `cmd` per value | `for_each`, usually `workdir` | Module or package fan-out; `pre` and `post` still run once. |
| Run `cmd` from a subdirectory | `workdir` | Keep it workspace-relative; combine with `{item}` for fan-out. |
| Shared static values | top-level or recipe `vars` | Placeholders instead of duplicated literals; keep recipe-specific overrides local. |
| Values computed at resolution | `var_commands` | Versions, commit IDs, or detected labels that must appear in expanded plans. |
| Command environment | top-level or recipe `env` | Top-level for shared defaults, recipe-level for overrides. |
| Shared shell functions | `shell_prelude` | Only when several script commands genuinely share behavior. |
| Host executables needed first | `requires` | Declare required, optional, Go, or Node commands. DON'T install them in hidden setup logic. |
| Persistent stage output | `log`, `log_stages`, `log_tee` | When a run needs an explicit log artifact, or selected output must not be tee'd. |
| Shared configuration mixin | `include` | For fields and recipes that should merge in. Use `@path:recipe` when the other workflow should stay isolated in its own directory. |
| Retry a flaky readiness check | `@retry` | Bounded attempts and delay. DON'T retry deterministic failures or a whole recipe blindly. |

## Arguments and placeholders

Start at the smallest recipe, and add typed arguments only when the recipe needs
an input contract:

```toml
[recipes.build]
help = "Build a Go command."
cmd = 'go build -o "bin/{name}" "{pkg}" {@}'
sync_out = ["bin/{name}"]

[recipes.build.arguments.pkg]
help = "Main package to build."
type = "rel_path"
position = 1
default = "./cmd/tool"
values = "@go-main-packages"
```

- DO keep type and range validation in Shadowtree through `required`, `default`,
  `min`, and `max`, not in the shell body.
- DO use `values` to expose candidates; safely checkable providers such as
  `@enum` also constrain accepted values.
- DO reserve `{name:shell}` for an unquoted shell word, `{name:dq}` for inside
  double quotes, and `{name:raw}` for deliberate raw syntax or word splitting.

## Cleanup

```toml
[recipes.integration]
pre = ["docker compose up -d"]
cmd = "go test ./integration"
post = ["docker compose down"]
```

- DON'T replace `post` with `trap`; the lifecycle already guarantees cleanup
  after failure and initial cancellation.
- DO use `{status:pre}` and `{status:cmd}` in `post` only when cleanup or
  reporting must branch on earlier status.
- DO pass reference arguments with brackets, such as `@build[mode=release]`.
  Referenced recipes reuse the current workspace and run no sync-out of their
  own.

## Persistence model

- DO keep the default sandbox for checks, tests, builds, and speculative work.
- DO use recipe-local `sync_out` when only named outputs should persist after
  success.
- DO set `sandboxed = false` when writes are inherently direct, as in a format,
  tidy, install, or dev workflow that must edit the checkout or another host
  location.
- DON'T define top-level `sync_out`, and DON'T configure sync-out for an
  unsandboxed recipe: there is no sandbox to copy from.

## Validate the contract

Run `--check` and `--print` on the exact recipe with representative arguments,
adding `--shell` for expanded shell syntax.

- `--check` covers command shape, references, cycles, workdir, log paths,
  placeholders, typed values, and current or future reserved-name rules.
- `--print` confirms stages, sandbox mode, workdir, arguments, and sync-out
  without running commands.
- DO exercise success, failure, cancellation-sensitive cleanup, malformed input,
  unrelated recipe-name collisions, and unknown future values whenever the
  changed contract exposes those paths.

When changing Shadowtree itself, keep runtime, schema, editor diagnostics, docs,
examples, and this skill aligned. In an ordinary consumer project, change only
the owning config and its directly affected documentation or tests.
