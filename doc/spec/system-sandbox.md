# System sandbox requirements

Status: draft. Implementation begins only after REQ-RUST-001, REQ-RUST-002,
REQ-RUST-003, REQ-RUST-004, and REQ-RUST-005 are satisfied and independently
verified.

System mode delegates image storage, image building, layer caching, container
isolation, networking, volumes, and container cleanup to Docker, Podman, or
nerdctl. Shadowtree owns recipe resolution, deterministic plans and identities,
the temporary project workspace, lifecycle behavior, sync-out, diagnostics,
and cancellation.

## REQ-SBOX-001 — Select system sandbox mode explicitly

The existing recipe `sandboxed` field accepts exactly:

| Value | Behavior |
| --- | --- |
| omitted or `true` | Existing namespace-overlayfs or copied-workspace sandbox |
| `false` | Direct host-checkout execution |
| `"system"` | System container runtime with immutable images and an ephemeral container |

`"system"` is the only accepted string. Other strings and types fail config
validation; they are never truthy aliases or fallback requests. System mode is
sandboxed for lifecycle and sync-out: it never mounts the source checkout
read-write and never falls back to `true` or `false` behavior.

Schema, runtime decoding, recipe inheritance, built-in overrides, LSP,
completion, help, print output, examples, docs, and agent guidance must expose
the same three-value contract.

## REQ-SBOX-002 — Detect one usable system runtime

Execution and host-capability validation probe `docker`, `podman`, then
`nerdctl` in stable order. Executable presence is insufficient: a bounded,
non-interactive probe must verify the client can reach its engine and supports
the image, build, labelled-volume, nested-mount, read-only bind, UID/GID,
signalling, and automatic-removal operations Shadowtree needs.

An installed but unusable candidate records a concise diagnostic and detection
continues. If none is usable, Shadowtree fails before pulling, building,
creating a workspace/volume, or running user code. The error reports every
candidate and failure reason.

Listing, help, completion, config loading, and static `--print` never require or
probe a runtime. Static output reports `runtime: <not probed>`. Runtime-specific
flags and result parsing remain inside one direct-argv adapter; Shadowtree does
not construct shell commands.

## REQ-SBOX-003 — Select profile-owned default images and tooling

An optional `[recipes.<name>.system].base_image` overrides the default and is
valid only when effective `sandboxed = "system"`. Runtime arguments and
`var_commands` cannot select image identity.

Without an override, the effective profile selects a release-pinned, tested,
non-`latest` slim image family:

- Go: supported Go directive version or documented release default;
- Node with npm, pnpm, or Yarn: release-pinned slim Node;
- Node with Bun: release-pinned slim Bun;
- Rust: the exact/default toolchain resolved by REQ-RUST-002;
- no profile: release-pinned minimal Ubuntu.

Explicit profile selection wins over detection. Commands are never inspected to
guess a profile or distribution. A locally present mutable base tag is not
refreshed implicitly; digest pins are recommended and future refresh policy is
separate work.

Node/Bun planning must reuse the existing package-manager detector and extend
that owner to return name, exact declared version, provenance, and project path.
Precedence remains `packageManager`, recognized lockfile, then npm. pnpm and
Yarn tooling uses Corepack with a managed install directory and an exact
declared or release-pinned manager version. Bun selects the Bun family. npm uses
the base version unless an exact supported pin selects managed tooling. Tooling
is version-checked after setup.

## REQ-SBOX-004 — Build five ordered immutable stages

The final recipe image uses this lower-to-higher chain:

```text
external profile/default image
-> base
-> tooling
-> system packages
-> recipe packages (`requires`)
-> project dependencies (final recipe image)
```

Each stage has a deterministic canonical tag, full untruncated ownership/key
labels, and its immediate lower-stage key as an input. Shadowtree walks upward,
validates each expected tag, and builds from the nearest valid lower stage.

A changed stage may rebuild stages above it but never invalidates, retags, or
rebuilds a lower stage. Thus a tooling change preserves base; a system-package
change preserves base/tooling; a recipe-package change preserves all three
lower stages; and a dependency change rebuilds only dependencies.

Canonical reusable tags conceptually follow:

```text
shadowtree.local/stage/base:<key>
shadowtree.local/stage/tooling:<key>
shadowtree.local/stage/system-packages:<key>
shadowtree.local/stage/recipe-packages:<key>
shadowtree.local/stage/dependencies:<key>
```

The recipe-scoped reference
`shadowtree.local/<project-key>/<recipe-key>:<dependency-key>` aliases the exact
dependency-stage image. Base/tooling and other immutable stages may be shared
across compatible projects. Existing tags with absent or mismatched labels are
collisions and are neither executed nor overwritten silently.

## REQ-SBOX-005 — Give each immutable stage one owner

Stage responsibilities are:

1. Base derives from the resolved external image and adds only versioned
   base-plan metadata. The invocation helper is not image content.
