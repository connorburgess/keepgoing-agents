![Keepgoing Agents - supervision for long-running coding agents](docs/assets/keepgoing-banner.jpg)

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
- **Waits out known usage limits.** When the provider reports a reset time within `wait_if_reset_within`, it waits instead of handing off. With failover disabled, a known future reset also waits without spending recovery retries, while ordinary checks keep observing structured provider status (this correction is unreleased). Explicit holds and loop-wide bounds still win.
- **Reports a completion declaration.** `complete` can send one subscribed `complete` event per completion episode. It explicitly records a declaration, not independently verified success, and omits the private note. Available in v0.1.3 or newer; [delivery semantics](docs/operations.md#completing-a-task).
- **Knows when a person is needed.** A pending permission prompt or question, or a loop marked with `keepgoing await-user`, means nothing is sent. The person gets an `await_user` report instead of the loop sitting idle. See [docs/agent-chats.md](docs/agent-chats.md).
- **Can hold after no Git progress.** Optional `loop.limits.no_progress_timeout` (or `init --no-progress-timeout`) tracks elapsed time across turns, restarts and handoffs. This is a Git activity signal, not a judgment of useful work. Available in v0.1.3 or newer; [semantics and limitations](docs/operations.md#no-progress-timeout).
- **Can cap automatic send attempts.** Optional `loop.limits.max_sends` (or `init --max-sends`) shares a persistent allowance across wakes, recovery and handoffs. Failed attempts count; resume does not reset it. Available in v0.1.3 or newer; [semantics and limitations](docs/operations.md#total-send-allowance).
- **Can hold supervision at a deadline.** Optional `loop.limits.deadline` (or `init --deadline`) sets an absolute cutoff. The next check persists an awaiting-user hold before automatic provider work. It does not cancel running turns or enforce a spend/turn budget. Available in v0.1.3 or newer; see [deadline behavior](docs/operations.md#loop-deadline).
- **Honors stop and pause.** The daemon watches the control file and requests a check within seconds. Provider work, including stop execution, requires a [verified loaded configuration](docs/operations.md#loaded-configuration-checks); stale or unreadable sources withhold it until reload/repair.
- **Reports through signed webhooks.** Payloads are signed with the [Standard Webhooks](https://www.standardwebhooks.com/) scheme. Bearer tokens are supported too.
- **Includes two helpers.** `ci-wait` waits for every workflow on an exact commit and follows a replacement run after a cancellation. `net-retry` reruns a command only while the network is down.

## Looping and setup

keepgoing already runs the loop: it sends the configured task prompt when the agent becomes idle and applies the retry and fallback rules when work cannot continue. A separate loop skill is not required. Enabling from a BB thread records a persistent [loop owner](docs/operations.md#one-owner-per-loop); another thread needs an explicit takeover. Task instructions tell the agent what to do; the supervisor runs outside that agent session and decides when another turn may start. It needs a running host and scheduler or daemon to do this.

`keepgoing init` guides task setup, discovers installed supported CLIs, and writes a disabled configuration outside the checkout. It supports existing headless account directories and ordered fallbacks without requiring YAML edits. Integrations can use `init --non-interactive --json` for structured preview/create results and `--task-file -` for private task entry through stdin. Task input is copied once into the generated `prompt.md`; later edits to the original task file are not tracked. Edit the saved prompt to revise future task prompts. See the [setup guide](docs/setup.md). These setup commands require v0.1.2 or newer; v0.1.1 does not include them.

`keepgoing doctor` checks setup, BB target identity, CLI versions, headless login status and saved resume IDs, private-file access, and recent tick history without model calls. It also checks runtime path access on macOS, Linux, and Windows without writing data and inspects ACL metadata on those platforms. Windows null/absent DACLs are errors; populated or empty DACLs need review. Full ACL privacy evaluation remains unavailable; Windows runtime checks open and close existing handles without creating files or taking locks. For an explicitly selected BB script automation, it can also report registration, next-run time, and last-run metadata without changing the timer. [Its report](docs/diagnostics.md) distinguishes credential presence from usable quota and marks unsupported checks explicitly.

The [v0.1.3 reliability release](docs/release-verification.md#v013-reliability-release-evidence) includes [BB retry-aware stall handling](docs/operations.md#bb-retry-and-stall-handling): active provider retries suppress stall nudges, and verified recovery restores ordinary stall handling without clearing holds or hard bounds.

A [bounded incident history](docs/diagnostics.md#bounded-tick-incident-history) retains recognized release/development/candidate build labels and records normalized observations, structured error metadata, decision inputs and saved control flags across adapters. [Notification delivery history](docs/diagnostics.md#notification-delivery-evidence) keeps failed or unconfirmed alerts visible. [`keepgoing timeline`](docs/diagnostics.md#offline-incident-timeline) combines these records into a read-only text/JSON view with current flags, historical checks and holds, failed notifications and explicit evidence gaps.

These features have passed [offline recovery checks](docs/release-verification.md#check-recovery-with-local-stand-ins) using stand-in providers, rejected local alerts and actual held daemon restarts. The post-v0.1.3 smoke script also checks SIGKILL and restart after recorded held checks; crashes during sends or writes remain outside that check. Source-event timing and live recovery verification remain unfinished. Publication does not upgrade an existing supervisor, prove notification receipt, or establish readiness to migrate other threads. A separate [existing-loop deployment check](docs/release-verification.md#existing-loop-deployment-check) records one verified macOS executable replacement, diagnostic preview and successful ordinary scheduled check.

Preview manual probes with [`keepgoing fallbacks check --dry-run`](docs/fallbacks.md#preview-manual-probes). It shows the selected models, timeouts, and scheduled cap without executing probes or changing state. Dollar cost is unknown, and manual probes are outside the scheduled cap; remove `--dry-run` only when you intend to run them.

On `main` after v0.1.3, [`keepgoing goal create|revise|show`](docs/goals.md) stores private, revisioned goal metadata. Ordinary status shows its ID, revision and recorded state; `goal show --private` reveals its title, references and history. Goal edits preserve loop controls and never start work. Opt-in [`loop.prompt_goal`](docs/goals.md#current-goal-context-in-wake-messages) includes the latest private goal revision in automatic prompts and handoffs; stable operating instructions can then stay separate from evolving task metadata. Completion verification and review-stage bindings remain planned.

On `main` after v0.1.3, [`keepgoing transfer preview`](docs/transfer.md) compares a proposed BB destination with the current primary and displays controls, ownership and goal identity without changing them. `transfer apply` performs an explicit same-environment move with a persisted recovery fence and an awaiting-user hold; `transfer recover` resumes an interrupted move. The BB plugin adds [Move supervision here](integrations/bb-plugin-keepgoing/README.md#move-supervision-here) for plugin-created loops, with an explicit preview, held move and recovery path. These changes are unreleased and fixture-tested; model/account verification and live transfer remain unfinished.

The unreleased [local loop registry](docs/local-clients.md) lets optional clients discover explicitly registered configurations through `registry add|list|remove`. Registration never starts supervision or changes loop controls. The read-only [`client status`](docs/local-clients.md#registered-status-schema-1) interface adds binding checks, holds, goal identity, optional private goal display and explicit daemon/history evidence for a registered ID. Explicit [`client pause|stop`](docs/local-clients.md#registered-pause-and-stop) commands add restrictive holds without waiting for ticks or querying providers. [`client prepare-resume` and `client resume`](docs/local-clients.md#registered-resume) bind an explicit resume to a reviewed snapshot through a short-lived, single-use ticket. These interfaces are unreleased and fixture-tested; an optional read-only [native menu preview](integrations/macos/README.md) is implemented; interactive controls and installation remain planned.

The unreleased BB plugin [status view](integrations/bb-plugin-keepgoing/README.md#what-it-shows) also shows declared completion, the actual target thread, daemon/check evidence and recorded action outcomes. Failed refreshes remain visible; external scheduler health stays unverified. This view is fixture-tested, with live deployment outstanding.

`keepgoing status` now distinguishes current holds from [recorded decisions and outcomes](docs/operations.md#understanding-recorded-status), including failures, dry runs, stale history, and build-label differences. It also queries a running daemon for its current phase, next normal check time, and build comparison. `keepgoing start` now validates local setup, enables supervision, and runs in the foreground or background. [`service install|status|remove`](docs/services.md) manages macOS LaunchAgents and Linux systemd user units while preserving loop controls. Service status now explains registration/unit failures, unverified daemons, build differences, and overdue check metadata with [read-only health findings](docs/services.md#health-findings-in-service-status). Recorded Linux services also report structured manager failure reasons and main-process exit codes/signals; unknown details remain unavailable. Recorded services also inspect executable files and compare their on-disk contents with the invoking CLI without running them. Linux service status can also bind an authenticated daemon response to stable systemd process/invocation metadata; this is fixture-tested, with live service verification still outstanding. macOS status explicitly labels service identity and startup/exit checks as [unsupported](docs/services.md#macos-diagnostic-gaps), even when a loop daemon responds. The BB plugin now offers a [Supervise this thread setup flow](integrations/bb-plugin-keepgoing/README.md#supervise-this-thread), with separate preview, disabled creation, diagnostics, and background start; this is fixture-tested, with a live walkthrough still outstanding. Windows service management, expanded scheduler and live headless-session diagnostics, expanded BB transfer scope, and Cursor/Antigravity adapters are [planned next](docs/roadmap.md#next-first-supervised-loop-in-five-minutes). Mark finished work with `keepgoing complete --note "Task verified"`. This persists a completion hold across restarts; only an explicit `resume` clears it. Completion is declared by the agent or operator, never inferred from transcript text. Cursor and Antigravity are not built-in adapters today; see the [integration plan](docs/integrations.md#planned-integrations).

Prepared [v0.1.4 candidate notes](docs/releases/v0.1.4.md) cover the unreleased rate-limit fix and local goal/transfer/client features. Candidate preparation does not change the latest published version or install additional integrations.

## Install

The latest release is [v0.1.3](https://github.com/connorburgess/keepgoing-agents/releases/tag/v0.1.3), which includes the limits and incident diagnostics described above. See [release and package verification](docs/release-verification.md#v013-reliability-release-evidence) for tested paths and remaining live-verification gaps.

This quick start requires **v0.1.2 or newer**, including `init`, `doctor`, `complete`, `start`, and `service`. Check `keepgoing version` after installation. For v0.1.1, use its [versioned README and examples](https://github.com/connorburgess/keepgoing-agents/tree/v0.1.1), not the examples from `main`. A release includes implemented features; it does not certify the outstanding [live verification](docs/release-verification.md) checks.

With Homebrew on macOS or Linux:

```sh
brew install connorburgess/tap/keepgoing
# If already installed:
# brew update && brew upgrade keepgoing
```

With Go 1.24 or newer:

```sh
go install github.com/connorburgess/keepgoing-agents/cmd/keepgoing@latest
```

`go install` builds currently report `dev` from `keepgoing version` because they do not receive the release build label. Use `go version -m /path/to/keepgoing` on the installed executable to check its module version (v0.1.2 or newer), or choose a prebuilt release for an embedded version label.

Prebuilt binaries for macOS, Linux, and Windows, with SHA-256 checksums, are on the [releases page](https://github.com/connorburgess/keepgoing-agents/releases).

### Development build

With Git and Go 1.24 or newer, build from a checkout and keep the binary separate from an installed release. On macOS/Linux:

```sh
git clone https://github.com/connorburgess/keepgoing-agents.git
cd keepgoing-agents
revision=$(git rev-parse HEAD)
mkdir -p "$HOME/.local/bin"
go build -trimpath -ldflags "-X main.version=dev-$revision" \
  -o "$HOME/.local/bin/keepgoing-dev" ./cmd/keepgoing
"$HOME/.local/bin/keepgoing-dev" version
```

Use this absolute binary path wherever the following instructions say `keepgoing`; setup prints commands using the executable that ran it. Record the revision with any results. This is a source build labeled with its revision; it may include changes newer than the latest release. Building it does not upgrade a running supervisor or change its timer. See [release verification](docs/release-verification.md) for a provider-free binary smoke check and the separate live walkthrough.

### Optional macOS menu preview

The [native menu-bar preview](integrations/macos/README.md) shows explicitly registered local loops through the shared CLI. It reads on menu open or manual refresh, hides private goal text by default, and distinguishes controls, historical activity and daemon evidence. It is an unreleased, read-only developer build; controls, setup installation, signing and live UI verification remain planned. Opening it starts no supervisor or wake timer.

## Quick start

With v0.1.2 or newer, run this from an existing Git project with at least one commit:

```sh
keepgoing init
```

Choose a task and agent, review the resolved plan, then run the `start --foreground` command printed with your config path. Choose `--background` to detach the daemon and keep it running after the terminal closes. Existing pause, stop, awaiting-user, and completion holds remain; `resume` clears them explicitly. The default is a single agent with no fallbacks. Setup does not run agents or probes, change logins, or install a scheduler. See [guided and scripted setup](docs/setup.md) for accounts, operating limits, and noninteractive flags.

For advanced configuration on this build, copy the appropriate [example](examples/) to a private directory outside the checkout and edit its paths. Run commands with `-c /path/to/keepgoing.yaml`. Nothing is sent until you enable the loop and run ticks or a daemon; a missing control file means disabled. Release users should select examples from their matching tag, as described above.

Control the loop at any time (add `-c` with your generated config path to each command):

```sh
keepgoing complete --note "Task verified"  # finish the task; hold future wakes
keepgoing pause --note "reviewing by hand"
keepgoing resume
keepgoing stop                 # stop the running turn, then send nothing more
keepgoing retarget primary     # move back from a fallback by hand
```

Every completed `tick` prints one JSON line describing what it saw and did, so a scheduler or IDE can read it. A tick skipped because the lock was held or loaded config sources changed returns exit 75 and prints only to stderr. Other configuration errors also print only to stderr.

## Documentation

- [docs/diagnostics.md](docs/diagnostics.md): read-only setup checks, JSON reports, and unavailable checks.
- [docs/setup.md](docs/setup.md): guided setup, scripted flags, existing accounts, and first-run limits.
- [docs/architecture.md](docs/architecture.md): packages, boundaries, and the rules that do not bend.
- [docs/configuration.md](docs/configuration.md): every field in `keepgoing.yaml`.
- [docs/fallbacks.md](docs/fallbacks.md): candidates, probes, redundancy, and handoff.
- [docs/operations.md](docs/operations.md): schedulers, the daemon, webhooks, `ci-wait`, `net-retry`, and exit codes.
- [docs/services.md](docs/services.md): macOS LaunchAgent and Linux systemd user-service install/status/remove, recovery, and verification limits.
- [docs/windows-services.md](docs/windows-services.md): Task Scheduler investigation and delivery gates; Windows service management is not implemented.
- [docs/testing.md](docs/testing.md): running the suite, including on machines without Go, and per-platform results.
- [docs/release-verification.md](docs/release-verification.md): artifact smoke checks, release parity, and live walkthrough evidence.
- [docs/agent-chats.md](docs/agent-chats.md): supervising a chat thread that a person also talks to.
- [integrations/bb-plugin-keepgoing](integrations/bb-plugin-keepgoing/README.md): a thin BB plugin that shows keepgoing's state beside each supervised thread.
- [docs/integrations.md](docs/integrations.md): every tool and service keepgoing talks to, and how far each is verified.
- [docs/roadmap.md](docs/roadmap.md): what comes next.
- [examples/](examples/): configurations and scheduler setups to copy.
- [schema/](schema/): JSON Schemas for editor completion and validation.

## Limitations

- The BB adapter needs the `bb` CLI, including parent-filtered thread listing with hidden children. Verified active direct children suppress parent wakes, stall nudges, error recovery, and target moves. Unverified child discovery also waits; see [child awareness](docs/operations.md#bb-child-awareness). The built-in daemon checks structured child outcome notices before its normal interval, consuming each once and rechecking that the parent can wake. This is covered by offline fixtures; external timers retain their own cadence. Verified pending child interactions put the loop into `await_user` and report the child IDs without prompting the parent or answering the child. Error classification relies on BB's structured provider events. When those are missing, the error is `unknown`.
- Codex reports a usage limit only as prose in `codex exec --json`. When a Codex probe or turn fails without a recognized code, keepgoing asks the Codex app-server for its structured rate-limit snapshot (`account/rateLimits/read`) instead. This needs a Codex CLI with `app-server`. Until it is, a Codex error without a recognised code classifies as `unknown`. That means it re-prompts and then waits for a person instead of failing over. When Codex is the primary, running it through BB is more reliable, because BB reports rate-limit windows.
- On Windows, headless Claude Code and Codex turns run through a detached copy of the keepgoing binary instead of a shell wrapper. This is tested in a Windows 11 Arm VM; x64 Windows has not been checked.
- Switching back to the primary after a handoff is manual by default. Turn on `switch_back` to return automatically once the primary's limit resets and it passes a probe.
- A probe for a BB candidate runs the local `claude` or `codex` CLI with this machine's login, not through BB. Set `probe.env`, for example `CLAUDE_CONFIG_DIR` or `CODEX_HOME`, when a candidate uses a different account. This selects the probe's account only; keepgoing cannot switch a BB thread's login.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Use fictional data everywhere.

## License

[Apache-2.0](LICENSE). The project retains this license. The [roadmap delivery priorities](docs/roadmap.md#delivery-priorities-after-roadmap-reassessment) put core verification and release consistency ahead of further goal acceptance controls, review workflows, and optional menu-bar visibility.
