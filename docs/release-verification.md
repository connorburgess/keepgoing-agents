# Release verification

Release preparation and live compatibility are separate evidence. A successful fixture run does not certify a provider login, paid turn, installed service, or a five-minute onboarding experience.

## v0.1.2 maintenance release evidence

[v0.1.2](https://github.com/connorburgess/keepgoing-agents/releases/tag/v0.1.2) tags commit `966620873af35bacef648d5dd57e31150e78f424`. The [release run](https://github.com/connorburgess/keepgoing-agents/actions/runs/42790570495) passed its Build and publish job on that commit: Go tests, six-platform archive build, all checksum comparisons, and the unpacked Linux amd64 smoke check. All six published assets were subsequently downloaded and matched the published checksums; the downloaded macOS arm64 archive passed the same provider-free smoke check locally. No real agent was invoked.

[Homebrew PR #1](https://github.com/connorburgess/homebrew-tap/pull/1) updated all four formula URLs/checksums and merged as `90ffac5ace1414f70689ed4fccec4af23dcceeda`. Its [Fresh macOS install job](https://github.com/connorburgess/homebrew-tap/actions/runs/42791240831) passed on head `7dad7d90d561d86aa7a402ec63e9f64bdf47a7ba` against base `dbb5dcb258ad66c3f37d9652ecb879c7f8d97fc6`. The macOS 15 arm64 runner installed from the checked-out formula, verified its download, ran the version and `init --help` formula tests, and reported `v0.1.2`. This did not exercise a provider, service or existing supervisor.

This verifies release/package parity on those paths. Native Homebrew installation on Linux/Intel macOS, native Windows release installation, live provider sessions, service lifecycle and the full five-minute onboarding acceptance remain outstanding. Building the Windows archives is not a Windows installation test. The BB plugin is distributed separately. This release does not implement loop-wide work budgets, portable goals, or menu-bar visibility.

## Check an actual binary without provider calls

On macOS/Linux with Git and Python 3, run this from a checkout containing the smoke script, passing an absolute path to a native binary built from the same revision:

```sh
python3 scripts/smoke-release.py /path/to/keepgoing
```

For a release archive, first verify its SHA-256 against the release's `checksums.txt`, unpack it into a scratch directory, then pass the unpacked executable. Record the source revision, version output, archive checksum, OS/architecture, command and result. Do not substitute an installed binary of an unknown revision.

The script creates and removes a private temporary Git project and three private loops: BB, Claude Code and Codex. It exercises preview without writes, disabled creation, offline doctor, persisted disabled/pause/awaiting-input/completion holds across separate CLI invocations, completion preservation through disable/enable, conflicting ownership refusal, explicit takeover, and explicit resume while disabled. Every provider executable is replaced with a trap; any invocation fails the check. No daemon, service, timer or real account is used. Separate CLI invocations check persisted control state, not a long-running daemon restart or actual provider session resume. This script requires v0.1.2 or newer; v0.1.1 is expected to fail it.

CI runs the smoke script against a native built binary on macOS and Linux. Windows native tests remain in the regular suite; this POSIX smoke script is not Windows evidence. See [testing](testing.md) for daemon, recovery, stop precedence and other fixture coverage.

## Check recovery with local stand-ins

On macOS/Linux with Python 3, pass a native binary from the same revision as this script:

```sh
python3 -u scripts/smoke-recovery.py /path/to/keepgoing
```

This companion to the setup smoke check requires the unreleased reliability changes; v0.1.2 is expected to fail it. It prints the executable SHA-256, version and platform. CI runs it on native macOS/Linux builds; future release publication also gates on the unpacked Linux archive passing. Record the revision alongside that output. A local source build is not evidence that an installed supervisor was upgraded.

Four private temporary loops use synthetic BB events, a local process adapter, and real local Claude/Codex stand-in processes. The check covers active BB retry suppression beyond the nudge allowance, classified connection failure, bounded recovery attempts, a persistent awaiting-user hold, and a loopback webhook rejecting the alert. It restarts actual daemon processes while held, checks that the offline timeline retains the failure and alert warning without side effects, repairs only the stand-in, and requires explicit resume. Headless fixtures explicitly increase their synthetic retry allowance before resume, then reuse their saved fictional session across successful turns; resume itself does not refund spent attempts. Completion is sent once despite rejection and remains held across two daemon restarts. Send allowance and owner identity must survive; a conflicting owner is rejected. The BB held case also spans two ordinary check intervals.

The script supplies a minimal environment and explicit stand-in commands, without inheriting credentials or provider configuration. It creates no service or scheduler and removes its temporary state after stopping its supervisors. Allow about a minute for local processes and real daemon intervals. Tick timestamps are synthetic; daemon intervals use wall time. This is orderly restart evidence, not a process-crash, real network outage, provider cancellation, installed-service lifecycle or live session compatibility check. Windows remains outside this script's coverage. Existing Go fixtures separately cover stop/pause precedence and failure cases.

## Live install-to-completion walkthrough

Run each row separately with an explicitly selected build, scratch project, existing login and authorized spend. A blocked provider or platform leaves that row unverified. Never deliberately exhaust quota, create accounts, change logins or repoint an existing loop to make a check pass.

1. Record artifact/tag/checksum, OS, provider/BB CLI versions, start time and whether this is a fresh installation. Use a new private loop directory and a Git scratch project with an initial commit. Keep real prompts, thread/session IDs and account paths in private evidence, outside the repository.
2. Run `init` with the selected binary. For BB select an existing authorized scratch thread; for headless select Claude Code or Codex with its existing account. Use a small fictional task with observable acceptance criteria. Preview, then create with no fallbacks. Confirm status says disabled and inspect `doctor`; resolve errors and record unavailable checks.
3. Start in the foreground using the printed command. Have the first turn perform only the first part of the task and leave the second part pending. Observe a second supervised turn. For headless runners confirm the saved session ID is reused, and independently inspect the scratch result; a saved ID alone does not prove resume.
4. Pause between turns, check status, and verify no new wake across an ordinary interval. Exercise `await-user` separately, verify its hold and report, then explicitly resume. Record any permission interaction and verify that it is surfaced to the person without an automatic answer.
5. Stop the supervisor process cleanly, then restart the same loop and verify its hold/session metadata persists. Separately request `stop` while an authorized scratch turn is active, and observe cancellation and no subsequent wake. Record platform/provider cancellation limits.
6. Finish the task, independently check its acceptance criteria, and issue `complete --note` with an evidence reference. Confirm the completion hold across two check intervals and a supervisor restart. Status records a declaration; it is not independent proof of task success. On builds with completion reports (unreleased after v0.1.2), verify the subscribed event and record HTTP acceptance separately from receiver receipt. Completion delivery is single-attempt and is not replayed after restart.
7. End the scratch supervisor, retain private evidence, and record total setup time, manual corrections and failures. Remove only the scratch resources created for this run. Do not leave another scheduler or unattended loop running.

| Target | Required evidence | Current status |
| --- | --- | --- |
| BB | Two supervised turns, input/stop handling, completion after restart, elapsed onboarding time | Full current-build walkthrough outstanding; earlier live adapter/daemon checks are recorded in [integrations](integrations.md) |
| Claude Code | Two turns in one resumed session plus the same holds and completion checks | Outstanding; fixtures do not establish live multi-turn compatibility |
| Codex | Two turns in one resumed session plus the same holds and completion checks | Outstanding; live probes/limit observations do not establish this journey |
| Separate accounts | Authorized handoff/switch-back with verified distinct existing account contexts | Outstanding; use fixtures for quota exhaustion |
| Installed services | Native install/remove, login/reboot/sleep/network recovery and soak | Outstanding; requires separate platform lifecycle checks |

Ownership conflicts and takeover are covered by the binary smoke check using fictional owners. Live transfer of the supervised BB target remains unimplemented and must not be represented as ownership takeover.

## Publish a matching release

Before tagging, review the complete Unreleased changelog and limitations, run required checks on the chosen commit, build archives with `scripts/build-release.sh <version>`, and smoke-check unpacked native artifacts. Preserve the exact tested commit and archive checksums in release evidence. Cross-compilation alone is not a native run.

Publish the accumulated maintenance release separately from claims that the usability milestone is live-verified. Update the Homebrew formula's version, platform URLs and checksums to the actual published assets, then verify a fresh install and its `version` output. Update the README/setup version guidance in step with publication; `go install ...@latest` selects a tagged release, not `main`. Keep archived documentation and examples attached to their matching tag. The BB plugin has its own build/install path and is not installed by the core archive or Homebrew formula.

The guided quick start requires v0.1.2 or newer. Check `keepgoing version` after installation; if a package mirror or tap still provides v0.1.1, use the matching release archive or a [development build](../README.md#development-build). Neither publication nor this checklist establishes a live provider walkthrough. The release workflow verifies archive checksums and smoke-checks the unpacked Linux amd64 executable before publishing; native macOS checks also run in ordinary CI.
