# Shell Commands

Shadowtree recipes use shell command strings for process execution.

```toml
shell = "bash"

[recipes.example]
cmd = '''
set -euo pipefail
echo "hello"
'''
```

If `shell` is not set, Shadowtree uses `sh`. Supported config shells are `sh`
and `bash`; fish is supported only as a generated CLI completion shell.

## Command Strings

Command strings run through the configured shell after placeholder expansion.
A string that is exactly `@recipe` or `@path:recipe` invokes another recipe;
other strings run in the shell.

```toml
[recipes.test]
cmd = 'go test "{pkg}" {@}'
```

Use [Typed Arguments](typed-arguments.md) and placeholders for validated
inputs. Put defaults in argument definitions rather than by parsing raw shell
args yourself.

## Shell Prelude

Top-level `shell_prelude` is prepended to every script command:

```toml
shell_prelude = '''
require_tool() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "$1 is required" >&2
		exit 1
	}
}
'''
```

Recipe-level `shell_prelude` is appended after the top-level prelude for that
recipe.

## Multiline Scripts

Use multiline shell strings when a workflow needs shell functions,
conditionals, pipes, or multiple statements. Shadowtree shows multiline scripts
as `<script>` in help, completion, verbose boundaries, and log boundaries, so
large scripts do not flood inspection output.

For quoting and placeholder modes, see [Placeholders](placeholders.md).
