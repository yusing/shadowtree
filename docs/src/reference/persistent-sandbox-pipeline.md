# System Container Sandbox Pipeline

## Status

This document is the implementation plan for the system-container sandbox. The
behavior is not implemented yet.

Development starts from the current `main` behavior. The discarded persistent
sandbox implementation is not a compatibility target and none of its OCI
store, root-filesystem materialization, namespace runtime, state database,
journaling, leases, cache management, garbage collection, or repair machinery
should be recovered.

The system container runtime owns image storage, build-layer caching, container
construction, process isolation, networking, and container cleanup. Shadowtree
owns recipe resolution, the generated image build plan, the temporary project
workspace, lifecycle semantics, sync-out, diagnostics, and cancellation.

## User-visible outcome

A recipe may select one of three execution policies through the existing
`sandboxed` field:

```toml
[recipes.test]
sandboxed = "system"
cmd = "go test ./..."
```

| Value | Meaning |
| --- | --- |
| omitted or `true` | Run in the existing namespace-overlayfs or copied-workspace sandbox. |
| `false` | Run directly in the host checkout. |
| `"system"` | Build or reuse a recipe image with an installed system container runtime, then run the recipe in one ephemeral container. |

`"system"` is the only accepted string value. Configuration decoding, schema,
LSP diagnostics, completion, help, and inspection must reject every other
string rather than treating it as truthy or falling back to the existing
sandbox.

System mode remains a sandboxed mode for lifecycle and sync-out purposes. It
must not write the source checkout directly. `sync_out`, `--sync-out`, and
`--sync-out-all` retain their current success-only behavior.

## Product model

The execution path is deliberately thin:

```text
resolve recipe and referenced-recipe closure
-> detect one usable system container runtime
-> resolve the local base-image identity
-> derive and inspect the five ordered image-stage identities
-> build from the nearest valid lower stage when an exact stage is absent
-> resolve or create compatible mutable build-cache volumes
-> create a temporary copied project workspace
-> mount the workspace, invocation helper, and build caches
-> run the complete recipe lifecycle in one ephemeral container
-> sync selected successful outputs to the source checkout
-> remove the temporary workspace
```

The container runtime implements the persistent pipeline through five ordered,
independently tagged image stages:

```text
external profile/default image
-> base layer
-> tooling layer
-> system-packages layer
-> recipe-packages (`requires`) layer
-> project-dependencies layer (final recipe image)
-> ephemeral recipe container
```

Each stage is a stable cache and inspection boundary. Its key includes the key
of the immediately lower stage plus its own normalized inputs. When a stage
changes, Shadowtree selects or builds that stage and the stages above it. It
never invalidates, retags, or rebuilds a lower stage. For example, changing
tooling may rebuild system packages, recipe packages, and dependencies on top of
the new tooling parent, but the base layer remains untouched. Changing only
dependencies rebuilds no lower layer.

This direction matches the linear parent model implemented portably by Docker,
Podman, and nerdctl. “Independent” means every boundary has its own derived tag
and can be reused as the nearest valid parent; it does not claim that an upper
image can retain a parent digest after a lower layer changes.

Shadowtree does not implement an OCI registry client, content store, snapshotter,
overlay root, package database, layer publisher, mutable setup cache, or image
reference ledger. A missing derived image causes a normal runtime build. A
present and correctly labelled derived image is the complete readiness record.

## System runtime selection

For `sandboxed = "system"`, Shadowtree probes these executable names in this
stable order:

1. `docker`
2. `podman`
3. `nerdctl`

The first *usable* runtime wins. Finding an executable on `PATH` is insufficient:
the adapter must run a bounded, non-interactive operational probe that verifies
the client can reach its required engine and can perform the common image and
container operations used by Shadowtree. If a candidate is installed but its
daemon or service is unavailable, detection continues to the next candidate.

When none is usable, execution fails before image resolution, build, workspace
creation, or user code. The error lists every candidate and its concise failure
reason. System mode never falls back to `sandboxed = true` or `false`.

Runtime probing occurs for actual execution and for host-capability validation.
Configuration loading, recipe listing, help, completion, and static plan
printing remain host-independent and do not require a runtime. Static
inspection reports `sandboxed: system` and `runtime: <not probed>`; an execution
plan may report the selected runtime after the probe.

The initial implementation uses the portable common subset of the three CLIs
behind one internal adapter. Runtime-specific flags must remain inside that
adapter. Shadowtree invokes the runtime directly with an argument vector; it
does not construct a shell command. Unsupported runtime versions or missing
required capabilities fail with an actionable error.

An explicit runtime selector is not part of the first increment. Stable probe
order makes behavior deterministic without adding another configuration
surface. Add an override only if real multi-runtime use demonstrates a need.

## Recipe image identity

Users do not provide a separate image identifier in the first increment.
Shadowtree derives a collision-resistant local image reference, avoiding a new
required field and preventing two projects with a recipe named `test` from
sharing mutable state accidentally.

Conceptually, the reference is:

```text
shadowtree.local/<project-key>/<recipe-key>:<build-key>
```

