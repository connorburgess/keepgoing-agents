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

Unchecked items below are planned, not shipped commands. Implement one focused PR at a time. Work in the order below, with provider discovery able to proceed independently of setup. A live check needing an account or host blocks only that check. Record evidence and limitations in [integrations.md](integrations.md).

### 1. Task lifecycle and guided setup

- [x] Add explicit task completion with `keepgoing complete --note`, persisted in control state and shown in status. It holds wakes and probes across restarts; `resume` explicitly reopens the task with the current target/session and any revised prompt. Completion is never inferred from transcript prose. See [operations](operations.md#completing-a-task).
- [x] Add guided `keepgoing init`: collect the task or task file, project, agent/session, optional accounts, fallback order, and operating limits. Detect installed supported CLIs and offer a single-agent default.
- [x] Generate the loop prompt and control instructions, including how the agent marks completion or requests human input. The agent evaluates task progress; keepgoing consumes explicit control and structured provider status.
- [x] Store configuration and runtime state outside the checkout. Show the resolved target and planned actions before enabling. Preserve existing configuration, account logins, and restrictive permission settings.
- [x] Provide equivalent noninteractive flags so scripts and IDE integrations use the same setup path.

Implemented on `main`: the [setup guide](setup.md) covers BB targets, new headless sessions, existing account directories, ordered fallbacks, limits, and dry-run previews. Tests use temporary Git projects and fake CLI discovery; they do not log in or spend model quota. Existing headless/IDE session attachment is not supported. A real fresh-install walkthrough remains part of release verification below.

Acceptance: a fresh user configures one task without editing YAML or installing a skill; the task completes, supervision becomes inactive, and restarting the supervisor does not repeat completed work. Pause, stop, and awaiting-input still take precedence over wakes and recovery.

### 2. Diagnostics and understandable status

- [x] Add read-only `keepgoing doctor` for configuration, available CLI versions, structured BB target identity, explicit account-directory checks, Unix mode checks, and recent tick history, with text and versioned JSON reports. Unsupported or skipped checks are explicitly unavailable. See [diagnostics](diagnostics.md).
- [x] Add headless provider login-status diagnostics using Claude JSON and Codex status exit codes, with target account environments and working directories, redaction, and offline/extra-argument skips. Covered by fake responses and Unix subprocess fixtures, plus a live macOS status-only smoke check in the existing inherited account context. Credential presence does not verify server access, quota, or independent subscriptions.
- [ ] Extend diagnostics with live headless session checks, full ACL privacy evaluation, and external scheduler installation/liveness and binary inspection. Current doctor does not claim to verify these. Last-tick build-label comparison is now implemented, but it is historical evidence rather than live process inspection.
- [x] Add read-only OS runtime-access checks for ordinary matching real/effective non-root identities on macOS/Linux: directory write/search access, nearest existing ancestors for missing state, and direct append/lock access. No probe writes, lock acquisition, or control changes occur. Offline fixtures cover errors, symlinks, missing paths, privacy, and holds; native tests exercise denied directory/lock permissions. This does not guarantee future writes or audit ACL privacy. Privileged Unix identities remain unavailable; Windows support is described separately below. See [runtime path access](diagnostics.md#runtime-path-access).
- [x] Add Linux POSIX ACL metadata warnings for checked files/directories, including default ACLs that mode bits alone do not show. Size-only OS queries never read ACL values or principal IDs. Absent/hidden metadata and unsupported checks remain unavailable, not proof of privacy. Covered by offline fixtures and a native Linux temporary-directory ACL test. Full permission evaluation and ancestor traversal remain planned. See [ACL metadata](diagnostics.md#linux-acl-metadata).
- [x] Add macOS extended ACL metadata warnings using a fixed-size native header query without loading ACL entries or principal identifiers. Zero-length, malformed, and failed results never establish privacy. Native temporary-file/directory fixtures verify ACL detection despite owner-only modes, symlink targets, removal, and unchanged files; offline fixtures cover header validation, redaction, and holds. This detects metadata only, not effective allow/deny or inheritance rules. See [macOS ACL metadata](diagnostics.md#macos-acl-metadata).
- [x] Add Windows DACL metadata inspection through native APIs, distinguishing null/absent (error), empty (warning), populated (warning), and unavailable. No ACEs, SIDs, or descriptor contents are displayed, and permissions are never changed. Dedicated Windows CI fixtures cover in-memory descriptors and temporary files/directories; shared fixtures cover redaction and held offline loops. Full ACL privacy evaluation remains planned; runtime access checks are separate below. See [Windows DACL metadata](diagnostics.md#windows-dacl-metadata).
- [x] Add Windows runtime-access checks using existing handles only: directory create/traverse rights, Go-compatible history append rights, and lock read/write rights, without data writes, creation, truncation, or locks. Directory checks reject impersonation and enabled backup/restore privileges; unknown tokens and sharing conflicts remain unavailable. Dedicated native Windows CI fixtures cover directory denial, append-only ACLs, read-only files, missing state, unchanged contents, and released handles; shared fixtures cover offline holds. Remote shares and all Windows architectures remain unverified. See [runtime path access](diagnostics.md#runtime-path-access).
- [x] Keep ordinary diagnostics free of model calls; `--offline` also skips version and BB target commands. Privacy and command bounds are tested with offline fixtures.
- [x] Preview explicit manual probes with `fallbacks check --dry-run`, showing selected candidates/models, timeouts, invocation count, and the configured scheduled cap before execution. Manual checks are explicitly outside that cap; expected dollar cost is shown as unknown. Dry runs make no provider calls or runtime-state changes. The execution command prints the same plan before running; doctor only recommends the preview. Offline fixtures verify this behavior with fake commands. Numerical cost estimation and monetary spending limits are not implemented. See [probe previews](fallbacks.md#preview-manual-probes).
- [x] Expand status with task source, configured BB thread and quota group, current control holds, last decision/reason/outcome, observation target, history freshness, and an explicit unknown next-check time. Persist tick build labels and flag differences in status/doctor; preserve legacy state. Covered by offline fixtures, including dry runs, failed checks, and handoffs. See [recorded status](operations.md#understanding-recorded-status).
- [x] Show saved headless resume IDs in status and doctor, scoped to the configured target and labeled as historical metadata. Inspection reads only the bounded session record; it never recovers identity from transcript/output files or starts a session. Offline fixtures cover missing/corrupt records and privacy.
- [x] Add live diagnostics for the built-in daemon: a private local challenge verifies a responding instance for this state directory, with checking/waiting phase, next normal check time, dry-run mode, and live build-label comparison. Local fixtures and a built-binary macOS smoke check exercise controls, clean shutdown, and forced-exit detection without provider calls. External schedulers and service installation remain unverified.
- [x] Add explicit BB script-automation diagnostics with `doctor --bb-automation <id> --bb-project <id>`: identity-checked registration, enabled state, next-run timestamp, and last-run metadata. Disabled, due/missing next runs, and reported failures are warnings; unknown schemas and offline queries remain unavailable. Offline fixtures and a read-only macOS BB smoke check cover the implementation. No timers, scripts, or holds are changed. This does not verify script-to-loop binding, binary identity, or scheduler liveness, and does not replace daemon next-check information. See [BB automation diagnostics](diagnostics.md#bb-automation-diagnostics).
- [ ] Add verified live headless session identity and external scheduler liveness/next-run information. Historical tick build-label comparisons remain separate from the built-in daemon live query. Missing configured targets are flagged; live BB lookup is available in doctor.
- [x] Provide stable doctor JSON output and actionable fixes without printing credentials, environment values, task text, control notes, or raw CLI output. Status now has historical runtime fields; live session and scheduler checks remain pending above.

Acceptance: users can distinguish an agent working, awaiting input, completed work, a paused loop, and a supervisor that is not running. A loop attached to an older conversation is visible as such.

### 3. BB child awareness and loop ownership

The diagnostics baseline is implemented above, with remaining diagnostic gaps still tracked. Deliver these owner-approved additions in the order below, with one focused PR per item and offline fixtures; implementation and verification status is recorded per item. They do not authorize additional live model spend. keepgoing remains a supervisor: it observes existing relationships and lifecycle signals, and must never spawn or re-parent threads. Only structured BB fields and events inform decisions; transcript text is never a signal. Stop, pause, completion, and awaiting-user holds retain precedence.

#### 3.1. Child-aware idle and stall classification

- [x] Discover existing children from a parent-filtered list including hidden threads, then verify structured BB thread data (`bb thread show --json`): a child's `parentThreadId` matches the supervised thread and its status is `active`.
- [x] Treat a parent with any active child as busy, suppressing idle wakes, stall nudges, and error re-prompts while the child runs. Observe children without starting or managing their work.

Implemented on `main` with offline adapter, decision-table, and repeated-tick fixtures. Active children and unverified discovery also prevent handoffs/switch-back; stop and other control holds retain precedence. Fixtures verify hidden children, identity/relationship changes, failed and oversized discovery, timeouts, unchanged budgets, and the next-check wake. No live child turn or paid call was started. The full-inventory contract was inspected in BB 0.48.1. Unsupported commands fail conservatively; versions that silently truncate listings are not verified or supported. See [BB child awareness](operations.md#bb-child-awareness). Lifecycle-driven early checks and child permission escalation are described below.

Acceptance: a fixture parent with one active child receives no prompt across `max_nudges` intervals and does not exhaust its nudge budget. Once that child finishes, the idle parent is re-prompted on the next check, subject to control holds and pending interactions.

#### 3.2. Wake on child completion

- [x] Treat a child's structured completed, failed, or interrupted lifecycle notification as a wake signal for an idle parent, checked ahead of the normal polling/wake interval. Do not infer completion from message text.
- [x] Consume each notification once so repeated checks or restarts do not send duplicate prompts. Other active children and pending interactions still prevent a wake.

Implemented for the built-in daemon with offline adapter, supervisor, and CLI fixtures. It polls BB's structured child outcome notices at `control_poll`, consumes event sequences through the persisted per-target cursor, and rechecks idle eligibility before an ordinary tick. Controls, active children, pending parent interactions, queued work, locks, and timeouts prevent unsafe early wakes. Single and batched metadata follows the inspected BB 0.48.1 schema; no live child turn or model request was started. External schedulers retain their own cadence. Missing notifications or a crash after consumption fall back to ordinary checks; daemon restarts still perform an ordinary initial tick. See [early child checks](operations.md#early-checks-after-child-outcomes).

Acceptance: with an idle parent, a child completion causes the next parent prompt within one check cycle instead of waiting for the ordinary interval. Fixture checks also cover failed/interrupted notifications, repeated events, and held loops. This does not promise delivery while the host or supervisor is unavailable.

#### 3.3. Escalate child permission prompts and questions

- [x] A child waiting on a structured permission prompt or question puts the loop into `await_user`, with a report identifying the child. Send nothing to the parent and do not answer the child's interaction.

Implemented with offline adapter, decision-table, supervisor, and CLI/webhook fixtures. After verifying the child's identity and current parent relationship, the adapter queries structured interaction status. Confirmed pending input persists an `await_user` hold and reports child IDs before probes or recovery; no parent or child is prompted, stopped, or answered. Unknown, failed, oversized, and resolving interaction queries withhold wakes without asserting that a person still needs to answer. Control holds retain precedence, and explicit resume is required after the interaction clears. Completion-driven early checks escalate pending child input instead of waking the parent. No live child interaction or paid call was started; BB 0.48.1 field/status shapes were inspected locally. See [child input escalation](operations.md#child-permission-prompts-and-questions).

Acceptance: a fixture child with a pending interaction produces an `await_user` report naming that child, while the parent remains untouched. Pending interactions take precedence over child-completion wake signals.

#### 3.4. Single owner per loop

- [x] Record the owning BB thread ID in the control file on enable. `validate`, `enable`, and `daemon` refuse a conflicting owner when another active BB thread already targets the same configuration or control file, with an actionable message unless `--take-over` is explicit.
- [x] Serialize ownership checks and takeover against competing writers, including different config paths sharing one control file. An explicit takeover records the previous owner as handed off in loop ownership metadata; it does not spawn, re-parent, or alter BB thread relationships. Preserve existing control holds.

Implemented with persistent `control.ownership` metadata and explicit `--take-over`. Claims remain reserved even when a thread is idle or its status cannot be queried; no live status query silently releases ownership. `validate --take-over` previews without changing the claim. Identity-free scheduler/terminal calls preserve the existing owner; they cannot take over. Claims and transfers share the tick lock, control writers use a separate short lock, and a changed generation makes an existing daemon exit before further provider work. Restrictive controls remain available from any thread. Offline fixtures cover competing processes, shared-state config paths, directory aliases, refused control-file symlinks, holds, generation changes, daemon exit, and scheduler behavior. No live thread transfer or model call was used for verification. See [one owner per loop](operations.md#one-owner-per-loop).

Acceptance: enabling from a second fictional thread ID fails without changing ownership; `--take-over` succeeds and records the previous owner as handed off. Offline fixtures also cover conflicting `validate`/`daemon` calls and shared control-file paths. Define scheduler calls without a BB identity and stale ownership explicitly; an unavailable owner-status check must not silently authorize a takeover.

### 4. Start and run in the background

- [ ] Add a single start operation that validates setup, enables the loop, and starts supervision with a clear foreground/background choice. Detect an existing supervisor instead of starting a duplicate.
- [ ] Add user-service installation, removal, and status for launchd and systemd first; investigate Windows Task Scheduler or a user-service equivalent with the same controls. Preserve the daemon and external-scheduler options.
- [ ] Report scheduler failures, stale heartbeats, and startup/version mismatches. Document behavior across logout, reboot, sleep, and network loss without promising execution while the host is unavailable.

Acceptance: a user starts a configured loop with one command, closes the terminal in background mode, and can still inspect, pause, and stop it. Service removal leaves no scheduled wakes.

### 5. BB conversation setup and transfer

- [ ] Add "Supervise this thread" using the shared setup and diagnostics flow, including task entry and the target identity.
- [ ] Add "Move supervision here" for an existing loop. Show the old and new targets, preserve paused/stopped/awaiting-user intent, and serialize the move against ticks so the old target receives no further wakes after transfer.
- [ ] Validate provider/model/account labels and refresh thread-specific state when moving; do not carry an old session cursor or limit observation into the new thread. Offer resume explicitly after a held loop is transferred.
- [ ] Show task completion, current target, scheduler health, and last action in the existing banner/card UI. Moving a chat must not silently leave the user believing the new chat is supervised.

Acceptance: transfer a loop between fictional scratch threads while its scheduler runs, verify only the new target can receive subsequent wakes, and verify all control holds remain honored.

### 6. Cursor and Antigravity integrations

- [ ] Complete capability discovery for both CLIs, including structured lifecycle/error events, stable session identity, resume, permission waits, cancellation, account selection, and supported versions/platforms.
- [ ] Implement and verify a Cursor CLI adapter, then expose it in guided setup.
- [ ] Implement and verify an Antigravity CLI adapter, then expose it in guided setup.
- [ ] Investigate attaching to existing IDE chats separately. Publish precisely which sessions can be controlled; CLI support alone does not establish IDE-chat support.

Use the delivery gates and provider-specific plan in [integrations.md](integrations.md#planned-integrations). Thin IDE controls can call the same CLI once a supported session-control interface is established.

### 7. Release and recovery verification

- [ ] Publish the accumulated changes since v0.1.1 as a focused maintenance release, with matching Homebrew updates and accurate versioned documentation. This can ship before the new usability features.
- [ ] Run real two-turn/resume tests for Claude Code and Codex, and handoff/switch-back across separately authenticated accounts in scratch projects.
- [ ] Exercise network loss/recovery, provider failure, supervisor restart, pending human input, task completion, and stop/pause during recovery. Record exact supported behavior and unresolved limitations.
- [ ] Verify installed schedulers on their target platforms and run a long-lived supervision soak.
- [ ] Release the usability milestone after its acceptance checks pass, with an install-to-completion walkthrough for each supported provider.

Acceptance: a new installation can complete the documented walkthrough on supported platforms, and published claims distinguish live checks from fake-based tests. Never manufacture a real limit by deliberately exhausting an account; use controlled fixtures for rare failures and record naturally observed structured events when available.
