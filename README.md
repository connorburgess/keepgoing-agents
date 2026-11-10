# keepgoing-agents

`keepgoing` keeps long-running coding-agent loops going. It watches an agent session, re-prompts it when it goes idle or stalls, and hands the loop to a confirmed fallback when the session hits a usage limit. A person's stop or pause always wins. It is a single static binary with no runtime, built to be called from cron, launchd, systemd, CI, or an agent IDE's automations. It is not tied to any IDE or provider.

The tool never edits, commits to, or merges a repository. The agent does the work. `keepgoing` only keeps the agent working.

## Why

Unattended agent loops die for boring reasons. A session finishes a turn and nobody sends the next one. A turn hangs with no progress. The provider hits a usage limit at three in the morning. Watching for this by hand does not scale. Watching for it by searching transcripts for words like "limit" produces false positives that reroute whole loops.

`keepgoing` decides from structured status only: exit codes, JSON fields, HTTP status, and provider events. Anything it cannot classify is `unknown`, and `unknown` never moves a loop to another provider.

## What it does

- **Supervises one loop per config.** The loop can be a BB thread, a headless Claude Code or Codex session that keepgoing runs itself, or any process you describe with a status command and a resume command.
- **Wakes, nudges, and re-prompts.** An idle session gets the next prompt. A stalled one gets a nudge queued behind its turn, and after `max_nudges` nudges without progress the loop asks a person. An errored one gets a bounded number of re-prompts with backoff, then waits for a person.
- **Fails over only to confirmed fallbacks.** `fallbacks.yaml` lists candidates in order. Each one is probed on a schedule with a cheap model and again with the exact target model right before a handoff. A handoff needs at least `min_healthy` confirmed fallbacks, counted across distinct providers by default. The default is 1; set 2 for real redundancy.
- **Supports multiple accounts in headless runners.** Set `claude.env` or `codex.env` per account, with separate quota groups. Turns, probes, and Codex limit checks use that account's environment. See the [three-subscription setup](docs/fallbacks.md#multiple-subscriptions).
- **Waits out a limit that lifts soon.** When the provider reports a reset time within `wait_if_reset_within`, it waits instead of handing off.
- **Knows when a person is needed.** A pending permission prompt or question, or a loop marked with `keepgoing await-user`, means nothing is sent. The person gets an `await_user` report instead of the loop sitting idle. See [docs/agent-chats.md](docs/agent-chats.md).
- **Honors stop and pause at once.** The daemon watches the control file and acts on a change within seconds.
- **Reports through signed webhooks.** Payloads are signed with the [Standard Webhooks](https://www.standardwebhooks.com/) scheme. Bearer tokens are supported too.
- **Includes two helpers.** `ci-wait` waits for every workflow on an exact commit and follows a replacement run after a cancellation. `net-retry` reruns a command only while the network is down.

## Looping and setup

keepgoing already runs the loop: it sends the configured task prompt when the agent becomes idle and applies the retry and fallback rules when work cannot continue. A separate loop skill is not required. Task instructions tell the agent what to do; the supervisor runs outside that agent session and decides when another turn may start. It needs a running host and scheduler or daemon to do this.

`keepgoing init` guides task setup, discovers installed supported CLIs, and writes a disabled configuration outside the checkout. It supports existing headless account directories and ordered fallbacks without requiring YAML edits. See the [setup guide](docs/setup.md). This is implemented on `main`; v0.1.1 does not include it.

`keepgoing doctor` checks setup, BB target identity, CLI versions, headless login status, private-file access, and recent tick history without model calls; [its report](docs/diagnostics.md) distinguishes credential presence from usable quota and marks unsupported checks explicitly.

`keepgoing status` now distinguishes current holds from [recorded decisions and outcomes](docs/operations.md#understanding-recorded-status), including failures, dry runs, stale history, and build-label differences. Live scheduler and headless-session diagnostics, simpler background operation, BB thread transfer, and Cursor/Antigravity adapters are [planned next](docs/roadmap.md#next-first-supervised-loop-in-five-minutes). Mark finished work with `keepgoing complete --note "Task verified"`. This persists a completion hold across restarts; only an explicit `resume` clears it. Completion is declared by the agent or operator, never inferred from transcript text. Cursor and Antigravity are not built-in adapters today; see the [integration plan](docs/integrations.md#planned-integrations).

## Install

With Homebrew on macOS or Linux:

```sh
brew install connorburgess/tap/keepgoing
```

With Go 1.24 or newer:

```sh
go install github.com/connorburgess/keepgoing-agents/cmd/keepgoing@latest
```

Prebuilt binaries for macOS, Linux, and Windows, with SHA-256 checksums, are on the [releases page](https://github.com/connorburgess/keepgoing-agents/releases).

## Quick start

With a build containing guided setup, run this from an existing Git project with at least one commit:

```sh
keepgoing init
```

Choose a task and agent, review the resolved plan, then run the `validate`, `enable`, and `daemon` commands printed with your config path. The default is a single agent with no fallbacks. Setup does not run agents or probes, change logins, or install a scheduler. See [guided and scripted setup](docs/setup.md) for accounts, operating limits, and noninteractive flags.

For v0.1.1 or advanced configuration, copy the appropriate [example](examples/) to a private directory outside the checkout and edit its paths. Run commands with `-c /path/to/keepgoing.yaml`. Nothing is sent until you enable the loop and run ticks or a daemon; a missing control file means disabled.

Control the loop at any time (add `-c` with your generated config path to each command):

```sh
keepgoing complete --note "Task verified"  # finish the task; hold future wakes
keepgoing pause --note "reviewing by hand"
keepgoing resume
keepgoing stop                 # stop the running turn, then send nothing more
keepgoing retarget primary     # move back from a fallback by hand
```

Every completed `tick` prints one JSON line describing what it saw and did, so a scheduler or IDE can read it. A tick skipped because the lock was held, or stopped by a config error, prints only to stderr.

## Documentation

- [docs/diagnostics.md](docs/diagnostics.md): read-only setup checks, JSON reports, and unavailable checks.
- [docs/setup.md](docs/setup.md): guided setup, scripted flags, existing accounts, and first-run limits.
- [docs/architecture.md](docs/architecture.md): packages, boundaries, and the rules that do not bend.
- [docs/configuration.md](docs/configuration.md): every field in `keepgoing.yaml`.
- [docs/fallbacks.md](docs/fallbacks.md): candidates, probes, redundancy, and handoff.
- [docs/operations.md](docs/operations.md): schedulers, the daemon, webhooks, `ci-wait`, `net-retry`, and exit codes.
- [docs/testing.md](docs/testing.md): running the suite, including on machines without Go, and per-platform results.
- [docs/agent-chats.md](docs/agent-chats.md): supervising a chat thread that a person also talks to.
- [integrations/bb-plugin-keepgoing](integrations/bb-plugin-keepgoing/README.md): a thin BB plugin that shows keepgoing's state beside each supervised thread.
- [docs/integrations.md](docs/integrations.md): every tool and service keepgoing talks to, and how far each is verified.
- [docs/roadmap.md](docs/roadmap.md): what comes next.
- [examples/](examples/): configurations and scheduler setups to copy.
- [schema/](schema/): JSON Schemas for editor completion and validation.

## Limitations

- The BB adapter needs the `bb` CLI. Error classification relies on BB's structured provider events. When those are missing, the error is `unknown`.
- Codex reports a usage limit only as prose in `codex exec --json`. When a Codex probe or turn fails without a recognized code, keepgoing asks the Codex app-server for its structured rate-limit snapshot (`account/rateLimits/read`) instead. This needs a Codex CLI with `app-server`. Until it is, a Codex error without a recognised code classifies as `unknown`. That means it re-prompts and then waits for a person instead of failing over. When Codex is the primary, running it through BB is more reliable, because BB reports rate-limit windows.
- On Windows, headless Claude Code and Codex turns run through a detached copy of the keepgoing binary instead of a shell wrapper. This is tested in a Windows 11 Arm VM; x64 Windows has not been checked.
- Switching back to the primary after a handoff is manual by default. Turn on `switch_back` to return automatically once the primary's limit resets and it passes a probe.
- A probe for a BB candidate runs the local `claude` or `codex` CLI with this machine's login, not through BB. Set `probe.env`, for example `CLAUDE_CONFIG_DIR` or `CODEX_HOME`, when a candidate uses a different account. This selects the probe's account only; keepgoing cannot switch a BB thread's login.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Use fictional data everywhere.

## License

[Apache-2.0](LICENSE).