Each reusable stage also has a project-independent canonical tag:

```text
shadowtree.local/stage/base:<base-key>
shadowtree.local/stage/tooling:<tooling-key>
shadowtree.local/stage/system-packages:<system-packages-key>
shadowtree.local/stage/recipe-packages:<recipe-packages-key>
shadowtree.local/stage/dependencies:<dependencies-key>
```

The recipe-scoped reference is an alias for its exact dependency-stage image;
it gives inspection a stable project/recipe name without preventing canonical
stage reuse across projects. A new recipe alias never causes a canonical stage
to rebuild when that stage tag and labels already match.

- `project-key` is a digest of the canonical source-root and root-config paths.
  It scopes local images from different checkouts and projects. Moving a
  checkout intentionally gives it a new local image namespace.
- `recipe-key` is a readable normalized recipe name plus a digest of the owning
  config path and top-level recipe name.
- `build-key` is a digest of the canonical image build inputs.

The exact tag encoding must satisfy the strictest naming rules shared by
Docker, Podman, and nerdctl. Full untruncated keys and the Shadowtree build-plan
version are stored as image labels. Image inspection must verify those labels
before reuse; a tag with absent or mismatched ownership labels is rejected as a
collision instead of being run or overwritten silently.

The final build key is the project-dependencies-layer key. Each stage has its
own key and names its immediate parent explicitly:

- **Base:** selected platform, effective profile family, external image
  selector and resolved local ID/digest, and versioned base-plan format.
- **Tooling:** base-layer key, detected package-manager identity and exact
  version, exact profile-tool versions, and versioned tooling setup plan.
- **System packages:** tooling-layer key, supported distribution/package
  provider, and normalized system-package declarations.
- **Recipe packages (`requires`):** system-packages-layer key plus exact
  `requires.go_commands` and `requires.node_commands` declarations collected
  from the transitive recipe-reference closure.
- **Project dependencies:** recipe-packages-layer key,
  profile/package-manager identity, canonical manifest and lockfile contents,
  and every workspace file actually consumed by locked dependency installation.

This parent-key chain gives the required one-way invalidation. A tooling change
cannot change the base key. A system-package change cannot change the base or
tooling keys. A recipe-package change cannot change base, tooling, or system
packages. A dependency change cannot change any lower-layer key.

Recipe source files, argument values, `cmd`, `pre`, `post`, environment values,
logging options, and sync-out selections do not join the build key unless they
alter an image-building declaration. Changing ordinary recipe execution inputs
therefore reuses the image.

This reference is an implementation-owned local cache key, not a published
user image name or compatibility contract. Registry push/pull, user-selected
tags, and sharing built recipe images across machines are non-goals for the
first increment.

## Base image resolution

System mode selects its default base from the effective profile. Explicit
`profile` configuration wins; otherwise Shadowtree uses its existing nearest
project-marker detection. The initial mappings are:

| Effective project kind | Default image family |
| --- | --- |
| Go profile | Release-pinned slim official Go image, using the existing resolved Go directive when supported |
| Node profile with npm, pnpm, or Yarn | Release-pinned slim official Node image |
| Node profile with Bun | Release-pinned slim official Bun image |
| Rust profile | Release-pinned slim official Rust image, using an exact supported toolchain file when present |
| No profile | Release-pinned minimal Ubuntu image |

“Release-pinned” means Shadowtree releases own concrete, tested selectors; the
implementation must not resolve a floating `latest` tag as a hidden default.
When a Go directive does not identify an available supported image version, use
the release's documented Go default or fail for an explicitly unsupported
version according to the profile contract. Node version-range inference is not
added implicitly: until Shadowtree has a supported exact Node-version resolver,
the release-pinned Node image is authoritative.

Rust profile support is a prerequisite delivered and verified before system
sandbox implementation starts. It preserves the current nearest-marker
precedence rules: an explicit `profile` wins; otherwise a nearest `Cargo.toml`
selects `rust` when no nearer Go or Node marker wins. The Rust profile resolves
an exact toolchain from `rust-toolchain.toml` or `rust-toolchain` when present
and supported, otherwise it uses its documented release-pinned default.

Cargo workspace discovery, toolchain identity, host and target triples,
built-in recipes, dependency inputs, and cache metadata belong to that normal
profile owner. System mode consumes those resolved values through the same
profile interface used by Go and Node; it must not add container-specific Cargo
scanners, toolchain parsing, workspace discovery, or built-in command logic.

A recipe may override the default:

```toml
[recipes.test]
sandboxed = "system"

[recipes.test.system]
base_image = "docker.io/library/golang:1.26-alpine"
```

The value is called a *base image* so it cannot be confused with Shadowtree's
derived recipe image. The initial increment has no top-level system table and
no CLI override.

The `system` table is invalid unless the effective `sandboxed` value is
`"system"`. An omitted recipe `system.base_image` uses the effective-profile
mapping above, including the Ubuntu default when no profile exists. It accepts a
non-empty OCI image selector and supports normal static recipe placeholders only
when they are fully resolved before runtime detection; runtime arguments and
`var_commands` must not determine image identity.

