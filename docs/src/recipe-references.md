# Recipe References

Recipe references compose Shadowtree recipes without starting another
Shadowtree process.

```toml
[recipes.generate]
cmd = "go generate ./..."

[recipes.test]
pre = ["@generate"]
cmd = "go test ./..."
```

Referenced recipes run in the current workspace. They do not create a nested
sandbox and do not run their own sync-out. Sync-out belongs to the top-level
invoked recipe.

## Direct References

A command string that is exactly `@recipe` or `@path:recipe` invokes another
recipe:

```toml
cmd = "@build"
cmd = "@webui:gen-schema"
pre = ["@generate"]
```

In command-list fields such as `pre` and `post`, only strings that are exactly
`@recipe` or `@path:recipe` are direct references. Bracket-style arguments are
also part of the reference form, such as `@build[mode=dev]` or
`@webui:gen-schema[mode=dev]`. Other strings run in the shell.

## Command-Position Dispatch

In `sh` and `bash` script commands, a literal `@recipe` command word also
dispatches a recipe:

```toml
[recipes.test]
cmd = '''
if [ -f schema.json ]; then
	@generate mode=dev
fi
'''
```

Leading assignment prefixes apply to that recipe command's environment:

```sh
FOO=bar @generate mode=dev
```

Assignment values, variables, quoted text, and ordinary command arguments do
not dispatch recipes:

```sh
FOO="@generate"
$FOO
echo @generate
```

## Cross-Config References

Use `@path:recipe` to invoke a recipe from another Shadowtree config:

```toml
[recipes.gen-schema]
cmd = "@webui:gen-schema"
```

The path is resolved relative to the referencing config file. Shadowtree loads
`path/.shadowtree.toml`, and the referenced recipe runs from that path.

## Arguments

Use bracket-style arguments to pass named or positional args:

```toml
pre = ["@build[component=api, mode=dev]"]
cmd = "@test[package=./internal/recipe]"
post = ["@webui:gen-schema[mode=dev]"]
```

Comma separators split the argument list, and surrounding whitespace is
ignored. Static references such as `@generate` and `@webui:gen-schema` can be
validated by the editor; dynamic references such as `@{target}` are resolved at
runtime after placeholder expansion.