2. Tooling installs profile/package-manager tools and shims under a managed
   prefix. Its key omits project/recipe identity so compatible projects share
   exact tooling such as one Corepack/pnpm image.
3. System packages installs normalized `requires.system_packages` using one
   supported distribution provider and owns ordinary OS/package-database
   changes.
4. Recipe packages installs exact `requires.go_commands` and
   `requires.node_commands` collected from the transitive recipe-reference
   closure. Direct/optional command requirements are checked, not guessed.
5. Project dependencies performs supported locked preparation without storing
   a source snapshot and becomes the final recipe image.

Generated Containerfiles and minimal contexts are inspectable and form the
single plan consumed by every runtime adapter. Build progress phases and runtime
output go to stderr; machine-readable recipe stdout remains clean. Failed
builds never select an older key or publish the requested tag.

## REQ-SBOX-006 — Prepare dependencies deterministically and safely

Recognized locked preparation is:

- Go: `go mod download`;
- Rust: `cargo fetch --locked` using REQ-RUST-004 inputs;
- npm: `npm ci --ignore-scripts`;
- pnpm: `pnpm install --frozen-lockfile --ignore-scripts`;
- Yarn: `yarn install --immutable --mode=skip-builds`;
- Bun: `bun install --frozen-lockfile --ignore-scripts`.

No recognized lockfile means automatic preparation is skipped rather than
performing an unlocked install. Node/Bun lifecycle/build scripts are disabled
because they can consume arbitrary source that cannot be safely pre-keyed. A
recipe needing generation performs it explicitly in `pre` inside the ephemeral
container.

Dependency keys include the parent key, package-manager identity, platform,
workdir, canonical manifest/lockfile contents, and every statically known
workspace/config/patch input consumed by the manager. Ordinary project source
is excluded. Go module and Rust registry/Git sources live outside the project
mount. Node/Bun adapters must prove that seeded stores, links, workspace paths,
and ownership remain valid after the temporary workspace mount.

Private credentials are invocation authority, never image inputs. A runtime
adapter must prove non-persistent secret mounts before enabling private fetch.
Secrets never enter Containerfiles, args, image environment/history, labels,
keys, or contexts. Secret-bearing steps are limited to fetch/install forms that
do not execute project/downloaded package code. Unsupported secret transport
fails closed and never copies host credential directories or weakens isolation.

## REQ-SBOX-007 — Persist mutable build caches only within one project

Repeated runs must not compile an unchanged project from scratch. The final
immutable recipe image is combined at execution with runtime-owned mutable
named volumes for compiler/framework build caches. These are not OCI layers and
never determine image validity or correctness.

Every mutable cache key, volume name, label lookup, lock, inspection query, and
reset operation includes the canonical project key plus workspace/module root,
provider/cache format, target platform/triple, exact toolchain, relevant system
ABI/environment identity, effective UID/GID, and provider-specific inputs that
the tool cannot fingerprint safely.

Mutable caches are never shared across canonical roots, repositories,
checkouts, or Git worktrees—even when contents and compatibility match. No
config, include, recipe reference, or content equivalence can opt into such
sharing. Compatible `test`, `build`, `check`, and `bench` recipes inside one
project may share their provider cache. Recipe name, source hashes, command,
run ID, ordinary args, and lockfile contents do not split a compatible cache.

Provider contracts initially include:

- Go: mutable `GOCACHE`; explicit outputs remain workspace/sync-out state.
- Rust: workspace-scoped normal Cargo `target`, keyed by REQ-RUST-002 metadata;
  Cargo subcommand or debug/release alone does not split it.
- Node/Bun: only explicit proven framework/compiler adapters; arbitrary cache,
  `.cache`, `dist`, or host paths are not inferred or exposed.

Providers declare shared-concurrent or exclusive access. Exact-cache waits
report progress on stderr and support cancellation. Ordinary failures preserve
cache data. A positively recognized corruption may retry once with a new empty
volume; unknown failures preserve the volume and original error. Cache growth
is accepted like host compiler caches, but every volume is attributable through
REQ-SBOX-010.

## REQ-SBOX-008 — Run one ephemeral lifecycle at a stable path

One top-level invocation runs `pre`, every `for_each` command, main, nested
recipe references, and `post` in one ephemeral container. Stage-per-container
execution is invalid because it loses process, writable-layer, and cleanup
continuity.

The host creates a copied temporary workspace and mounts it read-write at the
same canonical project path inside every container invocation. The source
checkout is never the writable bind. Stable in-container paths preserve
compiler fingerprints and debug/build-script behavior.

Shadowtree copies its static lifecycle helper into the private invocation
directory and mounts it read-only with a restrictive resolved-plan/secret file.
The helper validates the protocol version. Upgrading Shadowtree therefore does
not invalidate any persistent image layer. Invocation values are redacted from
diagnostics and removed with the temporary workspace.