Before deriving the build key, Shadowtree asks the selected runtime for the
base image's local immutable ID or digest. When the base image is absent, the
runtime pulls it using its normal policy and credentials, then Shadowtree
inspects it again. Shadowtree does not read registry credentials or implement
registry authentication.

An existing local mutable tag is not refreshed automatically. Consequently, a
recipe keeps using the locally resolved base identity until the user updates
that runtime's local image or changes the configured selector. Digest-pinned
bases are recommended for reproducibility. An explicit refresh policy may be
designed later; it must not be hidden network work during every recipe run.

Shadowtree never guesses a profile from recipe command text. No detected or
specified profile means the documented Ubuntu base, not command inspection.

## Node and Bun tooling detection

System mode reuses the existing Node package-manager detection owner rather
than introducing a container-specific detector. Detection retains its current
precedence:

1. nearest `package.json` `packageManager` prefix;
2. nearest recognized lockfile in the existing stable order; and
3. npm.

The existing detector must be extended to return one structured identity with
name, exact version when declared, source/provenance, and owning project path.
Built-in commands and system-image planning consume that same result. Do not
parse `packageManager` or scan lockfiles again in the runtime adapter.

The detected manager selects both the base family and tooling setup:

- Bun selects the slim Bun base and uses the Bun version supplied by the base
  unless an exact supported `packageManager` pin selects another image/tooling
  identity.
- npm, pnpm, and Yarn select the slim Node base.
- npm uses the base npm when unpinned; an exact supported npm pin is installed
  into the managed tooling prefix.
- pnpm and Yarn use Corepack. The tooling build prepares the exact manager
  version from `packageManager` when present, or a release-pinned default when
  detection came only from a lockfile, then enables only that manager into the
  managed tooling prefix. For pnpm this includes the equivalent of
  `corepack enable pnpm` with an explicit install directory.

If the selected slim Node image lacks the required Corepack executable,
Shadowtree uses a release-pinned, trusted Corepack setup declared by the tooling
plan or fails clearly; it never installs an unpinned current Corepack release.
The completed tooling image is validated by running the selected manager's
version command and comparing it with the resolved identity.

Tooling keys omit project and recipe identity. Projects on the same platform,
base identity, package-manager name/version, and setup-plan version therefore
reuse the same runtime-owned tooling image. This is the primary cross-project
sharing path; for example, two pnpm projects using the same Node base and pnpm
version share one prepared Corepack/pnpm tooling image.

## Image construction

Shadowtree generates a Containerfile and a minimal build context from the
resolved image inputs. The generated file is inspection output and the single
source of truth passed to every runtime adapter; do not maintain separate
Docker, Podman, and nerdctl build recipes.

Each stage is built from the exact image produced by the preceding stage:

1. **Base layer.** Resolve or pull the profile-selected or explicit external
   image, add only the versioned base-plan metadata and ownership labels, and
   publish the derived base tag. The invocation helper is not image content.
2. **Tooling layer.** Starting from the base layer, prepare
   profile/package-manager tooling under `/opt/shadowtree/tooling` and expose
   its `bin` directory through `PATH`. This layer owns Corepack shims and
   selected pnpm, Yarn, npm, or other profile tooling; it does not install
   recipe requirements.
3. **System-packages layer.** Starting from the tooling layer, install the
   normalized `requires.system_packages` set using a supported distribution
   adapter. Operating-system packages may legitimately modify `/usr`, `/lib`,
   `/etc`, and package databases.
4. **Recipe-packages (`requires`) layer.** Starting from the system-packages
   layer, install exact `requires.go_commands` and `requires.node_commands`
   under `/opt/shadowtree/requirements`. These are recipe-declared executable
   tools, not project lockfile dependencies.
5. **Project-dependencies layer.** Starting from the recipe-packages layer,
   perform supported locked project installation, publish its runtime result
   under `/opt/shadowtree/dependencies`, and tag this stage as the final recipe
   image. This layer does not contain a project source snapshot.

Every stage uses an implementation-owned content-derived tag and full key
labels. Shadowtree walks from base to dependencies, inspecting and validating
each expected tag. Once it finds a missing or invalidated stage, it builds from
the nearest valid lower image; it never rebuilds earlier valid stages. Base and
tooling images may be shared across all compatible projects. Higher images may
also be shared when their parent key and complete declarations match. The
dependency image is content keyed and may be reused across equivalent
checkouts, but no correctness guarantee depends on cross-project reuse.

`requires.commands` and `requires.optional_commands` never guess installers.
They are checked against the completed image/container `PATH`; missing required
commands fail and missing optional commands warn.

Build output is streamed as progress to stderr using the selected runtime's
plain, non-interactive mode when available. Runtime stdout/stderr and exit
status remain visible on failure. Shadowtree reports at least these phases
before potentially long work: runtime detection, base-image resolution,
recipe-image lookup, image build or reuse, and container execution.

The runtime publishes the final tag only after a successful build. A failed
build does not replace or masquerade as the requested image. Concurrent misses
may perform duplicate equivalent builds; correctness must not depend on a
Shadowtree lock or state database. Runtime build caches may accelerate the
work, but their presence is never a correctness requirement.

