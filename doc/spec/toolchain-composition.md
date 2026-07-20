# Composable toolchain requirements

System recipes may reference work from more than one supported project
ecosystem while retaining one immutable image plan and one lifecycle container.
Toolchain composition is built-in Shadowtree behavior; it does not expose
Containerfile fragments or arbitrary project setup commands.

## REQ-TOOL-001 — Compose every supported profile

Every supported profile has one Shadowtree-owned toolchain provider. Every
non-empty combination of supported profile toolchains is supported in one
system image. Shadowtree must never reject a combination merely because two
otherwise supported profile kinds occur together.

Adding a profile or a Node-profile toolchain variant is incomplete until its
provider composes with every existing provider. Supported profiles, built-ins,
toolchain providers, runtime validation, schema, LSP, completion, docs,
examples, and agent guidance must remain aligned through the existing profile
sources of truth.

Profiles contribute toolchains explicitly through the resolved recipe-reference
graph. Shadowtree never infers a toolchain by inspecting command text. A future
explicit toolchain requirement must resolve through the same providers rather
than introduce another installation mechanism.

## REQ-TOOL-002 — Resolve one canonical toolchain contract

The top-level system recipe and every transitively referenced recipe contribute
their profile toolchain, effective config identity, recipe, project-relative
workdir, platform, version constraint, variant, and provenance. Resolution
preserves those origins for diagnostics while producing a canonical sorted
toolchain set independent of the root profile and reference traversal order.

For one non-side-by-side toolchain kind, equal exact versions merge; an exact
version and an unspecified request select the exact version; two unspecified
requests select the provider's release-pinned default; and different exact
versions fail with both origins. Every accepted request resolves to an exact
identity before image or cache keys are computed.

Node profile resolution continues to use the project package-manager owner.
npm, pnpm, Yarn, and Bun are supported variants of that project toolchain and
retain their exact manager identity and provenance.

## REQ-TOOL-003 — Use one managed Trixie foundation

Automatically composed toolchains use the release-pinned managed
`debian:trixie-slim` foundation on a common selected platform. Shadowtree owns
deterministic setup and exact post-install verification for every selected
toolchain. Providers install into disjoint managed prefixes and publish only
declared commands and environment, so no provider depends on being the base,
first, or last toolchain.

An explicit `system.base_image` used with any toolchain or system package must
be a supported pinned Debian or Ubuntu foundation. Shadowtree still performs
complete provider setup and validates the actual distribution; it never assumes
the custom base already contains the root profile's toolchain. Other explicit
bases fail during planning. A recipe without toolchains and system packages may
retain the general pinned-base behavior defined by the system sandbox.

Provider setup guarantees coexistence of the language tools, not arbitrary
native project dependencies. Go cgo builds, Node/Bun native addons, Rust native
build scripts, and similar work may require compilers, libc development files,
headers, linkers, and project libraries declared as system packages.

## REQ-TOOL-004 — Share exact toolchain combinations

Two projects with the same foundation, platform, exact canonical toolchain
combination, provider contract versions, installation operations, verification,
and provider-owned environment share the immutable toolchain layer. An
unspecified request is share-capable because it resolves to an exact selected
version before identity is computed.

The reusable toolchain key excludes canonical project root, config path, recipe
name, origin and provenance paths, workdir, manifests, lockfiles, dependency
contexts, runtime arguments, run ID, and UID/GID. These values remain available
to diagnostics, dependency planning, recipe aliases, or project-owned mutable
cache identity as applicable.

Changing a project manifest without changing the resolved toolchain combination
must preserve the toolchain key. Changing the foundation, platform, exact
toolchain or package-manager identity, provider contract, or provider-owned ABI
input must change it.

## REQ-TOOL-005 — Prepare every contributed ecosystem

Each resolved toolchain contributes its locked dependency preparation, optional
dependency seed, and proven mutable cache descriptors for its owning
project-relative workdir. Plans and seeds remain separately owned through
validation, canonical ordering, keying, inspection, and lifecycle application;
they are not reduced to the root profile's dependency contract.

All seed paths are validated before any seed is copied. Escaping, duplicate, or
ancestor/descendant targets fail unless one provider proves the inputs
equivalent. Cache providers with the same destination must merge under one
compatible contract or fail before container creation; multiple distinct
volumes are never mounted at the same destination.

## REQ-TOOL-006 — Keep composition inspectable and fail closed

Static expanded output reports the managed or explicit foundation, platform,
canonical toolchain identities, variants, provenance and required-by origins,
shared toolchain key, provider setup and verification, dependency plans, seeds,
caches, native-build qualification, and conflicts without probing a runtime.

Planning fails before image construction for conflicting exact versions,
platform disagreement, provider filesystem, command, environment, dependency,
seed, or cache ownership conflicts, or an unsupported explicit foundation.
Build-time provider verification fails before user code when installed identity
or foundation assumptions are false. No failure selects a stale different-key
image or silently drops a contributed toolchain.

Acceptance covers every non-empty combination generated from the supported
profile/provider registry, every Node toolchain variant in representative mixed
sets, root/reference-order invariance, cross-project exact layer reuse,
different-version separation, plural locked preparations and seeds, cache
ownership, static inspection, cold build and exact reuse, failure,
cancellation, one-container lifecycle behavior, and top-level-only sync-out.
