# Shadowtree

Shadowtree runs development recipes in a disposable workspace for the current
project. It is meant for tests, builds, linting, code generation, and cleanup
commands that should not mutate the host checkout unless you explicitly ask for
that.

On Linux, Shadowtree uses overlayfs in a user and mount namespace by default.
When namespace overlayfs is unavailable, it warns and falls back to a copied
workspace with the same isolation contract.

## Install

```sh
go install github.com/yusing/shadowtree/cmd/shadowtree@latest
```

Bash completion:

```sh
command -v shadowtree >/dev/null 2>&1 && eval "$(shadowtree completion bash)"
```

The `install` recipe appends the same guarded eval line to `~/.bashrc`.

Fish completion:

```sh
shadowtree completion fish > ~/.config/fish/completions/shadowtree.fish
```

Zsh completion:

```sh
command -v shadowtree >/dev/null 2>&1 && eval "$(shadowtree completion zsh)"
```

Completion is dynamic: it uses configured recipes plus recipes from the selected
profile. Without a config file, Shadowtree detects the nearest Go or Node
project marker upward from the current directory and exposes matching built-ins.

## Quick Start

Create a default TOML config in a project:

```sh
shadowtree init
```

In a project with Shadowtree config:

```sh
shadowtree config
shadowtree recipes
shadowtree help test
shadowtree help test color=false
shadowtree test
shadowtree build
shadowtree lint
```

Run one-off commands without adding a recipe:

```sh
shadowtree exec -- go test ./...
shadowtree exec -- npm test
```

Inspect the resolved plan before running a recipe:

```sh
shadowtree --print test
shadowtree --verbose build
```

Built-in Go workflow recipes run once per discovered Go module:

```sh
shadowtree --print test
```

prints the module fan-out:

```text
for_each: @go-modules
workdir: {item}
main: go test ./... {@}
```

## Sandboxed By Default

Sandboxed recipe writes stay inside the temporary workspace. The host checkout
is unchanged unless sync-out is requested.

Recipes that intentionally edit the checkout can opt out:

```toml
[recipes.tidy]
sandboxed = false
for_each = "@go-modules"
workdir = "{item}"
cmd = "go mod tidy"
post = ["if test -f go.work; then go work sync; fi"]
```

Use sync-out when a sandboxed recipe should copy selected results back:

```sh
shadowtree --sync-out internal/generated generate
shadowtree --sync-out dist --sync-out schema.json build-assets
```

Recipe-level sync-out:

```toml
[recipes.generate]
cmd = "go generate ./..."
sync_out = ["internal/generated"]
```

A selected path missing from the sandbox is mirrored as a deletion on the host.
Prefer narrow `--sync-out PATH` or recipe `sync_out` over `--sync-out-all`.

## Configure Recipes

Shadowtree discovers config upward from the current directory:

```text
.shadowtree.toml
```

Discovery stops at the git root when the current directory is inside a Git
repository.

Shadowtree config is TOML:

```toml
profile = "go"
shell = "sh"

shell_prelude = '''
require_tool() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "$1 is required" >&2
    exit 1
  }
}
'''

[vars]
go_ldflags = "-buildvcs=false"

[recipes.test]
help = "Run tests after regenerating code."
pre = ["@generate"]
cmd = "go test {pkg} {@}"

[recipes.test.arguments.pkg]
type = "rel_path"
position = 1
required = true
values = "@go-packages"

[recipes.build]
help = "Build a Go package."
cmd = "go build {go_ldflags} {project} {@}"

[recipes.build.arguments.project]
help = "Go main package to build."
type = "string"
position = 1
default = "./cmd/shadowtree"
values = "@go-main-packages"

[recipes.tidy]
help = "Tidy Go module files."
sandboxed = false
for_each = "@go-modules"
workdir = "{item}"
cmd = "go mod tidy"
post = ["if test -f go.work; then go work sync; fi"]
```

Use shell strings for process execution:

```toml
[recipes.test]
cmd = "go test {pkg} {@}"

[recipes.test.arguments.pkg]
type = "rel_path"
required = true
values = "@go-packages"
```

Command strings run through the configured shell after placeholder expansion.
A string that is exactly `@recipe` or `@path:recipe` invokes another recipe;
other strings run in the shell. Put defaults directly in `cmd` through typed
`arguments`.

