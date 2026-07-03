(pair
  (bare_key) @_key
  (string) @redact
  (#match? @_key "(_?token|_?secret|password|key)$"))