Distribution/package-manager support must be explicit and fail closed. Do not
embed a general package-manager abstraction merely to claim broad image
support. Add one verified profile/distribution path at a time.

## Project-dependencies layer

Automatic dependency preparation occurs only for a supported profile with a
recognized lockfile. Without a recognized lockfile, Shadowtree skips automatic
dependency installation rather than inventing an unlocked policy.

Initial deterministic commands remain:

- Go: `go mod download` with `go.mod` and `go.sum`;
- Rust: `cargo fetch --locked` with the Cargo workspace manifests and
  `Cargo.lock`;
- npm: `npm ci --ignore-scripts` with `package-lock.json`;
- pnpm: `pnpm install --frozen-lockfile --ignore-scripts`;
- Yarn: `yarn install --immutable --mode=skip-builds`; and
- Bun: `bun install --frozen-lockfile --ignore-scripts`.

Node/Bun dependency image construction deliberately disables package lifecycle
scripts. Such scripts may consume arbitrary source or configuration that cannot
be discovered safely from the lockfile before execution, and running them while
constructing a reusable image risks unsound cache keys and leaked build
credentials. A recipe that requires generation or native setup runs it
explicitly in `pre` inside the ephemeral container, where ordinary project
build caches can accelerate repeated work. Shadowtree may later support a
provider-specific declared-input contract, but it must not hash the entire
source tree or silently enable arbitrary install scripts.

The profile adapter must define where prepared dependencies live after the
project workspace is mounted. Image content hidden by the workspace bind mount
is not considered available:

- Go module downloads live outside the project mount and are copied from the
  dependency artifact to the final image's configured module-cache path. The Go
  compiler build cache is mutable invocation state defined below.
- Rust registry and Git dependency sources live outside the project mount. The
  dependency layer does not compile the project; compilation belongs to the
  mutable Cargo target cache.
- Node/Bun dependency output includes the package-manager store plus the exact
  project-local structure required by that manager. The lifecycle helper seeds
  that structure into the temporary copied workspace before `pre`; it never
  writes the source checkout.

Each Node/Bun adapter must prove that links and store paths remain valid after
seeding. Runtime-specific anonymous-volume copy-up behavior is not a portable
dependency mechanism and must not be assumed. Unsupported destination semantics
fail or skip according to the documented profile contract; mount ordering must
not be used as accidental behavior.

The dependency key contains the recipe-packages parent key, selected
package-manager identity, canonical manifest and lockfile contents, platform,
workdir, and every workspace file actually consumed by the selected manager,
such as workspace manifests and patch/config files. It does not hash ordinary
source files. Changing a consumed dependency input selects a new dependency
image without rebuilding base, tooling, system packages, or recipe packages.
Runtime build cache may reuse unchanged download/install work. Shadowtree does
not maintain its own package-manager cache directories, corruption retry state,
timestamps, or retention policy.

### Private dependency credentials

Private module, registry, Git, and SSH credentials are invocation authority,
not image inputs. They must never appear in generated Containerfiles, build
arguments, image environment, labels, layer contents, cache keys, or image
history.

Each runtime adapter must prove a bounded secret-mount capability before using
private credentials during a dependency build. Shadowtree passes only the
specific credential selected for that provider through a runtime-native secret
mount, never the whole host home or credential directory. If the selected
Docker, Podman, or nerdctl version cannot provide equivalent non-persistent
secret transport, dependency preparation fails with explicit guidance; it does
not copy credentials into the build context or retry with weaker isolation.

Secret-bearing dependency stages are limited to fetch/install forms that do not
execute project or downloaded package code: Go module download, Cargo fetch,
and Node/Bun installation with lifecycle/build scripts disabled. Shadowtree
does not expose a credential to an arbitrary generated build command. A
malicious credential helper or compromised package-manager executable remains
inside the selected image trust boundary and is not made safe by a secret mount.

Runtime secret syntax is adapter-owned. The canonical dependency plan records
only secret identifiers and required destinations, not values. Secret values do
not join an image key. The adapter verifies that the runtime excludes the secret
mount itself from the produced layer and that its reserved destination is absent
after the step. Shadowtree cannot prove that a malicious executable did not copy
a supplied value elsewhere, so only the non-executing preparation forms above
may receive it.

## Mutable project build caches

Repeated recipe execution must not recompile an unchanged project from scratch.
System mode therefore composes the final immutable recipe image with
runtime-owned mutable named volumes for compiler and framework build caches.
These volumes are a separate cache plane, not mutable OCI layers:

```text
immutable dependency-stage recipe image
+ compatible mutable build-cache volumes
+ temporary copied project workspace
+ ephemeral container writable layer
```

Immutable image layers define the reproducible execution environment. Mutable
build caches accelerate compilation but never select image identity, prove that
an artifact is current, or become necessary for correctness. A run against an
absent or empty cache must produce the same result as a warm run.

### Cache identity and sharing

One canonical cache key contains:

