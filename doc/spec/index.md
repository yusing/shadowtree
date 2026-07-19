---
pjdoc:
  version: 1
  kind: spec
  scope: root
  status: draft
  revision: SPEC-1
  files:
    - system-sandbox.md
    - rust-profile.md
---
# Shadowtree specification

Revision `SPEC-1` is intentionally limited to the new Rust profile prerequisite
and system sandbox. Existing README, mdBook, behavior-specification, and
workflow documentation remains outside this pjdoc catalog under `DFR-001`.

Implementation order is authoritative: complete and independently verify the
Rust profile requirements before beginning the system sandbox requirements.

- [System sandbox requirements](system-sandbox.md)

- [Rust profile requirements](rust-profile.md)
