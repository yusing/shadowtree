# Shadowtree Zed Extension

This extension adds a dedicated `Shadowtree TOML` language for Shadowtree TOML
config files.

It provides:

- TOML syntax highlighting based on `tree-sitter-toml`.
- Extra Shadowtree highlighting for recipe sections, `vars`, `var_commands`,
  and argument tables.
- Shell semantic highlighting for script-valued `cmd`, `pre`, `post`,
  `for_each`, `shell_prelude`, and `values` strings with `shell = "sh"` or
  `shell = "bash"`.
- Completion, diagnostics, and semantic tokens through `shadowtree-lsp`.

For installed usage, `shadowtree-lsp` must be available on Zed's `PATH`. If the
Shadowtree LSP does not start on initial open, or if Zed cannot find
`shadowtree-lsp`, set an explicit binary path in Zed settings:

```json
{
  "lsp": {
    "shadowtree-lsp": {
      "binary": {
        "path": "/home/you/go/bin/shadowtree-lsp",
        "arguments": []
      }
    }
  }
}
```

This removes ambiguity from startup and command discovery. Desktop-launched Zed
not inheriting the same `PATH` as your shell is one common cause.

When developing the extension remotely, Zed may upload and reload the dev
extension after it has already started `shadowtree-lsp`. Zed stops language
servers owned by the reloaded extension and may leave the existing buffer with a
`Stopped` server until `Restart Server` is run. That is a dev-extension reload
lifecycle issue, not a `shadowtree-lsp` path problem.

The following settings still remove startup ambiguity and let the server attach
when Zed classifies an already-open Shadowtree config as plain `TOML`:

```json
{
  "languages": {
    "TOML": {
      "language_servers": ["shadowtree-lsp", "..."]
    }
  },
  "file_types": {
    "Shadowtree TOML": [
      ".shadowtree.toml",
      "**/.shadowtree.toml",
      "*.shadowtree.toml",
      "**/*.shadowtree.toml",
      ".shadowtree/*.toml",
      "**/.shadowtree/*.toml"
    ]
  }
}
```

For installed extension usage, where Zed is not re-uploading the dev extension
on launch, the server starts normally from those settings or from Zed's `PATH`.

During development inside the Shadowtree checkout, the extension runs the local
server when no explicit binary is configured so LSP changes take effect without
installing a binary:

```sh
go run ./cmd/shadowtree-lsp
```

The LSP can also be enabled for generic `TOML` through user settings as a
fallback for Zed startup ordering, but that attaches Shadowtree diagnostics to
generic TOML buffers. Prefer explicit `Shadowtree TOML` file associations when
ordinary TOML files are open in the same workspace.
