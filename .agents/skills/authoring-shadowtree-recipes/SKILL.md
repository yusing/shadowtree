---
name: authoring-shadowtree-recipes
description: Design, write, review, or explain .shadowtree.toml recipes and choose the smallest Shadowtree feature for a workflow. Use when adding or changing recipes, typed arguments, lifecycle stages, recipe references, fan-out, profiles, includes, vars or env, requirements, logging, presets, value providers, sandbox policy, or recipe-local sync-out. For merely running an existing recipe, use using-shadowtree instead. When replacing legacy automation and removing its old entrypoints, also use migrate-to-shadowtree.
---

# Authoring Shadowtree Recipes

Use this skill when the recipe definition or configuration contract must change.
For invocation-only work, use `using-shadowtree` instead.
When replacing a Make target, script, task-runner command, or other legacy
automation, also use `migrate-to-shadowtree`; it owns contract inventory, caller
cutover, compatibility, removal, and old-versus-new verification.

## Start from the Existing Owner

Before editing:

1. Run `shadowtree config` when the active config path or profile is unknown.
2. Read the active `.shadowtree.toml` and every affected included config in
   full.
3. Inspect same-name profile recipes or overrides with `shadowtree help
   <recipe>` or `shadowtree --print <recipe>` only when their inherited behavior
   matters.
4. Reuse the existing recipe, argument, var, enum set, or include that owns the
   behavior. Do not introduce a parallel source of truth.
5. Choose the smallest feature set that makes the workflow explicit.

Treat a Shadowtree recipe as one named project workflow, not as a new task
language. Prefer one readable path from `pre` through `cmd` to `post`.

## Choose Features by Need

| Need | Use | Selection rule |
| --- | --- | --- |
| Standard Go, Node, or Rust workflows | `profile = "go"`, `profile = "node"`, or `profile = "rust"` | Opt in when profile recipes should be part of the config. Omit `profile` when the configured recipe set must remain exact. Rust provides Cargo `check`, `test`, `build`, `run`, `fmt`, and `clippy`; toolchains and optional components are never installed implicitly. |
| One reusable workflow | `[recipes.<name>]`, `help`, `cmd` | Start here. Give every project recipe concise help and one shell-string main command. |
| Setup before the main command | `pre` | Use for ordered preparation. A failing `pre` skips `cmd`. |
| Cleanup or reporting after `pre` or `cmd` | `post` | Use instead of shell `trap`; it runs after failed setup, failed main work, and initial cancellation. |
| Compose existing workflows | `@recipe` or `@path:recipe` | Reuse the current workspace without spawning another Shadowtree process or nested sandbox. |
| Validated user input | `arguments` plus `{name}` | Use typed positional or named inputs instead of parsing raw shell arguments. |
| Forward leftover command flags | `{@}` in `cmd` | Add only when the main command intentionally accepts undeclared trailing argv. |
| Named bundles of argument defaults | `presets` | Use when users repeatedly choose the same related values; explicit arguments still win. |
| Bounded or discoverable argument values | `values` or `enum_sets` | Prefer builtins such as `@enum`, `@lines`, `@glob`, or Go providers over custom shell discovery. |
| Repeat the same main command for values | `for_each` and usually `workdir` | Use for module/package fan-out. `pre` and `post` still run once. |
| Run the main command from a subdirectory | `workdir` | Keep it relative to the workspace; combine with `{item}` for fan-out. |
| Shared static values | top-level or recipe `vars` | Use placeholders instead of duplicating literals. Keep recipe-specific overrides local. |
| Values computed during resolution | `var_commands` | Use for versions, commit IDs, or detected labels that must appear in expanded plans. |
| Command environment | top-level or recipe `env` | Use top-level values for shared defaults and recipe values for overrides. |
| Shared shell functions | `shell_prelude` | Use only when multiple script commands genuinely share shell behavior. |
| Host executables required before setup | `requires` | Declare required, optional, Go, or Node commands; do not install them in hidden setup logic. |
| Persistent stage output | `log`, `log_stages`, `log_tee` | Use when a run needs an explicit log artifact or selected output should not be tee'd. |
| Shared configuration mixin | `include` | Use for fields and recipes that should merge into the current config. Use `@path:recipe` when the other workflow should stay isolated in its own directory. |
| Persist selected sandbox results | recipe `sync_out` | Use narrow workspace-relative paths copied back only after complete success. |
| Intentionally edit the host checkout directly | `sandboxed = false` | Use for format, tidy, install, or dev workflows whose writes are inherently direct. Do not combine with sync-out. |
| Run through the system container backend | `sandboxed = "system"` | Select explicitly when immutable system images and an ephemeral container are required. It is sandboxed for sync-out and never falls back to `true` or `false`. Inspect statically before execution. |
| Retry a flaky readiness check | `@retry` in `pre` or another shell command position | Use bounded attempts and delay; do not retry deterministic failures or the whole recipe blindly. |