- canonical project identity and module/workspace root;
- cache provider and versioned cache-format contract;
- target platform or compiler target triple;
- exact compiler/toolchain identity;
- the lowest immutable layer key that captures the relevant system ABI and
  build environment;
- effective container user/ownership policy; and
- normalized build-affecting configuration that the provider cannot safely
  fingerprint itself.

It does not contain recipe name, source hashes, command text, run ID, ordinary
arguments, or dependency lockfile contents. The compiler/build system owns
fine-grained source, feature, profile, flag, and dependency invalidation.
Consequently `test`, `build`, `check`, and `bench` share a cache when their
provider compatibility keys match. An incompatible toolchain, target, ABI, or
user selects a separate cache rather than mutating the old cache into a new
meaning.

Mutable compiler output is strictly project-scoped. Unlike immutable tooling
images, it is never shared across canonical project roots, repositories,
checkouts, or worktrees, even when their source contents, toolchains, and cache
compatibility inputs match. Project code and build scripts can write arbitrary
content into the cache, so no configuration, recipe reference, include, or
content-derived equivalence may opt into cross-project mutable sharing.

Compatible recipes and modules within one canonical project may share only the
provider cache owned by that same project. The project key is therefore a
mandatory, non-optional part of every mutable volume name, key, label lookup,
inspection query, lock, and reset operation. Immutable base and tooling images
remain eligible for safe cross-project sharing because recipes cannot mutate
them.

The runtime volume name is derived from the complete key and uses labels for
the untruncated project key, workspace root, provider, compatibility key,
ownership, and cache-format version:

```text
shadowtree-build-cache-<project-key>-<workspace-key>-<compatibility-key>
```

An existing name with missing or mismatched labels is rejected as a collision.
Shadowtree creates and mounts volumes through the selected runtime adapter; it
does not implement a host cache directory or copy a runtime volume into its own
persistent store.

### Provider contracts

Cache paths are profile/provider-owned. Shadowtree does not infer persistent
directories from recipe command strings or automatically retain arbitrary
directories named `cache`, `.cache`, `target`, or `dist`.

- **Go:** mount a project-compatible mutable `GOCACHE`. `GOMODCACHE` remains
  dependency/download content owned by the immutable dependency stage unless a
  later separate download-cache contract is justified. Explicit build outputs
  such as `go build -o bin/tool` still land in the temporary workspace.
- **Rust:** mount a project/workspace-scoped Cargo `target` volume at the normal
  stable project path so Cargo, build scripts, IDE tools, `test`, `build`, and
  `bench` see the standard layout. Registry and Git sources belong to dependency
  preparation, not this target cache. The key includes the exact Rust toolchain,
  host and target triples, applicable ABI layer, and Cargo cache-contract
  version; it does not split merely by Cargo subcommand or debug/release mode.
- **Node/Bun:** add mutable framework/compiler caches only through explicit
  adapters with stable semantics, such as a proven Next.js or TypeScript
  incremental cache contract. Installed dependencies remain in the immutable
  dependencies layer. Generated distributable output is ordinary workspace
  state and persists only through sync-out.

A custom recipe-cache declaration is deferred until these built-in providers
show which minimal path, sharing, concurrency, and export controls are actually
needed. The first implementation must not introduce an unrestricted arbitrary
host-path cache mount.

### Stable paths and sync-out

The host temporary workspace changes location on every invocation, but it must
always be mounted at the same canonical project path inside the container.
Compiler caches, debug information, build scripts, and incremental fingerprints
must not observe a new source path on every run.

Some provider caches, especially Cargo `target`, are mounted beneath the
project workspace and therefore obscure that subtree in the host-side copied
workspace. Before the container exits after a successful lifecycle, the
in-container helper exports any selected `sync_out` path that intersects a
cache-backed mount into a private ordinary-workspace export tree. Host sync-out
then reads that snapshot. Export preserves existing file type, symlink, mode,
path-confinement, and missing-path-as-deletion rules.

The helper does not export an entire build cache merely because it is mounted.
Only explicitly selected sync-out paths are copied. A failed or canceled run
does not sync outputs but keeps compatible cache contents for the next run.

### Concurrency, cancellation, and corruption

Each cache provider declares either `shared-concurrent` or `exclusive` access.
Go may use concurrent access where its cache contract permits it. Cargo target
and framework providers use their proven native locking behavior or an exact
cache-identity lock; a provider that cannot update safely is exclusive.

Waiting for an exclusive cache reports the cache/provider and elapsed wait on
stderr, supports cancellation, and never holds a global project or runtime
lock. The cache remains mounted for one bounded invocation and is released when
the runtime removes the container.

Ordinary compiler or recipe failure never causes cache deletion. On a
provider-recognized corruption error, Shadowtree may detach the old volume and
retry once with a newly created empty volume. Unknown failures preserve the
cache and return the original error. Cancellation may leave partial cache
entries; provider correctness must tolerate them or select exclusive access.

Build-cache volumes survive success, failure, cancellation, container removal,
and compatible upper image changes. Natural disk growth is accepted, as with a
host compiler cache, but every retained volume must be attributable and
inspectable as described below.

### Cache trust and portability boundaries

