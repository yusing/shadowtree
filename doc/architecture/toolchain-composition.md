# Composable toolchain architecture

This contract implements REQ-TOOL-001 through REQ-TOOL-006 by replacing the
single dominant profile image plan with one canonical composition assembled
from the resolved recipe-reference graph.

## CTR-TOOL-001 — Keep profiles and providers closed under composition

The registry underlying `recipe.SupportsProfile` and `recipe.Builtins` remains
the source of truth for supported profiles. Each profile descriptor identifies
one image toolchain provider; Node package-manager resolution selects the npm,
pnpm, Yarn, or Bun provider variant without creating an unrelated profile list.

Production validation requires exactly one provider for every supported
profile and no undeclared public profile provider. The complete selected set is
validated for filesystem prefixes, exported command names, environment keys,
platform, dependency targets, seed targets, and cache mount destinations.
Adding a profile requires generated contract coverage for all non-empty
combinations of the supported registry and capable-host representative coverage
with existing providers. This is the architecture invariant for REQ-TOOL-001;
there is no separate supported-combination allowlist.

## CTR-TOOL-002 — Preserve contributions until canonical resolution

The runner replaces the flattened system requirement result with structured
contributions containing source-relative canonical config identity, recipe
name, effective project-relative workdir, profile, platform constraint,
toolchain request, requirements, and reference origin. Same-config and
cross-config traversal use the same contribution shape and existing cycle
diagnostics.

Resolution merges constraints according to REQ-TOOL-002, records all origins,
selects exact defaults, and sorts the result by provider kind and exact
identity. Absolute checkout paths and traversal order never enter a reusable
image-stage key. Conflicts name both contributing recipes, config paths,
workdirs, requested identities, and the reference route where available.

## CTR-TOOL-003 — Install providers on a scoped Trixie filesystem

The default foundation is the managed `debian:trixie-slim` image selected by
the image-plan contract version. Provider setup may use verified archives or
official donor stages, but the final filesystem is always rooted in the common
foundation rather than a primary profile image.

The base stage verifies Debian/Ubuntu and installs `ca-certificates`, `curl`,
`tzdata`, and `wget` before any provider runs. Those packages are part of the
reusable base identity. The higher system-packages stage remains a literal
rendering of `requires.system_packages` and does not special-case matching
requests.

Providers install under disjoint versioned paths:

```text
/opt/shadowtree/toolchains/go/<identity>
/opt/shadowtree/toolchains/node/<identity>
/opt/shadowtree/toolchains/bun/<identity>
/opt/shadowtree/toolchains/rust/<identity>
```

They publish their declared executables through `/opt/shadowtree/bin` and
declare all environment changes. The complete contract rejects overlapping
owned paths, executable names, or environment keys unless one registry owner
defines the shared behavior. Setup runs in canonical provider order, but no
provider may depend on precedence to overwrite another.

Every provider verifies its exact version and relevant host/platform identity
after installation. Donor image, archive checksum, layout, setup operations,
verification, and environment form provider-versioned immutable inputs. An
explicit base is accepted only by the Debian/Ubuntu foundation owner, is
included in identity, and receives the same base-package setup, complete
provider setup, and early distribution check.

## CTR-TOOL-004 — Separate reusable tooling from project identity

The immutable chain remains:

```text
external managed or explicit foundation
-> verified foundation packages
-> composed toolchains
-> normalized system packages
-> exact typed provider commands
-> plural locked dependencies
```

The composed-toolchain stage key is a canonical encoding of the image-plan and
provider contract versions, foundation reference, platform, sorted exact
toolchain and manager identities, donor/archive inputs, installation and
verification operations, provider environment, and ABI inputs. It excludes all
project and origin identity listed by REQ-TOOL-004.

System-package, provider-command, and dependency stages include their immediate
parent key and only their owned inputs. The final recipe alias retains canonical
project and recipe ownership without contaminating reusable lower stages. The
plan version is intentionally bumped; the experimental single-profile image
keys have no migration or compatibility branch.

## CTR-TOOL-005 — Plan plural dependencies, seeds, and caches

Each provider resolves dependency manifests and lockfiles relative to its
contribution's canonical workdir. Dependency plans retain provider, exact
identity, normalized origin, workdir, commands, minimal context and hashes,
metadata, and optional seed through conflict validation and static inspection.
Only the final renderer combines their canonically ordered operations into the
dependency stage.

The lifecycle plan carries a sorted seed slice. It validates every source,
target, overlap, and confinement rule before copying any seed, then reports the
owning provider and target for a failure. A partial validation failure therefore
cannot make seed traversal order observable to user commands.

Cache descriptors are collected from every provider. Compatible descriptors
sharing a destination merge into one mount contract; incompatible overlaps
fail. Immutable toolchain layers may be shared across projects, while mutable
caches retain canonical project ownership and the compatibility identity
required by REQ-SBOX-007.

## CTR-TOOL-006 — Validate the registry and observable lifecycle

Pure planner tests generate every non-empty supported profile combination and
prove canonical resolution, provider ownership, stable order, exact and
unspecified version merging, root/reference-order invariance, cross-project
toolchain-key reuse, different-identity separation, explicit-foundation
rejection, and dependency/cache isolation.

Provider tests verify the generated setup and exact identity on Trixie.
Capable-host tests cover representative Node variants and the complete Go,
Node/Bun, and Rust composition while preserving REQ-SBOX-008 and REQ-SBOX-009:
one lifecycle container, progress, failure, cancellation, cache export, and
top-level-only sync-out. Static expanded inspection is exercised without a
runtime. Native builds are qualified rather than promised when their compiler,
headers, linker, or project libraries are not declared.
