; Base TOML highlighting from zed-extensions/toml.
(bare_key) @property
(quoted_key) @property
(boolean) @constant
(comment) @comment
(integer) @number
(float) @number
(string) @string
(escape_sequence) @string.escape
(offset_date_time) @string.special
(local_date_time) @string.special
(local_date) @string.special
(local_time) @string.special

[
  "."
  ","
] @punctuation.delimiter

"=" @operator

[
  "["
  "]"
  "[["
  "]]"
  "{"
  "}"
] @punctuation.bracket

; Shadowtree recipe references.
((string) @function @function.special
  (#match? @function "^['\"]@[^'\"[:space:]]+['\"]$"))

; Shadowtree config keys.
(pair
  (bare_key) @keyword
  (#any-of? @keyword
    "include"
    "profile"
    "shell"
    "shell_prelude"
    "sync_out"
    "env"
    "vars"
    "var_commands"
    "recipes"
    "help"
    "arguments"
    "sandboxed"
    "for_each"
    "workdir"
    "cmd"
    "pre"
    "post"
    "type"
    "path_kind"
    "position"
    "required"
    "default"
    "values"))

; Variables and generated variables.
(table
  (bare_key) @_section
  (pair
    (bare_key) @property @variable.special)
  (#any-of? @_section "vars" "var_commands"))

(table
  (dotted_key) @_section
  (pair
    (bare_key) @property @variable.special)
  (#match? @_section "(^|\\.)vars$"))
