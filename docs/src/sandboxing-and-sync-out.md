# Sandboxing and Sync-Out

Sandboxed recipe writes stay inside the temporary workspace. The host checkout
is unchanged unless sync-out is requested.

On Linux, Shadowtree uses overlayfs in a user and mount namespace by default.
When namespace overlayfs is unavailable, it warns and falls back to a copied
workspace with the same isolation contract. Other platforms always use the
copied workspace without a warning; on macOS APFS, files are copied as
copy-on-write clones.

Copied workspaces live at a new temporary path on every run. Go's build cache
keys include the package directory, so Shadowtree adds `-trimpath` to `GOFLAGS`
for copied workspaces unless `GOFLAGS` already sets a `-trimpath` flag. Set
`GOFLAGS=-trimpath=false` to opt out.

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
