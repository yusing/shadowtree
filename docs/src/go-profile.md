# Go Profile

The Go profile is selected when:

- `--profile go` is provided
- config has `profile = "go"`
- no config is loaded and Shadowtree detects `go.mod` or `go.work` upward from
  the current directory

## Built-In Recipes

```text
build      for each @go-modules: go build ./...
check      @vet && @test
fix        for each @go-modules when go > 1.26: go fix ./...
fmt        for each @go-modules: go fmt ./...
generate   for each @go-modules: go generate ./...
install    for each @go-modules: go install -ldflags="-s -w" ./...
lint       for each @go-modules: golangci-lint run ./... if available, otherwise go vet ./...
run        go -C {cwd} run {command}
test       for each @go-modules: go test ./...
test-race  for each @go-modules: go test -race ./...
tidy       for each @go-modules: go mod tidy; if go.work exists, go work sync
vet        for each @go-modules: go vet ./...
```

## Sandboxing

Built-in `fix`, `fmt`, and `tidy` are unsandboxed by default, so `go fix`,
`go fmt`, `go mod tidy`, and `go work sync` update the host checkout directly.

Other built-in Go recipes are sandboxed unless project config overrides them.

## Module Fan-Out

Module-wide Go built-ins use:

```text
for_each = "@go-modules"
workdir = "{item}"
```

The `./...` package pattern is evaluated inside each module directory, not at
the repo root.

## Arguments and Completion

Built-in `build` exposes an optional positional `pkg` argument with completion
from `@go-main-packages`.

Built-in `install` exposes a named `ldflags` argument defaulting to `-s -w` and
an optional positional `pkg` argument completed from `@go-main-packages`.

Other package-style Go built-ins expose `pkg` completion from `@go-packages`.

`fix` is available when the most common `go.mod` directive is greater than
`1.26`.

`fmt` exposes an optional positional `target` from `@go-packages` plus
`@glob "*.go"`.

Built-in `run` has:

- `cwd`: named argument defaulting to `.`, completed from `@go-modules`
- `command`: required positional `rel_path` argument completed from
  `@go-main-packages` plus `@glob "*.go"`

`command` is interpreted by `go run` after `go -C {cwd}`, so non-default `cwd`
values use paths relative to that directory.