Use a recipe reference from `cmd`, `pre`, or `post`:

```toml
[recipes.gen-swagger]
help = "Regenerate Swagger files."
cmd = "go generate ./internal/api"

[recipes.test]
help = "Regenerate API files, then test."
pre = ["@gen-swagger"]
cmd = "go test {pkg} {@}"

[recipes.test.arguments.pkg]
type = "rel_path"
required = true
values = "@go-packages"
```

Referenced recipes run in the current workspace. They do not start a nested
sandbox or run their own sync-out.

In command lists, only strings that are exactly `@recipe` are direct recipe
references:

```toml
pre = ["echo 123", "@gen-swagger"]
```

Use bracket-style arguments to pass named or positional arguments:

```toml
pre = ["@build[component=godoxy, mode=dev]"]
```

Use shell script strings when a workflow needs shared shell logic, conditionals,
pipes, or multiple statements.

Use `for_each` to fan out one recipe command across values from the same value
providers used by argument `values`. `pre` runs once before all items, `cmd`
runs once per item, `post` runs once after the loop, and the first failing item
stops later items:

```toml
[recipes.lint]
help = "Run golangci-lint in every Go module."
for_each = "@go-modules"
workdir = "{item}"
cmd = "golangci-lint run ./..."
```

`workdir` is optional for any recipe and makes the main command run from a
relative workspace path. With `for_each`, `workdir` is expanded per item:
`{item}` is the candidate value, `{item_help}` is its help text when present,
and `{item_index}` is the zero-based index.

## Typed Arguments

Recipes can define typed arguments. Arguments can be passed positionally, by
name, or with bracket-style syntax:

```sh
shadowtree build ./cmd/shadowtree
shadowtree build project=./cmd/shadowtree
shadowtree 'build[project=./cmd/shadowtree]'
```

Supported argument types are `string`, `int`, `float`, `bool`, `path`, and
`rel_path`. `path` accepts absolute and relative paths and provides fish-style
path completion for relative paths, absolute paths, and `~/`. `rel_path`
accepts relative paths only and completes relative checkout paths. Path
arguments can set `path_kind` to `any`, `file`, `dir`, or `executable` to
filter completion candidates.

Use `{@}` in `cmd` to splice leftover recipe CLI args:

```toml
[recipes.test]
cmd = "go test {pkg} {@}"

[recipes.test.arguments.pkg]
type = "rel_path"
position = 1
required = true
values = "@go-packages"
```

For typed recipes, positional values and known `key=value` argument values are
consumed by typed arguments and excluded from `{@}`. Unknown identifier
`key=value` tokens remain errors; command flags such as `-run=TestName` pass
through. Use `--` to pass following tokens literally to `{@}`.

## Built-In Profiles

Supported profiles are `go` and `node`. A profile is selected by explicit
`--profile`, then by config `profile`, then by marker detection only when no
config file is loaded. Detection walks upward from the current directory:
`package.json` selects `node`, and `go.mod` or `go.work` selects `go`. The
nearest marker wins; if Go and Node markers are in the same directory, Go wins.

Configs that omit `profile` do not receive detected built-ins. This keeps local
config recipes exact unless the config opts into a profile.

## Built-In Go Recipes

When `--profile go` is set, config has `profile = "go"`, or Shadowtree runs in a
detected Go project without a config file, it adds these recipes:

```text
build      for each @go-modules: go build ./...
check      @vet && @test
fix        for each @go-modules when go > 1.26: go fix ./...
fmt        for each @go-modules: go fmt ./...
generate   for each @go-modules: go generate ./...
lint       for each @go-modules: golangci-lint run ./... or go vet ./...
run        go run {command}
test       for each @go-modules: go test ./...
test-race  for each @go-modules: go test -race ./...
tidy       for each @go-modules: go mod tidy; if go.work exists, go work sync
vet        for each @go-modules: go vet ./...
```