A mutable project cache is writable by project code and may contain malicious
or corrupted data after running an untrusted branch. It is never mounted into
any other canonical project root or worktree, never searched for
Shadowtree/runtime executables, and never used as an authority for image or
dependency validity. Compiler/provider fingerprinting remains the correctness
boundary. Users who change trust context can inspect and reset the exact project
cache before execution.

Image tags, volume names, and labels protect against accidental collision and
stale selection, not a malicious same-user process with access to the same
container runtime. Shadowtree must not describe local cache labels as
cryptographic provenance.

Runtime support requires more than image build and `run`: the adapter's
capability probe must verify labelled volume create/inspect/remove, nested
cache-volume mounts beneath the project bind mount, read-only invocation-file
mounts, effective UID/GID access, signal forwarding, and automatic container
removal. Rootless mappings and SELinux relabelling remain runtime-specific
adapter responsibilities. A runtime that cannot provide the required mount and
ownership contract is unusable for system mode and must not receive weakened
fallback flags.

Slim profile images are release-tested defaults, not universal compatibility
claims. Alpine/musl versus glibc, native compiler/linker availability, headers,
`pkg-config`, OpenSSL, CMake, and similar native inputs remain observable base
and system-package choices. Shadowtree does not silently switch distributions
to make a cache entry or compilation succeed.

## Container execution

One top-level recipe invocation uses one ephemeral container. `pre`, every
`for_each` main command, nested recipe references, `cmd`, and `post` share that
container's process namespace, writable image layer, project workspace, and
environment. Running one disposable container per stage is invalid because it
would break setup and cleanup continuity.

The host prepares a copied temporary workspace for system mode and bind-mounts
that copy read-write at the chosen project path in the container. It does not
bind-mount the source checkout. A host-visible copy is required because a
Docker daemon cannot necessarily see a client-only mount namespace or overlay
mount. Existing copy exclusions and symlink/path safety rules remain in force.

The lifecycle helper is invocation content, not image content. Shadowtree copies
the current static helper into the host-visible private invocation directory and
bind-mounts it read-only at a reserved container path. The runtime starts that
helper as the container process and passes a resolved plan through a separate
private read-only invocation file. A Shadowtree binary upgrade therefore does
not invalidate base, tooling, system-package, recipe-package, or dependency
layers.

The helper validates the invocation protocol version before running and
executes existing lifecycle semantics inside the image rather than translating
the recipe into an ad hoc shell script. Invocation files use restrictive host
permissions, are removed with the temporary workspace, and are never included
in an image or mutable build cache. Secrets must not appear in runtime command
arguments, image labels, build keys, or build context. Invocation-only
environment values remain confined to the restrictive plan/secret mounts and
are redacted from diagnostics.

The container is always ephemeral and is requested with the runtime's automatic
removal behavior. On normal completion, failure, or cancellation, Shadowtree
waits for the runtime to remove it. Cancellation is forwarded to the lifecycle
helper so current post-stage cleanup semantics are preserved; bounded forced
container removal is the final fallback after graceful termination fails.

No container-engine socket, host home, sibling project, host system tree, or
arbitrary host path is mounted into the recipe container. The selected runtime
defines the actual kernel isolation and default network behavior. Shadowtree
must document the effective mounts and runtime command and must not claim a
stronger security boundary than the selected engine provides.

## Lifecycle, logging, and sync-out

System mode preserves the observable recipe contract:

1. Resolve arguments, placeholders, references, `for_each`, environment, and
   logging before user code.
2. Resolve, build, or reuse the five exact ordered layers; the dependency-layer
   image is the final recipe image.
3. Resolve or create compatible mutable build-cache volumes, acquire any
   provider-required cache locks, and report waits.
4. Create the copied workspace and seed its locked dependency result when the
   selected profile requires project-local dependency paths.
5. Mount the workspace at its stable container path, mount provider caches, and
   start the read-only invocation helper.
6. Open the configured recipe log.
7. Run `pre` commands in order.
8. Run the main command once or once per `for_each` item only after successful
   setup.
9. Run `post` after success, setup failure, main failure, or initial
   cancellation.
10. Preserve the first `pre` or main failure unless only `post` fails.
11. On complete success, export selected paths that intersect cache mounts and
    perform ordinary sync-out.
12. Remove invocation files and the copied workspace, release cache locks, and
    retain compatible cache volumes.

Machine-readable recipe output remains on stdout. Runtime selection, image
lookup/build progress, warnings, and cleanup diagnostics use stderr. Redirected
output is newline-delimited and does not rely on transient terminal rendering.

Nested `@recipe` and `@path:recipe` references execute inside the top-level
container and do not start nested containers, build separate images, or perform
their own sync-out. Their image requirements are included in the top-level
image build key. A referenced recipe explicitly requiring an incompatible
execution policy or base-image contract is rejected during resolution rather
than silently switching backends.

Aggregate recipe execution still treats each selected top-level recipe as its
own invocation and image identity. Failure and cancellation behavior follow the
aggregate command's existing contract.

## Inspection and operability

Static `--print` output includes:

