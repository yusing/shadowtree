# Sandboxing and Sync-Out

Sandboxed recipe writes stay inside the temporary workspace. The host checkout
is unchanged unless sync-out is requested.

The `sandboxed` field accepts three values:

- omitted or `true`: namespace overlayfs with copied-workspace fallback;
- `false`: direct host-checkout execution;
- `"system"`: the system-container backend, with no fallback to either other
  mode.

Static help, completion, config validation, and `--print` do not probe a
container runtime. A system plan reports `runtime: <not probed>`. Until the
runtime adapter is available, execution fails explicitly without creating a
workspace or falling back.

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
