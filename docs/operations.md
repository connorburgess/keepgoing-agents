# Operations

## Running it

There are two ways to run keepgoing. Pick one per loop.

- **`keepgoing tick` from a scheduler.** Each run makes one pass and exits. Ticks never overlap, because a tick that finds the lock held exits 75. See [examples/schedulers](../examples/schedulers) for launchd, cron, a BB automation script, and a GitHub Actions job.
- **`keepgoing daemon`.** This runs a pass every `daemon.interval`. Between passes it checks `control.json` every `daemon.control_poll`, so changes such as `keepgoing stop`, `pause`, or `complete` are acted on within seconds. The daemon reloads `keepgoing.yaml` and `fallbacks.yaml` before each pass and keeps the last good config when an edit is broken. Changing `state_dir` requires a restart: a running daemon retains the previous configuration so its lock and diagnostic endpoint continue to describe the state it supervises. SIGINT or SIGTERM ends it at once and cancels any command in flight. A control change written while a pass is running starts the next pass immediately after it. Only one daemon runs per state directory, and a second one exits 75. `--dry-run` applies to every pass. See the systemd unit in the examples.

Ending the daemon does not stop the agent. To stop the agent, run `keepgoing stop`.

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

Background mode survives the launching terminal closing. It does not install a user service, restart after crashes or reboots, or guarantee survival across OS logout/session cleanup. It cannot execute while the host sleeps or is off. After wake, the next available check applies current controls; there is no catch-up queue of missed ticks. Network errors retain the normal structured-error policy. User-service installation and lifecycle soak testing remain separate roadmap work. Offline fixtures exercise detached startup, parent exit, duplicate refusal, status, holds, and stop with a local process adapter; no live model call or multi-account handoff is part of these checks.

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

These describe a supervisor decision and its outcome. `succeeded` means the supervisor operation returned successfully, not that the agent turn or task finished. A failed send is not a successful action; a dry-run wake is a plan that was not executed. An action of `none` with outcome `succeeded` means a no-action check completed successfully. Raw provider errors and prompts are not copied into this record. Failures before a record can be saved leave the previous history intact. Older state files still load, with detailed history unavailable until a new tick records it.

`runtime.state`, `runtime.reason`, and `runtime.source` distinguish current control/configuration from history. Stop takes precedence, followed by disabled, paused, awaiting-user, and completed holds. Without a hold, an invalid configured target or closed operating window is shown before interpreting history. Historical states use names such as `recorded_active` and `recorded_idle`; they do not claim the agent is currently running. A structured pending interaction is `recorded_awaiting_input`, not inferred from a reason string. A failed check is `check_failed`; a dry run is `dry_run`. A target change or an unobserved target after handoff is not attributed the previous target's state.

`runtime.freshness` is `never`, `recent`, `stale`, or `future`. The default stale threshold is 30 minutes; use `--stale-after` above your scheduler's interval. Stale, future-dated, or incomplete history cannot establish current activity. Current control holds remain visible even with stale history. For a responding built-in daemon, `runtime.nextCheck` reports the next normal check time or an in-progress check; otherwise it remains unknown. A configured daemon interval does not establish an external scheduler's schedule.

`runtime.binaryComparison` compares the last recorded tick's build label with this status process: `match`, `different`, or `unknown`. Empty and `dev` labels are unknown. A different label can indicate a stale scheduler binary, but does not prove which build is newer or currently running. Doctor also flags a different recorded build. Manual and dry-run ticks can replace the latest history, so this is not a running-process inventory.

Existing `control.note` output is retained for compatibility; status is a private operational report. Doctor continues to withhold control notes, task text, environment values, and raw errors. This feature is implemented on `main`, not in v0.1.1, and is covered by offline fixtures for control precedence, failures, dry runs, handoffs, version comparisons, and legacy state.

## Completing a task

When the authorized work is finished and verified, the agent or operator runs:

```sh
keepgoing complete -c keepgoing.yaml --note "Example task verified"
```

This sets `control.completed` in `control.json` and records a control event in `runs.jsonl`. Status shows `completed` unless a higher-priority disabled, stop, pause, or awaiting-user hold is also set; JSON always exposes the completion flag. No model output or transcript text is inspected to decide completion. There is no completion webhook event yet.

On the next check, a completed loop skips observation, yield checks, progress reads, scheduled probes, wakes, retries, handoffs, and switch-back. The hold survives process restarts. A pass already in flight can finish before noticing the change. Completion does not terminate the current agent turn or cancel messages already queued by another caller; use `stop` when the running turn must end. Stop still wins even when completion is set.

