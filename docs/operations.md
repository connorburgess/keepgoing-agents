# Operations

## Running it

There are two ways to run keepgoing. Pick one per loop.

- **`keepgoing tick` from a scheduler.** Each run makes one pass and exits. Ticks never overlap, because a tick that finds the lock held exits 75. See [examples/schedulers](../examples/schedulers) for launchd, cron, a BB automation script, and a GitHub Actions job.
- **`keepgoing daemon`.** This runs a pass every `daemon.interval`. Between passes it checks `control.json` every `daemon.control_poll`, so changes such as `keepgoing stop`, `pause`, or `complete` request a check within seconds. The daemon reloads `keepgoing.yaml` and `fallbacks.yaml` before each pass and retains the last timing and diagnostic context when an edit is broken. Provider work is withheld while the loaded source snapshot cannot be verified. Changing `state_dir` requires a restart: a running daemon keeps its original lock and diagnostic endpoint and withholds provider work until the original configuration is repaired or the daemon is restarted with a valid configuration. SIGINT or SIGTERM ends it at once and cancels any command in flight. A control change written while a pass is running starts the next pass immediately after it. Only one daemon runs per state directory, and a second one exits 75. `--dry-run` applies to every pass. See the systemd unit in the examples.

Ending the daemon does not stop the agent. To stop the agent, run `keepgoing stop`.

### Control-change detection

Unreleased source includes a content fingerprint alongside the regular control file's modification time and size when checking for changes between daemon passes. This catches same-size edits whose filesystem timestamp was preserved or did not advance. Published v0.1.5 and earlier use metadata alone and can wait until the normal interval in that case. Scheduler-driven ticks are unaffected: each invocation already reloads controls.

Unreleased Windows source opens control loads and content-poll snapshots with read, write and delete sharing, and the control/goal writer uses `FileRenameInfoEx` with replacement and POSIX semantics. Both are necessary: the legacy `MoveFileEx` call can reject an open destination even when that reader permits deletion. This write path requires Windows 10 version 1709 or newer and a filesystem supporting POSIX-style rename; unsupported operations return an error without falling back to legacy rename or removing the destination first. See Microsoft’s [rename operation](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information) and [version requirement](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/wdm/ne-wdm-_file_information_class). The handle is read-only; delete sharing permits another handle to replace the file without granting the reader delete access. An opened snapshot continues reading its original file after a cooperating writer atomically replaces it, and the next poll/load observes the replacement. A reader opened without delete sharing can still block a save, including other runtime-file readers; only control snapshot reads use the new sharing mode. The writer opens only its staging file with delete access, closes that handle on every return, and never asks to ignore read-only attributes. This is not a retry mechanism, a guarantee that all writes succeed, or isolation from external in-place edits.

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
| 1 | failed: the target could not be observed, an action failed, or `fallbacks check` found redundancy unmet |
| 2 | usage or config error |
| 3 | `ci-wait`: no run appeared for the commit |
| 75 | skipped: the lock is held, a yield check reported other work, or `net-retry` waited longer than `--max-wait` for the network |
| 124 | `ci-wait`: timed out |

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

The ledger is independent of diagnostic history, target identity and per-turn retry/nudge counts. Repeated idle turns, errors, handoffs, switch-back, restart and resume do not reset it. Elapsed time includes downtime, disabled periods, operating-window closures and pauses; it is not active execution time. Reordering or duplicating repository entries does not alter the source identity. Changing the source list rebases the fingerprint while preserving elapsed time, so a configuration edit alone is not progress. Dry runs preview the result without changing this ledger or controls. Child-completion polling skips provider queries when the saved timer is due, unavailable or lacks a baseline, leaving fresh Git verification to the next ordinary tick.

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

Events: `wake`, `nudge`, `reprompt`, `handoff`, `switch_back`, `degraded`, `recovered`, `await_user`, `limit_wait` (unreleased source), `complete` (v0.1.3+), `stop`, `error`, `probe_failed`, `test`, or `all`. `complete` and `limit_wait` are included in the default event list on `main`; an explicit event list must include them or `all`. Both make a single attempt without following redirects. Other events retain their existing one-retry policy. `keepgoing report test` sends a `test` event, whatever events are subscribed.

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
