# Known Limits

Shadowtree intentionally keeps the feature set small and explicit.

- Shadowtree is not a complete untrusted-code security sandbox.
- Shadowtree does not require reflinks.
- Copied workspaces use a new path per run; tests that open files still miss
  Go's test cache because it records opened files by absolute path.
- Shadowtree does not currently provide Docker, remote execution, matrix jobs,
  watch mode, or persistent named sessions.
- Built-in language profiles currently cover Go, Node, and Rust.
- Editor integrations complement runtime validation; the CLI loader remains
  authoritative.

For the full behavioral reference, see [Behavior Spec](reference/spec.md).
