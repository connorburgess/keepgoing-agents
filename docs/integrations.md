# Integrations

This lists everything keepgoing talks to and how far each has been checked. Each item has one status:

- **Live**: exercised against the real tool or service.
- **Fakes**: covered by tests against a stand-in that follows the real tool's documented or observed output.
- **Untested**: written but not run.

## Agent providers

| Integration | Used for | Status | Next check |
| --- | --- | --- | --- |
| BB threads (`bb` CLI) | primary loops and fallbacks | **Live** for reading status, log events, and the interaction list, with dry-run ticks against a real thread. Interaction lifecycle shapes come from real `system/interaction/lifecycle` events. **Fakes** for tell, stop, and model update. Event shapes come from real `provider/error`, `provider/rateLimits/updated`, and `system/error` events. | Send, stop, and a model switch against a scratch thread. Capture a real blocked rate-limit window. |
| Claude Code headless (`claude -p`) | headless loops and probes | **Live** for the probe: one real request returned a healthy result. **Fakes** for multi-turn runs, resume, and error results. | One real two-turn run in a scratch directory. Capture a real usage-limit result. |
| Codex headless (`codex exec --json`) | headless loops and probes | **Live** for the probe: a real `codex exec --json --ephemeral` run completed. It used about 21,000 input tokens, most of them cached, because Codex sends its own system prompt. Its event stream included an `error` item from a local hook warning before the turn, which correctly does not fail the probe. **Fakes** for multi-turn runs and resume. | Capture a real usage-limit event and map its structured fields. |
| Generic process adapter | any other agent | **Fakes**, including batch files on Windows | none needed beyond fakes |

Other coding agents can be supervised today through the process adapter, given a status command and a resume command. Candidates for first-class adapters, once their headless status output has been checked:

- Cursor CLI
- Gemini CLI
- OpenCode
- Aider

## Agent chats

| Integration | Status | Next check |
| --- | --- | --- |
| Supervising a live BB chat thread | **Live** dry-run ticks against a real chat. **Fakes** for waking, pending interactions, `await-user` reports, and resume. | Supervise a real working chat with `keepgoing daemon` and a webhook. See [agent-chats.md](agent-chats.md). |

## Schedulers and hosts

| Integration | Status | Next check |
| --- | --- | --- |
| launchd (`examples/schedulers/launchd.plist`) | **Untested** example | load it on a test account and confirm one tick every interval |
| cron (`examples/schedulers/crontab.txt`) | **Untested** example | run it on a Linux VM |
| systemd user unit (`examples/schedulers/keepgoing@.service`) | **Untested** example | enable it on the Ubuntu VM and confirm `systemctl stop` ends the daemon at once |
| BB automation (`examples/schedulers/bb-automation.sh`) | **Untested** example | run it from a scratch BB automation against a scratch thread |
| GitHub Actions (`examples/schedulers/github-actions.yml`) | **Untested** example | run it in a scratch repository with fake candidates |
| `keepgoing daemon` | **Fakes**, through an end-to-end test of stop handling, the single instance rule, and exit | a long-running soak on a VM |

## Reporting

| Integration | Status | Next check |
| --- | --- | --- |
| Standard Webhooks signing | **Live** against the specification's published test vector. **Fakes** for delivery to a local receiver. | none |
| Text format (`source`, `sent_at`, `text`) | **Fakes** | deliver one `keepgoing report test` to a real receiver that expects this shape |
| Bearer scheme | **Untested** | add a delivery test |

## Developer tooling

| Integration | Status | Next check |
| --- | --- | --- |
| `gh` for `ci-wait` | **Fakes** | run it against a real pull request in this repository |
| `net-retry` connectivity probe | **Fakes** | run it with the network cut on a VM |
| GitHub Actions CI (`.github/workflows/checks.yml`) | runs on every push and pull request | none |
| Release workflow | **Untested**. The build script was run locally. | push a pre-release tag |

## Operating systems

See [testing.md](testing.md) for the per-platform results. The suite passes on macOS, on Ubuntu 26.04 ARM64, and on Windows 11 Arm. On Windows, the headless Claude Code and Codex runners and the end-to-end tests are POSIX only.

## Not planned

keepgoing is IDE-independent. An IDE or agent workspace integrates by calling the CLI and reading its JSON. A thin BB plugin that renders `keepgoing status --json` is on the roadmap. Editor extensions are not planned.
