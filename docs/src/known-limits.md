# Known Limits

Shadowtree intentionally keeps the feature set small and explicit.

- Shadowtree is not a complete untrusted-code security sandbox.
- Shadowtree does not require reflinks.
- Shadowtree does not currently provide Docker, remote execution, matrix jobs,
  watch mode, or persistent named sessions.
- Built-in language profiles currently cover Go and Node.
- Editor integrations complement runtime validation; the CLI loader remains
  authoritative.

For the full behavioral reference, see [Behavior Spec](reference/spec.md).
