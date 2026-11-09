# Integrations

This lists everything keepgoing talks to and how far each has been checked. Each item has one status:

- **Live**: exercised against the real tool or service.
- **Fakes**: covered by tests against a stand-in that follows the real tool's documented or observed output.
- **Untested**: written but not run.

## Agent providers

| Integration | Used for | Status | Next check |
| --- | --- | --- | --- |
| BB threads (`bb` CLI) | primary loops and fallbacks | **Live** for reading status, log events, and the interaction list, with dry-run ticks against a real chat of about 4,100 events: the first tick paged through it in about 2 seconds, and later ticks read only new events. **Live** for waking a scratch thread, switching its model through retarget and back, and stopping a running turn. Interaction lifecycle shapes come from real `system/interaction/lifecycle` events. Event shapes come from real `provider/error`, `provider/rateLimits/updated`, and `system/error` events. | Capture a real blocked rate-limit window. |
| Claude Code headless (`claude -p`) | headless loops and probes | **Live** for the probe: one real request returned a healthy result. **Fakes** for multi-turn runs, resume, and error results. | One real two-turn run in a scratch directory. Capture a real usage-limit result. |
| Codex headless (`codex exec --json`) | headless loops and probes | **Live** for the probe: a real `codex exec --json --ephemeral` run completed. It used about 21,000 input tokens, most of them cached, because Codex sends its own system prompt. Its event stream included an `error` item from a local hook warning before the turn, which correctly does not fail the probe. **Fakes** for multi-turn runs and resume. | Capture a real usage-limit event and map its structured fields. A failed probe now saves its raw output to `probe-failures/`, so the next real Codex limit is captured automatically. |
| Generic process adapter | any other agent | **Fakes**, including batch files on Windows | none needed beyond fakes |

Other coding agents can be supervised today through the process adapter, given a status command and a resume command. Candidates for first-class adapters, once their headless status output has been checked:

- Cursor CLI
- Gemini CLI
- OpenCode
- Aider

## Agent chats

| Integration | Status | Next check |
| --- | --- | --- |
| Supervising a live BB chat thread | **Live**: keepgoing supervises a real working chat through a BB automation. It woke the chat when it went idle, the agent ran `keepgoing await-user` when blocked, and the owner got the report. **Fakes** for pending interactions. | Catch a real pending interaction. |

## Schedulers and hosts

| Integration | Status | Next check |
| --- | --- | --- |
| launchd (`examples/schedulers/launchd.plist`) | **Untested** example | load it on a test account and confirm one tick every interval |
| cron (`examples/schedulers/crontab.txt`) | **Untested** example | run it on a Linux VM |
| systemd user unit (`examples/schedulers/keepgoing@.service`) | **Untested** example | enable it on the Ubuntu VM and confirm `systemctl stop` ends the daemon at once |
| BB automation (`examples/schedulers/bb-automation.sh`) | **Live**: a 10-minute BB script automation runs `keepgoing tick` for a real chat. It woke the chat when it went idle and stayed quiet while the chat was busy or awaiting its owner. | none |
| GitHub Actions (`examples/schedulers/github-actions.yml`) | **Untested** example | run it in a scratch repository with fake candidates |
| `keepgoing daemon` | **Live** against a scratch BB chat: it woke the idle chat every interval, held off while the loop was awaiting its owner, ran a pass as soon as the control file changed, and exited cleanly on SIGTERM. | a long-running soak on a VM |

## Reporting

| Integration | Status | Next check |
| --- | --- | --- |
| Standard Webhooks signing | **Live** against the specification's published test vector. **Fakes** for delivery to a local receiver. | none |
| Text format (`source`, `sent_at`, `text`) | **Live**: test, wake, and await-user reports were delivered to a real receiver | none |
| Bearer scheme | **Live** with a real receiver. No unit test yet. | add a delivery test |

## Developer tooling

| Integration | Status | Next check |
| --- | --- | --- |
| `gh` for `ci-wait` | **Fakes** | run it against a real pull request in this repository |
| `net-retry` connectivity probe | **Fakes** | run it with the network cut on a VM |
| GitHub Actions CI (`.github/workflows/checks.yml`) | runs on every push and pull request | none |
| Release workflow | **Live**: tagging v0.1.0 published binaries for six platforms with checksums. A downloaded binary's checksum and version were checked. | none |

## Operating systems

See [testing.md](testing.md) for the per-platform results. The suite passes on macOS, on Ubuntu 26.04 ARM64, and on Windows 11 Arm. On Windows, the headless Claude Code and Codex runners and the end-to-end tests are POSIX only.

## Agent workspaces

| Integration | Status | Next check |
| --- | --- | --- |
| BB plugin (`integrations/bb-plugin-keepgoing`) | **Live**: installed in BB against a real loop. `bb keepgoing status`, `pause`, and `resume` ran through the plugin host. In BB's default context banner, the segment was checked in a browser against a real supervised thread: it showed `keepgoing: running`, expanded to the loop details, and showed a Pause button. The context card row has not been seen yet, because the card is an experiment that is off by default. | Turn on the context card experiment and check the row. |

## Not planned

keepgoing is IDE-independent. An IDE or agent workspace integrates by calling the CLI and reading its JSON, as the BB plugin does. Editor extensions are not planned.