Do not add a field merely because it exists. Every selected feature must answer a
current workflow requirement.

## Write the Smallest Recipe First

```toml
[recipes.test]
help = "Run tests."
cmd = "go test ./..."
```

Prefer shell strings for `cmd` and shell-string entries in `pre` and `post`.
Do not write TOML argv arrays for command fields.

Add typed arguments only when the recipe needs an input contract:

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

[recipes.build.arguments.name]
help = "Output filename."
default = "tool"
```

Supported argument types are `string`, `int`, `float`, `bool`, `path`,
`rel_path`, `duration`, and `duration:seconds`. Use `required`, `default`,
`min`, and `max` to keep type and range validation in Shadowtree rather than in
the shell body. Use `values` to expose candidates; safely checkable providers
such as `@enum` also constrain accepted values.

Quote free string and path placeholders in shell commands, for example
`command "{path}"`. Use `{name:shell}` only in an unquoted shell word,
`{name:dq}` only inside double quotes, and `{name:raw}` only for deliberate raw
shell syntax or word splitting.

## Put Cleanup in `post`

```toml
[recipes.integration]
help = "Run integration tests."
pre = ["docker compose up -d"]
cmd = "go test ./integration"
post = ["docker compose down"]
```

Never replace `post` with `trap`. Shadowtree guarantees this lifecycle:

1. Run `pre` in order.
2. Run `cmd` only after successful setup, once or once per `for_each` value.
3. Run `post` after success, failure, or initial cancellation.
4. Preserve the first `pre` or `cmd` failure unless only `post` fails.
5. Sync outputs only after every stage succeeds.

Use `{status:pre}` and `{status:cmd}` in `post` only when cleanup or reporting
must branch on earlier status.

## Compose Without Nested CLI Calls

```toml
[recipes.check]
pre = ["@generate"]
cmd = "@test"
```

Pass reference arguments with brackets, such as `@build[mode=release]`.
Referenced recipes reuse the current workspace and do not run their own
sync-out. Never write `shadowtree <recipe>` inside a recipe to compose workflows.

## Choose One Persistence Model

- Keep the default sandbox for checks, tests, builds, and speculative work.
- Use recipe-local `sync_out` when only named outputs should persist after
  success.
- Set `sandboxed = false` when the workflow must edit the checkout or another
  host location directly.
- Set `sandboxed = "system"` only for the explicit system-container contract;
  do not use runtime names or truthy string aliases.
- Use `system.base_image` only for a literal pinned non-`latest` override. A
  system base must be a pinned Debian or Ubuntu foundation. Shadowtree
  preinstalls `ca-certificates`, `curl`, `tzdata`, and `wget`; declare additional
  distribution packages through `requires.system_packages`.
- System-mode `pre`, `cmd`, nested references, and `post` share one ephemeral
  container; nested references do not perform nested sync-out.
- System mode uses an internal overlay workspace on capable local Docker and
  Podman engines and a copied private fallback for nerdctl, SELinux, or unsafe
  overlay setup. Recipes do not select this internal strategy, and it never
  falls back to ordinary workspace or host execution.
- System mode defaults to `LANG=C.UTF-8` instead of inheriting host locale
  variables. Set `env.LANG` or an `LC_*` value explicitly only when the selected
  base provides that locale.
- Go and Rust system recipes receive provider-owned project caches
  automatically; do not add arbitrary host cache mounts or split compatible
  caches by recipe name, command, ordinary arguments, or lockfile content.
- Never define top-level `sync_out`.
- Prefer narrow sync-out paths; a selected path missing in the sandbox mirrors
  as a host deletion.
- Do not configure sync-out for an unsandboxed recipe; there is no sandbox to
  copy from.

## Validate the Contract

Validate the exact recipe and representative arguments after editing:

```sh
shadowtree --check <recipe> [args...]
shadowtree --check --shell <recipe> [args...]
shadowtree --print <recipe> [args...]
```

- Use `--check` for command shape, references, cycles, workdir, log paths,
  placeholders, typed values, and current or future reserved-name rules.
- Add `--shell` only for expanded `sh` or `bash` syntax.
- Use `--print` to confirm stages, sandbox mode, workdir, arguments, and
  sync-out without running commands.
- Exercise success, failure, cancellation-sensitive cleanup, malformed input,
  unrelated recipe-name collisions, and unknown future values when the changed
  contract exposes those paths.

Keep runtime, schema, editor diagnostics, docs, examples, and this skill aligned
when changing Shadowtree itself. In an ordinary consumer project, change only
the owning config and its directly affected documentation or tests.
