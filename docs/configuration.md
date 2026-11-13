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
| `thread` | required, unless `new_thread` | The BB thread id. |
| `new_thread` | `false` | Fallback candidates only, unreleased source. Instead of `thread`: a handoff to this candidate creates a new BB thread on the candidate's `provider`, with `model` and `reasoning`. Requires `provider`. See [hand off to a new conversation](fallbacks.md#hand-off-to-a-new-conversation). |
| `project`, `environment` | the continued conversation's | With `new_thread` only: where the new thread is created. |
| `permission_mode` | `accept-edits` | With `new_thread` only: `accept-edits`, `auto` or `full`. Always passed to `bb thread spawn`, so the new thread never takes a project default. `accept-edits` asks before anything but file edits. |
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

## An agent defined in configuration

Unreleased source (roadmap K2); **fixture-tested with stand-in agents only, no real agent has been run from a definition**. `adapter: custom` describes an agent keepgoing has no built-in runner for, from its documented command-line interface. It runs one turn per process through the same runner as headless Claude Code and Codex: a detached turn, a private prompt file, per-target session storage and the owned stop.

Three rules hold for every definition:

- **No shell.** Every command is an argv list. A setting's value is always exactly one argument.
- **The prompt is never an argument.** It is passed on standard input, or in a private file the command is pointed at.
- **No text is matched.** A JSON field is compared for equality with codes you declare. A value that is not shaped like a code (letters, digits, `_ . : -`, at most 64) is treated as absent. Anything you do not declare is `unknown` to keepgoing.

```yaml
loop:
  adapter: custom
  custom:
    name: example-agent              # label; the provider when none is set
    workdir: /example/project
    env: {EXAMPLE_HOME: /example/account-a}
    values: {model: example-model}   # this loop's values for the settings
    definition:
      settings: [model]              # names the commands may use as {name}
      start: [example-agent, run, --json, [--model, "{model}"]]
      resume: [example-agent, run, --json, [--model, "{model}"], --session, "{session}"]
      prompt: stdin                  # or file, with {prompt_file} in the commands
      output:
        format: jsonl                # or json: one object
        max_bytes: 1048576
        select: {field: type, equals: result}
      session: {field: session}      # or {file: .example/session}
      finished: {exit_codes: [0], field: status, equals: [ok]}
      waiting: {exit_codes: [2], field: status, equals: [needs_input]}
      errors:
        field: error.code
        classes: {quota_exceeded: rate_limit, unauthorized: auth}
        http_status_field: error.status
        exit_codes: {75: network}
      probe:
        argv: [example-agent, check, --json, [--model, "{model}"]]
      limits:
        argv: [example-agent, limits, --json]
        blocked: {field: blocked, equals: ["true"]}
        resets_at_field: resets_at
      capabilities: {exact_resume: true, can_be_stopped: true, structured_errors: true, structured_waiting: true}
```

| Key | Meaning |
| --- | --- |
| `name`, `workdir`, `env` | A label (lower-case letters, digits, hyphens), where turns run, and extra environment for turns, the probe and the limit lookup. |
| `values` | Values for the definition's `settings`. One line each. |
| `settings` | The names the commands may refer to as `{name}`. `model` is also what `status` and probes call the model. |
| `start`, `resume` | Argv. The first argument is the executable, written out. An item is one argument, or a list of arguments left out together when a setting they refer to has no value. `resume` is used when a session is saved and may refer to `{session}`. |
| `prompt` | `stdin` (default) or `file`. With `file` the commands must refer to `{prompt_file}`, the private file holding the prompt, and standard input is empty. |
| `output` | `format` is `json` (one object) or `jsonl` (one object per line; lines that are not objects are skipped). `max_bytes` (default 1 MiB, 1 KiB to 16 MiB) bounds what is read: larger output is not read at all, the state is unknown and nothing is sent. `select` picks the object every rule reads: the last one whose field has that value, or the last object without it. |
| `session` | Where the session identity comes from: a `field` of the output (with its own optional `select`), or a `file` under the working directory (one token, at most 256 bytes, a regular file). An identity must be a token that does not begin with a hyphen; anything else is not saved. |
| `finished` | The turn ended healthy: the exit code is one of `exit_codes`, when given, **and** `field` has one of `equals`, when given. Defaults to exit code 0. |
| `waiting` | The turn ended waiting on a person, with the same form. It raises one awaiting-user hold and one report; nothing is approved or answered. After you `resume`, the same turn no longer raises it and the next send continues the session. |
| `errors` | Classifies a failed turn. `classes` maps the agent's codes in `field` to `rate_limit`, `auth`, `overloaded`, `network` or `unknown`; `http_status_field` is read as an HTTP status; `exit_codes` maps failing exit codes. Anything unmatched is `unknown`. |
| `probe` | A small real request that lets the agent be a [fallback](fallbacks.md): it receives the fixed probe prompt the way turns receive theirs, uses the probe's model for `{model}`, and is judged by `finished` and `errors`. Without it a candidate needs a `command` probe. |
| `limits` | One command printing one JSON object, run only when a failed turn or probe is unclassified, in the same environment. `blocked` (a field and its values) says the account is blocked by a usage limit; `resets_at_field` is an RFC 3339 time or Unix seconds. |
| `capabilities` | `exact_resume`, `can_be_stopped`, `structured_errors`, `structured_waiting`. |

