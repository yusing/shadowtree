# Repository Guidelines

## Project Structure & Module Organization

Shadowtree is a Go CLI module (`github.com/yusing/shadowtree`). The entry point lives in `cmd/shadowtree/`. Internal packages live under `internal/`: `recipe` resolves configured recipes and Go profile defaults, `runner` manages sandboxed execution with Linux namespace overlayfs or copied-workspace fallback plus sync-out, `configfile` loads project config, `completion` emits shell completion, `shadowtreelsp` implements editor completion/semantic tokens, and `detect` handles profile detection. Design notes and behavioral spec live in `docs/spec.md`. Editor integrations live under `editors/`: `zed-shadowtree` is a Zed extension with a nested Rust crate and Tree-sitter queries, and `vscode-shadowtree` is a VS Code schema-binding manifest. The shared Shadowtree config schema lives at `schemas/shadowtree.schema.json`. Build outputs go in `bin/`; editor build outputs such as `editors/zed-shadowtree/target/` should stay untracked. Agent skill in `.agents/skills/shadowtree`

## Operating Principles

Do not rely on `shadowtree` skill since you're currently working `shadowtree`.

On significant changes, update (where applicable) README, spec, json schema,
agent `SKILL.md`, lsp (syntax highlighting, autocomplete and diagnostic), shell
completion, and the reference configs under `examples/all-features*.shadowtree.toml`.

Follow global duplication-control rules. In this repo, prefer these existing
sources of truth over local copies:

- supported script shells: `internal/scriptref.SupportedShell` and
  `internal/scriptref.Parser`
- command shape and recipe references: `recipe.ValidateCommand`,
  `recipe.IsScriptCommand`, `recipe.ParseRecipeReference`, and
  `scriptref.Parse`
- profile support: `recipe.SupportsProfile` and `recipe.Builtins`
- global flags: `internal/globalflag.All` and `internal/globalflag.Lookup`
- config schema surfaces: `schemas/shadowtree.schema.json`, runtime, LSP
  completions/diagnostics, editor docs, agent skill docs, and the all-features
  example configs must stay aligned

## Build, Test, and Development Commands

Use the local CLI before installing a binary:

```sh
go run ./cmd/shadowtree test
go run ./cmd/shadowtree check
go run ./cmd/shadowtree build
go run ./cmd/shadowtree fmt
go run ./cmd/shadowtree tidy
```

`test` runs `go test ./...`. `check` runs `go vet ./...` then tests. `build` writes `bin/shadowtree` with `-buildvcs=false`. `fmt` runs `gofmt -w cmd internal`. `tidy` updates `go.mod` and `go.sum`. After installing, the shorter `shadowtree <recipe>` form is equivalent. Prefix test, build, lint, and static-check commands with `rtk` when running them from the shell.

For editor/schema work, use focused verification rather than Go checks unless Go behavior changed:

```sh
rtk taplo check editors/zed-shadowtree/extension.toml editors/zed-shadowtree/languages/shadowtree-toml/config.toml
rtk cargo +stable check --manifest-path editors/zed-shadowtree/Cargo.toml
rtk go test ./internal/shadowtreelsp
node -e "for (const path of ['schemas/shadowtree.schema.json','editors/vscode-shadowtree/package.json']) JSON.parse(require('fs').readFileSync(path, 'utf8'));"
```

When changing Zed query files, also compile them against the pinned `tree-sitter-toml` grammar from `editors/zed-shadowtree/extension.toml` with `tree-sitter query` or `npx tree-sitter-cli query`.

## Coding Style & Naming Conventions

This project targets Go 1.26.4. Run `gofmt` on Go changes. Use short lowercase package names, MixedCaps for exported identifiers, mixedCaps for internal identifiers, and concise godoc for exported API. Prefer early returns, wrapped errors with `%w`, lowercase error strings, and modern standard library helpers appropriate for Go 1.26. Keep new behavior in existing packages unless a new package boundary is clearly justified.

## Testing Guidelines

Tests use the standard `testing` package and live beside code as `*_test.go`. Name tests by behavior, for example `TestResolveAppliesTypedArguments`. Prefer table tests when they clarify cases, but keep helpers inside test files. Run focused tests during iteration, such as `go test ./internal/recipe`, then `go run ./cmd/shadowtree check` before broad changes are complete.

Schema changes should be checked with a representative Shadowtree TOML file through a schema-aware tool, not only by JSON parsing the schema. Zed query changes should be checked against a sample `.shadowtree.toml` so invalid node names, captures, and predicates fail before review. LSP completion changes should include focused `internal/shadowtreelsp` tests.

## Agent-Specific Instructions

Sandboxed recipes isolate writes by default. On Linux, namespace overlayfs runs commands at the source checkout path inside the namespace so Go test caching remains stable; writes land in the overlay upperdir unless explicitly synced. When namespace overlayfs is unavailable, Shadowtree warns and falls back to a copied workspace.

Use `sync_out` or `--sync-out` when a sandboxed recipe should mirror selected paths back to the host checkout. A missing selected path is mirrored as a deletion. Use `--sync-out-all` only when the whole sandbox should be applied back. Recipes that intentionally edit the checkout directly should set `sandboxed = false`.

Do not overwrite host checkout files unless the requested recipe, explicit sync-out, or edit requires it. Prefer existing utilities and patterns, avoid unrelated refactors, and verify the specific surface changed.