The runtime removes the container after success, failure, or cancellation.
Cancellation reaches the helper so `post` retains current semantics; bounded
forced removal follows only when graceful cleanup fails. No engine socket, host
home, sibling project, host system tree, or arbitrary host path is mounted.

## REQ-SBOX-009 — Preserve lifecycle, output, and sync-out contracts

System mode preserves existing resolution, argument, environment, logging,
failure, recipe-reference, aggregate, and sync-out behavior. `post` runs after
pre/main failure or initial cancellation; the first pre/main failure is
preserved unless only post fails; sync-out occurs only after complete success.

Cache-backed paths such as Cargo `target` are nested volume mounts and may be
invisible in the host copy. Before successful container exit, the helper exports
only selected sync-out paths intersecting cache mounts into a private ordinary
workspace tree. Host sync-out consumes that snapshot with existing file,
symlink, mode, confinement, and missing-path-as-deletion rules. Failure and
cancellation keep compatible cache data but export nothing.

Nested references execute inside the top-level container, build no nested
image, and perform no nested sync-out. Their image requirements join the
top-level keys; incompatible backend/base contracts fail during resolution.
Aggregate execution treats each selected top-level recipe as its own container
invocation while preserving existing aggregate failure and cancellation rules.

Progress uses stderr and reports runtime detection, base resolution, each stage
lookup/build/reuse, cache resolution/wait, container execution, export, sync,
and cleanup. Redirected output is newline-delimited and never depends on
transient terminal rendering.

## REQ-SBOX-010 — Inspect and reset project caches clearly

The cache management surface is:

```text
shadowtree cache inspect [recipe] [--json]
shadowtree cache reset <recipe>
shadowtree cache reset --all
```

Inspection is read-only. Without a recipe it reports labelled mutable volumes
for the current canonical project; with a recipe it resolves that recipe's
provider identities. Human and stable JSON output include runtime/native name,
project/workspace ownership, provider/format, complete compatibility inputs and
key, UID/GID policy, recipes sharing the cache inside the project, existence,
active use, size when available without mounting, explicit unknown size
otherwise, collision/malformed/stale diagnostics, and exact scoped reset and
runtime-native administration references.

Inspection never mounts a volume, starts a measuring container, refreshes a
timestamp, or mutates state. Static recipe print shows the planned caches,
mounts, concurrency, and cache-backed sync intersections without runtime access.

Reset by recipe reports other same-project recipes sharing the selected cache.
`--all` is confined to fully labelled volumes owned by the current canonical
project and is never machine-wide. Reset validates exact names/labels, refuses
active volumes, treats missing volumes idempotently, and removes no images,
dependencies, source, or synced outputs. There is no automatic retention or GC.

## REQ-SBOX-011 — Fail closed at trust and compatibility boundaries

System mode fails before user code for unusable runtimes, unresolvable bases,
unsupported stage/provider/platform requirements, tag/label collisions,
insecure private-secret transport, build failure, missing required commands,
unsafe workspace/helper/cache mounts, incompatible cache ownership, or unsafe
cache-backed export.

Runtime capability must cover rootless UID/GID mapping and SELinux relabelling
where applicable; missing capability never receives weakened fallback flags.
Slim images are tested defaults, not universal ABI claims: musl/glibc, linkers,
headers, OpenSSL, CMake, `pkg-config`, and native dependencies remain explicit
base/system-package choices.

Mutable project cache content is untrusted project-writable state. It is never
mounted into another project/worktree, searched for Shadowtree/runtime
executables, or authoritative for images/dependencies. Local tags and labels
prevent accidental collision but are not cryptographic provenance against a
same-user process controlling the runtime.

Errors identify phase, runtime, recipe, affected stage/cache, and safe recovery
action. Inspection stays read-only; execution never runs a stale different-key
image after a failed build.

## Delivery sequence and acceptance

After the Rust prerequisite, implementation proceeds through mode/schema,
runtime adapter, deterministic image stages, one-container lifecycle, mutable
cache providers and inspection/reset, one complete preparation provider, then
private-dependency and capable-host validation. Each public increment keeps
runtime, schema, LSP, completion, docs/spec, examples, and agent guidance
aligned.

The first end-to-end system slice must demonstrate cold build then exact reuse,
success/failure/cancellation/post behavior, checkout isolation, selected
sync-out, and cache inspection/reset. A Rust workspace must run `test` followed
by `build` without recompiling unchanged crates, expose the same project-scoped
Cargo volume in inspection, and give an exact reset operation. Capable-host
validation must cover Docker, Podman, and nerdctl rather than mocks alone.

Explicit non-goals for this increment are an in-process OCI/runtime/registry
stack, Shadowtree filesystem cache store, global persistent database/journal,
automatic GC, long-lived recipe containers, engine-socket forwarding,
privileged mode, cross-project mutable caches, arbitrary host cache paths,
installer inference from command names, and silent unlocked dependency
installation.