**Capabilities must be observable.** Validation refuses a capability with no rule behind it, and a rule without its capability, so a definition cannot claim more than keepgoing can check or carry rules that are silently ignored:

- `exact_resume` needs `session` and a `resume` command that refers to `{session}`. Without it every turn starts a new session.
- `structured_errors` needs `errors`. Without it every failure is `unknown`.
- `structured_waiting` needs `waiting`.
- `can_be_stopped` needs nothing: the turn runs under keepgoing's own wrapper. Without it `stop` holds the loop but does not signal a running turn.
- `retries_itself`, `fails_over_itself`, `schedules_itself` and `child_work` are refused for now: a definition has no way to let keepgoing observe them.

`keepgoing doctor` resolves the executable without running it and lists what the definition lets keepgoing do and what it cannot (`agent.<target>.capabilities` and `agent.<target>.limits`). It queries no version, login or account for a defined agent. The [capability matrix](adapters.md) has a column for defined agents. Guided `init` and `fallbacks add` do not create one; write the entry by hand. The saved session identifier is used to resume but is not shown by `status` or `keepgoing runs`.

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

`loop.limits.max_new_threads` (default `3`; `0` means the default; at most `10`) is separate: it caps the BB thread creations a loop attempts for handoffs to candidates with `bb.new_thread`, and is not a bound on repeated work. Every attempt not proven refused counts, and two are never made less than one hour apart (fixed). See [hand off to a new conversation](fallbacks.md#hand-off-to-a-new-conversation).

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
| `report.events` | `handoff, switch_back, degraded, recovered, await_user, limit_wait, child_error, complete, stop, error` (the `complete` event requires v0.1.3 or newer; `limit_wait` and `child_error` are unreleased source) |
| `report.timeout` | `20s` |
| `yield.busy_command`, `yield.timeout` | required, `10s` |
| `daemon.interval`, `daemon.control_poll` | `5m`, `2s` |
| `tick_timeout` | `3m`, or `probe.timeout` plus `90s` when that is longer. A check ends within this plus 90 seconds. Minimum `2m40s`; with fallbacks an explicit value must be at least `probe.timeout` plus `90s`. See [check time budget](operations.md#check-time-budget) |

## Tick output

Each tick prints one JSON line:

```json
{"at":"2026-11-09T12:00:00Z","loop":"example-loop","target":"primary","control":"enabled","state":"error","errorClass":"rate_limit","action":"handoff","reason":"error: rate_limit","window":"no window configured","handoff":{"from":"primary","to":"codex-thread","reason":"rate_limit","tried":["codex-thread"]}}
```

`action` is one of `none`, `wake`, `nudge`, `reprompt`, `await_user`, `stop`, or `handoff`. Optional fields include `resetsAt`, `redundancy`, `probes`, `yielded`, `dryRun`, `warnings`, `outcome` (`succeeded`, `failed` or `dry_run`; absent when the check could not be recorded) and `missingWorktrees`. A probe entry with `skipped` produced no result. With `--dry-run`, the tick decides and records but sends nothing and runs no probes.
