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

The `install` recipe uses default `go install`, honors `FISH_CONFIG_DIR` and
`FISH_COMPLETIONS_DIR`, generates completion from `shadowtree` on `PATH`,
installs fish completion when `fish` is available, and appends single guarded
eval lines to `~/.bashrc` and `~/.zshrc` when those shells are available.

The `install-skill` recipe installs every local agent skill from
`.agents/skills/` to `${AGENTS_SKILLS_DIR:-$HOME/.agents/skills}`.
