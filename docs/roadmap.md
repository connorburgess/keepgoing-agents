# Roadmap

## Done

**Milestone 1: supervise one loop.** Config validation, the pure decision engine, BB and process adapters, stall detection, time windows, the tick lock, control commands, and JSON tick output.

**Milestone 2: confirmed fallbacks.** `fallbacks.yaml`, headless Claude Code and Codex runners, three probe tiers with a daily cap, the health registry, redundancy across providers or groups, and handoff with an exact probe. It also covers model switching on BB threads, the generated brief with a file override, and manual retarget. BB limit detection uses structured provider-error and rate-limit events, and the loop waits for a reset when it is near.

**Milestone 3: operate it.** The daemon with immediate control handling and config reload. Signed webhook reports in the Standard Webhooks format, checked against its published test vector. `ci-wait`, `net-retry`, yield checks, scheduler examples, JSON Schemas with a drift test, Windows locking, and release automation.

**Verification pass.** The test suite was run on Ubuntu 26.04 ARM64 and Windows 11 Arm VMs. An independent audit of the docs against the code fixed these:

- Stop now wins even while the loop is paused or disabled.
- The daemon now notices a control change made during a pass.
- Relative config paths now work with headless runners and commands.
- Retarget now applies the target's model.
- A brief error no longer half-switches a candidate.
- Failed manual probes no longer delay scheduled probes.

See [integrations.md](integrations.md) for what is verified live.

**Agent chats.** BB pending interactions mean waiting on a person. `await-user` reports at once. A guide covers supervising a live chat thread. A session that stays stalled through `max_nudges` nudges now asks a person.

**0.1.0, released.** keepgoing supervises a live BB chat through a BB automation, reporting to a real webhook. The live checks verified:

- BB send, stop, and model switching against a scratch thread;
- the daemon's wakes, await-user hold, and SIGTERM exit;
- signed reports in the bearer and text formats;
- the release workflow, by publishing 0.1.0.

**BB plugin.** `integrations/bb-plugin-keepgoing` shows keepgoing's state in each supervised thread's context banner or card and adds `bb keepgoing status`, `pause`, `resume`, and `stop`.

