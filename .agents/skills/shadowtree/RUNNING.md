# Running Recipes

Running or inspecting an existing recipe, leaving `.shadowtree.toml` unchanged.
Invocation, inspection, lifecycle, configuration form, and persistence live in
[`SKILL.md`](SKILL.md).

Use an existing recipe when it owns the requested operation. Run the selected
operation once, then only the smallest non-overlapping validation.

When recipe ownership is unknown, use the `[built-in]` and `[overridden]`
markers in `shadowtree recipes`; custom config recipes are unmarked.

## Arguments

- DO use `--` only to forward every following token through the recipe's `{@}`
  placeholder. This is uncommon, and useful when a literal token would otherwise
  read as a named argument: `shadowtree test pkg=./internal/recipe -- --cookie NAME=value`.
- DON'T turn `shadowtree test ./...` into `shadowtree test -- ./...`.
- DO use `--all` only when the recipe declares aggregate support, placing it
  before the recipe name.
- DO put `--` before passthrough flags that take separate bare values under
  `--all`: `shadowtree --all test -- -run TestName`.
- DON'T combine `--all` with an explicit primary target.

## Built-ins

Profile built-ins have established interfaces. Invoke one directly when its name
and intended operation are known, and likewise a project override that keeps the
built-in interface: preserved positional and named arguments, and supported
trailing tool arguments forwarded through `{@}`.

The common Go built-ins:

| Recipe | Operation |
| --- | --- |
| `fmt` | Formats source and persists the edits |
| `test` | Runs tests |
| `test-race` | Runs tests with the race detector |
| `vet` | Runs `go vet` only |
| `check` | Runs `vet`, then `test` |
| `build` | Builds packages or an artifact; never the routine way to check a change |

- DO use `fmt` rather than `shadowtree exec -- gofmt -w ...`.
- DO use `--print` when an override's behavior, not its usage, needs inspection.
- DON'T call `recipes` or `help` merely to confirm one of these known shapes.
- DON'T follow an `unknown argument` error with `help`. The error is conclusive:
  the selected recipe does not expose that token. Correct the override when it is
  meant to keep the built-in contract and changing it is in scope; otherwise omit
  the option, or use the owning tool directly when no recipe exposes the
  operation.

## Working directory

Invoke Shadowtree from the repository or module that owns the target paths.
Execution starts there unless the resolved recipe declares a `workdir`.

Shadowtree may discover a superproject configuration while invoked from a
registered submodule. That configuration can intentionally serve recipes to the
submodule, but it does not move execution to the superproject.

- DO keep the invocation in the target repository or module.
- DON'T change to the configuration directory merely to inspect or run a
  same-name recipe.
- DON'T use `help` to infer configuration ownership from argument value lists.
- DO `--print` the exact invocation when a parent override may depend on
  parent-only paths, assets, or setup. Its resolved `workdir`, variables, and
  stages must make sense from the target working directory.
- DO use the repository or module's authoritative tool when no selected recipe
  owns the operation at that boundary, rather than probing parent recipes with
  `help`.

## Validation scope

Choose one recipe per validation scope: `test` for a focused behavioral check,
`vet` for static analysis alone, `check` when both are required, and `build` only
when the requested outcome is a build artifact or a build-specific condition.

Because `check` already runs `vet` and `test`:

- DON'T run overlapping scopes, such as `shadowtree vet && shadowtree check`,
  `shadowtree test && shadowtree check`, or all three chained.
- DON'T repeat unchanged coverage merely to accumulate successful commands, and
  don't append `build` to routine validation.
- DO treat a focused test during iteration followed by one required broader
  `check` as acceptable; those are different scopes.

## `exec`

Use `shadowtree exec -- <cmd> [args...]` only when no recipe owns the operation
and the arbitrary command specifically needs Shadowtree's sandbox:

```sh
shadowtree exec -- ./scripts/reproduce-bug.sh
```

- DON'T reproduce an existing recipe such as `fmt`, `test`, `test-race`, `vet`,
  `check`, or `build` through `exec`, and don't use `exec` as a ceremonial
  wrapper.
- DO prefer the existing persistent recipe for editing work. Because the sandbox
  is disposable, a formatter, generator, or migration under `exec` leaves the
  host checkout untouched unless its exact outputs are synced out.
- DO add invocation-local sync-out only when the requested output must persist
  and every selected path is in scope:
  `shadowtree --sync-out internal/generated exec -- generate-command`.
- DO run the repository or module's authoritative tool directly when no recipe
  owns the operation and the command does not need the sandbox.
