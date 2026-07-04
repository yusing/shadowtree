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

Fish completion:

```sh
shadowtree completion fish > ~/.config/fish/completions/shadowtree.fish
```

Completion is dynamic: it uses configured recipes plus recipes from the detected
or selected profile.

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
shadowtree test
shadowtree build
shadowtree lint
```

Run one-off commands without adding a recipe:

```sh
shadowtree run -- go test ./...
shadowtree run -- npm test
```

Inspect the resolved plan before running a recipe:

```sh
shadowtree --print test
shadowtree --verbose build
```

CLI args replace `default_args`:

```sh
shadowtree test ./internal/recipe
```

runs:

```sh
go test ./internal/recipe
```

## Sandboxed By Default

Sandboxed recipe writes stay inside the temporary workspace. The host checkout
is unchanged unless sync-out is requested.

Recipes that intentionally edit the checkout can opt out:

```toml
[recipes.tidy]
sandboxed = false
cmd = ["go", "mod", "tidy"]
```

Use sync-out when a sandboxed recipe should copy selected results back:

```sh
shadowtree --sync-out internal/generated generate
shadowtree --sync-out dist --sync-out schema.json build-assets
```

Recipe-level sync-out:

```toml
[recipes.generate]
cmd = ["go", "generate", "./..."]
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
pre = [["@generate"]]
cmd = ["go", "test"]
default_args = ["./..."]

[recipes.build]
help = "Build a Go package."
cmd = '''
go build {go_ldflags} "$@"
'''
default_args = ["{project}"]

[recipes.build.arguments.project]
help = "Go package to build."
type = "string"
position = 1
default = "./..."
values = '''
go list -f '{{.ImportPath}}{{"\t"}}{{.Doc}}' ./...
'''

[recipes.tidy]
help = "Tidy Go module files."
sandboxed = false
cmd = ["go", "mod", "tidy"]
```

Prefer argv arrays for exact process execution:

```toml
[recipes.test]
cmd = ["go", "test"]
default_args = ["./..."]
```

Use `@recipe` as the first argv item to invoke another Shadowtree recipe
directly from `cmd`, `pre`, `post`, or argument `values`:

```toml
[recipes.gen-swagger]
help = "Regenerate Swagger files."
cmd = ["go", "generate", "./internal/api"]

[recipes.test]
help = "Regenerate API files, then test."
pre = [["@gen-swagger"]]
cmd = ["go", "test"]
default_args = ["./..."]
```

Referenced recipes run in the current workspace. They do not start a nested
sandbox or run their own sync-out; sync-out is applied only for the top-level
recipe invocation.

In `pre` and `post` command lists, a string that is exactly `@recipe` is also a
recipe reference. Other strings remain shell scripts:

```toml
pre = ["echo 123", "@gen-swagger"]
```

Use bracket-style arguments to pass named or positional arguments from a string
recipe reference:

```toml
pre = ["@build[component=godoxy, mode=dev]"]
```

Use shell script strings when a workflow needs shared shell logic, conditionals,
pipes, or multiple statements.

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

## Built-In Go Recipes

When Shadowtree detects `go.mod` or `go.work`, or when `--profile go` is set, it
adds these recipes:

```text
test       go test ./...
test-race  go test -race ./...
vet        go vet ./...
lint       golangci-lint run ./... or go vet ./...
build      go build ./...
generate   go generate ./...
tidy       go mod tidy
```

`tidy` is unsandboxed by default, so `go.mod` and `go.sum` changes are written
directly to the host checkout. Other built-in Go recipes are sandboxed unless
project config overrides them.

Project config can override any built-in recipe field. Use
`shadowtree --print <recipe>` to inspect the final command.

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

The `install` recipe follows the same convention as `git-agent`: it installs the
binary to `${PREFIX:-$HOME/.local}/bin`, honors `DESTDIR`, `BINDIR`,
`XDG_CONFIG_HOME`, `FISH_CONFIG_DIR`, and `FISH_COMPLETIONS_DIR`, and installs
fish completion only when the fish config directory exists.

The `install-skill` recipe installs the local Shadowtree agent skill to
`${AGENTS_SKILLS_DIR:-$HOME/.agents/skills}/shadowtree`.
