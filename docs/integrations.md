# Integrations

This lists everything keepgoing talks to and how far each has been checked. Each item has one status:

- **Live**: exercised against the real tool or service.
- **Fakes**: covered by tests against a stand-in that follows the real tool's documented or observed output.
- **Untested**: written but not run.
- **Research only**: investigated and planned; no implementation or compatibility verification.

## Agent providers

| Integration | Used for | Status | Next check |
| --- | --- | --- | --- |
| BB threads (`bb` CLI) | primary loops and fallbacks | **Live** for reading status, log events, and the interaction list, with dry-run ticks against a real chat of about 4,100 events: the first tick paged through it in about 2 seconds, and later ticks read only new events. **Live** for waking a scratch thread, switching its model through retarget and back, and stopping a running turn. Interaction lifecycle shapes come from real `system/interaction/lifecycle` events. Event shapes come from real `provider/error`, `provider/rateLimits/updated`, and `system/error` events. | Capture a real blocked rate-limit window. |
| Claude Code headless (`claude -p`) | headless loops and probes | **Live** for the probe: one real request returned a healthy result. **Fakes** for multi-turn runs, resume, and error results. | One real two-turn run in a scratch directory. Capture a real usage-limit result. |
| Codex headless (`codex exec --json`) | headless loops and probes | **Live** for usage limits: a real limit event was captured. It carries the limit only as prose. keepgoing classifies it instead from the Codex app-server's structured `account/rateLimits/read` (`ordinaryUsageAllowed`, `rateLimitReachedType`, window `resetsAt`). A live probe on a limited account reported `rate_limit` with the correct reset time. **Live** for the probe: a real `codex exec --json --ephemeral` run completed. It used about 21,000 input tokens, most of them cached, because Codex sends its own system prompt. Its event stream included an `error` item from a local hook warning before the turn, which correctly does not fail the probe. **Fakes** for multi-turn runs and resume. | One real two-turn run in a scratch directory. |
| Generic process adapter | any other agent | **Fakes**, including batch files on Windows | none needed beyond fakes |

