# Shadowtree

Shadowtree runs development recipes in a disposable workspace for the current
project. On Linux it uses overlayfs in a user and mount namespace by default,
and falls back to a copied workspace when namespace overlayfs is unavailable.
It is intended for codegen, tests, builds, and linting without mutating the
host checkout by default.

## Usage

```sh
shadowtree test
shadowtree test -v ./internal/...
shadowtree lint
shadowtree run -- go test ./...
```

By default, recipe writes stay inside the sandbox. On hosts that do not support
namespace overlayfs, Shadowtree warns and uses the same sandbox contract with a
copied workspace. Recipes that should edit the checkout directly can opt out:

```toml
[recipes.tidy]
sandboxed = false
cmd = ["go", "mod", "tidy"]
```

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
```

Recipes that intentionally change the host checkout set `sandboxed = false` in
`.shadowtree.toml`.

The `install` recipe follows the same convention as `git-agent`: it installs the
binary to `${PREFIX:-$HOME/.local}/bin`, honors `DESTDIR`, `BINDIR`,
`XDG_CONFIG_HOME`, `FISH_CONFIG_DIR`, and `FISH_COMPLETIONS_DIR`, and installs
fish completion only when the fish config directory exists.

## Config

Shadowtree discovers config upward from the current directory:

1. `.shadowtree.toml`
2. `.shadowtree.yaml`
3. `.shadowtree.yml`

TOML is the default format:

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
pre = [["go", "generate", "./..."]]
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

CLI args replace `default_args`:

```sh
shadowtree test ./internal/recipe
```

runs:

```sh
go test ./internal/recipe
```

Recipes can define typed arguments. Arguments can be passed positionally,
by name, or with bracket-style syntax:

```sh
shadowtree build ./cmd/shadowtree
shadowtree build project=./cmd/shadowtree
shadowtree 'build[project=./cmd/shadowtree]'
```

Supported argument types are `string`, `int`, `float`, and `bool`.

## Editor Support

Shadowtree includes a shared JSON Schema for `.shadowtree.toml` plus editor
integration files for Zed and VS Code under `editors/`. These provide
completion, diagnostics, schema validation, Shadowtree-specific highlighting,
and shell semantic highlighting inside script-valued config fields. See
`docs/spec.md` and the editor integration READMEs for implementation details.

Install the Zed language server with:

```sh
go install github.com/yusing/shadowtree/cmd/shadowtree-lsp@latest
```

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

Project config can override any built-in recipe field.

## Fish Completion

```sh
shadowtree completion fish > ~/.config/fish/completions/shadowtree.fish
```

Completion is dynamic: it uses configured recipes plus recipes from the detected
or specified profile.
