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
| `claude` | `claude -p` with JSON output (`stream-json` in unreleased source), no session persistence, restricted tools | exit 0 and the result is not an error |
| `codex` | `codex exec --json --ephemeral` in a read-only sandbox | exit 0 and a `turn.completed` event. On failure without a recognized code, the Codex app-server's `account/rateLimits/read` decides whether it was a usage limit, and when it resets. |
| `command` | your `probe.command` | exit 0, unless stdout says `{"ok": false}` |

The kind defaults from the provider: `claude-code` uses `claude`, and `codex` uses `codex`. Other providers need `kind: command`.

Other probe settings:

- `probe.cli` overrides the executable.
- `probe.workdir` sets where the probe runs. The default is a fresh temporary directory.
- `probe.exit_codes` maps a command probe's exit codes to error classes.
- `probe.env` adds environment variables, overriding inherited `claude.env` or `codex.env` values. This applies to scheduled, manual, handoff, and primary switch-back probes, including Codex rate-limit checks. Values are literal, with no `~` or shell-variable expansion. A command probe also receives `KEEPGOING_PROBE_TIER`, `KEEPGOING_PROBE_MODEL`, and `KEEPGOING_CANDIDATE`.

A `claude` or `codex` probe runs the CLI on this machine with its login. For a BB candidate, that confirms the CLI and account BB would use only when they share the login. When a candidate uses a different account, point the probe at it with `probe.env`, for example `CLAUDE_CONFIG_DIR` or `CODEX_HOME`.

Probe cost depends on the CLI, account billing, model, context overhead, caching, and provider retries. keepgoing does not estimate a dollar amount or enforce a spending limit. A small prompt does not establish a small bill: one historical Codex probe used about 21,000 input tokens, most cached. That observation is not an estimate for future runs.

Set `probe.every` and `probe.daily_cap` with that in mind. The daily cap limits scheduled probe invocations per candidate, not dollars or all provider requests.

There are three tiers:

- **Scheduled probes** run during ticks inside the time window, at most every `probe.every` per candidate and at most `probe.daily_cap` times per UTC day. A passing probe of any tier also resets the interval. A failed manual or exact probe does not, so the scheduled re-check still runs. A failed scheduled probe sends a `probe_failed` report. They use `probe.model` when set, allowing a different model for the check. `scheduled_disabled: true` turns them off.
- **Exact probes** run right before a handoff, against the candidate's real target model. A candidate that fails is skipped and the next one is tried.
- **Manual probes** run with `keepgoing fallbacks check`. Add `--id` for one candidate or `--exact` to use the target model. Manual probes do not count toward the daily cap. The command exits 1 when the redundancy rule is not met, so a scheduler can alert on it. It holds the tick lock while it runs, and it ignores pause and the time window. Naming a disabled candidate with `--id` probes it anyway.

