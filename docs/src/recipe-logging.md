# Recipe Logging

Use recipe logging when a run should keep a copy of selected stage output.

```toml
[recipes.test]
cmd = "go test ./..."
log = "logs/test-{run_id}.log"
log_stages = ["pre", "cmd", "post"]
log_tee = true
```

## Log Path

`log` is expanded with recipe placeholders, including `{run_id}`. It must be a
relative local path under the active config file directory, or under the source
checkout when no config path exists.

Parent directories are created with mode `0755`, and the log file is opened or
truncated with mode `0644` before recipe commands start.

## Logged Stages

`log_stages` selects which stages to write to the log. Valid values are:

- `pre`
- `cmd`
- `post`

If `log_stages` is omitted, Shadowtree logs all three stages. The `cmd` stage
includes every `for_each` main command item. The `for_each` value-provider
command itself is not `cmd` stage output.

## Tee Behavior

`log_tee` defaults to `true`, preserving terminal output while writing selected
stage output to the log.

Set `log_tee = false` to send selected stage stdout and stderr only to the log.

## Boundaries

Each selected logged command is preceded by a compact boundary:

```text
== pre[0]: <script> ==
== cmd: @build ==
== post[0]: <script> ==
```

Long one-line commands are truncated in the boundary. Multiline script bodies
are shown as `<script>` and are not dumped into boundary lines.

When a selected parent stage invokes a nested recipe, nested output is written
through the parent stage writers. Nested recipe `log` settings do not open
another file during a recipe reference.
