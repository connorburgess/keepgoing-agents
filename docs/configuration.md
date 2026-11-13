# Configuration

Use [`keepgoing init`](setup.md) to generate a disabled configuration outside the checkout, or maintain the YAML directly as described below.

`keepgoing.yaml` describes one loop. Relative paths resolve against the directory holding the file, and a leading `~/` expands to the home directory. This includes `state_dir`, prompt and handoff files, repositories, working directories, and commands written as a path such as `./status.sh`. A bare command name such as `bb` is looked up on `PATH`. Unknown fields are errors. Editors that support JSON Schema can use [schema/keepgoing.schema.json](../schema/keepgoing.schema.json). Fallbacks have their own file, described in [fallbacks.md](fallbacks.md).

```yaml
version: 1
loop:
  name: example-loop
  state_dir: ~/.local/state/keepgoing/example-loop
  adapter: bb                     # bb | process | claude | codex | cursor (unreleased)
  provider: claude-code           # optional for the primary; used in reports
  group: claude-example-account   # quota pool; same-group fallbacks are skipped on a limit
  bb: {thread: thr_example123}
  prompt: Continue the loop.      # or prompt_file
  handoff_file: HANDOFF.md        # optional
  progress: {repositories: [~/src/example-app], stall_after: 30m, min_wake_interval: 60m}
  window: {tz: UTC, days: {Mon: ["08:00", "23:00"]}}
  errors: {max_reprompts: 3, backoff: 10m, failover_on: [rate_limit], wait_if_reset_within: 30m, report_wait_after: 6h}
  fallbacks: fallbacks.yaml
  report: {secret_file: ~/.config/keepgoing/example.env}
  yield: {busy_command: ["./busy-check.sh"]}
  daemon: {interval: 5m, control_poll: 2s}
```

