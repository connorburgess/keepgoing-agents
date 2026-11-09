# keepgoing-agents

`keepgoing` keeps long-running coding-agent loops going. It watches an agent session, re-prompts it when it goes idle or stalls, and hands the loop to a confirmed fallback when the session hits a usage limit. A person's stop or pause always wins. It is a single static binary with no runtime, built to be called from cron, launchd, systemd, CI, or an agent IDE's automations. It is not tied to any IDE or provider.

The tool never edits, commits to, or merges a repository. The agent does the work. `keepgoing` only keeps the agent working.

## Why

Unattended agent loops die for boring reasons. A session finishes a turn and nobody sends the next one. A turn hangs with no progress. The provider hits a usage limit at three in the morning. Watching for this by hand does not scale. Watching for it by searching transcripts for words like "limit" produces false positives that reroute whole loops.

`keepgoing` decides from structured status only: exit codes, JSON fields, HTTP status, and provider events. Anything it cannot classify is `unknown`, and `unknown` never moves a loop to another provider.

## What it does

- **Supervises one loop per config.** The loop can be a BB thread, a headless Claude Code or Codex session that keepgoing runs itself, or any process you describe with a status command and a resume command.
- **Wakes, nudges, and re-prompts.** An idle session gets the next prompt. A stalled one gets a nudge queued behind its turn. An errored one gets a bounded number of re-prompts with backoff, then waits for a person.
- **Fails over only to confirmed fallbacks.** `fallbacks.yaml` lists candidates in order. Each one is probed on a schedule with a cheap model and again with the exact target model right before a handoff. A handoff needs at least `min_healthy` confirmed fallbacks, counted across distinct providers by default. The default is 1; set 2 for real redundancy.
- **Waits out a limit that lifts soon.** When the provider reports a reset time within `wait_if_reset_within`, it waits instead of handing off.
- **Honors stop and pause at once.** The daemon watches the control file and acts on a change within seconds.
- **Reports through signed webhooks.** Payloads are signed with the [Standard Webhooks](https://www.standardwebhooks.com/) scheme. Bearer tokens are supported too.
- **Includes two helpers.** `ci-wait` waits for every workflow on an exact commit and follows a replacement run after a cancellation. `net-retry` reruns a command only while the network is down.

## Install

```sh
go install github.com/connorburgess/keepgoing-agents/cmd/keepgoing@latest
```

Go 1.24 or newer. Release binaries for macOS, Linux, and Windows are built by the release workflow when a version is tagged.

## Quick start

```sh
cp examples/keepgoing.bb.yaml keepgoing.yaml        # edit the thread id and paths
cp examples/fallbacks.yaml fallbacks.yaml           # list your real fallbacks
keepgoing validate
keepgoing fallbacks check                           # probe every candidate once
keepgoing enable
keepgoing tick                                      # or: keepgoing daemon
keepgoing status
```

Nothing is sent until you run `enable`. A missing control file means disabled.

Control the loop at any time:

```sh
keepgoing pause --note "reviewing by hand"
keepgoing resume
keepgoing stop                 # stop the running turn, then send nothing more
keepgoing retarget primary     # move back from a fallback by hand
```

Every completed `tick` prints one JSON line describing what it saw and did, so a scheduler or IDE can read it. A tick skipped because the lock was held, or stopped by a config error, prints only to stderr.

## Documentation

- [docs/architecture.md](docs/architecture.md): packages, boundaries, and the rules that do not bend.
- [docs/configuration.md](docs/configuration.md): every field in `keepgoing.yaml`.
- [docs/fallbacks.md](docs/fallbacks.md): candidates, probes, redundancy, and handoff.
- [docs/operations.md](docs/operations.md): schedulers, the daemon, webhooks, `ci-wait`, `net-retry`, and exit codes.
- [docs/testing.md](docs/testing.md): running the suite, including on machines without Go, and per-platform results.
- [docs/integrations.md](docs/integrations.md): every tool and service keepgoing talks to, and how far each is verified.
- [docs/roadmap.md](docs/roadmap.md): what comes next.
- [examples/](examples/): configurations and scheduler setups to copy.
- [schema/](schema/): JSON Schemas for editor completion and validation.

## Limitations

- The BB adapter needs the `bb` CLI. Error classification relies on BB's structured provider events. When those are missing, the error is `unknown`.
- The headless Codex adapter reads error codes from `codex exec --json` events, but it has not yet been checked against a real Codex usage-limit event. Until it is, a Codex error without a recognised code classifies as `unknown`. That means it re-prompts and then waits for a person instead of failing over. When Codex is the primary, running it through BB is more reliable, because BB reports rate-limit windows.
- Headless runners need macOS or Linux. On Windows, the BB and process adapters work, but the Claude Code and Codex runners do not.
- Switching back to the primary after a handoff is manual, with `keepgoing retarget primary`.
- A probe for a BB candidate runs the local `claude` or `codex` CLI with this machine's login, not through BB. Set `probe.env`, for example `CLAUDE_CONFIG_DIR` or `CODEX_HOME`, when a candidate uses a different account.
- A session that stays active without progress is nudged every `min_wake_interval` and never escalates to a person on its own.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Use fictional data everywhere.

## License

[Apache-2.0](LICENSE).
