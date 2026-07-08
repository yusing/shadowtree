# Editor Support

Shadowtree includes a shared JSON Schema for Shadowtree TOML config files plus
editor integration files for Zed and VS Code under `editors/`.

The Zed extension provides a dedicated `Shadowtree TOML` language, syntax
highlighting, Shadowtree-specific highlighting, shell semantic highlighting for
script-valued fields, and LSP completion, diagnostics, and semantic tokens.

The VS Code extension binds the shared schema to Shadowtree TOML files through
Even Better TOML. Completion, hover, and validation come from that extension.

Install the Zed language server with:

```sh
go install github.com/yusing/shadowtree/cmd/shadowtree-lsp@latest
```

See [the full spec](reference/spec.md), the
[Zed extension README](https://github.com/yusing/shadowtree/blob/main/editors/zed-shadowtree/README.md), and the
[VS Code extension README](https://github.com/yusing/shadowtree/blob/main/editors/vscode-shadowtree/README.md) for
implementation details.

## VS Code Config

```json
"files.associations": {
  ".shadowtree.toml": "toml",
  "**/.shadowtree.toml": "toml",
  "*.shadowtree.toml": "toml",
  "**/*.shadowtree.toml": "toml",
  ".shadowtree/*.toml": "toml",
  "**/.shadowtree/*.toml": "toml"
},
"evenBetterToml.schema.associations": {
  "^file://.*/[^/]*\\.shadowtree\\.toml$": "https://raw.githubusercontent.com/yusing/shadowtree/main/schemas/shadowtree.schema.json",
  "^file://.*/\\.shadowtree/.*\\.toml$": "https://raw.githubusercontent.com/yusing/shadowtree/main/schemas/shadowtree.schema.json"
}
```

## Zed Config

```json
"file_types": {
  "Shadowtree TOML": [
    ".shadowtree.toml",
    "**/.shadowtree.toml",
    "*.shadowtree.toml",
    "**/*.shadowtree.toml",
    ".shadowtree/*.toml",
    "**/.shadowtree/*.toml"
  ]
},
"languages": {
  "TOML": {
    "language_servers": ["shadowtree-lsp", "..."]
  }
}
```
