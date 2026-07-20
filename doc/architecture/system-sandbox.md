# System sandbox architecture

This contract implements REQ-SBOX-002, REQ-SBOX-008, REQ-SBOX-009, and
REQ-SBOX-011 by replacing the unconditional system-workspace copy with one
runtime-owned overlay strategy and a behaviorally equivalent copied fallback.

## CTR-SBOX-001 — Represent one private workspace result

The runner owns one system-workspace object containing the canonical source
lower path, a private invocation root, upper and work directories, protected
whiteouts, cache-export root, and the selected mount strategy. The object owns
materialization, selected-path sync, whole-workspace sync, failure-log recovery,
and cleanup; runtime adapters only make its writable view visible in the
container.

The overlay upper is the authoritative change set. Regular files and symlinks
represent creates or replacements, directories represent changed directory
metadata and merged children, opaque directories hide lower children, and
kernel or `user.overlay.whiteout` entries represent deletions. Sync uses the
existing rooted copy and overlay-application primitives rather than teaching
runtime adapters a second filesystem-diff format.

A copied fallback implements the same object with a materialized root and no
upper layer. Callers select paths and lifecycle outcomes without branching on
the underlying strategy.

## CTR-SBOX-002 — Prepare protected lower-layer exclusions

System workspace preparation performs only the metadata and access checks
needed to preserve REQ-SBOX-008 exclusions. It creates protected upper-layer
whiteouts for `.git`, unreadable paths, and unsupported special files, but
retains `.shadowtree.toml`, `.shadowtree/`, includes, and other configuration
needed by nested cross-config references. A protected whiteout is never applied
to the host during whole-workspace sync.

The system exclusion policy and normal workspace exclusion policy are explicit
inputs to one whiteout owner. They must not be inferred from destination paths
or duplicated as runtime-specific filters. Unreadable paths remain recorded for
quoted diagnostics and retain the existing overlap rejection for selected
sync-out and whole-workspace sync. Any protected exclusion overlapping a
selected sync-out path likewise fails before user code so materialization can
never reinterpret that exclusion as a host deletion.

Upper and work directories are created below one private temporary directory so
they share a filesystem. Preparation validates path delimiters, extended
attribute support, directory entry type support, lower compatibility, and all
required ownership before any runtime mount or user code.

## CTR-SBOX-003 — Let each runtime own its visible overlay mount

Shadowtree does not pass the normal sandbox's private user-namespace mount to a
container engine. That mount is private and Docker resolves bind sources in the
daemon namespace. Instead, each capable runtime creates the overlay where its
container setup can see it:

- Podman uses its native overlay-volume form with the canonical source as lower
  and Shadowtree-managed `upperdir` and `workdir`. Rootless execution retains
  the validated `--userns=host` mapped-root contract.
- Docker creates one invocation-scoped, labelled local-driver volume using an
  OverlayFS mount with the canonical source, upper, work, and `userxattr`, then
  mounts that volume with copy-up disabled. The volume is lifecycle state, not
  a project build cache, and never appears in cache inspection.
- nerdctl uses the copied fallback while its supported CLI cannot express the
  required driver-specific volume options.

Runtime detection remains bounded and state-free. It records which workspace
strategies a candidate can express from exact CLI capabilities; a functional
overlay attempt occurs only after runtime selection and uses invocation-scoped
state. A Docker engine that satisfies the baseline lifecycle contract but does
not expose local-volume `--driver` and `--opt` support remains usable with the
copied strategy. Overlay failure may select the copied fallback only if container
creation proves user code did not start. A start or attach failure never causes
automatic replay through another strategy.

SELinux-enabled execution uses an overlay only when the runtime can label the
merged mount for the private container without recursively relabeling or
changing the source lower tree. Otherwise the runtime selects the copied
fallback and retains private relabelling of Shadowtree-owned temporary paths.
Shadowtree never adds privileged mode, host `CAP_SYS_ADMIN`, an engine socket,
or source relabelling to make overlay setup succeed.

## CTR-SBOX-004 — Preserve lifecycle and cache-export ordering

The complete resolved lifecycle still runs once at the canonical source path.
Before an overlay is mounted, Shadowtree represents every existing dependency
seed target as an intentional, non-protected upper-layer whiteout. The confined
lifecycle therefore creates a fresh seeded target without recursively traversing
stale or differently owned lower-layer dependencies; the lower tree remains
unchanged, and the newly seeded target remains eligible for ordinary sync-out.
Copied workspaces retain their existing remove-and-replace behavior. Nested
references, aggregate boundaries, environment, cancellation, `post`, and
first-error behavior do not depend on the workspace strategy.

On complete success, selected sync-out follows this order:

1. validate and normalize selected paths;
2. materialize only those paths from the source lower plus upper exclusions and
   changes, or select them directly from the copied fallback;
3. merge cache-backed exports at their owning paths;
4. apply each completed path to the host through existing rooted confinement
   and missing-path-as-deletion behavior.

Whole-workspace sync applies upper changes and protected-whiteout exclusions to
the source, with cache exports merged into the final result before the
lifecycle returns. It may materialize the full private result when required for
safe ordering. Without whole-workspace sync, unchanged lower content is never
copied merely to prepare or export a system lifecycle.

On failure or cancellation, ordinary upper changes and cache exports are
discarded. A configured recipe log is recovered from its selected upper or
copied-fallback path after the container stops and before workspace cleanup.

## CTR-SBOX-005 — Make fallback and progress explicit

Overlay setup and copy fallback are two implementations of `sandboxed =
"system"`; neither may invoke host or normal workspace execution. Default
stderr progress reports workspace preparation immediately and exposes overlay
setup or copy fallback as meaningful phases. Verbose diagnostics identify the
runtime, strategy, and quoted fallback reason without exposing source,
temporary, image, identity, or plan values already covered by redaction.

Fallback copying preserves the existing `.git`, unreadable-path, mode, symlink,
special-file, cache-export, log, and sync semantics. An unavailable overlay is
not itself fatal when copying remains safe. An unavailable copy fallback, an
unsafe sync boundary, or uncertainty about whether user code started fails the
lifecycle rather than retrying or weakening isolation.

## CTR-SBOX-006 — Prove cleanup, confinement, and performance

Cleanup order is container, runtime-owned overlay mount or temporary volume,
upper/work state, materialized sync roots, export root, helper, plan, and
invocation root. Every stage is bounded and cancellation-independent. Cleanup
must handle rootful Docker work-directory ownership through the runtime or
another narrowly confined runtime-owned operation; ignored host `RemoveAll`
errors are not sufficient. The primary lifecycle error and cleanup error are
joined without losing an available recipe exit code.

Unit tests validate exact runtime argv, option delimiters, strategy selection,
whiteout formats, protected exclusions, materialization, cache merge ordering,
failure logs, and cleanup errors. Capable-host tests cover rootful and rootless
Docker, rootless Podman, enforcing-SELinux copied fallback for Docker and
Podman, unsupported OverlayFS filesystems, container create/start failures,
cancellation, and nerdctl copy fallback. Each test proves that the source is
unchanged without sync-out and that source labels are unchanged.

Performance evidence compares copied and overlay setup on a large-file and a
large-inode workspace. Overlay setup may remain linear in metadata entries, but
before user code it must copy no readable lower-file contents and create only
the protected whiteouts and runtime-owned mount state required by this
contract.