## loop

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | Lowercase letters, digits, and hyphens. It appears in every event and report. |
| `state_dir` | yes | Where control, state, health, log, lock, and turn files live. Keep it outside any repository. |
| `adapter` | yes | `bb`, `process`, `claude`, `codex`, or unreleased `cursor`, with the matching block below. |
| `provider`, `group` | no | `group` defaults to `provider`, then to `primary`. Set `group` so a limit on this account skips fallbacks that share it. |
| `prompt_goal` | `false` | Explicitly append the latest private goal specification and revision to automatic messages, including handoffs. A valid goal for this loop is required before sending. See [current goal context](goals.md#current-goal-context-in-wake-messages). |
| `prompt` or `prompt_file` | one of them | The text sent on wake, nudge, and re-prompt. The file is read on every send, so you can edit it while the loop runs. |
| `handoff_file` | no | Sent to a fallback when it takes over. When the file is missing, a brief is generated. |
| `switch_back` | no | Returns the loop to the primary once its limit resets and an exact probe passes. Off by default. See [fallbacks.md](fallbacks.md#switching-back). |
| `probe` | for switch-back | How to probe the primary, with the same fields as a candidate's `probe`. The kind defaults from `provider`. |

## bb

| Field | Default | Meaning |
| --- | --- | --- |
| `thread` | required | The BB thread id. |
| `cli` | `bb` | Path to the bb executable. |
| `model`, `reasoning` | none | Applied with `bb thread update` before the first message after a handoff to this agent. |
| `retry_signal_ttl` | `15m` | How long a provider `willRetry` notice keeps suppressing recovery without newer evidence: the newest notice on an errored thread, the newest provider or system log event on an active one, or a rate-limit notice's structured reset. Minimum `1m`. See [stale retry notices](operations.md#stale-retry-notices). |

## process

| Field | Default | Meaning |
| --- | --- | --- |
| `status_command` | required | argv. Prints a JSON status object on stdout. |
| `resume_command` | required | argv. Receives the prompt on stdin and the mode, `auto` or `queue`, in `KEEPGOING_MODE`. |
| `stop_command` | none | argv. Without it, a stop request means nothing more is sent. |
| `exit_codes` | none | Map from a status-command exit code to an error class, for example `{75: rate_limit}`. Any other non-zero exit is an error of class `unknown`. |
| `env` | none | Extra environment variables for all three commands. |
| `timeout` | `20s` | Per command. |

The status object looks like this:

```json
{"state": "idle", "errorClass": "rate_limit", "queued": 0, "background": 0, "gone": false, "awaitingInput": false}
```

`state` is one of `active`, `idle`, `error`, `stopping`, or `pending`. Common synonyms are accepted too: `running`, `busy`, and `working` mean active, `completed`, `done`, and `waiting` mean idle, `failed` means error, and `queued` and `starting` mean pending. Anything else is `unknown`. `awaitingInput: true` means the session is blocked on a person, and nothing is sent to it. `errorClass` is read only when the state is `error`. It may be a class name or a well-known code such as `429`. Stderr is never captured.

## claude

keepgoing runs `claude -p --output-format json` itself, one turn per send. Each turn after the first resumes the saved session.

| Field | Default | Meaning |
| --- | --- | --- |
| `workdir` | required | Where the agent runs. |
| `cli` | `claude` | Path to the executable. |
| `model`, `effort`, `permission_mode` | none | Passed through as flags. |
| `max_budget_usd` | none | Passed as `--max-budget-usd`. |
| `extra_args` | none | Appended as is. |
| `env` | none | Environment overrides for fresh and resumed turns, inherited by probes. For separate accounts, set `CLAUDE_CONFIG_DIR` (Claude) or `CODEX_HOME` (Codex). Values are literal; use absolute paths. |

## codex

keepgoing runs `codex exec --json` itself, one turn per send. Later turns resume the session with `codex exec resume`.

| Field | Default | Meaning |
| --- | --- | --- |
| `workdir` | required | Where the agent runs. |
| `cli` | `codex` | Path to the executable. |
| `model` | none | Passed as `-m`. |
| `sandbox` | none | Passed as `-s` on the first turn, and as a `sandbox_mode` override on resumed turns. |
| `skip_git_repo_check` | `false` | Passes `--skip-git-repo-check`. |
| `extra_args` | none | Appended as is. |
| `env` | none | Environment overrides for fresh and resumed turns, inherited by probes. For separate accounts, set `CLAUDE_CONFIG_DIR` (Claude) or `CODEX_HOME` (Codex). Values are literal; use absolute paths. |

Environment overrides inherit the rest of the supervisor's environment. They do not change the supervisor's login or environment. Codex's structured rate-limit check uses the same overrides as its turn. `probe.env` overrides inherited values for probes only, including their rate-limit checks. See the [three-account example](fallbacks.md#multiple-subscriptions).

A running headless turn cannot be steered or queued into. A nudge to a stalled headless turn is recorded as a warning instead. The attempt still counts toward `max_nudges` and `min_wake_interval`, so a headless turn that stays stalled reaches the awaiting-user hold after `max_nudges` warnings.

## cursor (unreleased)

The Cursor CLI adapter is available in source only, not v0.1.5. It accepts `cli` (default `agent`), required `workdir`, optional `model`, and optional `env`. There is no `extra_args` or automatic approval flag. Configure YAML directly; guided `init` support is unfinished.

Only standalone primary loops are accepted: no `fallbacks`, enabled `switch_back`, or Cursor fallback candidates. Failures remain unknown because the reviewed print schema lacks structured error classes/reset times. See [Cursor supervision](cursor.md) for stdin transport, exact identity, output bounds, permission behavior and unverified live acceptance.

## progress

| Field | Default | Meaning |
| --- | --- | --- |
| `repositories` | none | Repositories whose worktrees are fingerprinted. Without this, stall detection is off and an active session is left alone. |
| `stall_after` | `30m` | How long the fingerprint may stay unchanged while the session is active before a nudge. |
| `max_nudges` | `3` | Nudge attempts a stalled session gets, delivered or not, before the loop is marked awaiting user and reports `await_user`. Visible progress or a finished turn resets the count. |
| `min_wake_interval` | `60m` | Minimum gap between a nudge and anything sent before it: a wake, nudge, re-prompt, or handoff. |

The fingerprint covers every worktree of each repository. It includes `HEAD`, `git status --porcelain` for tracked files, and the size and modification time of tracked files that differ from `HEAD`. Untracked files, file contents, and transcripts are never read.

Idle wakes follow the supervisor's check cadence, subject to other gates. `min_wake_interval` spaces stall nudges; it does not throttle idle wakes. `max_nudges` resets when a turn ends and is not a loop-wide work budget.

## limits

These three optional fields are the only loop-wide bounds on repeated automatic work. None has a default: with all three omitted, an idle session is woken on every eligible ordinary check for as long as the loop stays enabled and no control hold applies. The `progress` and `errors` caps bound one stalled turn or one error episode and reset afterwards. See [bounds on repeated work](operations.md#bounds-on-repeated-work). In unreleased source, `validate` and `status` warn when all three are unset; the warning changes no behavior or exit code.

| Field | Default | Holds the loop when |
| --- | --- | --- |
| `deadline` | none | A check starts at or after this absolute time. |
| `max_sends` | none (`0` disables) | This many automatic send attempts have been reserved in the state directory. |
| `no_progress_timeout` | none (`0` disables) | The Git fingerprint of `progress.repositories` has not changed for this long. |

A reached limit persists an awaiting-user hold; it does not cancel a running or queued turn. Stop, pause and the other control holds take precedence over all three. When several are reached at once the hold names the first of `deadline`, `max_sends`, `no_progress_timeout`.

Optional `loop.limits.deadline` is an absolute RFC3339 timestamp including a timezone, for example:

```yaml
loop:
  # Other loop fields omitted.
  limits:
    deadline: "2030-01-01T18:00:00Z"
    max_sends: 20
```

At or after this time, the next ordinary check persists an awaiting-user hold before automatic provider observation, yield commands, fingerprints or probes. Stop and existing control holds retain precedence. Omission or an empty string leaves existing behavior unchanged. Past timestamps are allowed and hold on the next check. See [operations](operations.md#loop-deadline) for restart, resume, manual-command and clock semantics. This field is available in v0.1.3 or newer. Optional `max_sends` is a nonnegative integer (zero/omitted disables accounting). It caps total automatic send attempts made while enabled, including failed sends, recovery and handoffs; the count survives restart and resume. See [send allowance behavior](operations.md#total-send-allowance). Optional `no_progress_timeout` is zero/off or a duration of at least `1m`, and requires `progress.repositories`. It holds after elapsed time without verified Git metadata changes, including idle turns and downtime. See [no-progress semantics](operations.md#no-progress-timeout). These limits require v0.1.3 or newer; monetary enforcement is not implemented.

## window

This is optional. `tz` is an IANA zone name. `days` maps `Mon` through `Sun` to a `["HH:MM", "HH:MM"]` span, and the end is exclusive. A span whose end is earlier than its start runs past midnight. Its key is the day it starts. Days that are not listed are closed. Scheduled probes also run only inside the window.

## errors

| Field | Default | Meaning |
| --- | --- | --- |
| `max_reprompts` | `3` | Re-prompt attempts, delivered or not, allowed while the session stays in error, before the loop waits for a person. In unreleased source it is also the cap on [rejected takeover briefs](operations.md#rejected-takeover-briefs) per fallback, and on rejected switch-back briefs. |
| `backoff` | `10m` | Minimum gap between re-prompt attempts, and between handoff briefs to a fallback that rejected one. |
| `failover_on` | `[rate_limit]` | Error classes that may trigger a handoff: `rate_limit`, `auth`, `overloaded`, or `network`. `unknown` is not allowed. |
| `wait_if_reset_within` | `30m` | When the provider reports a limit reset this soon, wait instead of handing off. |
| `report_wait_after` | `6h` | Unreleased source. When a limit is being waited out with no handoff possible and its reported reset is further away than this, send one `limit_wait` report for that wait. The loop is not held and no decision changes. There is no off value: leaving it out or setting `0` uses the default. To receive nothing, give `report.events` an explicit list without `limit_wait`. See [a long limit wait](operations.md#a-long-limit-wait-with-no-way-out). |

The re-prompt budget resets when the session is seen idle, or when it is active and the Git fingerprint changes; seeing it active is not enough, because a re-prompt itself makes it active. An `auth` error goes to awaiting user unless a handoff happens, because retrying cannot fix it. For a class listed in `failover_on`, a reported reset time is honored: the loop waits for the reset instead of spending re-prompts when no fallback is available. For other classes the reset time is ignored. `failover_on: []` turns handoff off.

## Validation and defaults

- Durations use Go syntax such as `90s`, `10m`, or `4h`.
- `stall_after` and `min_wake_interval` must be at least `1m`. `daemon.interval` must be at least `10s`, and `daemon.control_poll` at least `100ms`.
- A zero or omitted number means its default. `max_reprompts: 0` means 3, not zero.
- A window span whose start equals its end is rejected.
- The BB CLI is given 30 seconds per call.

## report, yield, daemon

See [operations.md](operations.md).

| Field | Default |
| --- | --- |
| `report.url_env`, `report.secret_env` | `KEEPGOING_WEBHOOK_URL`, `KEEPGOING_WEBHOOK_SECRET` |
| `report.secret_file` | none |
| `report.source` | the loop name |
| `report.scheme` | `standard-webhooks` (or `bearer`) |
| `report.format` | `standard` (or `text`) |
| `report.events` | `handoff, switch_back, degraded, recovered, await_user, limit_wait, complete, stop, error` (the `complete` event requires v0.1.3 or newer; `limit_wait` is unreleased source) |
| `report.timeout` | `20s` |
| `yield.busy_command`, `yield.timeout` | required, `10s` |
| `daemon.interval`, `daemon.control_poll` | `5m`, `2s` |
| `tick_timeout` | `3m` (minimum `30s`): time budget for one check's optional work; see [check time budget](operations.md#check-time-budget) |

## Tick output

Each tick prints one JSON line:

```json
{"at":"2026-11-09T12:00:00Z","loop":"example-loop","target":"primary","control":"enabled","state":"error","errorClass":"rate_limit","action":"handoff","reason":"error: rate_limit","window":"no window configured","handoff":{"from":"primary","to":"codex-thread","reason":"rate_limit","tried":["codex-thread"]}}
```

`action` is one of `none`, `wake`, `nudge`, `reprompt`, `await_user`, `stop`, or `handoff`. Optional fields include `resetsAt`, `redundancy`, `probes`, `yielded`, `dryRun`, `warnings`, `outcome` (`succeeded`, `failed` or `dry_run`; absent when the check could not be recorded) and `missingWorktrees`. A probe entry with `skipped` produced no result. With `--dry-run`, the tick decides and records but sends nothing and runs no probes.