- `sandboxed: system`;
- effective base-image selector and its provenance;
- derived image inputs that are knowable without runtime access;
- provider build-cache plans, compatibility inputs, stable mount destinations,
  concurrency policy, and cache-backed sync-out intersections;
- referenced-recipe closure; and
- sync-out and lifecycle stages.

Execution diagnostics include:

- selected runtime name and version;
- resolved local base-image ID;
- full derived recipe image reference;
- each base, tooling, system-package, recipe-package, and dependency key and
  whether that component was reused or built;
- an inspectable generated Containerfile/build plan; and
- the final sanitized container invocation and mount destinations.

Mutable build caches add two bounded management commands:

```text
shadowtree cache inspect [recipe] [--json]
shadowtree cache reset <recipe>
shadowtree cache reset --all
```

`cache inspect` is read-only. With a recipe, it resolves that recipe's provider
cache identities; without one, it reports every labelled Shadowtree build-cache
volume owned by the current project. Its human and stable JSON output includes:

- selected runtime and runtime-native volume name;
- project and workspace ownership;
- provider and cache-format version;
- exact compatibility inputs and derived key;
- effective container UID/GID policy;
- recipes currently resolving to the same cache;
- whether the volume exists and whether the runtime reports it in use;
- size when the runtime exposes it through a bounded read-only operation,
  otherwise an explicit `unknown` rather than starting a measuring container;
- collision, malformed-label, or stale-compatibility diagnostics; and
- the exact scoped Shadowtree reset command and runtime-native volume reference
  for manual administration.

Inspection does not refresh timestamps, acquire a mutation lock, mount a
volume, or start a helper container. It may perform the same bounded runtime
probe and labelled volume inspection required to report actual state. Static
recipe `--print` remains runtime-independent and reports only the derived cache
plan.

`cache reset <recipe>` removes the compatible project cache volumes selected by
that recipe after showing that other recipes share them. `cache reset --all`
is confined to labelled caches owned by the current canonical project; it is
not a machine-wide wildcard. Reset validates every runtime name and full
ownership label, asks the runtime to remove only those exact volumes, and fails
without partial path guessing when a cache is mounted by an active container.
Missing volumes make reset idempotent. Reset is destructive cache eviction but
does not remove images, dependency layers, source files, or synced outputs; the
next recipe run recreates an empty compatible cache.

Inspection must not pull, build, start containers, refresh images, delete
images, or mutate runtime state unless the user explicitly invokes execution or
a future management command whose contract says so.

The first increment adds no Shadowtree image list, image reset, destroy, repair,
automatic retention policy, or garbage collector. Runtime-owned immutable
images remain manageable through the selected runtime. Mutable compiler-cache
growth is normal and visible through `cache inspect`; users can reclaim a
clearly scoped project cache with `cache reset` without requiring automatic GC.

## Failure contract

System mode fails before user code when any of the following occurs:

- no supported runtime is usable;
- the runtime version lacks a required common operation;
- the default or explicit base-image selector cannot be resolved locally or
  pulled;
- an existing derived tag has mismatched ownership labels;
- the generated build plan contains unsupported profile, distribution, package,
  dependency, platform, or recipe-reference requirements;
- private dependency access requires a secret transport the selected runtime
  cannot provide without persistence;
- image build or final image validation fails;
- the copied workspace, stable project-path mount, invocation-helper mount, or
  provider cache volume cannot be prepared safely;
- an existing cache volume has mismatched ownership/compatibility labels;
- cache ownership cannot be mapped to the effective container user;
- a selected cache-backed sync-out path cannot be exported safely; or
- required executables are missing from the completed image.

It also fails normally when container creation, recipe execution, cancellation,
cleanup, logging, or sync-out fails. Errors identify the phase, selected
runtime, recipe, and safe recovery action. Shadowtree never runs an older image
whose key differs from the requested inputs after a failed build.

## Explicit non-goals

This design does not include:

- an OCI client, runtime, snapshotter, root filesystem, or registry
  implementation in Shadowtree;
- direct use of `runc`, containerd APIs, Docker APIs, or Podman APIs when the
  supported CLI adapter suffices;
- a Shadowtree-owned persistent-state database, journal, lease, global lock,
  filesystem cache store, or garbage collector; narrowly scoped invocation
  locks and runtime-owned labelled volumes are part of the cache contract;
- long-lived recipe containers;
- baking the invocation lifecycle helper into persistent images;
- pushing or sharing derived recipe images;
- automatic refresh of mutable base-image tags;
- arbitrary host mounts, container-engine socket forwarding, privileged mode,
  or hidden capability escalation;
- mutable compiler-cache sharing across canonical projects, checkouts, or
  worktrees, including related ones;
- arbitrary recipe-declared host cache directories in the first increment;
- automatic installer inference from an executable name;
- silently unlocked dependency installation; or
- feature parity across unsupported distribution/package-manager combinations.

## Implementation sequence

Each step must keep runtime validation, schema, LSP, docs, completion,
inspection, examples, and tests aligned for the public surface it introduces.

### Prerequisite: Rust profile

