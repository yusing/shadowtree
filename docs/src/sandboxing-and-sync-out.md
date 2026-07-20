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
labelled-volume, mount, UID/GID, attached-start, signalling, and forced-removal operations,
and reports progress on stderr. Unusable candidates are diagnosed before the
next candidate is tried. Detection creates no image, volume, workspace, or
container, and system mode never falls back.

System images use a deterministic lower-to-higher chain: the managed
`debian:trixie-slim` foundation (or supported explicit foundation), base
distribution verification plus `ca-certificates`, `curl`, `tzdata`, and `wget`,
composed profile toolchains, normalized additional `requires.system_packages`,
exact recipe tool packages, and plural locked project dependencies. Every
immutable stage
has a content key, canonical local tag, complete ownership labels, and its
lower-stage key as an input. Existing tags are reused only when all expected
labels match; collisions fail without overwriting the image.

Every profile referenced by the top-level lifecycle contributes a toolchain.
Providers install into disjoint versioned paths below
`/opt/shadowtree/toolchains` and publish declared commands through
`/opt/shadowtree/bin`; root profile and reference order never choose a base.
A recipe may set a literal non-`latest` override only in system mode. The
override must be pinned Debian or Ubuntu so Shadowtree can guarantee the base
packages:

- Go uses an exact `toolchain` directive, the documented `1.26.4` release for
  the supported `go 1.26` directive, or fails with `system.base_image` guidance
  for an unsupported unpinned minor.
- Node uses exact Node `24.4.1` tooling; exact npm, pnpm, and Yarn declarations
  select verified manager variants, while Bun selects its exact provider.
- Exact `requires.node_commands` packages are installed by a managed Node/npm
  provider. A Bun project therefore composes Bun with Node/npm when such a
  command is required instead of assuming Bun provides `npm`.
- Rust uses the exact resolved Rust release and verifies any declared host
  qualifier against the selected Linux platform.
- A recipe without a profile still uses the managed Trixie foundation.

```toml
[recipes.build]
sandboxed = "system"
cmd = "go build ./..."

[recipes.build.system]
base_image = "debian:trixie-slim"

[recipes.build.requires]
system_packages = ["git"]
```

Locked dependency preparation runs for every contributed ecosystem whose
recognized lockfile exists:
`go mod download`, `cargo fetch --locked`, npm/pnpm/Yarn/Bun frozen installs
with package lifecycle scripts disabled. Generated Containerfiles use
manifest-only contexts; ordinary project source and private credentials are
never image inputs. Locked Yarn preparation currently requires
`nodeLinker: node-modules` in `.yarnrc.yml`; Plug'n'Play layouts fail during
planning rather than producing an unusable seed. All dependency seeds are validated for confinement and
overlap before any prepared state is copied into the private workspace.

Expanded static inspection reports the foundation, platform, canonical exact
toolchains and manager variants, provenance and required-by origins, reusable
toolchain key, provider setup and verification, dependency plans, seeds, and
caches without probing a runtime. Provider setup guarantees language tooling
coexistence, not native compiler or library availability; cgo, native addons,
Cargo build scripts, and similar builds must declare their compiler, headers,
linker, and project libraries through `requires.system_packages`.

Execution uses one named, explicitly removed container per top-level
lifecycle. The host checkout is copied first; only that private copy is mounted
read-write, at the checkout's canonical path. The container root is read-only,
`/tmp` is a private tmpfs, the helper and resolved plan are private read-only
mounts, and the runtime socket, host home, sibling projects, and host system
paths are not mounted. The current static-helper transport requires a Linux
host binary matching the selected image architecture and fails before building
otherwise. Cancellation sends `TERM` so `post` can run, followed by a bounded
forced kill only if cleanup does not finish. Sync-out runs only after complete
success, while recipe logs are preserved after failures.

System lifecycles do not inherit host locale selections that may be absent from
the slim foundation. Shadowtree removes inherited `LANG`, `LANGUAGE`, and
`LC_*`, then defaults to `LANG=C.UTF-8`. Explicit global or recipe `env` values
are applied afterward and may intentionally select another installed locale.

The system workspace excludes `.git` but retains `.shadowtree.toml` and
included Shadowtree configuration so cross-config recipe references can resolve
inside the container. Source paths that the invoking user cannot read are
omitted with a quoted warning. If an omitted path overlaps a selected sync-out
path, execution fails before the lifecycle starts; `--sync-out-all` likewise
fails when any source path was omitted. This prevents a partial private copy
from being mirrored back as an accidental deletion.

Runtime detection reads the engine's current security state in addition to
checking exact CLI capabilities. Rootless engines use mapped container root;
for Podman, Shadowtree also verifies that the engine reports container root as
mapped to the invoking host UID/GID and passes `--userns=host` so environment
or engine defaults cannot replace that mapping. On SELinux-enabled engines,
private relabelling applies only to the
temporary workspace, helper, plan, and export copies; Shadowtree never
relabels the source checkout. Missing mapping, relabelling, or parseable
security metadata rejects that runtime without retrying weaker flags.

## Project build caches

System mode gives Go a mutable `GOCACHE` volume and Rust a workspace-scoped
Cargo `target` volume. Cache identities include the canonical checkout and
workspace roots, provider format, target platform, exact toolchain, immutable
ABI inputs, and effective UID/GID. Recipe names, commands, ordinary arguments,
run IDs, source hashes, and lockfile contents do not split an otherwise
compatible cache. Different repositories, checkouts, and linked worktrees
never share a volume.

Go cache access is shared-concurrent. Cargo target access is exclusive; a
contending invocation reports that it is waiting and cancellation stops the
wait. Cache-backed sync-out paths are copied into a private ordinary snapshot
only after the complete lifecycle succeeds. Failure and cancellation preserve
the cache but export nothing.

```sh
shadowtree cache inspect
shadowtree cache inspect test
shadowtree cache inspect test --json
shadowtree cache reset test
shadowtree cache reset --all
```

Inspection is read-only and reports unknown size rather than mounting a volume
to measure it. Reset validates exact ownership labels, refuses active volumes,
treats missing volumes as already reset, and confines `--all` to the current
canonical project's caches.

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
