# Operations

## Running it

There are two ways to run keepgoing. Pick one per loop.

- **`keepgoing tick` from a scheduler.** Each run makes one pass and exits. Ticks never overlap, because a tick that finds the lock held exits 75. See [examples/schedulers](../examples/schedulers) for launchd, cron, a BB automation script, and a GitHub Actions job.
- **`keepgoing daemon`.** This runs a pass every `daemon.interval`. Between passes it checks `control.json` every `daemon.control_poll`, so changes such as `keepgoing stop`, `pause`, or `complete` request a check within seconds. The daemon reloads `keepgoing.yaml` and `fallbacks.yaml` before each pass and retains the last timing and diagnostic context when an edit is broken. Provider work is withheld while the loaded source snapshot cannot be verified. Changing `state_dir` requires a restart: a running daemon keeps its original lock and diagnostic endpoint and withholds provider work until the original configuration is repaired or the daemon is restarted with a valid configuration. SIGINT or SIGTERM ends it at once and cancels any command in flight. A control change written while a pass is running starts the next pass immediately after it. Only one daemon runs per state directory, and a second one exits 75. `--dry-run` applies to every pass. See the systemd unit in the examples.

Ending the daemon does not stop the agent. To stop the agent, run `keepgoing stop`.

### Control-change detection

Unreleased source includes a content fingerprint alongside the regular control file's modification time and size when checking for changes between daemon passes. This catches same-size edits whose filesystem timestamp was preserved or did not advance. Published v0.1.5 and earlier use metadata alone and can wait until the normal interval in that case. Scheduler-driven ticks are unaffected: each invocation already reloads controls.

Unreleased Windows source opens control loads and content-poll snapshots with read, write and delete sharing, and the control/goal writer uses `FileRenameInfoEx` with replacement and POSIX semantics. Both are necessary: the legacy `MoveFileEx` call can reject an open destination even when that reader permits deletion. This write path requires Windows 10 version 1709 or newer and a filesystem supporting POSIX-style rename; unsupported operations return an error without falling back to legacy rename or removing the destination first. See Microsoft’s [rename operation](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information) and [version requirement](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/wdm/ne-wdm-_file_information_class). The handle is read-only; delete sharing permits another handle to replace the file without granting the reader delete access. An opened snapshot continues reading its original file after a cooperating writer atomically replaces it, and the next poll/load observes the replacement. A reader opened without delete sharing can still block a save. The other records a tick replaces while another command may read them now use the same reader and writer: `state.json`, `sends.json`, `progress.json`, `health.json`, the incident and delivery journals, the loop registry and the daemon endpoint record. Client status, hold, resume, transfer, timeline and doctor reads of these files use that reader too. Before this, a reader holding `state.json` made the daemon's tick fail after acting. For these records, unlike control and goal files, a system or filesystem that reports the POSIX-style rename as unsupported (by Windows error code) falls back to the ordinary rename they used before, so such a volume keeps working but a reader can again block a save there. A sharing violation or access denial never falls back. A reader that arrives while a record is being replaced can be refused for an instant with one of those two codes; the shared reader retries only them, a few times over about thirty milliseconds, then returns the error. Saves are not retried. Per-target files (the BB cursor and headless turn files), configuration sources and any outside program that opens these files itself still use ordinary opens.

A failed `state.json` save has a consequence on every platform: the tick has already acted, so the record that a stop or prompt was sent is lost, the next tick can send it again, and the daemon waits its ordinary interval before that tick. `state.json` and control files are now synced before they are published; this is not a power-loss guarantee. The writer opens only its staging file with delete access, closes that handle on every return, and never asks to ignore read-only attributes. This is not a retry mechanism, a guarantee that all writes succeed, or isolation from external in-place edits.

The fingerprint streams the opened regular file's observed length with bounded memory and stays in memory; neither control text nor its digest is added to logs, status or reports. Missing, unreadable and non-regular files do not count as valid control evidence. A changed signature only requests a new check: ownership, configuration-source and parsed control checks still govern actions. Unchanged files do not trigger repeated early checks. This is change detection for local files, not an atomic snapshot across external in-place writers or a guarantee against hostile filesystem replacement. A saved stop can still wait for an in-flight check or adapter command; it does not kill the current command immediately.

### Loaded configuration checks

After acquiring `tick.lock`, ticks, daemon notification polls, retargets, and executing manual fallback probes verify that `keepgoing.yaml` and its loaded `fallbacks.yaml` still contain the exact bytes originally parsed. Changed, missing, unreadable, or non-regular sources suppress provider work. Ticks, retargets, and probe execution return exit 75 with a reload/retry diagnostic; early polls wait for the next ordinary reload instead of querying the stale target. Rejected work leaves control, state, health, history, and cursors unchanged. A skipped tick does not advance the last-check timestamp; a responsive daemon alone does not prove checks are succeeding.

Pause and other holds remain saved. **Stop execution also waits for a verified configuration:** the supervisor cannot safely observe or stop a target from an unverified snapshot. Repair the configuration and let the daemon reload (on its interval or the next control change), or reload and retry the command. Once verified, stop retains precedence over pause, disabled, completed, and awaiting-user holds. If the agent must stop before repair, use its provider's own stop control against the verified target. A changed `state_dir` requires restoring the original binding or restarting deliberately; the daemon never moves its lock or diagnostics automatically.

This is a prerequisite for a future cooperative thread-transfer operation, not a transfer feature. Only writers using the same `tick.lock` are serialized with provider work; arbitrary external file edits can still race after verification. The check covers the two loaded YAML sources, not referenced prompt/secret files, executables, or runtime records. Private source digests are kept only in memory and are not serialized or printed. Byte-identical replacements are accepted. No target-state reset, cursor/session migration, or automatic thread transfer is implemented.

## Starting supervision

After reviewing a configured task, choose one:

```sh
keepgoing start --foreground -c /path/to/keepgoing.yaml
keepgoing start --background -c /path/to/keepgoing.yaml
```

`start` defaults to foreground. It validates configuration and fallbacks, readable task-file type/access, recorded state/health and active target, configured agent executables, and headless working/account directories. Custom process command executables are checked without executing them. These are local setup checks; they do not verify authentication, provider access, model quotas, or the live target. No login command or probe runs during startup validation. Once started, ordinary ticks can send prompts and run configured probes. Use `doctor --offline` for a read-only inspection instead of starting supervision. `start` does not accept `--dry-run` or `--take-over`; transfer ownership explicitly first if needed.

Start acquires `daemon.lock` before changing controls, then the tick lock and short control lock. Another daemon returns exit 75 with no control changes. A scheduler tick in progress also returns 75; retry later. Start records a BB owner just like `enable`, refuses a conflicting owner, and sets `enabled` while preserving pause, stop, awaiting-user, completion, and the note. It never resumes a held task, retargets a session, or changes an external schedule. An existing stop is still processed by the ordinary tick, even while paused. External schedulers do not continuously hold the daemon lock: start cannot detect every installed timer. Choose one scheduling method per loop and remove your old schedule explicitly before switching.

Foreground startup prints the control state, then tick results; Ctrl-C ends the daemon. Background startup launches a detached copy of this executable with the inherited environment and working directory, and waits up to ten seconds for that specific child's acknowledgement. Success means the child owns the daemon lock, has opened live diagnostics, and has enabled the loop; it does not promise the first tick or a provider action has succeeded. Validation or diagnostic startup failures leave controls unchanged. A timeout or interrupted acknowledgement terminates the child; if it had already enabled the loop, that flag remains. Inspect `status` before retrying; crashes cannot make a multi-process start transaction atomic.

The background daemon disconnects from terminal input/output. Tick decisions and outcomes remain in private `state.json` and `runs.jsonl`; raw daemon stdout/stderr is discarded. Use `status`, `doctor`, and, when troubleshooting raw command errors, foreground mode. `pause`, `complete`, `await-user`, and `disable` hold future work while the daemon stays alive. `stop` stops the agent on a check and holds future prompts; it does not exit the daemon. To end the daemon itself, terminate the printed PID (SIGTERM on Unix, `Stop-Process -Id <PID>` in PowerShell). Ending it does not stop the agent.