Every result is kept in `health.json` with its time, tier, model, latency, exit code, error class, and a short detail code. Model output is never stored there. Unreleased source also keeps one [usage reading](#usage-readings) per candidate when a probe carries one.

A failed probe also saves its raw output to `probe-failures/<candidate>/` in the state directory. Captures are owner-only, capped at 64 KB, and only the newest five per candidate are kept. With the unreleased `stream-json` Claude probe a capture holds the event stream up to that cap, including the CLI's initialization message, instead of a single result object. They exist so an error shape keepgoing does not yet recognise, such as a provider's usage-limit event, can be mapped into a structured class later. keepgoing never reads them to make a decision. A candidate is healthy while its last probe passed within `probe.ttl`.

## Usage readings

Unreleased source records how much of an account's quota a provider says is used, when a probe that already runs happens to carry that information. It is a display value only. No decision reads it: health, redundancy, handoff and switch-back behave exactly as before, and a candidate shown at 100% that passes its probe is still healthy. Switching at a threshold is [planned](roadmap.md#quota-monitoring-planned), not implemented.

| Probe kind | Source | When a reading is recorded |
| --- | --- | --- |
| `claude` | `rate_limit_event` messages in the probe's own `stream-json` output | Whenever the probe's output contains the event with usable windows |
| `codex` | The app-server's `account/rateLimits/read` reply | Only when that read already happens: after a probe fails without a recognized code. A healthy Codex probe carries no reading. |
| `command`, and every other provider including Cursor | none | Never; shown as unavailable |

No provider call is added for this. keepgoing does not read stored credentials or call a provider usage endpoint. To get the event, the Claude probe now asks for `--output-format stream-json --verbose` instead of `json`; it is still one request, judged from the same final result object. That equivalence is checked with fixtures, not against a live account, and a CLI that rejects those flags would fail its probe.

The Claude event is not a documented contract. Its shape was observed on one Claude Code version (2.1.318), where each window under `rate_limit_info.unifiedWindows` carried a `utilization` between 0 and 1, a `resetsAt` time and a `status`. Parsing is deliberately narrow:

- No event, another shape, or a window without a numeric `utilization` between 0 and 1 gives no reading for that window. It is never an error and never changes the probe result.
- Window names are opaque keys. keepgoing does not assume which windows exist or what they mean.
- When a probe emits several events, later ones override earlier ones per window.
- Names and status values must be short identifiers (letters, digits, `_`, `-`, `.`, at most 64 characters); anything else is dropped. At most eight windows are kept.

One reading per candidate is stored in `health.json` under `usage`: the observation time, the source, and for each window its key, used fraction, optional reset time and optional status. No raw output is stored there. A later probe without a reading leaves the earlier one in place, so a reading can be older than the last probe; its own time is always shown. Health files written before this field existed, and stored values that are not a valid reading, load as unavailable.

`keepgoing fallbacks list` prints a `usage <id>:` line per candidate, and `keepgoing status` a `usage:` line for the primary and each candidate. Both show each window's percentage, reset time and status, then the reading's age and source, or `unavailable`. Their JSON carries `usage` with `state` (`available` or `unavailable`), `observedAt`, `ageSeconds`, `source` and `windows`.

What a reading does not tell you:

- `unavailable` means nothing was recorded. It does not mean usage is low.
- A reading describes the login the probe ran with. For a BB candidate that is the account BB uses only when they share the login, as with probe health.
- It is as fresh as the probe that carried it. Nothing refreshes it between probes, and BB does not expose the event for turns it runs.
- The primary is probed only for switch-back, so it usually shows unavailable.
- Headless Claude turns still use `json` output and record no reading.

Verification uses fictional fixtures only: malformed, partial, multi-event and absent events, out-of-range values, older health files, and CLI output. No live probe was run for this change.

## Preview manual probes

Implemented on `main`; v0.1.1 does not include the preview. Inspect the plan before choosing to run paid probes:

```sh
keepgoing fallbacks check -c /private/example-loop/keepgoing.yaml --dry-run
keepgoing fallbacks check -c /private/example-loop/keepgoing.yaml --id candidate-a --exact --dry-run --json
```

Dry runs read configuration only: no provider command, lock, health update, or runtime-state write occurs. They work with paused, stopped, or completed loops and do not change those holds. Exit 0 means a plan was printed, not that any account or model is healthy. Unknown candidate IDs and positional arguments fail before any probe runs; use `--id` to select one candidate. Without `--id`, disabled candidates are omitted; an explicit ID includes a disabled candidate.

The plan shows the selected candidates, providers, quota groups, probe kinds, effective models, timeouts, invocation count, and configured scheduled daily cap. The cap is explicitly **not applicable** to manual checks, including `--exact`. An empty model means the provider default is unresolved. Monetary cost and provider request count are unknown; custom commands may also have arbitrary side effects. Commands, environment values, task text, executable paths, and BB thread IDs are withheld.

With `--json`, stdout contains a versioned `probe_plan` object with `schemaVersion: 1`. `estimatedCostUSD` is `null` and `costStatus` is `unknown`, except when no candidates are selected (`0` and `no_probes`). `scheduledCapApplies` is always false for this command. `invocationCount` counts selected probe invocations, not provider requests, tokens, or successful checks.

Remove `--dry-run` to execute. The command prints the plan to stderr before probing, while stdout retains its existing health-result format. `--json` also formats that stderr plan as JSON. Execution proceeds without an interactive confirmation; failure to write the plan aborts it. The plan is not a reservation or a health check. Doctor never invokes these probes automatically. Offline fixtures verify selection, privacy, unchanged holds, and plan-before-execution ordering using a fake command; no paid model calls were used to test this feature.

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

## Multiple subscriptions

For three Claude subscriptions, use one headless target per account, with a distinct `group` and `claude.env.CLAUDE_CONFIG_DIR`. Claude Code documents [separate configuration directories for multiple logins](https://code.claude.com/docs/en/authentication#log-in-with-multiple-accounts). Sign in to each directory yourself before enabling the loop; keepgoing selects an existing login and never creates accounts or copies credentials.

Copy [keepgoing.multi-account.yaml](../examples/keepgoing.multi-account.yaml) and [fallbacks.multi-account.yaml](../examples/fallbacks.multi-account.yaml), replacing the fictional absolute paths. The primary uses account A:

```yaml
loop:
  adapter: claude
  group: claude-account-a
  claude:
    workdir: /fictional/src/example-app
    model: sonnet
    env:
      CLAUDE_CONFIG_DIR: /fictional/accounts/claude-a
  fallbacks: fallbacks.multi-account.yaml
  switch_back: {enabled: true}
```

The fallback file lists B and C in order, each with its own group and directory. It sets `redundancy: {min_healthy: 1, span: group}`, so one healthy alternative account is enough. With `min_healthy: 2`, both other accounts must be healthy before the first handoff. Accounts sharing a quota must share a group, even if their config directories differ.

When A is limited, keepgoing probes B with B's environment and target model, then starts B's turn with that same environment. C is next if B cannot take over. A later limit on B can hand off to a healthy C. With switch-back enabled, the loop can return to A after the reset and a passing probe using A's environment. Each target retains its own session; a handoff sends the brief to the target rather than copying the previous account's session.

Leave account selection in `claude.env` or `codex.env` so turns and probes agree. An explicit `probe.env` override wins for probes only; pointing it at another account would no longer verify the account that runs the work. Environment entries override matching inherited variables, but other inherited authentication settings remain in effect. Verify each CLI's active login with the intended environment before enabling the loop. Keep credentials out of committed YAML and use the CLI's login storage.

The same pattern works for Codex with `adapter: codex`, a `codex` block, and a separate absolute `CODEX_HOME` per account. The Codex [authentication storage](https://github.com/openai/codex/blob/main/codex-rs/login/src/auth/storage.rs) uses that home for credentials. Its turns, probes, and structured rate-limit checks all receive the selected environment.

BB candidates still use the account selected by BB. Setting `probe.env` only changes the local probe; it cannot switch a BB thread's account.

## Handoff

A handoff happens only when all of these hold:

1. The active target is in error, with a class listed in `errors.failover_on`. The default is `rate_limit` only.
2. The provider did not report a reset within `errors.wait_if_reset_within`.
3. The redundancy rule is met.
4. A candidate passes its exact probe in the same tick.

The candidate then receives a brief and becomes the active target. If `loop.handoff_file` exists, its text is sent as is. Otherwise keepgoing generates a brief that names the loop, what happened, and the repositories. The brief asks the agent to honor stop and pause first, inspect current state, and recover work without duplicating it. The goal from the loop's prompt follows.

With failover disabled (`errors.failover_on: []`), a rate-limit error with a structured future reset waits without spending recovery retries. Ordinary checks continue observing the target, so an earlier structured recovery can take effect on the next check. Once the reset passes, a still-errored target uses bounded recovery. Without a known reset, bounded recovery applies immediately. This correction is available in v0.1.4 and later. Existing awaiting-user holds require explicit `resume`; stop, pause, completion and configured loop-wide bounds retain precedence. This does not add account polling or schedule an extra check at the reset timestamp. The wait sends no report however far away the reset is; in unreleased source `timeline --kind limit_wait` shows these checks, and reporting a long wait is [planned](roadmap.md#priority-reliability-checkpoints) (R8).

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
