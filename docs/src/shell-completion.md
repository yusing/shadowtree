# Shell Completion

Shadowtree can generate bash, fish, and zsh completion.

## Install Completion

Bash:

```sh
command -v shadowtree >/dev/null 2>&1 && eval "$(shadowtree completion bash)"
```

Fish:

```sh
shadowtree completion fish > ~/.config/fish/completions/shadowtree.fish
```

Zsh:

```sh
command -v shadowtree >/dev/null 2>&1 && eval "$(shadowtree completion zsh)"
```

The repository `install` recipe appends guarded eval lines for bash and zsh and
installs fish completion when fish is available.

## Dynamic Completion

Generated shell scripts call back into Shadowtree:

```sh
shadowtree __complete fish <words...>
shadowtree __complete bash <cursor> <line> [current]
shadowtree __complete zsh <words...>
```

Completion uses:

- configured recipes
- built-in recipes from the selected profile
- built-ins from a detected profile when no config is loaded
- recipe `help` text
- typed argument metadata
- `values` providers for the active argument

## Supported Behavior

- `shadowtree <TAB>` completes core commands and resolved recipes.
- `shadowtree te<TAB>` completes matching recipe names such as `test`.
- `shadowtree help <TAB>` completes recipe names.
- `shadowtree help test <TAB>` completes `color=false`.
- `shadowtree --profile <TAB>` completes `go` and `node`.
- `shadowtree build <TAB>` completes configured recipe arguments such as
  `project=`.
- `shadowtree benchmark preset=<TAB>` completes recipe-local preset names.
- `shadowtree build[<TAB>` completes bracket-style arguments such as
  `build[project=`.
- `shadowtree test race=<TAB>` completes `true` and `false` for bool
  arguments.
- `path` arguments complete relative paths, absolute paths, and `~/` paths.
- `rel_path` arguments complete relative paths only.
- `path_kind` filters path candidates to files, directories, or executable
  files.

Completion parses config, checks profile markers, and runs only argument
`values` commands needed for the active argument.