**Homebrew.** `brew install connorburgess/tap/keepgoing` installs the release binaries from the [homebrew-tap](https://github.com/connorburgess/homebrew-tap) repository.

**Switch-back.** With `switch_back` on, the loop returns to the primary between the fallback's turns once the primary's limit resets and it passes an exact probe.

**Windows runners.** Headless Claude Code and Codex turns now run on Windows, through a detached copy of the keepgoing binary. Tested in a Windows 11 Arm VM.

**Codex limits.** A real Codex limit showed that `codex exec --json` reports it only as prose. keepgoing now classifies Codex failures from the app-server's structured rate-limit snapshot, with the reset time.

**Multiple accounts.** Headless Claude Code and Codex targets accept account environment overrides for fresh and resumed turns. Probes inherit them, and Codex limit checks use the same effective environment. The three-account example uses separate quota groups. Covered by fakes; live handoff between separate subscriptions remains to be verified.

## Next: first supervised loop in five minutes

keepgoing is the loop runner. A user supplies a task, chooses an agent, and starts the loop; a separate loop skill is not required. The current prompt and idle-wake mechanism already repeats work. This milestone makes task setup, completion, recovery, and operation one understandable experience. An optional agent skill may explain the CLI, but cannot be a prerequisite for ordinary use.

The items below are planned, not shipped commands. Implement one focused PR at a time. Work in the order below, with provider discovery able to proceed independently of setup. A live check needing an account or host blocks only that check. Record evidence and limitations in [integrations.md](integrations.md).

### 1. Task lifecycle and guided setup

- [ ] Define an explicit task-completion signal and CLI control, separate from pause, stop, and awaiting a person. Completion must survive supervisor restarts, prevent further wakes, appear in status, and leave a clear way to start a new task. Never infer completion by reading transcript prose.
- [ ] Add guided `keepgoing init`: collect the task or task file, project, agent/session, optional accounts, fallback order, and operating limits. Detect installed supported CLIs and offer a single-agent default.
- [ ] Generate the loop prompt and control instructions, including how the agent marks completion or requests human input. The agent evaluates task progress; keepgoing consumes explicit control and structured provider status.
- [ ] Store configuration and runtime state outside the checkout. Show the resolved target and planned actions before enabling. Preserve existing configuration, account logins, and restrictive permission settings.
- [ ] Provide equivalent noninteractive flags so scripts and IDE integrations use the same setup path.

Acceptance: a fresh user configures one task without editing YAML or installing a skill; the task completes, supervision becomes inactive, and restarting the supervisor does not repeat completed work. Pause, stop, and awaiting-input still take precedence over wakes and recovery.

### 2. Diagnostics and understandable status

- [ ] Add `keepgoing doctor` for configuration, CLI versions, target identity, account configuration paths, state permissions, and scheduler health. Check login through structured provider interfaces where available; report unavailable checks explicitly.
- [ ] Keep ordinary diagnostics free of model calls. Offer explicit paid probes with their expected cost and configured cap visible before execution.
- [ ] Show which task and session are supervised, the selected account label, last check, last action, reason for waiting, completion state, and next scheduled check or why its time is unknown. Flag a stale binary, missing target, or missing scheduler.
- [ ] Provide stable JSON output and actionable fixes without printing credentials or environment values.

Acceptance: users can distinguish an agent working, awaiting input, completed work, a paused loop, and a supervisor that is not running. A loop attached to an older conversation is visible as such.

### 3. Start and run in the background

- [ ] Add a single start operation that validates setup, enables the loop, and starts supervision with a clear foreground/background choice. Detect an existing supervisor instead of starting a duplicate.
- [ ] Add user-service installation, removal, and status for launchd and systemd first; investigate Windows Task Scheduler or a user-service equivalent with the same controls. Preserve the daemon and external-scheduler options.
- [ ] Report scheduler failures, stale heartbeats, and startup/version mismatches. Document behavior across logout, reboot, sleep, and network loss without promising execution while the host is unavailable.

Acceptance: a user starts a configured loop with one command, closes the terminal in background mode, and can still inspect, pause, and stop it. Service removal leaves no scheduled wakes.

### 4. BB conversation setup and transfer

- [ ] Add "Supervise this thread" using the shared setup and diagnostics flow, including task entry and the target identity.
- [ ] Add "Move supervision here" for an existing loop. Show the old and new targets, preserve paused/stopped/awaiting-user intent, and serialize the move against ticks so the old target receives no further wakes after transfer.
- [ ] Validate provider/model/account labels and refresh thread-specific state when moving; do not carry an old session cursor or limit observation into the new thread. Offer resume explicitly after a held loop is transferred.
- [ ] Show task completion, current target, scheduler health, and last action in the existing banner/card UI. Moving a chat must not silently leave the user believing the new chat is supervised.

Acceptance: transfer a loop between fictional scratch threads while its scheduler runs, verify only the new target can receive subsequent wakes, and verify all control holds remain honored.

### 5. Cursor and Antigravity integrations

- [ ] Complete capability discovery for both CLIs, including structured lifecycle/error events, stable session identity, resume, permission waits, cancellation, account selection, and supported versions/platforms.
- [ ] Implement and verify a Cursor CLI adapter, then expose it in guided setup.
- [ ] Implement and verify an Antigravity CLI adapter, then expose it in guided setup.
- [ ] Investigate attaching to existing IDE chats separately. Publish precisely which sessions can be controlled; CLI support alone does not establish IDE-chat support.

Use the delivery gates and provider-specific plan in [integrations.md](integrations.md#planned-integrations). Thin IDE controls can call the same CLI once a supported session-control interface is established.

### 6. Release and recovery verification

- [ ] Publish the accumulated changes since v0.1.1 as a focused maintenance release, with matching Homebrew updates and accurate versioned documentation. This can ship before the new usability features.
- [ ] Run real two-turn/resume tests for Claude Code and Codex, and handoff/switch-back across separately authenticated accounts in scratch projects.
- [ ] Exercise network loss/recovery, provider failure, supervisor restart, pending human input, task completion, and stop/pause during recovery. Record exact supported behavior and unresolved limitations.
- [ ] Verify installed schedulers on their target platforms and run a long-lived supervision soak.
- [ ] Release the usability milestone after its acceptance checks pass, with an install-to-completion walkthrough for each supported provider.

Acceptance: a new installation can complete the documented walkthrough on supported platforms, and published claims distinguish live checks from fake-based tests. Never manufacture a real limit by deliberately exhausting an account; use controlled fixtures for rare failures and record naturally observed structured events when available.
