# Recipe Lifecycle

Recipes run through `pre`, `cmd`, `post`, and optional sync-out in a fixed
order.

For a sandboxed recipe:

1. Create a temporary workspace with namespace overlayfs, or copy the source
   tree if namespace overlayfs is unavailable.
2. Open or truncate the configured log file when `log` is set.
3. Run `pre` commands in order.
4. Run the resolved main command, or once per `for_each` candidate when set.
5. Run `post` commands in order.
6. If all phases succeeded, sync configured or requested paths back.
7. Remove the temporary workspace.

For an unsandboxed recipe, Shadowtree skips the temporary workspace and runs
directly from the source checkout. Sync-out is not performed because command
writes already target the host checkout.

`sandboxed = "system"` is a distinct sandboxed lifecycle. It retains sync-out
semantics and never falls back to the ordinary disposable workspace or direct
host execution. Static inspection does not probe a runtime. After preparing the
immutable image chain, Shadowtree copies the checkout to a private workspace and
mounts that copy read-write at the checkout's canonical path in one ephemeral
container. A private read-only helper and resolved-plan file run `pre`, main or
`for_each`, nested references, and `post` without mounting the host checkout.
Successful runs apply existing sync-out rules from the private copy; failures
and cancellation export no selected outputs. Logs retain their normal behavior.

## Failure Behavior

- If a `pre` command fails, the main command is skipped.
- `post` commands still run after a `pre` or main command failure.
- `post` commands run as cleanup after the run context is canceled, such as by
  SIGINT.
- Sync-out does not run after failure.
- The process exits with the first failing command's exit code when available.
- Recursive recipe references fail with a cycle error.

## Structured Stages

Use a structured `pre` or `post` table when a stage command needs execution
controls:

```toml
[recipes.benchmark.pre]
cmd = "benchmark_prepare"
timeout = "120s"
```

`timeout` is parsed as a Go duration and must be greater than zero. It limits
that one stage command. Timeout failure follows normal stage-order rules: a
failing `pre` skips the main command, and `post` commands still run.

## Retry

Use `@retry` in a shell command position to retry flaky setup or readiness
checks:

```toml
pre = "@retry[count=30,delay=1s] benchmark_prepare"
```

`count` is the maximum number of attempts and `delay` is the duration between
failed attempts. Omitted values default to `count=3` and `delay=1s`.

`@retry` can wrap external commands, shell functions, or literal recipe
references:

```toml
pre = "@retry[count=5] @prepare"
```

When retrying a shell function under `set -e`, return failures explicitly, for
example `cleanup_step || return $?`; shells suppress `errexit` while command
status is tested by retry logic.
