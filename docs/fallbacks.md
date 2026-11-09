# Fallbacks

`fallbacks.yaml` is the maintained list of agents the loop may move to when its current agent cannot continue. Point the loop at it with `loop.fallbacks`.

## Candidates

Each candidate is a full agent definition plus an id and a probe:

```yaml
candidates:
  - id: claude-second-account
    adapter: bb
    provider: claude-code
    group: claude-second-account
    bb: {thread: thr_example123, model: claude-fable-5-1, reasoning: medium}
    probe: {model: claude-haiku-5-5}
```

- `id` is how state, health, and reports refer to the candidate. `primary` is reserved for the loop's own agent.
- `provider` is required. Redundancy counts by it.
- `group` is the account or quota pool and defaults to the provider. Candidates in the active target's group are never handoff targets, never count toward redundancy, and are not probed on schedule, because they draw on the same quota. Set the primary's `group` in `keepgoing.yaml` too.
- Order matters. A handoff tries healthy candidates from the top.
- `disabled: true` keeps a candidate in the file without using it.

A BB candidate may name the same thread as the primary with a different `model`. The handoff then switches the thread's model with `bb thread update` before sending the brief.

## Probes

A probe is one tiny real request: "Reply with exactly: OK", with no tools, one turn, and a hard timeout. It proves the CLI is installed, logged in, and online, that the model is available, and that the account is not rate limited.

| Kind | What runs | Healthy when |
| --- | --- | --- |
| `claude` | `claude -p` with JSON output, no session persistence, restricted tools | exit 0 and the result is not an error |
| `codex` | `codex exec --json --ephemeral` in a read-only sandbox | exit 0 and a `turn.completed` event |
| `command` | your `probe.command` | exit 0, unless stdout says `{"ok": false}` |

The kind defaults from the provider: `claude-code` uses `claude`, and `codex` uses `codex`. Other providers need `kind: command`.

Other probe settings:

- `probe.cli` overrides the executable.
- `probe.workdir` sets where the probe runs. The default is a fresh temporary directory.
- `probe.exit_codes` maps a command probe's exit codes to error classes.
- `probe.env` adds environment variables. A command probe also receives `KEEPGOING_PROBE_TIER`, `KEEPGOING_PROBE_MODEL`, and `KEEPGOING_CANDIDATE`.

A `claude` or `codex` probe runs the CLI on this machine with its login. For a BB candidate, that confirms the CLI and account BB would use only when they share the login. When a candidate uses a different account, point the probe at it with `probe.env`, for example `CLAUDE_CONFIG_DIR` or `CODEX_HOME`.

Probe cost depends on the CLI:

- A Claude Code probe on a small model costs a fraction of a cent.
- A Codex probe sends Codex's own system prompt. One real run used about 21,000 input tokens, most of them cached.

Set `probe.every` and `probe.daily_cap` with that in mind.

There are three tiers:

- **Scheduled probes** run during ticks inside the time window, at most every `probe.every` per candidate and at most `probe.daily_cap` times per UTC day. A passing probe of any tier also resets the interval. A failed manual or exact probe does not, so the scheduled re-check still runs. A failed scheduled probe sends a `probe_failed` report. They use `probe.model` when set, so a cheap model can confirm the path for almost nothing. `scheduled_disabled: true` turns them off.
- **Exact probes** run right before a handoff, against the candidate's real target model. A candidate that fails is skipped and the next one is tried.
- **Manual probes** run with `keepgoing fallbacks check`. Add `--id` for one candidate or `--exact` to use the target model. Manual probes do not count toward the daily cap. The command exits 1 when the redundancy rule is not met, so a scheduler can alert on it. It holds the tick lock while it runs, and it ignores pause and the time window. Naming a disabled candidate with `--id` probes it anyway.

Every result is kept in `health.json` with its time, tier, model, latency, exit code, error class, and a short detail code. Model output is never stored there.

A failed probe also saves its raw output to `probe-failures/<candidate>/` in the state directory. Captures are owner-only, capped at 64 KB, and only the newest five per candidate are kept. They exist so an error shape keepgoing does not yet recognise, such as a provider's usage-limit event, can be mapped into a structured class later. keepgoing never reads them to make a decision. A candidate is healthy while its last probe passed within `probe.ttl`.

## Defaults

| Field | Default |
| --- | --- |
| `redundancy.min_healthy` | 1 |
| `redundancy.span` | `provider` |
| `probe.every` | `4h` |
| `probe.ttl` | `6h`, and it must be at least `every` |
| `probe.daily_cap` | 6 |
| `probe.timeout` | `90s` |

Scheduled probes run one after another inside the tick, so a long list of slow probes makes that tick long. The daemon still notices a stop or pause written during the tick and acts on it right after.

## Redundancy

```yaml
redundancy:
  min_healthy: 2
  span: provider
```

A handoff is allowed only when the healthy, eligible candidates cover at least `min_healthy` distinct values of `span`:

- `provider`: two Claude Code accounts count as one. This is the default, because outages and policy changes are usually per provider.
- `group`: each account or quota pool counts.
- `none`: each candidate counts.

Below the minimum, keepgoing warns, refuses handoffs, reports `degraded` once, and keeps supervising the current agent under the ordinary error rules. It reports `recovered` when the rule is met again.

## Handoff

A handoff happens only when all of these hold:

1. The active target is in error, with a class listed in `errors.failover_on`. The default is `rate_limit` only.
2. The provider did not report a reset within `errors.wait_if_reset_within`.
3. The redundancy rule is met.
4. A candidate passes its exact probe in the same tick.

The candidate then receives a brief and becomes the active target. If `loop.handoff_file` exists, its text is sent as is. Otherwise keepgoing generates a brief that names the loop, what happened, and the repositories. The brief asks the agent to honor stop and pause first, inspect current state, and recover work without duplicating it. The goal from the loop's prompt follows.

## Switching back

By default, returning to the primary is manual:

```sh
keepgoing retarget primary
```

To return automatically, turn on switch-back in `keepgoing.yaml`:

```yaml
loop:
  provider: claude-code       # lets the primary be probed like a claude-code candidate
  switch_back:
    enabled: true
    min_after: 1h             # when the provider reported no reset time
    retry_every: 30m          # after the primary fails its probe
  probe: {model: claude-haiku-5-5}   # optional; the switch-back probe uses the primary's own model
```

Switch-back only happens when all of these hold:

1. A fallback is active and has just finished a turn, so the fallback is never interrupted mid-turn.
2. The primary's limit has had time to lift. That means the reset time the provider reported at the handoff, or `min_after` since the handoff when none was reported.
3. The last failed attempt was at least `retry_every` ago.
4. The primary passes an exact probe right now.

The primary then gets a short brief saying its limit has reset and the loop is back with it, and a `switch_back` report is sent. If the probe fails, the fallback keeps working and switch-back tries again later.

`loop.probe` takes the same fields as a candidate's `probe`. Its kind defaults from `loop.provider`. Switch-back needs `loop.fallbacks`, and it needs a way to probe the primary.
