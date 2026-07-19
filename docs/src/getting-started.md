# Getting Started

## Install

```sh
go install github.com/yusing/shadowtree/cmd/shadowtree@latest
```

## Shell Completion

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

## Create a Config

Create a default TOML config in a project:

```sh
shadowtree init
```

Shadowtree discovers `.shadowtree.toml` upward from the current directory.
Discovery stops at the outer Git repository boundary when the current directory
is inside a Git repository. Registered submodules continue into their
superprojects when they contain no nearer config. Independent nested repositories
and linked worktrees remain boundaries.

## Run Recipes

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

## Inspect Plans

Inspect the resolved plan before running a recipe:

```sh
shadowtree --print test
shadowtree --print --expanded test
shadowtree --check --shell test
shadowtree --verbose build
```

`--verbose` prints compact stage boundaries such as `== cmd: @build ==` before
commands run. Multiline scripts are shown as `<script>`, so verbose headers do
not dump script bodies.

Built-in Go workflow recipes normally run once. Select the recipe-specific
aggregate plan explicitly:

```sh
shadowtree --all --print test
```

prints its semantic scope and target source:

```text
scope: all
target_domain: packages
target_source: go-packages
main: go test {item} {@}
```
