# Shadowtree Manual

Shadowtree is a project-local development task runner. Commands are declared as
recipes in `.shadowtree.toml`, resolved into inspectable plans, and run in a
sandbox by default so ordinary checks and builds do not mutate the host
checkout.

## Start Here

- [Getting Started](getting-started.md) - install Shadowtree, create a config,
  run recipes, and inspect plans.
- [Sandboxing and Sync-Out](sandboxing-and-sync-out.md) - understand isolated
  execution, direct checkout edits, and copying generated files back.
- [CLI Inspection](cli-inspection.md) - inspect help, recipe listings, printed
  plans, expanded plans, and dry checks.
- [Configuration Files](configuration.md) - understand config discovery and the
  top-level TOML shape.
- [Recipe Fields](recipes.md) - see the recipe fields that define a workflow.
- [Typed Arguments](typed-arguments.md) - define positional and named recipe
  inputs with validation and completion.
- [Profile Selection](built-in-profiles.md) - choose Go, Node, or Rust built-ins
  explicitly or through marker detection.
- [Editor Support](editor-support.md) - JSON Schema, Zed, VS Code, and
  `shadowtree-lsp`.
- [Development](development.md) - recipes used by the Shadowtree repository
  itself.

## Feature Areas

- Configuration: [includes](includes.md), [variables and environment](variables-and-env.md),
  [shell commands](shell-commands.md), [placeholders](placeholders.md),
  [recipe references](recipe-references.md), and [recipe resolution](recipe-resolution.md).
- Recipes: [lifecycle](recipe-lifecycle.md), [fan-out and workdirs](fan-out-and-workdirs.md),
  [logging](recipe-logging.md), [tool requirements](tool-requirements.md), and
  [reserved names](reserved-names.md).
- Arguments: [typed arguments](typed-arguments.md), [value providers](value-providers.md),
  [variadic args](variadic-args.md), and [presets](presets.md).
- Profiles: [Go](go-profile.md), [Node](node-profile.md), and
  [Rust](rust-profile.md).
- Integrations: [shell completion](shell-completion.md) and [editor support](editor-support.md).

## Reference

- [Full behavior spec](reference/spec.md)
- [Known limits](known-limits.md)
- [All-features example](https://github.com/yusing/shadowtree/blob/main/examples/all-features.shadowtree.toml)
- [All-features base include](https://github.com/yusing/shadowtree/blob/main/examples/all-features-base.shadowtree.toml)
- [Benchmark presets example](https://github.com/yusing/shadowtree/blob/main/examples/benchmark-presets.shadowtree.toml)