`fix`, `fmt`, and `tidy` are unsandboxed by default, so `go fix`, `go fmt`,
`go mod tidy`, and `go work sync` write directly to the host checkout. Other built-in Go
recipes are sandboxed unless project config overrides them. Module-wide Go built-ins use
`for_each = "@go-modules"` and `workdir = "{item}"`; the `./...` package pattern
is evaluated inside each module directory, not at the repo root. Built-in
`build` exposes an optional positional `pkg` argument with shell completion from
`@go-main-packages`; other package-style Go built-ins expose `pkg` completion
from `@go-packages`; `fix` is available when the most common
`go.mod` directive is greater than `1.26`. `fmt` exposes an optional positional `target`
from `@go-packages` plus `@glob "*.go"`. Built-in `run` takes a required
positional `command` argument with `rel_path` type and completes from
`@go-main-packages` plus `@glob "*.go"`.

Project config can override any built-in recipe field. Use
`shadowtree --print <recipe>` to inspect the final command.

## Built-In Node Recipes

When `--profile node` is set, config has `profile = "node"`, or Shadowtree runs
in a detected Node project without a config file, it loads the nearest
`package.json` and runs built-ins from that package directory. Node built-ins are
unsandboxed by default because package-manager and framework commands commonly
update lockfiles, dependency state, caches, and generated outputs.

Package manager detection uses `packageManager` first (`pnpm`, `yarn`, `bun`,
or `npm`), then lockfiles in this order: `pnpm-lock.yaml`, `yarn.lock`,
`bun.lockb`, `bun.lock`, `package-lock.json`, `npm-shrinkwrap.json`. The default
is `npm`.

Default Node recipes:

```text
install    <pm> install
dev        package script dev, or inferred framework dev command
build      package script build, or inferred framework build command
start      package script start, or inferred framework preview/start command
test       package script test, or vitest/jest/playwright/bun test fallback
lint       package script lint, or ESLint/Oxlint/Biome
fmt        package script fmt/format, or Prettier/Oxfmt/Biome
typecheck  package script typecheck/type-check, or vue-tsc/svelte-check/tsc
check      available lint, typecheck, and test recipes in that order
```

Framework inference recognizes `next`, `vite`, `nuxt`, `astro`, and
`@sveltejs/kit`. Package scripts also fill recipe gaps. Script names are
normalized before becoming recipe names: `:` and characters outside
`[A-Za-z0-9._-]` become `-`, repeated dashes collapse, and edge dashes are
trimmed. For example, `lint:fix` becomes recipe `lint-fix`, but the generated
recipe still runs the original `lint:fix` script.

## Editor Support

Shadowtree includes a shared JSON Schema for `.shadowtree.toml` plus editor
integration files for Zed and VS Code under `editors/`.

The Zed extension provides a dedicated `Shadowtree TOML` language, syntax
highlighting, Shadowtree-specific highlighting, shell semantic highlighting for
script-valued fields, and LSP completion, diagnostics, and semantic tokens.

The VS Code extension binds the shared schema to Shadowtree TOML files through
Even Better TOML. Completion, hover, and validation come from that extension.

Install the Zed language server with:

```sh
go install github.com/yusing/shadowtree/cmd/shadowtree-lsp@latest
```

See `docs/spec.md` and the editor integration READMEs for implementation
details.

## Development

This project uses Shadowtree for its own development tasks. Before installing a
`shadowtree` binary, run the local CLI with `go run`:

```sh
go run ./cmd/shadowtree recipes
go run ./cmd/shadowtree test
go run ./cmd/shadowtree check
go run ./cmd/shadowtree build
go run ./cmd/shadowtree install
```

After installing or building `shadowtree`, use the shorter form:

```sh
shadowtree test
shadowtree check
shadowtree build
shadowtree install
shadowtree fmt
shadowtree tidy
shadowtree install-skill
```

Recipes that intentionally change the host checkout set `sandboxed = false` in
`.shadowtree.toml`.

The `install` recipe uses default `go install`, honors `FISH_CONFIG_DIR` and
`FISH_COMPLETIONS_DIR`, generates completion from `shadowtree` on `PATH`,
installs fish completion when `fish` is available, and appends single guarded
eval lines to `~/.bashrc` and `~/.zshrc` when those shells are available.

The `install-skill` recipe installs the local Shadowtree agent skill to
`${AGENTS_SKILLS_DIR:-$HOME/.agents/skills}/shadowtree`.
