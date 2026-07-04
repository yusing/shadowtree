(comment) @comment.inclusive
(string) @string

((string) @function @function.special
  (#match? @function "^['\"]@[^'\"[:space:]]+['\"]$"))
