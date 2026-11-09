# Operations

## Running it

There are two ways to run keepgoing. Pick one per loop.

- **`keepgoing tick` from a scheduler.** Each run makes one pass and exits. Ticks never overlap, because a tick that finds the lock held exits 75. See [examples/schedulers](../examples/schedulers) for launchd, cron, a BB automation script, and a GitHub Actions job.
- **`keepgoing daemon`.** This runs a pass every `daemon.interval`. Between passes it checks `control.json` every `daemon.control_poll`, so `keepgoing stop` or `pause` is acted on within seconds. The daemon reloads `keepgoing.yaml` and `fallbacks.yaml` before each pass and keeps the last good config when an edit is broken. SIGINT or SIGTERM ends it at once and cancels any command in flight. A stop or pause written while a pass is running starts the next pass immediately after it. Only one daemon runs per state directory, and a second one exits 75. `--dry-run` applies to every pass. See the systemd unit in the examples.

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

Events: `wake`, `nudge`, `reprompt`, `handoff`, `degraded`, `recovered`, `await_user`, `stop`, `error`, `probe_failed`, `test`, or `all`. `keepgoing report test` sends a `test` event, whatever events are subscribed.

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
