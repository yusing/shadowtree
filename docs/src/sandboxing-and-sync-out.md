# Sandboxing and Sync-Out

Sandboxed recipe writes stay inside the temporary workspace. The host checkout
is unchanged unless sync-out is requested.

The `sandboxed` field accepts three values:

- omitted or `true`: namespace overlayfs with copied-workspace fallback;
- `false`: direct host-checkout execution;
- `"system"`: the system-container backend, with no fallback to either other
  mode.

Static help, completion, config validation, and `--print` do not probe a
container runtime. A system plan reports `runtime: <not probed>`. Execution and
`--check` probe Docker, Podman, then nerdctl in that order. Each probe is bounded
and non-interactive, verifies engine access plus the required image, build,
labelled-volume, mount, UID/GID, signalling, and automatic-removal operations,
and reports progress on stderr. Unusable candidates are diagnosed before the
next candidate is tried. Detection creates no image, volume, workspace, or
container, and system mode never falls back.

System images use a deterministic lower-to-higher chain: pinned external base,
base metadata, profile tooling, normalized `requires.system_packages`, exact
recipe tool packages, and locked project dependencies. Every immutable stage
has a content key, canonical local tag, complete ownership labels, and its
lower-stage key as an input. Existing tags are reused only when all expected
labels match; collisions fail without overwriting the image.

Profiles select pinned Debian/Ubuntu slim defaults. A recipe may set a literal
non-`latest` override only in system mode:

- Go uses an exact `toolchain` directive, the documented `1.26.4` release for
  the supported `go 1.26` directive, or fails with `system.base_image` guidance
  for an unsupported unpinned minor.
- Node uses `node:24.4.1-bookworm-slim`; exact npm, pnpm, and Yarn declarations
  select verified tooling, while Bun selects its exact slim release image.
- Rust uses the exact resolved Rust release and verifies any declared host
  qualifier against the selected Linux platform.
- A recipe without a profile uses `ubuntu:24.04`.

```toml
[recipes.build]
sandboxed = "system"
cmd = "go build ./..."

[recipes.build.system]
base_image = "golang:1.26.4-bookworm"

[recipes.build.requires]
system_packages = ["ca-certificates", "git"]
```

Locked dependency preparation runs only when the recognized lockfile exists:
`go mod download`, `cargo fetch --locked`, npm/pnpm/Yarn/Bun frozen installs
with package lifecycle scripts disabled. Generated Containerfiles use
manifest-only contexts; ordinary project source and private credentials are
never image inputs.

On Linux, Shadowtree uses overlayfs in a user and mount namespace by default.
When namespace overlayfs is unavailable, it warns and falls back to a copied
workspace with the same isolation contract.

## Edit the Host Checkout Directly

Recipes that intentionally edit the checkout can opt out:

```toml
[recipes.tidy]
sandboxed = false
for_each = "@go-modules"
workdir = "{item}"
cmd = "go mod tidy"
post = ["if test -f go.work; then go work sync; fi"]
```

## Sync Selected Outputs

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