Background mode survives the launching terminal closing. It does not install a user service, restart after crashes or reboots, or guarantee survival across OS logout/session cleanup. It cannot execute while the host sleeps or is off. After wake, the next available check applies current controls; there is no catch-up queue of missed ticks. Network errors retain the normal structured-error policy. macOS and Linux user-service management are available separately through [`service install|status|remove`](services.md); live service lifecycle verification remains roadmap work. Windows service support is still planned; see the [Task Scheduler investigation](windows-services.md). Offline fixtures exercise detached startup, parent exit, duplicate refusal, status, holds, and stop with a local process adapter; no live model call or multi-account handoff is part of these checks.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | ok |
| 1 | failed: keepgoing could not run or could not record its result, or `fallbacks check` found redundancy unmet. For `tick`, see [the contract for schedulers](#exit-codes-for-external-schedulers) |
| 2 | usage or config error |
| 3 | `ci-wait`: no run appeared for the commit |
| 75 | skipped: the lock is held, a yield check reported other work, or `net-retry` waited longer than `--max-wait` for the network |
| 124 | `ci-wait`: timed out |

### Exit codes for external schedulers

Unreleased; fixture-tested only. The exit code of `keepgoing tick` says whether keepgoing ran and recorded the check. It does not say whether the session, the provider, the network or a repository was healthy. That is in the check's output and in `status`.

| `tick` exits | When | What a scheduler should do |
| --- | --- | --- |
| 0 | The check ran and was recorded. This includes a check that failed to observe or act: the session could not be observed, the target's adapter could not be built, a send or stop failed, a tracked repository or the fallback health file could not be read, or the operating window could not be evaluated. Such a check has `"outcome":"failed"` in its JSON line and in `runs.jsonl`, `outcome failed` in `status`, a line on stderr and an `error` report. A check that only left probes out for lack of time has outcome `succeeded` and a warning. | Nothing; run again on schedule. |
| 75 | The check was skipped: another tick holds the lock, the yield command reported other work, or the configuration files changed while loading. | Treat as success. |
| 1 | The check could not be recorded or could not start: `control.json`, `state.json`, the run log or incident history could not be read or written, the lock could not be taken, or loop ownership was refused. | Treat as a failure; a person should look. |
| 2 | Usage or configuration error. | Treat as a failure. |

Because such a failure no longer stops the scheduler, it must be visible another way. `status` prints `failed checks: N in a row since <time>` while consecutive checks fail, and `doctor` reports the same as a `supervisor.failed_checks` warning; both clear on the next completed check. Nothing escalates on that count by itself: the `error` report is sent for each failed check, and a persistent failure otherwise waits for someone to look.

Before this change a check that could not observe or act exited 1. A scheduler that disables a job after a few consecutive failures (a BB automation does after three) was therefore switched off by an outage, and nothing ran when the network came back. Scripts that relied on exit 1 to detect a failed send should read `outcome` from the JSON line instead.

### External schedulers can stop on their own

keepgoing cannot run itself when an external scheduler stops calling it. Known ways this happens:

- The scheduler disables the job after consecutive failures or timeouts. A tick killed by the scheduler's own timeout writes nothing, so keepgoing's history simply ends.
- The host sleeps, reboots or loses the user session.

What keepgoing shows afterwards, when someone runs it:

- `status` prints `history: stale` once the last check is older than the stale threshold, with the reason `no recent check; inspect the supervisor and its scheduler`. It does not query any scheduler.
- `doctor --bb-automation <id> --bb-project <id>` reads the selected BB automation and warns `The selected automation is disabled; scheduled runs are not expected.` See [BB automation diagnostics](diagnostics.md#bb-automation-diagnostics).
- Nothing notifies anyone. No report is sent when checks stop, because nothing is running to send it. A self-check that reports the gap on the next run is a [roadmap item](roadmap.md#priority-reliability-checkpoints).

Prefer `keepgoing start`, the daemon or a managed [service](services.md) where one is available: the service manager restarts it, and it does not depend on a scheduler's failure counting. If you keep an external scheduler, give its timeout a comfortable margin over `loop.tick_timeout` (below) and treat exit 75 as success.

### Check time budget

Unreleased; fixture-tested only. A check has a hard limit: `loop.tick_timeout` (default `3m`) plus 90 seconds, so **4m30s at the defaults**. Whatever is slow, a check ends by then, apart from the two seconds a killed command is given to exit.

Each step is either mandatory, with a fixed maximum that is reserved for it until it has run, or optional, and then it runs only in time that no later mandatory step has reserved:

| Step | Kind | Maximum | Notes |
| --- | --- | --- | --- |
| Yield command | mandatory | `yield.timeout` (10s) | runs first |
| [Connectivity check](#network-outages) | mandatory when needed | `network.timeout` (8s) | reserved until it has run; made at most once |
| Observing the target (for BB: status, children, log, interactions) | mandatory | 90s for the whole observation, 30s per `bb` command | at the limit the check records `could not verify state; no action taken` |
| Scheduled fallback probes | optional | `probe.timeout` each (90s) | started only when the whole timeout fits; never shortened |
| Git progress fingerprint | mandatory | 60s for the whole fingerprint, 30s per `git` command | reserved until it has been taken |
| Exact probes before a handoff | optional | `probe.timeout` each | may be shortened to the time that is free |
| Exact probe before a switch-back | optional | `probe.timeout` | started only when the whole timeout fits |
| Model switch and send (wake, nudge, re-prompt or brief) | mandatory | 60s (30s per `bb` command) | reserved until the send has been made. A [handoff to a new thread](fallbacks.md#hand-off-to-a-new-conversation) uses the same 60s instead: read the place 10s, `bb thread spawn` 30s, look the thread up by title 20s. Looking for a thread whose creation is unsettled, at the start of a check, takes up to 20s out of the observation's 90s |
| Routine reports (`probe_failed`, `degraded`, `recovered`, `wake`, `nudge`, `reprompt`) | optional | two attempts of `report.timeout` (20s) each | sent only with at least 10s free, and limited to the free time |
| Essential reports (`await_user`, `error`, `stop`, `handoff`, `switch_back`, `complete`, `limit_wait`, `child_error`) | mandatory | 30s reserved: three reports of 10s each | use free time when there is some |

With the network down, each due fallback's scheduled probe ran to its 90-second timeout and was followed by a failed report, before the session was considered at all. Four due fallbacks made one check last over seven minutes, so a scheduler with a five-minute timeout killed it before it wrote anything.

How the time is spent:

- **Scheduled probes are never shortened.** One is started only when its whole timeout fits. A probe that then runs to its timeout is a real failure: recorded, reported, counted against the daily cap, and not due again until `probe.every`. One that does not fit is listed in the check's `probes` with `"skipped":"tick_timeout"`, a warning names `loop.tick_timeout`, and it stays due. So a fallback that hangs costs one check, after which the others are probed; it cannot starve them.
- **A due handoff comes first.** When the target is in an error that the loop fails over on and the redundancy rule is already met, the scheduled probes wait and the time goes to the exact probes the handoff needs. See [fallbacks](fallbacks.md#redundancy) for what that relies on. If no handoff is attempted after all (a limit wait, a hold, busy children), the scheduled probes run after the decision in the same check. If one is attempted and no fallback takes over, the held-back probes run then; while any of them ran or is still due, no re-prompt is spent and the check records a deferred handoff instead, within the limit below. After the late probes the redundancy picture and the `degraded` or `recovered` report are brought up to date.
- **Exact probes for a handoff may be shortened**, because a handoff is worth trying with the time there is. A probe stopped early is listed with `"skipped":"cut_short"`, is not held against the fallback, and the next candidate is tried in the same check. A fallback stopped early goes to the back of the order on the next check, so a hanging one cannot keep a working one from being tried.
- **A handoff that could not be confirmed is put off, within a limit.** The check records `handoff deferred: no fallback could be confirmed in this check; the next check retries` with the cause in brackets (incident cause `handoff_deferred`), spends no re-prompt and raises no hold. The limit is `errors.max_reprompts` such checks within one error episode, or the same number of early stops for one fallback. Past it the result is treated as real: the fallback's probe is recorded as failed, and the ordinary error rules act on the current target (re-prompts, then a hold, or a limit wait). The counts end with the episode, on the same evidence as the re-prompt count (the session seen idle, or active with Git progress), and on a handoff, switch-back, retarget or resume. They are not reset by a check in between, so the checks need not be consecutive: a fallback that is healthy but slower than the time left on three error checks of one long episode is recorded as failed. Raise `tick_timeout` or lower `probe.timeout` if that happens.
- **Mandatory steps are never cut by the accounting**, so a wake or re-prompt is not lost to slow probes and nothing is stopped in the middle of a write.
- Stop, pause and the other holds are handled before any optional work.

**Reports.** A routine report that cannot have ten seconds is not sent. The check's warnings say so and the delivery records get a `not_sent` entry with no request and no attempt, so `status` shows it among the notification warnings. Essential reports are still sent, three per check at most. The `limit_wait` and `child_error` reports make a single attempt per wait or episode; when no time is left for one, nothing is recorded and no request is made, so the attempt is kept for the next check.

**The limit, tested.** `TestCheckEndsWithinItsHardLimitInEveryCombination` charges every step either its maximum or almost nothing, independently (yield, observation, fingerprint, model switch, send), with probes hanging and reports failing on both attempts, against idle, active, rate-limited, unknown-error, auth-error and errored-children sessions, with and without previously healthy fallbacks, with the connectivity check configured throughout and failing at its timeout in some: 1,512 checks per configuration. The longest is 4m00s of the 4m30s limit at the defaults, and 4m30s of a 5m00s limit with `probe.timeout: 2m`. The same fixture run against the code before this accounting gives 4m40s and 5m10s. These are fake-clock figures: each step is charged its configured limit, and no command was actually hung. They rest on a limit being enforced by killing the command; a process that ignores the kill and keeps its output open is abandoned after two more seconds, and a filesystem call that hangs is abandoned, not stopped. Local file writes (state, run log, incident history) are not timed.

**Configuration.** The minimum `tick_timeout` is `2m40s`, the time the mandatory steps can take less the 90 seconds. The connectivity check uses only what `tick_timeout` has beyond that, up to `network.timeout`: at `2m40s` it is not made, and from `2m48s` it has its full 8 seconds. With fallbacks it must also be at least `probe.timeout` plus 90 seconds, or no scheduled probe could ever start. If `tick_timeout` is omitted it grows to that value, so a file with a long `probe.timeout` keeps loading; an explicit value that is too small is an error naming both settings. `status` prints the budget on its `check budget:` line and says when it was raised from the default (`tickBudget` in `status --json`), and `doctor` reports it as `supervisor.tick_timeout`. Those, and `validate`, warn when `tick_timeout` exceeds `3m15s`, because the limit is then over 4m45s and no longer leaves a margin under a five-minute scheduler timeout. That includes an omitted `tick_timeout` beside a `probe.timeout` above 105 seconds. The daemon and managed services have no such timeout.

## Status for other tools

`keepgoing status --json` prints the control file, state, window, and redundancy summary. It also prints `primaryAgent` and `targetAgent`, which describe the loop's own agent and the one it is supervising right now. Each has its `adapter`, `provider`, and `model`, plus `bbThread` for a BB thread or `workdir` for a headless runner. An agent workspace can use these to show keepgoing's state beside the right thread.

## Understanding recorded status

`keepgoing status` is read-only and makes no provider calls. It shows the task source (inline or a file, without task text), configured target/thread and quota group, current control holds, and the latest recorded decision. `status --json` keeps its existing fields and adds `taskSource`, optional `taskFile`, `targetConfigured`, and `runtime`; agent identities now include `group`. A missing or disabled fallback no longer appears to be the primary: `targetConfigured` is false and `targetAgent` is empty. Use `doctor` to check live BB target identity separately.

New ticks persist `state.lastCheck` with:

- `at`, the caller's `version`, and the final action `target`;
- `observedTarget`, `observedState`, and the structured `awaitingInput` flag, kept separate from the destination of a handoff;
- the selected `action` and supervisor-generated `reason`;
- `outcome` (`succeeded`, `failed`, or `dry_run`) and a `dryRun` flag.

`state.lastWakeAt`, printed as `last sent`, is the time of the latest wake, nudge or re-prompt attempt, including one whose adapter call failed or was unsupported. A failed send makes `lastCheck.outcome` `failed`. An unsupported send does not: the check records its action with outcome `succeeded` and a warning in the tick output, and only `lastPromptSend.outcome: adapter_error` marks the attempt. These describe a supervisor decision and its outcome. `succeeded` means the supervisor operation returned successfully, not that the agent turn or task finished. A failed send is not a successful action; a dry-run wake is a plan that was not executed. An action of `none` with outcome `succeeded` means a no-action check completed successfully. Raw provider errors and prompts are not copied into this record. Failures before a record can be saved leave the previous history intact. Older state files still load, with detailed history unavailable until a new tick records it.

`runtime.state`, `runtime.reason`, and `runtime.source` distinguish current control/configuration from history. Stop takes precedence, followed by disabled, paused, awaiting-user, and completed holds. Without a hold, an invalid configured target or closed operating window is shown before interpreting history. Historical states use names such as `recorded_active` and `recorded_idle`; they do not claim the agent is currently running. A structured pending interaction is `recorded_awaiting_input`, not inferred from a reason string. A failed check is `check_failed`; a dry run is `dry_run`. A target change or an unobserved target after handoff is not attributed the previous target's state.

`runtime.freshness` is `never`, `recent`, `stale`, or `future`. The default stale threshold is 30 minutes; use `--stale-after` above your scheduler's interval. Stale, future-dated, or incomplete history cannot establish current activity. Current control holds remain visible even with stale history. For a responding built-in daemon, `runtime.nextCheck` reports the next normal check time or an in-progress check; otherwise it remains unknown. A configured daemon interval does not establish an external scheduler's schedule.

`runtime.binaryComparison` compares the last recorded tick's build label with this status process: `match`, `different`, or `unknown`. Empty and `dev` labels are unknown. A different label can indicate a stale scheduler binary, but does not prove which build is newer or currently running. Doctor also flags a different recorded build. Manual and dry-run ticks can replace the latest history, so this is not a running-process inventory.

Existing `control.note` output is retained for compatibility; status is a private operational report. Doctor continues to withhold control notes, task text, environment values, and raw errors. This feature is implemented on `main`, not in v0.1.1, and is covered by offline fixtures for control precedence, failures, dry runs, handoffs, version comparisons, and legacy state.

## Completing a task

When the authorized work is finished and verified, the agent or operator runs:

```sh
keepgoing complete -c keepgoing.yaml --note "Example task verified"
```

This sets `control.completed` in `control.json` and records a control event in `runs.jsonl`. Status shows `completed` unless a higher-priority disabled, stop, pause, or awaiting-user hold is also set; JSON always exposes the completion flag. No model output or transcript text is inspected to decide completion. In v0.1.3 or newer, a new completion declaration also attempts a subscribed `complete` webhook event after saving control and appending the control log.

The standard-format event is `keepgoing.complete`, with `basis: explicit_declaration` and `verification: not_recorded`. Both standard and text formats say that completion was declared and keepgoing has not independently verified success. This does not contradict verification performed by the operator or agent; keepgoing has no independent acceptance evidence in this version. The private control note, task/prompt text and evidence descriptions are omitted from this notification.

Deduplication uses the persisted `completed` flag under the control lock. Only a false-to-true transition submits a report. Repeated `complete` commands, changed notes, enable, other holds, ticks and restarts do not replay it. An explicit `resume` clears completion, permitting a later declaration to submit a new report. Legacy completed records and declarations made without a subscription are not backfilled when reporting is enabled. A report describes the declaration at its recorded time; concurrent resume/stop or network delays may mean it no longer describes current controls.

Completion delivery makes one HTTP attempt without automatic retries or redirects, including network errors and 5xx responses. HTTP acceptance is not proof of human receipt. Control is saved before delivery, and delivery does not retain the control lock: stop and pause remain available while the webhook waits. Report failures warn without undoing completion. A crash, log-append failure or reporter configuration failure after control is saved can lose the notification; repeated commands intentionally do not replay uncertain delivery. A bounded [diagnostic delivery ledger](diagnostics.md#notification-delivery-evidence) now records setup/attempt/HTTP outcomes in v0.1.3 or newer, with receipt always unverified. It does not authorize retries or repair crash gaps before reporting begins; there is no retry command. Do not resume/re-complete solely to retry a notification: resume reopens work. This is an at-most-once submission policy, not an exactly-once delivery guarantee; external writers, storage loss, deletion/rollback of private control state and older binaries are outside deduplication guarantees. Verification uses local webhook and command fixtures, not a live completion event from this project loop.

On the next check, a completed loop skips observation, yield checks, progress reads, scheduled probes, wakes, retries, handoffs, and switch-back. The hold survives process restarts. A pass already in flight can finish before noticing the change. Completion does not terminate the current agent turn or cancel messages already queued by another caller; use `stop` when the running turn must end. Stop still wins even when completion is set.

To reopen the task, or continue with a revised `prompt` or `prompt_file`, run `keepgoing resume`. Resume explicitly clears completion along with pause, stop, and awaiting-user, and clears the old note. It preserves the current target and session; it does not create a fresh conversation. It also does not enable a disabled loop: use `enable` separately in that case. `enable` alone never clears completion. Manual commands such as `fallbacks check` remain explicit operations and can still run while a task is completed.

Use `await-user` when unfinished work needs a person, and `complete` when all authorized work is done. If the task is an entire roadmap, completing one item is not completion of the task.

## Bounds on repeated work

keepgoing repeats work by design: each ordinary check may send one message to the session. Which rule applies depends on what the adapter reports.

| Session | Sent | Spacing | Count kept | Count resets when |
| --- | --- | --- | --- | --- |
| Idle | wake | none: every eligible check | none | not applicable |
| Active, Git fingerprint unchanged for `stall_after` | nudge | `min_wake_interval` since the last send | `max_nudges` | the fingerprint changes, or the session is seen in any state other than active |
| Error | re-prompt | `errors.backoff` | `max_reprompts` | the session is seen idle (a turn ended without an error), or it is active and the Git fingerprint changes |

The nudge and re-prompt caps each bound one stalled turn or one error episode. They are not a bound on total work: a session that keeps finishing turns is woken again on the next check, and both counts start over. Seeing the session active does not by itself restart the re-prompt count, because a re-prompt makes the session active: a re-prompted turn that fails again spends the same allowance, so repeated failures reach the hold. The rejected handoff-brief counts restart on the same condition. The condition is evaluated at checks, so it is coarser than "one error": the count carries over when a session works for hours without a commit and then fails again, and when queued input starts the next turn before any check sees the session idle. Unrelated errors can therefore share one allowance and reach the hold sooner than three failures of the same kind would. Without `progress.repositories` there is no Git evidence, and the count restarts only when a check sees the session idle. `keepgoing resume` clears the hold but, as before, does not refund re-prompt attempts: if the session is still in error after a resume, the next check holds again until `max_reprompts` is raised or the session is seen idle or making Git progress. Before this change (unreleased) any active check restarted the count, so a session alternating between active and error was re-prompted without limit. A handoff also restarts the re-prompt count, and a switch-back restarts both. With no `loop.limits` field configured, which is the default, nothing limits how many times this repeats. Sending then stops only through `complete`, a stop, pause or disable, or an awaiting-user hold: a per-episode cap, an auth error without a handoff, a missing target, or a child waiting on a person. Other gates, such as the operating window, queued messages, running children or a pending interaction, defer a send without ending the repetition.

Unreleased source makes that visible. `keepgoing validate` prints a `warning:` line on stderr when none of the three limits below is configured, and otherwise a `bounds:` line naming those that are. `keepgoing status` prints the same on its `bounds:` line, and `status --json` adds `workBounds` with `limits` (configured limit names in decision order, possibly empty), `unbounded` and, when unbounded, `warning`. The warning is advice about the loaded configuration only: no default limit is applied, nothing is refused, exit codes are unchanged, and it says nothing about whether a configured limit has been reached. Other commands, including `start`, `tick` and `doctor`, do not print it.

Three optional limits apply across turns, targets, restarts and resume. Each persists an awaiting-user hold on the next ordinary check after it is reached:

| Limit | Counts | Not reset by |
| --- | --- | --- |
| [`max_sends`](#total-send-allowance) | automatic send attempts of every kind, including failed ones | progress, turn completion, handoff, restart, resume |
| [`deadline`](#loop-deadline) | host-clock time against an absolute cutoff | restart, resume, retarget; only editing the configured value moves it |
| [`no_progress_timeout`](#no-progress-timeout) | host-clock time since the Git fingerprint last changed | turn completion, recovery, handoff, restart, resume |

`max_sends` is the only count-based bound on total sends. The two time limits bound how long checks continue, so the number of sends they allow depends on the check interval. `no_progress_timeout` also needs Git to be a meaningful progress signal for the task.

Precedence is fixed. Stop wins over everything and is the only rule that acts on a running turn. Disabled, paused, awaiting-user and completed come next and send nothing. Then `deadline`, `max_sends` and `no_progress_timeout`, in that order, each ahead of every observation of the session.

What the limits do not cover:

- A turn already running or queued is not cancelled, and nothing bounds how long one turn runs or what it spends. Only `stop` reaches a running turn.
- Scheduled and exact probes, activation and manual commands are not counted by `max_sends`. Scheduled probes keep their own daily cap and stop once any limit hold is persisted.
- The per-episode caps and spacing count attempts, not deliveries. Once the adapter has been called for a wake, nudge or re-prompt, the attempt advances `max_nudges` or `max_reprompts` and starts `min_wake_interval` or `errors.backoff`, whether the call returned cleanly, failed or was unsupported. keepgoing cannot tell a failed send from one that was delivered before the error, so it treats both alike. The cost is that an adapter whose sends keep failing reaches the awaiting-user hold after `max_nudges` or `max_reprompts` attempts even if none arrived; inspect the adapter, then `resume`. Before this unreleased change, v0.1.5 and earlier retried such a send on every check with no cap or spacing. An idle wake has no cap either way: a failing wake is still attempted on each eligible check, bounded only by `max_sends` and the time limits.
- Rejected handoff and switch-back briefs are bounded per target in unreleased source, as described under [rejected takeover briefs](#rejected-takeover-briefs). In v0.1.5 and earlier a fallback that passed its exact probe but rejected the brief was probed and sent to again on every check.
- The bounds depend on the private runtime records described in the sections below. Deleting or rolling them back, power loss, storage corruption and older binaries are outside what is tested.

Evidence is offline only. Decision tables enumerate every session state and error class against each observation gate, the operating window, switch-back, failover and spent or fresh per-episode counts. For each combination they check that a reached limit asks for a person and never sends, and that stop and the other holds win with or without a limit reached. A supervisor fixture with a fake adapter repeats wake, stalled nudge, wake and error re-prompt across completed turns with no Git change. With no limit it sends on every eligible check for twelve rounds without a hold; with each limit, and with all three together, sending stops at the configured point, one awaiting-user report is made, later rounds send nothing, and a stop request still reaches an active turn. Separate pure tables replay one-minute checks through the decision and the attempt accounting, and a fake-adapter fixture with failing and unsupported sends checks two attempts a backoff or wake interval apart, then one hold, where the earlier code sent on 20 of 20 checks. These tests establish when keepgoing stops sending in those fixtures. They do not establish that an agent's work terminates, and no live provider, installed service or paid call was used.

### Rejected takeover briefs

A fallback can pass its exact probe and still not take the loop: its adapter cannot be built, its model cannot be applied, or the brief send fails or is unsupported. The same can happen to the primary on switch-back. Unreleased source counts those rejections so the same target is not probed and sent to on every check.

**Handoff.** Each fallback has its own count and spacing within one error episode:

- After a rejection, that fallback is skipped until `errors.backoff` has passed. Skipped means not probed and not sent to.
- After `errors.max_reprompts` rejections it is skipped for the rest of the episode, and the next healthy fallback is tried.
- The episode ends on evidence that the session recovered: a turn that finished without an error (the session is seen idle), or verified Git progress while it is active. It also ends when a handoff or switch-back succeeds, on `retarget`, on a BB transfer and on `resume`. The counts then start over.
- Seeing the session active does not end the episode. A re-prompt from keepgoing, or a provider retry, makes it active; if that turn errors again the fallbacks stay capped. Without `progress.repositories` there is no Git evidence, so only a finished turn ends it.
- Every brief send is still reserved against `max_sends`. A failed adapter build or activation happens before the send, so it counts toward the cap but not toward `max_sends`.

When no fallback can be offered a brief on a check, the loop does what it already did when no fallback could take over: it waits for a reported reset, or applies the re-prompt rules to the current target and holds once those are spent. The reason, hold note and report then say that every confirmed fallback is capped or backing off after rejecting its handoff brief. The tick's `handoff.skipped` lists each skipped fallback as `backing_off` or `capped`.

**Switch-back.** Attempts were already spaced by `switch_back.retry_every`. After `errors.max_reprompts` rejected switch-back briefs, keepgoing stops trying and keeps the fallback working. No hold is raised, because the loop is still running on the fallback. When the cap is reached it sends one `switch_back` report whose data has `stopped: true`, so a subscriber must not read every `switch_back` report as a completed move. Each later check carries a warning, and `status` prints a `switch-back: stopped` line and sets `takeoverBriefs.switchBackStopped`. There is no time-based retry: it starts again only after `resume`, `retarget` or the next handoff.

**Trying a repaired target again.** `keepgoing resume` clears all rejected-brief counts, for fallbacks and for switch-back, as well as the holds it already clears. Use it after repairing a fallback that was capped while the primary is still in error, or after repairing the primary when switch-back has stopped. `keepgoing retarget <id>` clears them too and moves the loop without waiting for a brief. Pause, enable and the other controls leave the counts alone, and so does the registered `client resume`, which does not change `state.json`. `status` lists capped fallbacks on a `handoff briefs:` line and in `takeoverBriefs.capped`.

**A brief that arrived anyway.** A send reported as failed may still have been delivered. keepgoing then counts a rejection and keeps the loop pointed at the current target while that fallback starts working. It can be sent up to `errors.max_reprompts` briefs in total, and a later fallback may also be handed the loop, so two sessions can work on the task at once. This is the same uncertainty as a failed wake or re-prompt; check the fallback's session when a handoff brief is reported as failed.

**Dry runs.** A dry run previews the first fallback that would be offered a brief. When none would be, it previews the wait or hold a real check would reach, and changes no counts.

**Why per fallback, and what it costs.** The count belongs to one fallback so that a single broken fallback cannot use up the attempts of a healthy one that is listed after it. The cost is more total attempts: with N fallbacks that all reject, one error episode can make up to N times `max_reprompts` probes and brief sends before the loop falls through. A single count for the whole episode would cap that total at `max_reprompts`, but one fallback's failures would then stop the others from being tried. The rule is one pure function, `decide.BriefGate`, applied per fallback in the handoff loop, so changing to a per-episode count is a small change. Both limits reuse `errors.max_reprompts` and `errors.backoff`; there is no separate setting.

Evidence is offline: a decision table for the gate, and fake-adapter fixtures with a rate-limited primary and two fallbacks that pass the probe and reject the brief, with and without a reported reset. In those, each fallback is probed and sent to twice, ten minutes apart, over forty one-minute checks, where the earlier code did so on every check. Further fixtures cover an error, active, error sequence that keeps the caps, recovery that clears them, a fallback whose adapter cannot be built, a restart mid-episode, older state files, `resume`, `retarget` and transfer. No live handoff was run.

## No-progress timeout

In v0.1.3 or newer, set `loop.limits.no_progress_timeout: 2h` or use `init --no-progress-timeout 2h`. It requires at least one `progress.repositories` entry and a duration of at least one minute; zero/omission disables it. There is no default timeout. This measures elapsed host-clock time without a change in the existing Git metadata fingerprint, not useful work, goal completion, tokens or spend. Research, discussion and untracked files may leave no fingerprint change: use a deadline or send allowance when Git is not an appropriate progress signal.

The first enabled ordinary tick saves a baseline in private `progress.json` before any automatic provider observation, yield command or probe. Subsequent ticks verify Git again. A changed fingerprint for the same repository list resets the timer; an unchanged fingerprint at or beyond the timeout persists/reports an awaiting-user hold. Progress is checked even outside the operating window or while the agent is busy. No running or queued turn is cancelled. Stop, existing holds, the absolute deadline and exhausted total-send allowance take precedence. A failed read, fingerprint or ledger write prevents automatic provider work with a diagnostic error; unknown evidence never grants more time.

The ledger is independent of diagnostic history, target identity and per-turn retry/nudge counts. Repeated idle turns, errors, handoffs, switch-back, restart and resume do not reset it. Elapsed time includes operating-window closures while checks keep running. Elapsed time still includes pauses and disabled or held periods while checks keep running. Unreleased source no longer counts time in which no check ran at all (a host that slept, a stopped scheduler) beyond one ordinary interval, or time in which the connectivity check found the network down; see [a long time offline](#a-long-time-offline). It is still not active execution time. Reordering or duplicating repository entries does not alter the source identity. Changing the source list rebases the fingerprint while preserving elapsed time, so a configuration edit alone is not progress. Dry runs preview the result without changing this ledger or controls. Child-completion polling skips provider queries when the saved timer is due, unavailable or lacks a baseline, leaving fresh Git verification to the next ordinary tick.

Once held, new Git changes or a configuration edit alone do not reopen supervision. Inspect the work and tracking, then explicitly `resume` after verified progress, increasing the timeout, or disabling it under the task's authorization. Resume alone with unchanged Git and an expired timer immediately holds again. Re-enabling a disabled timeout uses the retained ledger. A fresh state directory starts a fresh baseline; deleting/rolling back the ledger loses the bound and is not a recovery method.

`status` reports `progressAllowance` with `timeout`, historical `changedAt` and `observedAt`, and `due`; unavailable evidence instead has `error`, and a new allowance has `baselinePending`. Status does not query Git or change the ledger. `due` means the saved timestamp has reached the interval: a future tick may verify new progress, so it does not prove that a hold has been applied. The allowance summary adds no repository paths or fingerprint hashes; the existing diagnostic `state` fields are unchanged. Host-clock rollback before the last observation fails closed until corrected or caught up; forward jumps can cause an earlier hold. Persisted holds survive clock changes.

The tick lock and synced replacement preserve accounting across process restarts; power loss, storage corruption, manual deletion/rollback, external writers ignoring the lock and older binaries are outside this guarantee. Manual commands remain outside the automatic bound. Verification uses offline state/decision/supervisor and CLI fixtures, including real Git metadata in a temporary fixture repository. No live model or installed service was used.

## Total send allowance

In v0.1.3 or newer, set `loop.limits.max_sends: 20` or use `init --max-sends 20` to allow at most 20 automatic send attempts while accounting is enabled for that state directory. Zero or omission disables accounting; there is no default cap. Setup previews the limit. `status` exposes `sendAllowance` with `max`, `used`, `remaining`, and `reached`; an unreadable ledger instead has `error` and omits unknown counts. These are local accounting facts, not proof of delivery or agent completion.

Wakes, stall nudges, error re-prompts, handoffs and switch-backs share the allowance. Each attempt is reserved in private `sends.json` before calling the adapter. Failed, unsupported or uncertain sends consume an attempt; a process exit after reservation can consume one without delivery. Multiple fallback attempts in one tick each count. Probes, activation, manual commands and prompts sent outside keepgoing are not sends under this limit. It is not a token, monetary or agent-turn budget, and it does not stop work already running or queued.

The next ordinary tick after exhaustion persists/reports an awaiting-user hold before automatic provider queries, probes, yield or fingerprints. Exhaustion during failed recovery prevents further sends and holds in that tick. Stop and existing holds take precedence. Child polling does not query providers or consume notices when exhausted or accounting is unavailable. Dry runs reserve nothing and persist no hold. Repeated checks retain the hold without repeating its report.

Restart, progress, turn completion, target changes, ownership takeover and resume do not reset the count. To continue, increase the total cap under the task's authorization and explicitly `resume`; changing configuration alone retains the hold. Disabling the cap permits uncounted sends after resume but preserves the previous count, which applies again if re-enabled. The allowance starts at zero when first enabled in an existing state directory; earlier activity cannot be reconstructed. A fresh state directory has a fresh allowance. Never delete or roll back the ledger to recover from exhaustion. There is no reset command in this version.

Malformed, unreadable or unwritable accounting fails closed with a diagnostic error; no unverified allowance is assumed. Repair runtime access or restore the last trustworthy ledger before continuing. The tick lock serializes reservations, and synced temporary-file replacement preserves them across process restarts independently of `state.json`. This does not guarantee survival of power loss, storage corruption, manual deletion/rollback, external writers ignoring the lock, or an older binary that does not implement this limit. Verification uses offline CLI, decision and supervisor fixtures, including simulated exit before recording a check; no live provider was used. A separate [no-progress timeout](#no-progress-timeout) is available in v0.1.3 or newer.

## Loop deadline

Set `loop.limits.deadline` to a timestamp with a timezone, or pass `--deadline "2030-01-01T18:00:00Z"` to `init`. There is no default deadline. Setup previews it in text and JSON; `status` exposes `deadline` and `deadlineReached`. These status fields describe the configured cutoff and host-clock comparison, not proof that a supervisor has run or that an agent stopped. Status itself changes no controls.

Each ordinary tick compares its start time with the deadline. At equality or later, it records `await_user`, persists the reason in the existing control hold, and sends the subscribed awaiting-user report. This happens before automatic provider queries, yield, fingerprinting and all automatic probes/recovery. Repeated checks or supervisor restarts retain the hold and do not repeat that report. Child-outcome polling also skips providers after the cutoff and leaves hold persistence to the next ordinary tick. External schedulers retain their own cadence. Dry runs preview the decision and record diagnostic history without setting a hold, sending reports or calling providers.

An in-flight check may finish past the deadline, and an already-running or queued agent turn is not cancelled. This is a cutoff for future supervision checks, not a real-time execution timeout, total-turn cap or monetary budget. It relies on the host clock; clock changes can move an unreached cutoff. After a hold is persisted, a clock rollback or deadline edit does not clear it. Stop retains precedence and can still cancel a running turn through its adapter.

To continue, revise or remove the deadline under the task's existing authorization and explicitly `resume`. Resume alone with an expired deadline causes another hold on the next check. Enable, restart, retarget and ownership takeover do not extend the configured deadline. Existing disabled/pause/stop/completion/awaiting-user holds retain precedence. Manual operations, including `fallbacks check`, `retarget` activation and read-only `doctor` queries, remain explicit operations outside this automatic cutoff. Existing config-snapshot and ownership checks still apply.

Available in v0.1.3 or newer. Verification uses decision tables, CLI and supervisor fixtures; no live provider or service deadline was exercised.

## Yielding to other work

```yaml
yield:
  busy_command: ["./busy-check.sh"]
```

When the command exits 0, the tick does nothing and exits 75. Any other exit means the loop may run. A command that cannot run is a warning, and the tick goes ahead. This lets heavy scheduled jobs on the same machine take priority.

## Webhook reports

```yaml
report:
  secret_file: ~/.config/keepgoing/example.env
  events: [handoff, degraded, recovered, await_user, stop, error]
```

The URL and secret come from `KEEPGOING_WEBHOOK_URL` and `KEEPGOING_WEBHOOK_SECRET`. Rename them with `url_env` and `secret_env`. Values are read from the environment first, then from `secret_file`, a `KEY=value` file. Secrets never appear in logs, errors, or reports. The URL must use https. Plain http is allowed only for localhost.

- **`scheme: standard-webhooks`** is the default. It signs each body with HMAC-SHA256 and sends `webhook-id`, `webhook-timestamp`, and `webhook-signature`. The secret is base64, optionally prefixed `whsec_`.
- **`scheme: bearer`** sends the secret as a bearer token instead.
- **`format: standard`** is the default. It sends `{"type": "keepgoing.<event>", "timestamp": ..., "data": {...}}`.
- **`format: text`** sends `{"source", "sent_at", "text"}` for receivers that expect a plain message.

A failed delivery is retried once on a network error or a 5xx response. A report that cannot be delivered is a warning on the tick, never a failure. Check delivery with:

```sh
keepgoing report test --dry-run   # print the body
keepgoing report test             # deliver it
```

Events: `wake`, `nudge`, `reprompt`, `handoff`, `switch_back`, `degraded`, `recovered`, `await_user`, `limit_wait` (unreleased source), `child_error` (unreleased source), `complete` (v0.1.3+), `stop`, `error`, `probe_failed`, `test`, or `all`. `complete`, `limit_wait` and `child_error` (unreleased source) are included in the default event list on `main`; an explicit event list must include them or `all`. All three make a single attempt without following redirects. Other events retain their existing one-retry policy. `keepgoing report test` sends a `test` event, whatever events are subscribed.

## Situations that need a person

- **The agent is waiting on a person.** A pending interaction is reported once as `await_user`, and nothing is sent until it is answered. `keepgoing await-user --note` reports at once and holds the loop until `keepgoing resume`.
- **The session stays stalled.** After `progress.max_nudges` nudges without visible progress, the loop is marked awaiting user and reports `await_user`.
- **The target is gone.** An archived, deleted, or missing target marks the loop awaiting user and reports `await_user`.
- **A headless turn was stopped.** Stopping a headless turn ends its process without an exit code, so it reads as an `unknown` error. After `keepgoing resume`, it is re-prompted under the ordinary error rules.
- **Only `resume` clears a stop.** `enable` does not.

### A long limit wait with no way out

Unreleased source; **offline fixtures only**. A provider limit with a reported reset is waited out without prompts when no handoff can happen: failover is off for that error class, or no fallback is confirmed. That wait can last days. When the reported reset is further away than `errors.report_wait_after` (default `6h`), keepgoing sends one `limit_wait` report for that wait, saying which target is waiting, when the limit resets and why no handoff is possible.

- **It does not hold the loop or change a decision.** Checks keep observing. If the limit lifts early, the next check acts exactly as it would have without the report. Nothing needs to be resumed.
- **One report per wait.** The wait is recorded in `state.json`, so repeated checks and a restarted supervisor stay quiet. The wait ends only on positive evidence: the loop moved to another target, the session is idle or working without a provider retry, or it is in error with no reset still ahead. A check that merely decided something else (outside the time window, queued messages, a pending interaction, child work, a provider retry, an unknown or pending state) does not end it, so a loop with a daily window reports a multi-day wait once. After the wait has ended, a later long wait is reported again.
- **A reset that moves later.** Within one wait, a second report is sent only if the reported reset moves more than one hour later than the reset last reported, and a third only if it then moves more than two hours later again. Three is the most any single wait sends, so a provider whose reset keeps sliding cannot produce a stream. A reset that moves earlier is never re-reported.
- **At most once, never retried.** The wait is saved before the request is made, the same order as holds and completions. A report makes a single request without following redirects. If the supervisor is killed mid-request, or the request fails, that report is lost: it is not sent again by later checks, held or not, and a failure stays visible in `status` and the notification records. If `state.json` cannot be written, nothing is sent.
- **Subscription.** `limit_wait` is in the default `report.events` because, like `await_user`, it says a person may be needed. An explicit `report.events` list receives it only if it names `limit_wait` or `all`: existing explicit lists are unchanged and get nothing new. With no subscription the wait is still recorded and shown, marked as not sent; subscribing during the wait sends its one report on the next check.
- **Holds win.** A paused, disabled, completed, awaiting-user or stopped loop does not observe its target, so it neither sends this report nor forgets one already recorded. Dry runs send and record nothing.
- **Turning it off.** There is no off value: `report_wait_after: 0` means the default. To receive nothing, give `report.events` an explicit list without `limit_wait`. The wait is then still recorded and shown as not sent.
- **When it does not apply.** No report is sent when a handoff is possible. That includes a long wait you chose with a large `wait_if_reset_within` while a fallback is confirmed. The same setting with no confirmed fallback leaves no way out and is reported. With no reported reset time the supervisor does not wait at all: it re-prompts within `errors.max_reprompts` and then holds for a person, which reports `await_user`. So a limit without a reset time is never waited out silently.
- **Where it shows.** `status` prints the recorded wait, its reset time and whether the report was requested, failed or not subscribed (`limitWait` in JSON). `timeline --kind limit_wait_report` shows the check that detected it, and the delivery appears as a `limit_wait` notification.

- **Older builds.** Once a `limit_wait` delivery or a marked check is recorded, v0.1.5 and earlier refuse `deliveries.json` (unknown event label) and `incidents.json` (newer schema) rather than rewrite them. Do not run an older build against the same state directory.

The payload carries the loop name, the target alias, the reset time, the reason the threshold and the report's number within the wait. It carries no task text, transcript or thread ID. This is the same content class as the existing `handoff` report.

## ci-wait

```sh
keepgoing ci-wait --repo example-org/example-app --pr 42
keepgoing ci-wait --repo example-org/example-app --sha <40-char sha> --branch cb/example --json
```

This waits until the newest run of every workflow on the exact commit has finished. It looks at the 50 most recent runs on the branch, and it pins the pull request's head commit when it starts. A failed run makes it exit 1. When a run is replaced, for example cancelled by a pull request edit, the newer run of the same workflow is followed. Then it prints each run's jobs. It uses the `gh` CLI, so authentication stays with `gh`. Every `gh` call goes through the network retry below.

## Network outages

Unreleased; **fixture-tested only**. A re-prompt, a handoff brief or a fallback probe sent while the network is down fails and still spends its allowance. In one observed outage two of three allowed re-prompts went into a dead network; a third failure would have ended in a hold with the network back minutes later. keepgoing now checks connectivity first.

**What keepgoing sends, by default.** One HTTP `GET https://api.github.com/zen`, with Go's standard `User-Agent` and no loop name, host name, thread ID, token or other identifier. keepgoing contacted no host by itself before this change. It is sent only when one of the following is about to happen, at most once per check, and never in a dry run:

- an error **re-prompt**, a **handoff**, or an **awaiting-user hold for a session in error** (a spent re-prompt cap or an auth error);
- the first **scheduled fallback probe** that is due;
- the exact probe of a **switch-back**.

Idle wakes, nudges, stop, pause and every other hold do not trigger it. To change it, set `loop.network`: `url` for another address, `command` for an argv whose exit 0 means online, or `check: "off"` for no check at all. With neither `url` nor `command`, the `net-retry` variables apply first: `KEEPGOING_NET_PROBE`, a command line run by the platform's shell (`/bin/sh -c`, or `cmd /C` on Windows), or `KEEPGOING_NET_PROBE_URL`. Only `network.command` is run without a shell. Any HTTP response means online; a transport error means offline. No response body, error text or provider output is read. The check has `network.timeout` (8s), reserved inside the [check's hard limit](#check-time-budget).

**While it fails.**

- An errored session is not re-prompted, not handed off and not held. The check records action `none` with the reason `network down; waiting for connectivity` (incident stage and cause `network_down`), outcome `succeeded`, exit 0. The re-prompt count, the backoff clock, the handoff-brief counts and the send allowance are untouched. A hold that has nothing to do with the session's error (a child waiting on a person, a loop deadline, the send limit, a missing target) is raised as usual.
- Due scheduled probes and a due switch-back probe are not run. They appear in the check's `probes` with `"skipped":"network_down"`, are not recorded as failures, do not count against the daily cap, and stay due.
- A check that has already found the network down, because a probe was due, does not go on to send an idle wake, a nudge or a switch-back brief either. A check that had no reason to test the network still sends them.
- The tick line has `"network":"down"`, a warning names `loop.network`, and `status` prints `network: down at the last connectivity check, failing since <time>`.

**When it returns.** The first check whose connectivity check passes acts as usual: it re-prompts if the session is still in error (subject to `errors.backoff` from any earlier re-prompt) and runs the probes that were left due. Nothing is retried faster than the ordinary check interval.

**A long outage, and a check that is wrong.** Two cases look the same from the check: a real outage, which may last weeks, and a check target that this machine cannot reach while the provider works (a DNS filter or blocked host, a scheduler environment without its proxy settings, a proxy that refuses the connection, a TLS-inspecting gateway whose certificate is not trusted). The first must be waited out; the second must not hold a loop in silence. So after `network.verify_after` (default `1h`) of failed checks, and again at that interval, keepgoing asks the provider directly, with the one exact probe a switch-back would use for the current target:

- **The provider answers**, by passing the probe or by refusing it for a reason of its own (a rate limit, an expired login, overload). The network works, so the check is set aside for this outage: the tick line has `"network":"down_untrusted"`, a warning and `status` say so, and the ordinary error rules apply again. This is also how an expired login is found after a long gap: the next check holds with `auth error` and sends its `await_user` report once. The check is believed again only after it has itself passed.
- **The probe does not get through.** It is a real outage. Nothing is spent and no hold is raised, however long it lasts: the fixture runs three weeks. A `network_wait` report is attempted at each verification until one is accepted; in a real outage it usually cannot be delivered, and nothing depends on it.
- **No probe is configured for the target** (`loop.probe`, set by default for the `claude-code` and `codex` providers). keepgoing cannot tell the two cases apart, keeps waiting, and the report says so.

The verification probe is the only thing sent during an outage. Into a dead network it fails without reaching the provider. `keepgoing doctor` runs the connectivity check once and reports it as `network.check`, so an unreachable target is found before it matters; `doctor --offline` does not run it.

**What counts as online, and captive portals.** Online means the target answered the request itself: a response that is not a redirect and not `511 Network Authentication Required`. Any other status counts, because a server that answers shows the network works. A network that needs a sign-in answers requests itself, with a redirect to its sign-in page (on another host or on the target's own name), a `511`, or, for `https`, a certificate this machine does not trust; all of these read as offline. keepgoing does not follow redirects, so a `network.url` must answer directly: `doctor` shows a target that only redirects as unreachable. A portal that answers a plain `http` request in place, with its own page and an ordinary status, cannot be told from the target; use `https`.

**Recorded outage and status.** The `network:` line reflects the last connectivity check. It is cleared when a check passes, and also when a later check needs no connectivity check and finds the session active or idle, since nothing is waiting for the network then. The check that finds the network back has `"network":"restored"` and `networkDownFor` in its tick line and a `network back` warning with the duration, which `runs.jsonl` keeps. The outage starts at the first check that found it, not at the moment the network dropped. `timeline` shows the waiting checks as one retained entry with its first and last time and a count, whatever its length; it does not tell a portal from no network.

**Reports that make one attempt.** `limit_wait` and `child_error` reports are not attempted while the connectivity check is failing, because a failed attempt is not repeated. They are sent by the first check after it passes.

**Limits.**

- The check says the network is reachable, not that the provider is. A provider outage with a working network is still re-prompted into, as before.
- With `tick_timeout` at its minimum there is no room for the check in the time accounting. It is then not made, and `validate`, `status` and `doctor` say so; raise `tick_timeout` to `2m48s` or more to have it.
- `doctor` tests the target from the environment `doctor` runs in, which may differ from a scheduler's.
- Recovery starts at the first check after connectivity returns, so up to one scheduler or daemon interval after a portal is cleared. Reacting to a wake or a network change sooner is a [roadmap item](roadmap.md#priority-reliability-checkpoints).

### A long time offline

Unreleased; **fixture-tested only**. `TestOfflineForAnyLengthThenRecoversOnce` runs one scenario at 20 minutes, 4 hours, 3 days and 3 weeks offline: a turn with two working children fails as the network drops, checks run only now and then with a long stretch of none, the machine wakes behind a captive portal, and the portal is cleared. At every length nothing is sent while offline or behind the portal, no hold is raised, and the first check after connectivity returns sends exactly one re-prompt, which tells the parent that two children are in error.

What each time-driven or counted mechanism does across such a gap:

| Mechanism | Across a long gap | Right? |
| --- | --- | --- |
| Retry-notice expiry (`bb.retry_signal_ttl`) | The notice is dead after 15 minutes and is not waited on again. | yes |
| `errors.backoff` | Has long passed; the first re-prompt after the outage is not delayed by it. | yes |
| `stall_after`, `min_wake_interval`, nudge count | Apply only to an active session. The stall clock does not reach back past the moment the session was seen active again after an error, or past an outage or a gap with no check, so a turn that works again after a long outage is not nudged for the time it could not work. Before, it was nudged at once (`no progress for 72h`). The nudge count restarts whenever the session is seen not active. | changed here |
| Re-prompt count | Kept across the gap: attempts made before it still count, and nothing is spent or held during it. If the cap was already spent before the outage, the hold is raised by the first check that finds the network up. | yes; see follow-ups |
| Handoff-deferral and rejected-brief counts | Kept until the error episode ends. Their backoff has long passed. | yes |
| Probe `every`, `ttl`, daily cap | No probe is run or recorded while offline. After a gap longer than `ttl` every fallback's result has expired: the first check back runs the due probes (as many as fit its time), `degraded` may be reported once, and a handoff is refused until enough have passed again. The daily cap is per calendar day. | acceptable; a handoff can lag by a check or two |
| Limit-wait episode and its report | A reset that passed while offline ends the wait; the session is then handled as an ordinary error. The report is not attempted while offline. | yes |
| Child-error episode, its report and the notice | Children that failed stay failed. The report waits for connectivity and is sent once; the parent is told on its first re-prompt or wake. | yes |
| `network.verify_after` | One provider probe and one report attempt per interval, for as long as the outage lasts. | yes |
| Provider limit reset recorded for switch-back | A reset that passed makes switch-back due; its probe waits for connectivity. | yes |
| Usage readings and credential expiry recorded by probes | Not examined in this change. They are refreshed only by probes, so they are as old as the last probe. | follow-up |
| Incident history retention | Identical consecutive checks are folded into one entry with a first time, a last time and a count, so the offline period stays one explainable span. | yes |
| Run graph staleness labels | Not examined in this change. | follow-up |
| `loop.limits.deadline` | An absolute time. One that passed while offline holds the loop at the next check with `loop deadline reached`, whatever the network is doing. This is the owner's choice and is not softened. | yes |
| `loop.limits.no_progress_timeout` | Counts only time in which progress was possible: see below. | changed here |

**`no_progress_timeout` across a gap.** It used to count all host-clock time, so a machine that slept past it was held on waking although nothing could have run. It now counts time in which supervised work was possible:

- Time between two checks of any kind counts up to an ordinary interval: `30m`, or three times `daemon.interval` if that is longer. A longer gap means no check ran at all (the host slept, the scheduler or daemon was stopped), and the rest of it is not counted. Checks that run while the loop is paused, held or disabled are checks, so that time counts as before. keepgoing does not know an external scheduler's cadence: if yours runs less often than every half hour, set `daemon.interval` to its interval, or part of every gap goes uncounted and the limit takes longer to reach.
- Time in which the connectivity check found the network down is not counted, and the limit does not hold while it is down.
- Everything else counts as before, including time outside the operating window while checks run.

The limit keeps its meaning as "this long with no Git change while the loop could work", and `status` shows the adjusted `changedAt`. If you relied on it as a wall-clock cutoff, use `loop.limits.deadline`, which is absolute.

## net-retry

```sh
keepgoing net-retry --max-wait 2h --attempts 6 -- git push origin cb/example
```

This reruns the command only when it failed while the network was down. When `--attempts` runs out while still offline, it returns the command's own exit code. A failure while the network is reachable is returned at once, so a rejected push or merge is never repeated blindly. Connectivity is an HTTP GET to `KEEPGOING_NET_PROBE_URL`, which defaults to `https://api.github.com/zen`. Set `KEEPGOING_NET_PROBE` to a shell command to check connectivity your own way.

### Saved headless resume IDs

For a configured Claude Code or Codex target, `status --json` includes `targetSession` with the saved `id` when recognized, its `state` (`saved`, `missing`, or `unavailable`), `source: "runner_session_file"`, `liveVerified: false`, and an explanatory `message`. Text status shows the same identity as saved history. After retargeting, status reads the selected target's record; missing or disabled targets do not fall back to another target's ID. BB and custom process targets omit `targetSession`.

The record is read without executing a provider or inspecting turn output. Missing metadata does not mean no turn is running, and saved metadata does not verify live identity or resumability. Unrecognized/non-UUID, oversized, or inaccessible records are withheld without modifying the runner's existing resume behavior. See [saved session diagnostics](diagnostics.md#saved-headless-sessions).

### Inspecting a running daemon

`status` and `doctor` query the built-in daemon's private local endpoint for this state directory. The `daemon` object identifies a responsive instance, its phase, next normal check time while waiting, dry-run mode, and build comparison against the inspecting CLI. This is separate from historical `runtime` fields. Agent activity and successful future checks are not established by an endpoint response. `doctor --offline` skips the query.

The endpoint starts with the daemon and is removed on clean exit; stale or unavailable endpoints remain unverified. Endpoint records are private state and must not be shared. A missing endpoint does not rule out an older daemon or external scheduler. See [live daemon diagnostics](diagnostics.md#live-daemon-diagnostics) for fields and limits.

## BB retry and stall handling

Available in v0.1.3 or newer; **fixture-tested only**. A BB `provider/error` with `willRetry: true` suppresses wakes, stall nudges, re-prompts and target moves while BB reports an active or errored thread and the notice is fresh (see [stale retry notices](#stale-retry-notices)). These checks record `provider is retrying the turn`; repeated checks do not spend the active turn's nudge allowance. Stop, pause, existing holds, child-input escalation and configured loop-wide deadline/send/no-progress limits still win. Scheduled fallback probes keep their existing policy.

Queued `client/turn/requested` input is not recovery. The retry summary clears on a new `turn/started`, or on matching `providerThreadId` metadata for an agent-message delta/start or successful turn completion. Failed/interrupted completion ends retry intent while retaining the error classification. Tool output, transcript prose, unknown statuses and mismatched/missing provider identity do not establish recovery. A failed or incomplete log scan suppresses prompts and target moves with `provider retry state unverified`, while preserving the active state so an explicit stop can still execute. An active provider error with missing retry intent is also unverified until later structured evidence resolves it. An idle thread with a complete scan follows normal idle rules.

When a tick observes retrying and a later tick observes verified active recovery on the same target, private `state.json` remembers a recovery timestamp across restarts. Ordinary stall detection waits up to one `progress.stall_after` interval from that observation; a real Git change can provide a newer baseline. Already-spent nudges, Git fingerprints/timestamps and independent loop-wide allowances are unchanged. Recorded `unchangedFor` continues to mean elapsed Git time, including the retry; it is not the shortened ordinary stall timer. Later genuine stalls can still nudge or exhaust the remaining nudge allowance. Dry runs do not consume recovery grace. A retry and recovery occurring entirely between observations do not create grace.

Old BB cursors rebuild their summary from structured events once, without replaying previously consumed child notices. Failed scans do not commit retry recovery. This needs readable BB log history; unavailable evidence remains unverified. The cursor stores metadata only, never message content. Recovery never clears a saved hold, and this change neither diagnoses an internet root cause nor adds a unified incident timeline or automatic alert replay. Offline fixtures cover repeated active retries, same-turn recovery, later stalls, restart, partial reads, queued input, migration and controls. No live outage was induced or installed loop changed for verification.

### Stale retry notices

Unreleased; **fixture-tested only**. A provider can announce a retry and then stop without saying so, for example when the network is lost. Two shapes were observed: a turn ends `failed`, further `willRetry: true` events arrive after that completion, and the errored thread goes silent; or a re-prompted turn logs a notice while BB still reports the thread `active`, and then logs nothing more. Honoring the last notice forever left both unattended, so a notice now counts as a live retry only while there is fresh evidence behind it. The rules use structured data alone: BB's thread status, each event's `createdAt`, turn events, rate-limit windows and the check time. The interval is `bb.retry_signal_ttl` (default `15m`, minimum `1m`).

- **Thread status `error`:** nothing is running, so only notices count. The notice is honored while the newest `willRetry: true` event is at most the interval old. After that the observation stops reporting a retry and the ordinary error handling applies: re-prompts spaced by `errors.backoff` up to `max_reprompts`, then an awaiting-user hold. An `unknown` error still never triggers a handoff.
- **Thread status `active`:** any newer provider or system event in the thread log (tool output, another notice, a started item) shows the turn is alive, and the notice keeps suppressing nudges exactly as before, for as long as such events continue. Requested input (`client/...` events: a person's message, a child notice, keepgoing's own nudge) is not thread activity and does not keep the notice alive. Once the log has been silent for the interval, the notice stops suppressing the ordinary stall handling described under [a silent active thread](#a-silent-active-thread). It never becomes a re-prompt or a handoff, and it earns no recovery grace.
- **A notice after the failed completion**, with no new turn started, is aged the same way. A failed or interrupted completion with no later notice ends retry intent at once, as before.
- **Rate limit with a structured reset:** for a notice that is itself classified `rate_limit`, the interval starts at the reset time, so the notice can stand until the reset and one interval after it. A notice of any other class never borrows the reset of an earlier blocked window. On an errored thread the notice is honored only while a retry is actually queued or the reset is within `errors.wait_if_reset_within`; with a far reset and nothing queued the ordinary limit rules decide (a handoff when a fallback is confirmed, a recorded limit wait otherwise), so a notice cannot keep the loop on a limited account past a healthy fallback. A rate-limit notice with no structured reset expires like any other.
- **A retry BB has queued:** BB's retry plugin schedules its retry of a failed turn as a queued message, and BB counts it in the thread's `queuedMessageCount`. An errored thread with anything queued is left alone under the existing rule (`messages already queued after error`), ahead of any handoff or re-prompt and whether or not a notice has expired. This is what protects a retry that is genuinely scheduled, including one for a rate limit with no reset keepgoing can see (a pooled route). It was established by reading the installed BB 0.48.1 server and plugin code, not by observing a live retry.

#### A silent active thread

An active thread whose retry notice has expired is handled by progress evidence, and what happens depends on whether the loop tracks Git.

*With `progress.repositories`.* The ordinary stall rules apply. At the defaults (`stall_after: 30m`, `min_wake_interval: 60m`, `max_nudges: 3`), for a re-prompt sent into a dead network at 0:00 whose turn logs one notice and then nothing:

| Time after the re-prompt | What keepgoing records |
| --- | --- |
| up to about 0:16 | `provider is retrying the turn` |
| 0:16 to 1:00 | `active`, `retry signal: expired`; `min_wake_interval` since the re-prompt has not passed |
| about 1:00, 2:00, 3:00 | nudge 1, 2 and 3 |
| about 4:00 | awaiting-user hold, `no progress after 3 nudges`, one report |

The nudges queue behind the stuck turn and are not delivered while it is stuck; here they act only as a countdown to the hold. Nothing is recovered automatically in those four hours.

*Without `progress.repositories`.* There is no stall evidence and no nudge is sent. Instead, once the thread has been observed active with an expired notice for `stall_after` plus `max_nudges` times `min_wake_interval` without a break (3h30m at the defaults, counted from the first check that saw the notice expired), the loop is held for a person with reason `provider retry went silent on an active session` and one report. Any check that sees the thread retrying, logging provider events, or not active starts that period over. This bound applies only to a thread with an expired retry notice; an untracked active thread with no notice is still never nudged or held.

**To recover sooner**, do not wait for the hold: `keepgoing stop` asks the next check to stop the stuck turn, and once the thread is idle `keepgoing resume` clears the stop so the following check wakes it. Shorter `min_wake_interval` and `stall_after` shorten both timelines for every stall, not only this one.

**Why the interval does not cut off a slow, genuine retry.** It bounds silence, not the length of a retry: each failed attempt logs a newer notice, which restarts it. The notices come from the provider. BB's retry plugin is a separate mechanism that acts only after a turn has failed: in BB 0.48.1 it retries an unavailable provider up to four times, waiting one to two minutes before the first and doubling each time (up to eight to sixteen minutes before the last), and a subscription limit shortly after its reset. Each of those waits is a queued message, so the queued rule above covers it however long it is, and the interval does not need to. If the interval is still too short for a provider's own retries, the cost is bounded: on an errored thread at most `max_reprompts` extra prompts and then a hold, on an active thread at most `max_nudges` queued nudges or the hold above. Raise `retry_signal_ttl` for a provider known to pause longer between attempts. None of this has been checked against a live slow retry.

**Worked example and the default.** In one observed outage a provider logged ten notices over about 145 seconds (gaps of 1 to 37 seconds), gave up and failed the turn; a second turn failed the same way and ten more notices followed its failed completion, the last at 00:28:30, then nothing. With checks every ten minutes from 00:30 and the default interval, the notice expires at 00:43:30: the 00:30 and 00:40 checks record `provider is retrying the turn` and the 00:50 check sends the first re-prompt (fixture `TestIncidentRetrySeriesAfterFailedCompletion`). Before this change every check waited. Raising the provider's attempt count lengthens the series, not the gaps, so it does not affect expiry. Fifteen minutes is far longer than the 37-second gaps seen here. It was kept when nothing checked connectivity before sending, because a re-prompt sent while the network is still down spends one of `max_reprompts`. With the [network check](#network-outages) on, a shorter interval no longer risks that. Set `retry_signal_ttl` as low as `1m` to re-prompt sooner.

**Clocks.** Event times are bounded by what keepgoing knows locally. An event is never treated as later than the scan that read it, so a source clock running ahead cannot keep a notice fresh. An event that was not in the previous complete scan is never treated as older than that scan, so a BB host whose clock runs behind cannot expire a notice early; on the first scan of a log there is no earlier scan and old events keep their own time. A missing or malformed `createdAt` uses the scan time. A summary saved by an older build is aged from the first scan after the upgrade. A local clock that moves backwards delays expiry rather than causing it. The adapter uses the check's own time, including `tick --now`.

**Where it shows.** `status` prints `retry signal: expired` for the last check (`state.lastCheck.retrySignal` in `status --json`, `retrySignal` in tick output), and `timeline` and `incidents.json` record the observation's retry state as `expired`. A build older than this change that rewrites the journal records that value as `unavailable`.

## BB child awareness

For a BB target, each observation discovers existing direct children with `bb thread list --parent-thread <id> --include-hidden --json`. It then verifies each child's ID, `parentThreadId`, and status using `bb thread show <child-id> --json`. A matching child with status `active` prevents parent wakes, stall nudges, error re-prompts, handoffs, and switch-back. The recorded reason is `child work running`; the parent's own status is preserved. This does not start, stop, message, or re-parent any child.

A child that has finished and is now idle no longer blocks its parent. An idle parent is eligible for its ordinary wake on the next supervisor check, subject to controls and pending interactions. Waiting for children does not spend the nudge or error re-prompt budget. An explicit stop still applies to the supervised parent, not its children; pause, completion, disabled, and awaiting-user holds remain authoritative.

Discovery requires a complete parent-filtered inventory, including hidden children. The inspected BB 0.48.1 CLI/server implementation passes the parent filter to the database and does not apply a default pagination limit. This complete-list contract is required; versions that silently truncate this query are not supported or verified. CLIs with unsupported flags, incompatible response shapes, failed queries, missing relationship fields, or unknown child states produce `child status unverified` and no parent prompt or target move. A scan has one adapter timeout budget and accepts at most 200 listed children; larger inventories remain unverified. Duplicate IDs are inspected once. Confirmed missing, archived, deleted, or re-parented children do not count as active. This is a snapshot; a child starting after observation is seen on a subsequent check.

Offline fixtures verify discovery, identity matching, hidden children, conservative failures, controls, and repeated checks across the nudge limit, followed by an immediate next-check wake once the child finishes. No live child turn or model request was started for these tests. Child permission/question escalation is described below; ownership enforcement remains planned. Direct children only are inspected; idle children with active grandchildren do not establish direct active child work.

### Early checks after child outcomes

The built-in daemon also polls the active BB target's new log events at `daemon.control_poll`. A structured `client/turn/requested` system notice with `child-completed`, `child-failed`, `child-interrupted`, or `child-outcome-batch` metadata can bring the next check forward. The notice must contain matching, valid child IDs and terminal statuses in `systemMessageSubject.outcomes`; message text and child output are ignored. This schema was inspected in BB 0.48.1 and exercised with offline fixtures. Older schemas or absent notifications cannot trigger early checks. keepgoing does not change BB's child notification settings.

The parent is observed again before scheduling an early wake check. It must be idle, with no queued messages, background workers, pending parent interaction, active direct children, or incomplete child discovery. BB may already have activated the parent while delivering the notice; that needs no additional wake. A later parent turn start supersedes an earlier notice, and retry requests do not count as new outcomes. Normal tick rules, including controls, windows, yield checks, and current target state, still apply before any prompt is sent.

The existing per-target log cursor consumes each event sequence once, including across restarts, and must save successfully before an early check is authorized. Ordinary observations consume the same events. A crash, failed observation, or exhausted control-poll budget after consumption can lose that early opportunity; it is not retried from the notification. The normal interval remains the backstop. Restarting the daemon still performs its ordinary initial tick; notification deduplication does not disable ordinary idle wakes. Removing runtime state removes the cursor too.

Each notification poll shares the tick lock with normal ticks and retargets and has a total timeout of one `control_poll`. Held loops and loops outside their window do not query providers; lock contention, failed queries, and timeouts do not authorize an early check. With an available CLI and lock, a new eligible notice advances the next check within a control cycle plus query time. Host downtime and slow/unavailable commands have no delivery guarantee. Status continues to show the next **normal** check time; an eligible notice may bring it forward.

This acceleration applies to `keepgoing daemon` only. Cron, BB automations, and other external schedulers invoke `tick` on their own cadence; their timers are not changed. Fixtures cover completed/failed/interrupted notices, batching, deduplication, saved-cursor restart, control holds, other active children, pending parent interactions, lock contention, and bounded slow queries. No live child turns or paid calls were started for this verification. Confirmed pending child input discovered during an early completion check advances an escalation tick instead of a wake; the same hold rules apply.

### Child permission prompts and questions

After verifying each direct child's identity and parent relationship, keepgoing runs `bb thread interactions list <child-id> --json`. An interaction with that child's `threadId` and `status: pending` means human input is needed. Permission prompts, questions, and other pending interaction forms all count; their text, commands, answers, and payloads are never used or included in reports. Only verified child IDs are retained in the reason and report metadata.

Confirmed child input takes precedence over idle wakes, active siblings, stall handling, recovery, and switch-back. The next tick records `await_user`, persists the hold, and emits the configured `await_user` report with `children` and `pendingInteraction` metadata. It does this before scheduled probes, health checks, or repository fingerprinting. Stop and existing control holds still win. No message is sent to the parent or child, no interaction is answered, and no child is stopped or re-parented. An explicit stop continues to apply to the supervised parent only.

The hold survives supervisor restarts and remains after the child interaction is answered elsewhere. Inspect the named child, resolve its interaction, then explicitly `keepgoing resume -c /path/to/keepgoing.yaml`. Resuming while input is still pending creates the hold again on the next tick. Dry runs show the proposed escalation without changing control or sending reports. A failed webhook delivery does not remove the persisted hold; the reason remains available locally.

Child interaction queries share the existing child-discovery timeout and accept at most 500 records per child. Failed queries, missing/mismatched identities, missing or unknown statuses, and incompatible JSON make child status unverified and suppress parent wakes. `resolving` also suppresses wakes while a response is in flight, without asking the person to answer again. `resolved` and `interrupted` are terminal. If one verified child is pending while another child's status is unknown, the confirmed pending child still triggers a hold. Archived, deleted, missing, or re-parented children are excluded. Discovery is a snapshot of direct children, not their descendants.

Offline fixtures cover permissions/questions, identity verification, stale relationships, terminal/resolving/unknown states, cancellation, control precedence, one report per hold, restart persistence, explicit resume, privacy, and a pending interaction concurrent with a completion wake signal. The field/status contract was inspected in BB 0.48.1; live child permission and question flows remain unverified. Pending child input is checked on ordinary ticks and during completion-driven early observations, not by a separate continuous interaction watcher.

### Children in error

A direct child whose status is `error` is not running work. It does not keep the parent classified as `child work running`, and it does not make discovery unverified. Before this unreleased change nothing else happened: an idle parent got its ordinary wake with the ordinary prompt, and no tick output, `status`, `timeline` or message mentioned that a child had failed. After an outage that failed every child, the parent could carry on without knowing, and the children stayed failed until a person retried each one.

Unreleased source tells the session that owns the children, and records the count:

- Each BB check lists the verified direct children in error, from the status field alone. keepgoing never reads a child's log or messages, and never sends to, stops, retries or restarts a child.
- When the parent is woken, or re-prompted after an error of its own, and a child has newly entered error, that message ends with a fixed notice: a `[keepgoing:child-errors count=N]` line, then one sentence saying N direct child threads were in error, that keepgoing has not touched them, and that the session should decide whether to retry or replace that work. The notice holds the count only: no child ID, title, task text or error detail.
- Only growth is announced. keepgoing keeps a short hash of each child it has announced, never the IDs. A child not in that record triggers the notice; a set that only shrinks does not, so retrying one child, or archiving or deleting an errored one, says nothing.
- A recovered child is forgotten slowly. A child stays in the record until it has been out of error for `progress.min_wake_interval` (default `60m`). Failing again before that is the same failure and says nothing, so a child that flaps between error and a provider retry is announced once, not on every re-failure, and does not cause a wake each time. A child that has been out of error that long and fails again is announced again. This is the same rule, in the same code, as the owner report below; the two keep separate records only because they are told at different times.
- N is every child in error at that check, including ones announced earlier that the parent may have chosen to abandon. keepgoing cannot tell an abandoned child from one still waiting for a decision.
- With one exception, the notice adds no wake. It rides on a wake the idle parent would have received anyway, so the idle-wake rules, `max_sends`, the deadline, the no-progress timeout and every hold apply unchanged. Stop, pause, awaiting-user and completed send nothing. A child with a pending interaction still raises its hold first.
- The exception is the one case where the notice causes a wake that would not otherwise happen. When a child newly fails while at least one other is still `active`, the parent would be left alone as `child work running` until the last one finished, which can be hours. Instead an idle parent is woken once, with the reason `children in error while other children run`, and is then left alone again. That message is the notice alone, with no task prompt and no goal context, because the parent is waiting on purpose: its first line is `[keepgoing:child-errors count=N running=M]`, and it says M other children are still running and that it is not an instruction to continue the task. This wake is counted against `max_sends`, obeys every hold and limit, needs the same conditions as an idle wake (idle, inside the window, nothing queued or running in the background, complete discovery, no provider retry pending), and is sent no sooner than `min_wake_interval` after the previous send.
- A send the adapter reports as failed or unsupported does not count as announced, and may still have been delivered. After `errors.max_reprompts` such attempts for one announcement, keepgoing gives it up: later wakes go without the notice and the extra wake stops. `status` prints a `child notice:` line and the tick output sets `childErrorNoticeStopped`. Another child failing starts a new announcement with its own attempts, and `keepgoing resume` lets a given-up one be tried again.
- The notice is not added to stall nudges, error re-prompts or handoff briefs. A parent that is itself active or in error is not woken for errored children; it hears on its next wake.
- Incomplete discovery withholds the wake. It forgets nothing about a child it does not show, and a child it shows with a known status other than `error` counts as out of error. A count taken from an incomplete listing is a lower bound: the tick output sets `erroredChildrenPartial`, and `status` and `timeline` print "at least N (child discovery was incomplete)".

The tick output has `erroredChildren` (the count) and `childErrorNote` (true when this check's wake carried the notice). `status` prints a `children:` line with the count from the last recorded check and exposes `state.lastCheck.erroredChildren`; like the rest of that record it is historical and makes no provider call. `timeline` shows the count on each check that saw errored children, and a change in the count is never folded into the previous check. The incident journal stores the count only, under an optional field. Children belong to one thread, so a handoff, a switch-back, `retarget` and BB transfer all forget what was announced.

keepgoing does not decide whether failed child work should be redone. Whether it is depends on the parent agent acting on the notice. No hold is raised for errored children alone. The owner is told separately, by the `child_error` report described next.

#### Reporting children in error to the owner

Unreleased source; **offline fixtures only**. The notice above reaches the parent session, and only when it is next woken. The owner had no signal: the count was in `status`, `timeline` and tick output for anyone who looked. keepgoing now sends a `child_error` report when child threads of the supervised thread are first seen in error.

- **When.** On the check that first sees a child in error, whatever the parent is doing: idle, working, in error, waiting on a person, or waiting on other children. It does not wait for the parent to be woken.
- **Only growth.** One rule decides what counts as a new failure for both this report and the parent notice, using the same per-child hashes: a report is sent when a child newly fails, never because the set shrank. The two records are separate, because the report is sent on first sight and the notice on the next wake, so one can be ahead of the other.
- **A recovered child is forgotten slowly.** A child covered by a report is forgotten only after it has been out of error for `progress.min_wake_interval` (default `60m`). Until then, failing again is the same failure and sends nothing. A child that flaps between error and a provider retry is therefore reported once, not on every flap, and does not use up the reports other children may need. A child that stays recovered for that long and fails later is reported again.
- **Capped per episode.** An episode starts with the first report and ends when every covered child has been forgotten. It sends at most three reports. Children that fail after the third are recorded without a report, and `status` says so. A later failure after the episode has ended starts a new one. Children that keep failing one after another, each before the previous has been forgotten, are one long episode: after its three reports nothing more is sent until they have all stayed recovered, so check `status` for a loop in that state.
- **It does not hold the loop or change a decision.** The check acts exactly as it would have without the report. Nothing needs to be resumed.
- **At most once, never retried.** The episode is saved to `state.json` before the request is made. A report makes a single request without following redirects. If the supervisor is killed mid-request, or the request fails, that report is lost: it is not sent again, the check only carries a warning, and the failure stays visible in `status` and the notification records. If `state.json` cannot be written, nothing is sent.
- **Incomplete listings.** A report is still sent when the child listing was incomplete, marked as such, with both counts as lower bounds. An incomplete listing proves nothing about a child it does not show, so that child is kept. A child the listing does show with a known status other than `error` is treated as recovered, so an episode can still end when the listing never completes, for example because one child has a status this build does not know. Two cases remain. A covered child that a never-complete listing stops showing at all (it was archived or deleted) is kept until a complete listing or a target change; the record is bounded at 200 children, dropping those seen in error longest ago. And when a thread has more than 200 children the adapter inspects none of them, so no child is seen in error and no report is sent.
- **A parent that is gone.** An archived or deleted parent is not scanned for children. That is not evidence of recovery, so the episode is kept; if the thread comes back with the same children in error, nothing is sent again.
- **Holds win.** A paused, disabled, completed, awaiting-user or stopped loop does not observe its target for this purpose, so it neither sends this report nor forgets one already recorded. A failure that happens during such a hold is reported by the first check that observes the target after the hold is cleared, if the child is still in error then. A loop that stays completed or stopped never sends it. Dry runs send and record nothing. A check that raises a hold for a child waiting on a person sends `await_user` and not this report. After a handoff or switch-back in the same check, nothing is reported for the thread just left.
- **Subscription.** `child_error` is in the default `report.events`, because a parent left behind failed children is a loop that has silently stopped making progress. For existing loops this means: a loop whose `report` section has no `events` list starts receiving this event after upgrading; a loop with an explicit `events` list receives nothing new until it names `child_error` or `all`; a loop with no `report` section sends nothing, as before. With no subscription the failure is still recorded and shown as not sent, and subscribing while children are still in error sends the report on the next check. To receive nothing, give `report.events` an explicit list without `child_error`.
- **Where it shows.** `status` prints a `child errors:` block with the counts seen by the detecting check and whether the report was requested, failed, not subscribed or capped (`childErrorReport` in JSON). It says when the loop has no report section, and when earlier reports of the same episode were sent before the event was unsubscribed. The delivery appears as a `child_error` notification.
- **Older builds.** Once a `child_error` delivery is recorded, v0.1.5 and earlier refuse `deliveries.json` because of the unknown event label. An older build that rewrites `state.json` also drops the episode record, so returning to a newer build while the same children are still in error sends one duplicate report. Do not run an older build against the same state directory.

The payload carries the loop name, the number of children in error, the number still running, whether the listing was complete, and the report's number within the episode. It carries no child ID, title, task text or transcript.

Evidence is offline: adapter fixtures for listing errored children and for asking a child nothing but its status and pending interactions; a decision table over every enumerated input, with and without enough time since the last send, showing errored children change the chosen action only in the one case above and the notice follows only a wake; tables for growth-only announcement; supervisor fixtures for a shrinking set, a child that fails again, the notice-only wake and its spacing, the cap on rejected sends, blocked wakes, the send limit, dry runs, restart, partial scans and target moves; and a CLI journey against a fake `bb`. No live child thread failed or was retried for these tests.

## One owner per loop

When `keepgoing enable` runs inside BB, it records `BB_THREAD_ID` in `control.ownership`, with a generation number. The owner is the thread operating the loop; it is separate from the configured agent target. Claiming or transferring ownership never retargets the loop or changes a BB parent/child relationship. `status` shows the owner and latest handoff, and `status --json` includes the ownership metadata.

A different BB thread is refused by `validate`, `enable`, `start`, and `daemon`, as well as direct ticks, resume, retarget, and executing manual probes. Read-only status/doctor and probe previews remain available. Disable, pause, stop, complete, and await-user remain available from any thread. Missing `BB_THREAD_ID` identifies an external scheduler or ordinary terminal: commands use the existing claim without changing it. Legacy loops without ownership metadata continue working; their first enable from a BB thread records the claim. This relies on cooperative callers and private runtime files, not an authentication boundary.

From the BB thread that should become the new owner:

```sh
keepgoing validate -c /path/to/keepgoing.yaml --take-over
keepgoing enable -c /path/to/keepgoing.yaml --take-over
keepgoing status -c /path/to/keepgoing.yaml
```

Validation only previews eligibility; it never claims ownership or enables a loop. Validation against an existing runtime directory may create its private control lock; it creates no missing runtime directory. When replacing an existing owner, `enable --take-over` transfers only the claim, preserving disabled, pause, stop, awaiting-user, completion, and the existing note unless a new note is supplied. If the loop was disabled, a subsequent `enable` from the new owner is required; use `resume` separately to clear other holds. First claims and same-owner enables set `enabled` as usual. The previous owner is recorded as `handed_off` with a timestamp; only the latest handoff is retained. A same-owner enable leaves the generation unchanged. Takeover requires a valid `BB_THREAD_ID` and is never inferred from inactivity or a failed status lookup. No owner-status provider query is made: even an idle, archived, or missing owner retains its claim until an explicit transfer. Malformed ownership metadata is refused rather than silently discarded.

A running daemon is bound to its starting ownership generation. After a successful transfer it exits before further provider work, normally at its next control check, and releases its daemon lock. This also applies to a daemon started by an identity-free scheduler and to a previously unowned daemon when the loop is first claimed. `enable --take-over` does not start a replacement daemon; after the old daemon exits, start `daemon` from the new owner or scheduler. `daemon --take-over` can claim and start when no daemon already holds the process lock, preserving disabled and other held state. It cannot evict an existing daemon. `daemon --take-over --dry-run` is rejected; use validation to preview a transfer.

Ownership and transfers use the runtime state directory, so multiple config paths or directory symlinks sharing that directory share the same owner and locks. A `control.json` file symlink is refused: use the original state directory instead. Distinct runtime directories are distinct loops; moving a config to another state directory is not an ownership transfer. Do not alias individual runtime files with hard links or mix old binaries that discard ownership metadata. Removing or replacing runtime state also removes its ownership history and safeguards.

Claims and transfers serialize with ticks and early polls. If a tick is in flight, takeover exits 75 and must be retried; it never interrupts or rolls back that tick's work. Control updates have a separate short lock, preserving concurrent pause/stop requests without waiting for long provider calls. Control-lock contention also exits 75. No thread is created, stopped, re-parented, or messaged by ownership operations.

Offline fixtures verify conflicting identities, preview and explicit takeover, shared-state config paths, simultaneous claiming processes, directory aliases, control-file symlink refusal, hold preservation, scheduler access, and old-daemon exit including a transfer back to its original owner. Live transfers between real threads remain unverified; the tests make no live model calls.


## Owned headless cancellation

New headless Claude Code and Codex turns run through a detached keepgoing wrapper on macOS, Linux and Windows. Each turn gets a random private `stopKey` in `run.json`; it is passed to the wrapper only and removed from the agent environment. The saved PID remains advisory liveness metadata and never authorizes a signal.

A stop writes `stop-request.json` atomically with that key and a fresh request ID, then waits up to three seconds for matching `stop-ack.json`. The wrapper checks requests every 100 milliseconds and accepts only its own key. Old requests cannot cancel a new turn; old acknowledgements cannot confirm a new request. A timeout reports an unconfirmed stop and leaves the request pending. A matching receipt means the wrapper accepted cancellation, not proof that the operating system has completed termination. Later observations still determine liveness.

The live Unix wrapper verifies that it leads its own process group, then signals that group itself. While it is alive, its own PID/group cannot be recycled. Windows uses an unnamed native job containing the wrapper and descendants, terminated through the wrapper's retained job handle; no `taskkill` PID lookup occurs. Job setup failure prevents the agent from starting. Windows job membership/termination follows the [native job-object contract](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects).

This is a cooperative channel in private runtime storage, not a security boundary against writers who can replace those files. Never share the stop key or raw runner records in diagnostics. A crashed wrapper cannot acknowledge a new request, even if its old PID belongs to another process. Descendants that outlive an abrupt wrapper death, or detach from a Unix process group, are not recovered or tracked by this change. Inspect them manually; do not signal PIDs merely because a stale record names them.

Legacy turns without the key remain observable but automatic stop refuses them. Let an existing turn finish before upgrading, or inspect it manually using native process identity evidence. Do not use an older binary to stop new turns: old versions ignore the key and retain the unsafe saved-PID path. Supervisor restart does not change an existing wrapper/key. This implementation is covered by local subprocess and synthetic stale-record tests; it does not establish live provider resume/cancellation or installed-service acceptance.