Other coding agents can be supervised today through the process adapter, given a status command and a resume command. Cursor and Antigravity are the next planned first-class integrations; neither has an adapter yet. See [Planned integrations](#planned-integrations). Gemini CLI, OpenCode, and Aider remain discovery backlog items, with no compatibility claim or delivery commitment.

## Guided setup

`keepgoing init` is implemented on `main` for BB, Claude Code, and Codex. **Fakes** cover CLI discovery, ordered account configuration, task instructions, disabled state, and completion across repeated checks using the generated config. Temporary real Git repositories exercise progress validation. Setup checks executable availability and directory existence only; it does not invoke providers or verify login, subscription identity, BB target identity, or live task completion. See the [setup guide](setup.md).

A macOS Apple silicon native-binary smoke check also passed against an unpacked local release-format archive labeled `v0.1.2-smoke` (not published). It verified generated BB/Claude/Codex loops, offline diagnostics, persisted control holds and ownership using invocation traps in place of providers. All six cross-built archive checksums were checked locally; only the native macOS archive was executed. This is **Fakes** evidence, not live provider verification or a released v0.1.2. The [repeatable script and live walkthrough](release-verification.md) keep those checks separate.

## Diagnostics

Optional absolute loop deadlines are implemented on `main`, after v0.1.2. **Fakes** verify setup preview/create, status, exact cutoff/offset comparisons, automatic-provider suppression, stop/control precedence, persisted holds, repeated checks, clock rollback, dry runs and explicit resume/config changes. A cutoff does not cancel a running turn or complete the broader work-budget/pilot-readiness gates. No live provider or installed-service deadline was exercised. See [deadline behavior](operations.md#loop-deadline).

`keepgoing doctor` is implemented on `main`. **Fakes** cover version queries, BB target identity, privacy, account-environment propagation, recent-check classification, and read-only behavior. Unix subprocess fixtures cover time/output limits. Headless login-status checks use Claude JSON and Codex status exit codes; fixtures cover per-target account environments and working directories, unavailable/error cases, disabled fallbacks, and output redaction. A live macOS smoke check of the built doctor against both installed status commands returned `ok` in the existing inherited account context and created no keepgoing runtime state. Separate account selection and failure cases remain fixture-tested. These signals establish local credential presence only. Saved headless resume IDs are available in status and doctor, covered by offline fixtures for target attribution, missing/unrecognized records, bounded reads, privacy, and unchanged holds; they do not verify live identity or resumability. The built-in daemon now answers private local status queries for its phase, next normal check, dry-run mode, and build comparison. Local fixture tests cover lifecycle/control handling, fresh challenges, stale/wrong/copied endpoints, and redaction. A built-binary macOS smoke check verified responsive status, live build comparison, offline skipping, clean SIGTERM cleanup, and unavailable status after a forced process exit, while preserving a completion hold and making no provider calls. Windows is cross-compiled; live endpoint behavior there remains unverified. Live headless session identity, external scheduler inspection, service installation, and Windows ACL checks remain unavailable. Status and doctor compare the last recorded tick's build label with the current CLI; offline fixtures cover these historical comparisons, control holds, failed checks, dry runs, and handoff observation attribution. These historical comparisons do not verify a live scheduler; the built-in daemon query is separate. See [diagnostics](diagnostics.md).

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
| Managed macOS LaunchAgent (`service` commands) | **Fakes** for lifecycle/recovery; native plist validation and read-only missing-service query | live install/removal, login/reboot, and soak |
| Managed Linux systemd user unit (`service` commands) | **Fakes** for lifecycle/recovery; native unit-file validation without registration | live user-manager install/removal, login/reboot, and soak |
| Windows Task Scheduler | **Research only**: [current-user backend proposal and delivery gates](windows-services.md); no scheduler adapter or fixtures | supervisor-only shutdown/identity fixture, then scheduler implementation and native lifecycle verification |
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
| Release workflow | **Live**: v0.1.2 published six archives after Go tests, checksum checks and an unpacked Linux amd64 provider-free smoke check. All published archives were downloaded and their checksums matched; the downloaded macOS arm64 executable passed the smoke check locally. See [release evidence](release-verification.md#v012-maintenance-release-evidence). | native release installation on remaining platforms; live provider walkthroughs remain separate |
| Homebrew formula | **Live** installation on a fresh macOS 15 arm64 CI runner: v0.1.2 download verification, version and guided-setup help checks. Matching URLs/checksums are published for macOS/Linux. See [release evidence](release-verification.md#v012-maintenance-release-evidence). | native Linux and Intel macOS installs; full install-to-completion journey |

## Operating systems

See [testing.md](testing.md) for the per-platform results. The suite passes on macOS, on Ubuntu 26.04 ARM64, and on Windows 11 Arm, including the headless runners on Windows. The end-to-end CLI tests are POSIX only.

## Agent workspaces

| Integration | Status | Next check |
| --- | --- | --- |
| BB plugin (`integrations/bb-plugin-keepgoing`) | **Live**: installed in BB against a real loop. `bb keepgoing status`, `pause`, and `resume` ran through the plugin host. In BB's default context banner, the segment was checked in a browser against a real supervised thread: it showed `keepgoing: running`, expanded to the loop details, and showed a Pause button. The context card row has not been seen yet, because the card is an experiment that is off by default. | Turn on the context card experiment and check the row. |

## Planned integrations

This section describes future work, not additional supported adapters. keepgoing owns the repeated task prompt, supervision, recovery, and control state. Users should not need a separate loop skill. Task completion is an explicit control signal, not a phrase extracted from a transcript; see the [usability milestone](roadmap.md#next-first-supervised-loop-in-five-minutes).

There are two distinct integration targets: running an agent CLI against a project opened in any editor, and controlling an existing conversation inside an IDE. The first does not imply the second. A custom process wrapper is possible today only when it supplies the required structured status and control commands; the example scripts are placeholders, not working Cursor or Antigravity integrations.

### Cursor

The official [CLI overview](https://cursor.com/docs/cli/overview) documents print mode and resuming a specific session, and the [output-format reference](https://docs.cursor.com/en/cli/reference/output-format) documents machine-readable results. These are discovery starting points, not evidence that keepgoing already handles Cursor.

1. Verify the installed CLI/version, structured start/completion/error output, exact session resume, cancellation, and permission waits in a scratch project. Determine how account identity and limits are exposed without transcript matching.
2. Implement detached turns with per-target session/state storage, task prompts, explicit completion instructions, and compatible environment overrides where the provider supports account selection. Map only documented or observed structured errors; unknown remains unknown and never triggers failover.
3. Verify manual/scheduled/exact probes against the configured account and model. If a safe or sufficiently precise probe is unavailable, document the limitation and keep fallback eligibility disabled for that path.
4. Add setup discovery, diagnostics, examples, and supported-version/platform notes after the shared delivery gates below pass.
5. Investigate an official API, hook, or protocol for attaching to an existing Cursor IDE conversation. Do not assume a CLI session is interchangeable with an IDE chat or use UI scraping as its status source.

### Antigravity

The official [headless guide](https://antigravity.google/docs/cli/headless/) documents print mode and machine-readable output. The [CLI reference](https://antigravity.google/docs/cli/reference/) documents conversation-ID resume. Authentication, cancellation, permission state, and structured quota reporting still require verification for this integration.

1. Verify `agy` print-mode lifecycle events and conversation-ID resume, including failure after partial output, pending permission, cancellation, and missing authentication. Establish supported versions/platforms and whether separate account configurations are available.
2. Implement detached turns and per-target state, using stable conversation IDs rather than "most recent" resume. Feed the same task lifecycle and controls used by the other runners.
3. Establish probes and error mappings from structured evidence, including limits/reset times when available. Model-produced JSON about its own health is not a provider status signal. Keep unsupported error classes unknown.
4. Add setup discovery, diagnostics, examples, and live verification using the shared delivery gates.
5. Investigate supported IDE/session APIs or protocols for existing graphical conversations as a separate capability. Record whether attachment, status, send, and stop actually work before promising chat supervision.

### Shared delivery gates

- **Evidence:** record CLI version, platform, and supported capabilities. Commit only fictional fixtures shaped like real structured events, never private transcripts, credentials, or local state.
- **Lifecycle:** verify fresh turn, exact-session resume, completion, crash, restart, and stop. Reconcile state after a supervisor restart without duplicate sends. Permission waits must not become automatic approval or repeated prompts.
- **Account and fallback:** verify the configured account/model is used by both work and probes, quotas are grouped correctly, and handoff/switch-back only occurs after the required exact probe. Do not assume every provider supports multiple logins.
- **Control:** verify stop/pause while active, idle, limited, and recovering, plus awaiting-user and completed-task holds. No action may silently reactivate a held loop.
- **Failures:** test malformed output, nonzero exits, network errors, auth failures, limits, and unavailable status. Unknown never triggers failover; transcript prose is never classified.
- **Usability:** exercise setup, diagnostics, start, status, and removal with a fresh configuration. Clearly label any capability limited to a CLI session.
- **Proof:** fake-based regression tests plus a real two-turn/resume/stop smoke test in a scratch project before claiming live support. Handoff and limit support need their own evidence; unavailable live scenarios remain explicitly unverified.

### IDE surfaces and optional skills

Keep supervision in the standalone binary. Start with CLI adapters and the existing BB integration. Consider thin editor controls for setup, status, pause, stop, and attaching a supported session only after its control interface is verified. Maintain one control path instead of duplicating the decision engine in each editor extension.

A skill can help an agent invoke keepgoing or understand project instructions, but ordinary task looping must work without one. If an IDE already provides reliable external looping for the user's needs, keepgoing is optional; its added value is consistent supervision, explicit controls, confirmed account/provider fallback, and reporting across supported environments.
