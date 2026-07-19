# Rust Profile

The Rust profile is selected by `--profile rust`, `profile = "rust"`, or the
nearest `Cargo.toml` when no config is loaded. Same-directory Go and Node
markers retain precedence over Cargo.

## Built-In Recipes

```text
check   cargo +<toolchain> check
test    cargo +<toolchain> test
build   cargo +<toolchain> build
run     cargo +<toolchain> run
fmt     cargo +<toolchain> fmt
clippy  cargo +<toolchain> clippy
```

Trailing arguments are forwarded to Cargo. Use Cargo's own `--` delimiter when
passing arguments to a binary or test executable. `--all` adds Cargo's explicit
workspace flag for `check`, `test`, `build`, `fmt`, and `clippy`. It rejects
`run` because a workspace can contain multiple binaries.

`fmt` and `clippy` check their optional toolchain components before running and
print installation guidance when unavailable. Shadowtree never mutates the
host toolchain implicitly.

## Project and Toolchain Resolution

Shadowtree resolves Cargo metadata into one canonical workspace root, root and
member manifests, optional `Cargo.lock`, and Cargo target directory. Toolchain
selection uses the nearest `rust-toolchain.toml`, then `rust-toolchain`, then
Shadowtree's exact release default, `1.96.0`. Declarations must contain an exact
version; ambiguous channels such as `stable` and unknown future values fail
with the owning path and value.

The resolved contract includes the exact compiler, host triple, selected target
triple, Cargo registry and Git cache locations, workspace target directory, and
an exclusive project-scoped target-cache key. A lockfile enables the canonical
preparation command `cargo fetch --locked`.

Rust built-ins are sandboxed by default. Use normal recipe or invocation
sync-out controls for generated artifacts that should reach the host checkout.
