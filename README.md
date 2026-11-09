# keepgoing-agents

`keepgoing` keeps a long-running coding-agent loop going. It watches an agent session, notices when the session has gone idle, stalled, or hit an error, and re-prompts it, with a hard rule that a person's stop or pause always wins. It is a single binary with no runtime, built to be called from cron, launchd, CI, or an agent IDE's automation. It is not tied to any IDE or provider.

The tool never edits, commits to, or merges a repository. The agent does the work. `keepgoing` only keeps the agent working.

## Why

Unattended agent loops die for boring reasons: the session finishes a turn and nobody sends the next one, a turn hangs with no progress, or the provider returns an error at three in the morning. Watching for that by hand does not scale, and watching for it by grepping transcripts for words like "limit" produces false positives that reroute whole loops. `keepgoing` decides from structured status only (exit codes, JSON fields, HTTP status) and treats anything it cannot classify as unknown, which never triggers a drastic action.

## Status

Early. Milestone one is in place: supervise one loop through a BB thread or any process you can describe with a status command and a resume command. Confirmed fallbacks across providers, signed webhook reports, and a daemon mode are the next milestones; see [docs/roadmap.md](docs/roadmap.md).

## Install

```sh
go install github.com/connorburgess/keepgoing-agents/cmd/keepgoing@latest
```

Go 1.24 or newer. Prebuilt binaries will come with the first tagged release.

## Quick start

Write `keepgoing.yaml`:

```yaml
version: 1
loop:
  name: example-loop
  state_dir: ~/.local/state/keepgoing/example-loop
  adapter: bb
  bb:
    thread: thr_example123
  prompt: |
    Continue the authorized loop. Honor any newer stop or pause request first.
  progress:
    repositories: [~/src/example-app]
    stall_after: 30m
    min_wake_interval: 60m
  window:
    tz: UTC
    days:
      Mon: ["08:00", "23:00"]
      Tue: ["08:00", "23:00"]
```

Then:

```sh
keepgoing validate
keepgoing enable
keepgoing tick          # run one pass; schedule this every few minutes
keepgoing status
keepgoing pause --note "reviewing by hand"
keepgoing resume
keepgoing stop          # stop the running session on the next tick, send nothing more
```

Each `tick` prints one JSON line describing what it saw and did, so a scheduler or IDE can read it. Nothing is sent until you run `enable`; a missing control file means disabled.

## How a tick decides

1. Stop requested: stop the session if it is running. Nothing else happens.
2. Disabled, paused, or awaiting user: do nothing, do not even query the provider.
3. Observe the session through the adapter. If the adapter cannot report, take no action.
4. Outside the configured time window: do nothing.
5. Idle with nothing queued and no background work: send the prompt.
6. Active but the progress fingerprint has not changed for `stall_after`, and nothing was sent in the last `min_wake_interval`: queue a nudge behind the live turn.
7. Error: re-prompt with backoff, up to `max_reprompts`, then mark the loop awaiting user. An auth error goes straight to awaiting user. No error class causes a provider switch in this milestone.
8. Anything else (stopping, pending, unknown): do nothing.

The progress fingerprint hashes Git metadata and the size and modification time of changed tracked files. It never reads file contents or transcripts.

## Adapters

- **bb**: a [BB](https://getbb.app) thread, observed with `bb thread show --json` and prompted with `bb thread tell`.
- **process**: any session you can describe with three commands. The status command prints a small JSON object; the resume command receives the prompt on stdin and the mode in `KEEPGOING_MODE`; an optional stop command halts the session. Non-zero exit codes from the status command can be mapped to error classes. See [docs/configuration.md](docs/configuration.md).

## Documentation

- [docs/architecture.md](docs/architecture.md): packages, boundaries, and the rules that do not bend.
- [docs/configuration.md](docs/configuration.md): every field in `keepgoing.yaml`.
- [docs/roadmap.md](docs/roadmap.md): what comes next.
- [examples/](examples/): configurations to copy.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Use fictional data everywhere.

## License

[Apache-2.0](LICENSE).
