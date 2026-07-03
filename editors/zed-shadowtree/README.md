# Shadowtree Zed Extension

This extension adds a dedicated `Shadowtree TOML` language for `.shadowtree.toml`
and `shadowtree.toml` files.

It provides:

- TOML syntax highlighting based on `tree-sitter-toml`.
- Extra Shadowtree highlighting for recipe sections, `vars`, `var_commands`,
  and argument tables.
- Shell semantic highlighting for script-valued `cmd`, `shell_prelude`, and
  `values` strings with `shell = "sh"`, `shell = "bash"`, or `shell = "fish"`.
- Completion and semantic tokens through `shadowtree-lsp`.

For installed usage, `shadowtree-lsp` must be available on `PATH`. During
development inside the Shadowtree checkout, the extension can run the local
server with:

```sh
go run ./cmd/shadowtree-lsp
```
