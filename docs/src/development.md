# Development

This project uses Shadowtree for its own development tasks. Before installing a
`shadowtree` binary, run the local CLI with `go run`:

```sh
go run ./cmd/shadowtree recipes
go run ./cmd/shadowtree test
go run ./cmd/shadowtree check
go run ./cmd/shadowtree build
go run ./cmd/shadowtree install
```

After installing or building `shadowtree`, use the shorter form:

```sh
shadowtree test
shadowtree check
shadowtree build
shadowtree install
shadowtree fmt
shadowtree tidy
shadowtree install-skill
```

Recipes that intentionally change the host checkout set `sandboxed = false` in
`.shadowtree.toml`.

System-runtime confinement has an opt-in capable-host gate. Run it separately
for Docker, Podman, and nerdctl with a small Linux image that provides
`/bin/sh`, `id`, and `tr`:

```sh
SHADOWTREE_CAPABLE_RUNTIME=docker SHADOWTREE_CAPABLE_IMAGE=busybox:1.37.0 go test ./internal/systemsandbox -run TestCapableHostRuntimeConfinement -v
SHADOWTREE_CAPABLE_RUNTIME=podman SHADOWTREE_CAPABLE_IMAGE=busybox:1.37.0 go test ./internal/systemsandbox -run TestCapableHostRuntimeConfinement -v
SHADOWTREE_CAPABLE_RUNTIME=nerdctl SHADOWTREE_CAPABLE_IMAGE=busybox:1.37.0 go test ./internal/systemsandbox -run TestCapableHostRuntimeConfinement -v
```

Do not mark the multi-engine confinement gate complete from mocked adapters or
from only one runtime.

The `install` recipe uses default `go install`, honors `FISH_CONFIG_DIR` and
`FISH_COMPLETIONS_DIR`, generates completion from `shadowtree` on `PATH`,
installs fish completion when `fish` is available, and appends single guarded
eval lines to `~/.bashrc` and `~/.zshrc` when those shells are available.

The `install-skill` recipe installs every local agent skill from
`.agents/skills/` to `${AGENTS_SKILLS_DIR:-$HOME/.agents/skills}`, then removes
the legacy installed `shadowtree` skill.
