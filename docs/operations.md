# Operations

## Running it

There are two ways to run keepgoing. Pick one per loop.

- **`keepgoing tick` from a scheduler.** Each run makes one pass and exits. Ticks never overlap, because a tick that finds the lock held exits 75. See [examples/schedulers](../examples/schedulers) for launchd, cron, a BB automation script, and a GitHub Actions job.
- **`keepgoing daemon`.** This runs a pass every `daemon.interval`. Between passes it checks `control.json` every `daemon.control_poll`, so changes such as `keepgoing stop`, `pause`, or `complete` are acted on within seconds. The daemon reloads `keepgoing.yaml` and `fallbacks.yaml` before each pass and keeps the last good config when an edit is broken. Changing `state_dir` requires a restart: a running daemon retains the previous configuration so its lock and diagnostic endpoint continue to describe the state it supervises. SIGINT or SIGTERM ends it at once and cancels any command in flight. A control change written while a pass is running starts the next pass immediately after it. Only one daemon runs per state directory, and a second one exits 75. `--dry-run` applies to every pass. See the systemd unit in the examples.

Ending the daemon does not stop the agent. To stop the agent, run `keepgoing stop`.

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
