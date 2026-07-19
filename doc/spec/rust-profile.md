# Rust profile requirements

The Rust profile is an independently useful host workflow and a prerequisite
for the system sandbox. It must be implemented and verified before system
sandbox work begins. Container code consumes this profile contract; it does not
own Cargo or Rust discovery.

## REQ-RUST-001 — Select the Rust profile

Shadowtree must support `rust` through the existing profile owners:
`detect.Profile`, `recipe.SupportsProfile`, and `recipe.Builtins`.

An explicit `profile = "rust"` or equivalent CLI selection wins. Without an
explicit profile, the nearest supported project marker wins by directory
distance. At one directory, existing Go and Node precedence remains stable and
`Cargo.toml` is considered after those markers. Detection must not inspect
recipe command strings or require a container runtime.

Acceptance behavior:

- a directory whose nearest sole marker is `Cargo.toml` selects `rust`;
- an explicit non-Rust supported profile overrides a nearer Cargo marker;
- same-directory Go/Node/Cargo collisions follow the documented stable order;
- an unknown explicit profile is rejected rather than detected over;
- a directory with no supported marker returns no detected profile.

## REQ-RUST-002 — Resolve canonical Rust project metadata

The profile must resolve one canonical Cargo workspace or package root and
structured metadata for:

- workspace root and member manifests;
- root and member `Cargo.toml` paths;
- `Cargo.lock` when present;
- exact Rust toolchain identity;
- toolchain provenance;
- host triple;
- selected target triple; and
- cache-contract inputs required by later consumers.

Toolchain precedence is the nearest applicable `rust-toolchain.toml`, then
`rust-toolchain`, then the Shadowtree release's documented exact default. A
supported exact channel/version is normalized. Unsupported, malformed, or
ambiguous toolchain declarations fail with the owning path and value; they do
not silently use the release default.

Metadata resolution is reusable production behavior, not a test hook. Callers
must consume this owner rather than reparsing Cargo files or executing a second
workspace detector.

## REQ-RUST-003 — Provide host-usable Cargo built-ins

The Rust profile must provide the smallest coherent Cargo built-ins for normal
development, including at least `check`, `test`, `build`, `run`, `fmt`, and
`clippy` when the resolved toolchain supplies the required component.

Built-ins must use existing recipe concepts for arguments, `workdir`, aggregate
execution, sandbox policy, lifecycle stages, logging, cancellation, and
sync-out. They must run usefully on the host before system mode exists. Missing
optional Rust components produce actionable guidance; Shadowtree must not
install or mutate a host toolchain implicitly.

Commands must preserve Cargo's standard target/profile/feature behavior and
forward deliberately supported trailing arguments through existing typed
argument rules. Workspace/member aggregate behavior must be explicit rather
than inferred as arbitrary directory fan-out.

## REQ-RUST-004 — Expose dependency and cache contracts

The profile must expose, without container-specific types:

- the inputs for `cargo fetch --locked`;
- whether locked preparation is available;
- Cargo registry and Git source-cache destinations;
- the normal project/workspace `target` destination;
- exact compiler, host, target, and workspace compatibility inputs; and
- the provider concurrency policy needed for target-cache use.

The profile does not itself create persistent container volumes. It supplies
the semantic information required by REQ-SBOX-007 after that backend exists.

## REQ-RUST-005 — Keep all public surfaces aligned

Adding the profile must update runtime validation, configuration schema, LSP
completion and diagnostics, shell completion, README, mdBook behavior spec,
examples, and agent recipe-authoring guidance where those surfaces enumerate
profiles or built-ins.

Focused tests must cover positive detection, explicit override, malformed
toolchains, unrelated marker collisions, unknown future profile values,
workspace/member resolution, built-in success and failure, aggregate behavior,
cancellation, and redirected output. The repository check must pass without any
system-sandbox implementation or runtime installed.
