# Go Profile

The Go profile is selected when:

- `--profile go` is provided
- config has `profile = "go"`
- no config is loaded and Shadowtree detects `go.mod` or `go.work` upward from
  the current directory

## Built-In Recipes

```text
build      go build {pkg}; --all targets every main package
check      @vet && @test
fix        go fix {pkg} when go > 1.26; --all targets every package
fmt        go fmt {target}; --all targets every package
generate   go generate {pkg}; --all targets every package
install    go install -ldflags="-s -w" {pkg}; --all targets every main package
lint       lint {pkg}; --all targets every package
run        go -C {cwd} run {command}
test       go test {pkg}; --all targets every package
test-race  go test -race {pkg}; --all targets every package
tidy       go mod tidy; --all targets every module, then syncs go.work
vet        go vet {pkg}; --all targets every package
```

## Sandboxing

Built-in `fix`, `fmt`, and `tidy` are unsandboxed by default, so `go fix`,
`go fmt`, `go mod tidy`, and `go work sync` update the host checkout directly.

Other built-in Go recipes are sandboxed unless project config overrides them.

## Aggregate Scope

Normal Go built-ins are leaf recipes: they execute once from the recipe
workspace and preserve explicit target paths. Use the global flag before the
recipe to select its aggregate plan:

```sh
shadowtree fmt ./internal/recipe
shadowtree --all fmt
shadowtree --all build
```

Package-wide aggregate plans batch `./...` once per module. `build` and
`install` instead discover main packages and execute each from its owning
module with a module-relative target. `tidy` runs once per module. `run` does
not support `--all` because Shadowtree has no defined policy for supervising
multiple runnable processes.

Main-package discovery uses the recipe's resolved environment and forwarded Go
build-context flags, so `GOOS`, `GOARCH`, `GOFLAGS`, and `-tags` select the same
packages for discovery and execution.

Aggregate target discovery runs after `pre` in the active host checkout or
sandbox. An empty aggregate target set is an error. Project overrides do not
inherit a built-in aggregate plan.

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
