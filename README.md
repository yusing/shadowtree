# Shadowtree

Shadowtree is a project-local development task runner for repeatable checks,
builds, generation, cleanup, install workflows, and other project commands. It
uses one inspectable recipe interface for commands that should be composable,
editor-completable, and sandboxed unless they intentionally edit the checkout.

Recipes run in a sandbox by default. On Linux, Shadowtree uses overlayfs in a
user and mount namespace; when that is unavailable, it falls back to a copied
workspace with the same isolation contract.

## Install

```sh
go install github.com/yusing/shadowtree/cmd/shadowtree@latest
```

## Quick Start

Create a default config:

```sh
shadowtree init
```

Run and inspect recipes:

```sh
shadowtree recipes
shadowtree help test
shadowtree --print test
shadowtree test
shadowtree build
```

## Documentation

- [Manual home](https://yusing.github.io/shadowtree/)
- [Getting started](https://yusing.github.io/shadowtree/getting-started.html)
- [Configuration files](https://yusing.github.io/shadowtree/configuration.html)
- [Recipe fields](https://yusing.github.io/shadowtree/recipes.html)
- [Typed arguments](https://yusing.github.io/shadowtree/typed-arguments.html)
- [Profile selection](https://yusing.github.io/shadowtree/built-in-profiles.html)
- [CLI inspection](https://yusing.github.io/shadowtree/cli-inspection.html)
- [Editor support](https://yusing.github.io/shadowtree/editor-support.html)
- [Full behavior spec](https://yusing.github.io/shadowtree/reference/spec.html)

## Development

Use the local CLI before installing a binary:

```sh
go run ./cmd/shadowtree recipes
go run ./cmd/shadowtree test
go run ./cmd/shadowtree check
go run ./cmd/shadowtree build
```