To reopen the task, or continue with a revised `prompt` or `prompt_file`, run `keepgoing resume`. Resume explicitly clears completion along with pause, stop, and awaiting-user, and clears the old note. It preserves the current target and session; it does not create a fresh conversation. It also does not enable a disabled loop: use `enable` separately in that case. `enable` alone never clears completion. Manual commands such as `fallbacks check` remain explicit operations and can still run while a task is completed.

Use `await-user` when unfinished work needs a person, and `complete` when all authorized work is done. If the task is an entire roadmap, completing one item is not completion of the task.

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

Events: `wake`, `nudge`, `reprompt`, `handoff`, `switch_back`, `degraded`, `recovered`, `await_user`, `stop`, `error`, `probe_failed`, `test`, or `all`. `keepgoing report test` sends a `test` event, whatever events are subscribed.

## Situations that need a person

- **The agent is waiting on a person.** A pending interaction is reported once as `await_user`, and nothing is sent until it is answered. `keepgoing await-user --note` reports at once and holds the loop until `keepgoing resume`.
- **The session stays stalled.** After `progress.max_nudges` nudges without visible progress, the loop is marked awaiting user and reports `await_user`.
- **The target is gone.** An archived, deleted, or missing target marks the loop awaiting user and reports `await_user`.
- **A headless turn was stopped.** Stopping a headless turn ends its process without an exit code, so it reads as an `unknown` error. After `keepgoing resume`, it is re-prompted under the ordinary error rules.
- **Only `resume` clears a stop.** `enable` does not.

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

## BB child awareness

For a BB target, each observation discovers existing direct children with `bb thread list --parent-thread <id> --include-hidden --json`. It then verifies each child's ID, `parentThreadId`, and status using `bb thread show <child-id> --json`. A matching child with status `active` prevents parent wakes, stall nudges, error re-prompts, handoffs, and switch-back. The recorded reason is `child work running`; the parent's own status is preserved. This does not start, stop, message, or re-parent any child.

A child that has finished and is now idle no longer blocks its parent. An idle parent is eligible for its ordinary wake on the next supervisor check, subject to controls and pending interactions. Waiting for children does not spend the nudge or error re-prompt budget. An explicit stop still applies to the supervised parent, not its children; pause, completion, disabled, and awaiting-user holds remain authoritative.

Discovery requires a complete parent-filtered inventory, including hidden children. The inspected BB 0.48.1 CLI/server implementation passes the parent filter to the database and does not apply a default pagination limit. This complete-list contract is required; versions that silently truncate this query are not supported or verified. CLIs with unsupported flags, incompatible response shapes, failed queries, missing relationship fields, or unknown child states produce `child status unverified` and no parent prompt or target move. A scan has one adapter timeout budget and accepts at most 200 listed children; larger inventories remain unverified. Duplicate IDs are inspected once. Confirmed missing, archived, deleted, or re-parented children do not count as active. This is a snapshot; a child starting after observation is seen on a subsequent check.

Offline fixtures verify discovery, identity matching, hidden children, conservative failures, controls, and repeated checks across the nudge limit, followed by an immediate next-check wake once the child finishes. No live child turn or model request was started for these tests. Child permission/question escalation is described below; ownership enforcement remains planned. Direct children only are inspected; idle children with active grandchildren do not establish direct active child work.

### Early checks after child outcomes

The built-in daemon also polls the active BB target's new log events at `daemon.control_poll`. A structured `client/turn/requested` system notice with `child-completed`, `child-failed`, `child-interrupted`, or `child-outcome-batch` metadata can bring the next check forward. The notice must contain matching, valid child IDs and terminal statuses in `systemMessageSubject.outcomes`; message text and child output are ignored. This schema was inspected in BB 0.48.1 and exercised with offline fixtures. Older schemas or absent notifications cannot trigger early checks. keepgoing does not change BB's child notification settings.

The parent is observed again before scheduling an early wake check. It must be idle, with no queued messages, background workers, pending parent interaction, active direct children, or incomplete child discovery. BB may already have activated the parent while delivering the notice; that needs no additional wake. A later parent turn start supersedes an earlier notice, and retry requests do not count as new outcomes. Normal tick rules, including controls, windows, yield checks, and current target state, still apply before any prompt is sent.

The existing per-target log cursor consumes each event sequence once, including across restarts, and must save successfully before an early check is authorized. Ordinary observations consume the same events. A crash or failed observation after consumption can lose that early opportunity; it is not retried from the notification. The normal interval remains the backstop. Restarting the daemon still performs its ordinary initial tick; notification deduplication does not disable ordinary idle wakes. Removing runtime state removes the cursor too.

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