Complete and verify the ordinary Rust profile before beginning step 1. This is
an independent product increment, not the first internal package of the system
sandbox. It must:

- add `rust` through the existing `detect.Profile`, `recipe.SupportsProfile`,
  and `recipe.Builtins` owners;
- define explicit-versus-detected profile precedence with `Cargo.toml` markers;
- resolve one canonical Cargo workspace root;
- resolve exact `rust-toolchain.toml` or `rust-toolchain` identity with a
  documented release default;
- expose host-usable Cargo built-ins and their normal argument, workdir,
  aggregate, sandbox, logging, failure, and cancellation behavior;
- expose structured compiler/toolchain, host triple, target triple, workspace,
  manifest, and lockfile metadata for later consumers without referring to a
  container runtime;
- keep runtime, schema, LSP, completion, README, mdBook/spec, examples, and
  agent recipe-authoring guidance aligned; and
- pass focused profile tests plus the repository check independently of any
  system sandbox code.

System sandbox work is blocked on that stable profile contract. Later steps may
consume or extend its general interfaces but may not duplicate its detection or
parsing in a container package.

1. **Mode contract**
   - Replace the boolean-only recipe representation with a typed sandbox policy
     that decodes `true`, `false`, or `"system"` and retains current defaults.
   - Update schema, LSP completion/diagnostics, help, print output, docs, skills,
     and all-features examples.
   - Prove invalid strings/types, inheritance/overrides, sync-out rules, built-in
     defaults, and recipe-reference policy validation.

2. **Runtime adapter and capability check**
   - Implement bounded Docker, Podman, and nerdctl detection through one narrow
     interface.
   - Prove priority, unusable-client fallback, aggregate rejection diagnostics,
     cancellation, and no-probe static inspection.

3. **Deterministic image plan and identity**
   - Add `[recipes.<name>.system].base_image`, profile-selected slim Go,
     Node/Bun, Rust, and Ubuntu defaults plus the shared structured Node
     package-manager identity; consume the prerequisite Rust profile's resolved
     contract without parallel detectors.
   - Generate canonical Containerfile/build contexts and ordered layer
     keys/tags/labels, with the dependency stage as the final recipe image.
   - Prove same-input reuse; cross-project tooling reuse; project/recipe
     collision resistance; one-way invalidation that never rebuilds lower
     layers; rebuild of affected higher layers from the nearest valid parent;
     no rebuild on command/argument-only changes; label mismatch rejection; and
     failure without stale fallback.

4. **Single-container recipe lifecycle**
   - Add the statically built, read-only invocation-mounted helper and private
     resolved-plan/secret protocol without including helper identity in image
     keys.
   - Use a copied workspace bind mount and one automatically removed container.
   - Prove interactive and redirected I/O, success, empty `for_each`, setup/main/
     post failure, nested references, logging, cancellation, forced cleanup,
     sync-out, sync-out deletion, and `--sync-out-all`.

5. **Mutable build-cache providers**
   - Implement runtime-labelled named-volume ownership, compatibility keys,
     stable project-path mounts, provider locking, cache-backed sync-out export,
     read-only inspection, and scoped reset; reserve the `cache` command name
     and align completion, help, docs, and stable JSON output.
   - Prove a cold and warm Go cache; shared `test`/`build` cache reuse; a cold
     and warm Rust Cargo target cache without broad recompilation; source,
     feature, profile, and target changes; concurrent access; cancellation;
     recognized corruption retry; unknown failure preservation; UID/GID and
     rootless behavior; shared-cache reset reporting; and unavailable size
     reporting.

6. **One supported image preparation path**
   - Implement the smallest complete profile/distribution combination first.
   - Add package-manager setup, including validated Corepack activation where
     selected; system packages; exact requirement tools; and locked dependencies
     only where their confined outputs and runtime behavior are proven.
   - Expand support one adapter at a time rather than adding speculative generic
     abstractions.

7. **Private dependencies and capable-host validation**
   - Prove non-persistent secret delivery for each supported runtime before
     enabling private dependency builds; verify image history and layers do not
     contain supplied secrets.
   - Run the full path against each supported runtime on representative hosts,
     including absent images, warm images, build failures, engine outages,
     concurrent cold starts, cancellation, rootless volume ownership, SELinux
     hosts where applicable, cache inspection/reset, and runtime cleanup.
   - Do not claim production readiness based only on mocked CLI tests.

## Acceptance boundary

The first useful end-to-end slice is complete when one documented recipe can
set `sandboxed = "system"`, automatically select the correct profile base and
package-manager tooling, detect an installed runtime, build five independently
identified ordered layers on the first run, reuse them on the second run,
mount a compatible project build cache, execute the full lifecycle in one
ephemeral container, and sync a selected successful output without modifying
the checkout on failure. A demonstrated Rust workspace must then run `test`
followed by `build` without recompiling unchanged dependencies or crates, and
`cache inspect` must identify the shared volume and its exact reset operation.

Broader profile provisioning, more distributions, port publication, custom
networking, user-selected runtime preference, remote image sharing, and cache
cleanup remain later candidates until the core path is demonstrated.
