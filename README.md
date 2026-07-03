# Shadowtree

Shadowtree runs development recipes in a disposable copy of the current project.
It is intended for codegen, tests, builds, and linting without mutating the host
checkout by default.

## Usage

```sh
shadowtree test
shadowtree test -v ./internal/...
shadowtree lint
shadowtree run -- go test ./...
```

By default, recipe writes stay inside the temporary workspace. Copy files back
explicitly:

```sh
shadowtree --sync-out go.mod --sync-out go.sum tidy
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

Recipes that intentionally change the host checkout declare explicit
`sync_out` paths in `.shadowtree.toml`.

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

[recipes.test]
help = "Run tests after regenerating code."
pre = [["go", "generate", "./..."]]
cmd = ["go", "test"]
default_args = ["./..."]

[recipes.tidy]
help = "Tidy Go module files."
cmd = ["go", "mod", "tidy"]
sync_out = ["go.mod", "go.sum"]
```

CLI args replace `default_args`:

```sh
shadowtree test ./internal/recipe
```

runs:

```sh
go test ./internal/recipe
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
