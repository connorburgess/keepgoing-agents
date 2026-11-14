# Configuration

Use [`keepgoing init`](setup.md) to generate a disabled configuration outside the checkout, or maintain the YAML directly as described below.

`keepgoing.yaml` describes one loop. Relative paths resolve against the directory holding the file, and a leading `~/` expands to the home directory. This includes `state_dir`, prompt and handoff files, repositories, working directories, and commands written as a path such as `./status.sh`. A bare command name such as `bb` is looked up on `PATH`. Unknown fields are errors. Editors that support JSON Schema can use [schema/keepgoing.schema.json](../schema/keepgoing.schema.json). Fallbacks have their own file, described in [fallbacks.md](fallbacks.md).

```yaml
version: 1
loop:
  name: example-loop
  state_dir: ~/.local/state/keepgoing/example-loop
  adapter: bb                     # bb | process | claude | codex | cursor, custom (v0.2.0)
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
| `adapter` | yes | `bb`, `process`, `claude`, `codex`, or (v0.2.0) `cursor` or `custom` ([an agent defined in configuration](#an-agent-defined-in-configuration)), with the matching block below. |
| `provider`, `group` | no | `group` defaults to `provider`, then to `primary`. Set `group` so a limit on this account skips fallbacks that share it. |
| `prompt_goal` | `false` | Explicitly append the latest private goal specification and revision to automatic messages, including handoffs. A valid goal for this loop is required before sending. See [current goal context](goals.md#current-goal-context-in-wake-messages). |
| `prompt` or `prompt_file` | one of them | The text sent on wake, nudge, and re-prompt. The file is read on every send, so you can edit it while the loop runs. |
| `handoff_file` | no | Sent to a fallback when it takes over. When the file is missing, a brief is generated. |
| `switch_back` | no | Returns the loop to the primary once its limit resets and an exact probe passes. Off by default. See [fallbacks.md](fallbacks.md#switching-back). |
| `routes` | no | `standard`: follow the person's [standard route list](routes.md) instead of a `fallbacks` file. v0.2.0. Excludes `fallbacks`; for `bb`, `claude` and `codex` loops. |
| `routes_allow` | no | With `routes: standard`: `[new_conversation]` lets a standard route that starts a new conversation apply to this loop. Off by default. |
| `probe` | for switch-back and timing | How to probe the primary, with the same fields as a candidate's `probe`. The kind defaults from `provider`. |
| `timing` | no | Usage-aware timing. Off by default. Today it only probes the first route on a schedule to collect usage readings of its allowance; it never changes routes. See [timing](#timing). |

## bb

| Field | Default | Meaning |
| --- | --- | --- |
| `thread` | required, unless `new_thread` | The BB thread id. |
| `new_thread` | `false` | Fallback candidates only, v0.2.0. Instead of `thread`: a handoff to this candidate creates a new BB thread on the candidate's `provider`, with `model` and `reasoning`. Requires `provider`. See [hand off to a new conversation](fallbacks.md#hand-off-to-a-new-conversation). |
| `project`, `environment` | the continued conversation's | With `new_thread` only: where the new thread is created. |
| `permission_mode` | `accept-edits` | With `new_thread` only: `accept-edits`, `auto` or `full`. Always passed to `bb thread spawn`, so the new thread never takes a project default. `accept-edits` asks before anything but file edits. |
| `children.retry_errored` | `false` | v0.2.0. Opt in to [lane recovery](operations.md#lane-recovery): ask BB to retry the failed turn of a direct child thread that was seen working and then failed with a connection, overload or unknown error. Nothing else is ever done to a child. Per lane it uses `errors.max_reprompts` attempts spaced by `errors.backoff`. |
| `children.max_per_check` | `5` | With `retry_errored`: lanes worked on in one check. `1` to `20`; `0` means the default. |
| `children.max_per_episode` | `20` | With `retry_errored`: retries for one failure across all lanes. `1` to `100`; `0` means the default. |
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

<a id="cursor-unreleased"></a>

## cursor

The Cursor CLI adapter is in v0.2.0, not v0.1.5. It accepts `cli` (default `agent`), required `workdir`, optional `model`, optional `env`, and optional `fresh_start_versions`: up to 16 further CLI versions, written as the CLI prints them, for which you have confirmed that it writes its first stream records before submitting a prompt. That list only widens [when a new conversation may follow a turn that failed before starting](cursor.md#turns-identity-and-controls); leave it out unless you have checked. There is no `extra_args` or automatic approval flag. Use guided `init --agent cursor` or write the YAML directly.

Only standalone primary loops are accepted: no `fallbacks`, enabled `switch_back`, or Cursor fallback candidates. Failures remain unknown because the reviewed print schema lacks structured error classes/reset times. See [Cursor supervision](cursor.md) for stdin transport, exact identity, output bounds, permission behavior and unverified live acceptance.

## An agent defined in configuration

v0.2.0 (roadmap K2); **fixture-tested with stand-in agents only, no real agent has been run from a definition**. `adapter: custom` describes an agent keepgoing has no built-in runner for, from its documented command-line interface. It runs one turn per process through the same runner as headless Claude Code and Codex: a detached turn, a private prompt file, per-target session storage and the owned stop.

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
| `start`, `resume` | Argv. The first argument is the executable, written out: a bare name such as `example-agent` is looked up on `PATH`, and a path such as `./bin/example-agent` resolves against the directory of the configuration file, like every other command in a configuration (not against `workdir`). A shell given a command string (`sh -c …`, `cmd /c …`, `powershell -Command …`) is refused. That is a guard against an obvious mistake, not a security boundary: a wrapper script, `env sh -c` or another interpreter is not detected, and what a command does is yours to know. An item is one argument, or a list of arguments left out together when a setting they refer to has no value. `resume` is used when a session is saved and may refer to `{session}`. |
| `prompt` | `stdin` (default) or `file`. With `file` the commands must refer to `{prompt_file}`, the private file holding the prompt, and standard input is empty. |
| `output` | `format` is `json` (one object) or `jsonl` (one object per line; lines that are not objects are skipped). `max_bytes` (default 1 MiB, 1 KiB to 16 MiB) bounds what is read. A turn that printed more is not read at all, whatever the part that fits says: it is a failed turn of `unknown` class, no session is taken from it, and the ordinary re-prompts follow (up to `errors.max_reprompts`, then a hold). Nothing needs removing by hand; the next turn replaces the output. `select` picks the object every rule reads: the last one whose field has that value, or the last object without it. |
| `session` | Where the session identity comes from: a `field` of the output (with its own optional `select`), or a `file` under the working directory (one token, at most 256 bytes, a regular file). An identity must be a token that does not begin with a hyphen; anything else is not saved. |
| `finished` | The turn ended healthy: the exit code is one of `exit_codes` **and** `field` has one of `equals`, when a field is given. `exit_codes` defaults to `[0]`, also when a field is given: a field alone never makes a killed turn (exit 137) or one with an unreadable exit record healthy. |
| `waiting` | The turn ended waiting on a person, with the same form and the same `exit_codes` default. It is read before `finished`, so a turn both rules match is a wait; a waiting rule that would leave `finished` nothing to match is refused. It raises one awaiting-user hold and one report; nothing is approved or answered. See [after a wait](#after-a-turn-that-waited-on-a-person). |
| `errors` | Classifies a failed turn. `classes` maps the agent's codes in `field` to `rate_limit`, `auth`, `overloaded`, `network` or `unknown`; `http_status_field` is read as an HTTP status; `exit_codes` maps failing exit codes. Anything unmatched is `unknown`. |
| `probe` | A real request that lets the agent be a [fallback](fallbacks.md): it receives the fixed probe prompt the way turns receive theirs, uses the probe's model for `{model}`, and is judged by `finished` and `errors`. Its output is bounded as it is read. **keepgoing cannot tell what your probe command does.** If it is the agent's ordinary turn command, every probe is a full paid turn with the agent's tools and side effects. A hand-written fallbacks file, the only kind a defined agent gets, probes each fallback every 4 hours by default, up to 6 times a day (`probe.every`, `probe.daily_cap`), and once more before each handoff: **up to six full paid turns a day per fallback, plus one per handoff, while the loop runs**. Use the agent's cheapest documented check, or raise `probe.every` and lower `probe.daily_cap`. `fallbacks check --dry-run` and `doctor` say this too. Without a probe a candidate needs a `command` probe. |
| `limits` | One command printing one JSON object, run only when a failed turn or probe is unclassified, in the same environment. `blocked` (a field and its values) says the account is blocked by a usage limit; `resets_at_field` is an RFC 3339 time or Unix seconds. |
| `capabilities` | `exact_resume`, `can_be_stopped`, `structured_errors`, `structured_waiting`. |

**Capabilities must be observable.** Validation refuses a capability with no rule behind it, and a rule without its capability, so a definition cannot claim more than keepgoing can check or carry rules that are silently ignored:

- `exact_resume` needs `session` and a `resume` command that refers to `{session}`. Without it every turn starts a new session.
- `structured_errors` needs `errors`. Without it every failure is `unknown`.
- `structured_waiting` needs `waiting`.
- `can_be_stopped` needs nothing: the turn runs under keepgoing's own wrapper. Without it `stop` holds the loop but does not signal a running turn.
- `retries_itself`, `fails_over_itself`, `schedules_itself` and `child_work` are refused for now: a definition has no way to let keepgoing observe them.

### What moves work to a fallback

Your mapping decides it. A code, HTTP status or exit code that you map to a class in the loop's `errors.failover_on` (by default `rate_limit`) **moves work to a fallback**, and so does a `limits` lookup that reports the account blocked.

**Map only a real provider limit to such a class.** A refusal, a policy block or an account restriction is not a limit and must be left unmapped. Unmapped, it is `unknown`, which never moves work: the loop re-prompts within its bounds and then holds for a person. keepgoing is never a way around a provider's safeguards, and it cannot check that a code you mapped really means a limit, so it says every such mapping in words instead:

```text
failover (primary, defined agent example-agent): exit code 75 is treated as a network failure and will move work to a fallback
failover (primary, defined agent example-agent): a result whose error.code is "quota_exceeded" is treated as a rate limit and will move work to a fallback
```

`keepgoing validate` prints one such line per mapping, for the primary and for each defined fallback, and `doctor` reports them as `agent.<target>.failover` with a warning. Read them when you write a definition. Note that `http_status_field` uses keepgoing's own table: 429 is a rate limit, 401 and 403 an authentication failure, 503 and 529 an overloaded provider. If the agent returns 403 for a policy block, do not add `auth` to `failover_on`.

### After a turn that waited on a person

The hold's note is the fixed text keepgoing uses for any interaction it cannot answer. Deal with what the agent asked for in the agent itself, then run `keepgoing resume`. What happens next is not a continuation of the answer you gave: the waiting turn's output is still the latest turn, and a turn that matched `waiting` is never read as finished, even if it also matches `finished`. With the wait set aside it is a failed turn of `unknown` class, so the next check **sends the loop's task prompt again into the same session** as a re-prompt, which counts toward `errors.max_reprompts`. A later turn that ends waiting raises a new hold.

### Saved session

A saved session identity is checked again each time it is read. A value that is not a token (for example after the file was edited or damaged) is never passed to the agent. If the last turn reported its session, that is saved again and used. Otherwise keepgoing cannot name the conversation to continue, and it does not start a new one in its place, because that would lose the agent's context without anyone deciding to: the loop is held for a person with the reason `target gone (archived, deleted, or not found)`. Restore the identifier in `targets/<target>/session` under the state directory, or remove that file to start a new conversation on purpose, then `resume`.

`keepgoing doctor` checks that the working directory exists, resolves the executable of every command the definition names (start, resume, probe, limits) without running any, and lists what the definition lets keepgoing do and what it cannot (`agent.<target>.capabilities` and `agent.<target>.limits`). It queries no version, login or account for a defined agent. The [capability matrix](adapters.md) has a column for defined agents. Guided `init` and `fallbacks add` do not create one; write the entry by hand. The saved session identifier is used to resume but is not shown by `status` or `keepgoing runs`.

## progress

| Field | Default | Meaning |
| --- | --- | --- |
| `repositories` | none | Repositories whose worktrees are fingerprinted. Without this, stall detection is off and an active session is left alone. |
| `stall_after` | `30m` | How long the fingerprint may stay unchanged while the session is active before a nudge. |
| `max_nudges` | `3` | Nudge attempts a stalled session gets, delivered or not, before the loop is marked awaiting user and reports `await_user`. Visible progress or a finished turn resets the count. |
| `min_wake_interval` | `60m` | Minimum gap between a nudge and anything sent before it: a wake, nudge, re-prompt, or handoff. |
| `flag_after_sends` | `6` | Messages sent with no recorded change to the repositories that raise the [no-progress flag](dock.md#the-no-progress-flag): wakes, nudges, re-prompts, notices about failed lanes, and briefs sent on a move to another route or back, each by the send's own result, and turn retries the provider accepted. `0` turns this fact off. A whole number from 0 to 1000. The flag only informs: it never holds, pauses, moves or stops anything. A binary from before this key refuses a configuration that sets it. |
| `flag_after_errors` | `4` | Checks that found an error or retried the turn, with no recorded change to the repositories, that raise the same flag; `0` turns this fact off. A whole number from 0 to 1000. A binary from before this key refuses a configuration that sets it. |

The fingerprint covers every worktree of each repository. It includes `HEAD`, `git status --porcelain` for tracked files, and the size and modification time of tracked files that differ from `HEAD`. Untracked files, file contents, and transcripts are never read.

Without `repositories` the no-progress flag is not available: it reads "not available: no progress source is configured", never "no progress".

Idle wakes follow the supervisor's check cadence, subject to other gates. `min_wake_interval` spaces stall nudges; it does not throttle idle wakes. `max_nudges` resets when a turn ends and is not a loop-wide work budget.

## limits

These three optional fields are the only loop-wide bounds on repeated automatic work. None has a default: with all three omitted, an idle session is woken on every eligible ordinary check for as long as the loop stays enabled and no control hold applies. The `progress` and `errors` caps bound one stalled turn or one error episode and reset afterwards. See [bounds on repeated work](operations.md#bounds-on-repeated-work). In v0.2.0, `validate` and `status` warn when all three are unset; the warning changes no behavior or exit code.

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

At or after this time, the next ordinary check persists an awaiting-user hold before automatic provider observation, yield commands, fingerprints or probes. Stop and existing control holds retain precedence. Omission or an empty string leaves existing behavior unchanged. Past timestamps are allowed and hold on the next check. See [operations](operations.md#loop-deadline) for restart, resume, manual-command and clock semantics. This field is available in v0.1.3 or newer. Optional `max_sends` is a nonnegative integer (zero/omitted disables accounting). It caps total automatic send attempts made while enabled, including failed sends, recovery and handoffs; the count survives restart and resume. See [send allowance behavior](operations.md#total-send-allowance). Optional `no_progress_timeout` is zero/off or a duration of at least `1m`, and requires `progress.repositories`. It holds after elapsed time without verified Git metadata changes, including idle turns. Downtime counts through v0.1.5; v0.2.0 does not count time in which no check ran (beyond one usual interval) or in which the connectivity check found the network down. See [no-progress semantics](operations.md#no-progress-timeout). These limits require v0.1.3 or newer; monetary enforcement is not implemented.

## window

This is optional. `tz` is an IANA zone name. `days` maps `Mon` through `Sun` to a `["HH:MM", "HH:MM"]` span, and the end is exclusive. A span whose end is earlier than its start (or the same) runs past midnight. Its key is the day it starts. Days that are not listed are closed. Scheduled probes also run only inside the window.

Times are read on the local clock in `tz`, including on the days it changes for daylight saving: a `22:00` start opens at 22:00 local time whatever the offset. Each time means the first moment on its day at which the local clock shows that time or later:

- A time the clock skips when it is set forward (for example `02:30` in New York on Sun 10 Mar 2030, when 01:59 is followed by 03:00) opens or closes the window at that jump. A span that lies wholly inside the skipped time is empty that day.
- A time the clock shows twice when it is set back (for example `01:30` in New York on Sun 3 Nov 2030) means its first occurrence. A span ending at `01:30` is closed through the repeated hour; a span starting at `01:30` stays open through it.

Released in v0.2.0; before, a span's times on a day the clock changed were counted in hours from midnight, so every edge after the change was off by the size of the change.

## timing

v0.2.0, and the first part of the planned [usage-aware timing](roadmap.md#usage-aware-timing-planned) (U3). Off by default. Turned on, it probes the loop's first route on a schedule, including while the loop runs on it, so that its allowance has usage readings to [project](fallbacks.md#reading-history-and-projection). **It does not change routes yet**: nothing decides from the readings until the step decision (U4) exists. `validate`, `status` and `doctor` say so: `timing: collecting usage readings for the first route (…); it does not change routes yet`.

```yaml
loop:
  provider: claude-code          # or set loop.probe
  fallbacks: fallbacks.yaml
  probe: {model: example-small}  # optional; the probe's model, see below
  timing:
    enabled: true
    windows: [seven_day]         # the provider's window keys timing acts on
    near_reset: 6h               # default
    probe_every: 12h             # default
```

| Field | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Turns timing on. Each probe is a small paid request. |
| `windows` | required when on | The usage windows timing acts on, by the provider's own keys as `fallbacks list` shows them (for example `seven_day`). Keys are not interpreted. |
| `near_reset` | `6h` | How close to a named window's reported reset counts as near, between 1m and 168h. |
| `probe_every` | `12h` | The interval between timing probes of the first route; at least 1h. |

Timing needs `loop.fallbacks` (its `probe.daily_cap` and `probe.timeout` apply) and a way to probe the first route (`loop.provider` of `claude-code` or `codex`, or `loop.probe`); `validate` refuses it otherwise and names what is missing. It is not available for Cursor loops.

When timing probes run:

- Every `probe_every`, counted from the last scheduled probe of the first route or its last passing probe of any tier.
- Once more for each window instance when a named window's reset, from the latest reading, comes within `near_reset`, unless that reading was already taken that close to the reset. The reset time is known from an earlier reading, so this needs no extra probing to time.
- Only where scheduled probes run: inside the loop's `window`, never on a dry run, and not when the network is down or the check has no time left. They count against the first route's own `probe.daily_cap`, and none runs when the fallbacks file sets `probe.scheduled_disabled` (`validate` warns).

**What it costs.** `validate`, `status`, `doctor` and `fallbacks check --dry-run` state it, for example: `about 14 small paid probes a week for this allowance at a 12h cadence, plus one as each named window (seven_day) nears its reset, never above probe.daily_cap (6 a day); on example-small, the probe model`. The probes use `loop.probe.model` when set, otherwise the route's own model.

**What the readings cover.** A probe reports the windows its model reports. A window for one model alone is reported only on a probe that uses that model, so it is read only when the probe's model is the route's own model; a cheaper probe model does not report it. The cost line says which applies. Probing on the route's own model to read such a window is a decision for the step design (U4), with its cost stated.

**Where the readings come from.** A probe runs the CLI on this computer with its sign-in, or `loop.probe.env`. For a loop on a BB thread that may not be the account BB uses for the thread; `validate` and `doctor` warn whenever timing is on for a BB loop.

## errors

| Field | Default | Meaning |
| --- | --- | --- |
| `max_reprompts` | `3` | Re-prompt attempts, delivered or not, allowed while the session stays in error, before the loop waits for a person. In v0.2.0 it is also the cap on [rejected takeover briefs](operations.md#rejected-takeover-briefs) per fallback, and on rejected switch-back briefs. |
| `backoff` | `10m` | Minimum gap between re-prompt attempts, and between handoff briefs to a fallback that rejected one. |
| `failover_on` | `[rate_limit]` | Error classes that may trigger a handoff: `rate_limit`, `auth`, `overloaded`, or `network`. `unknown` is not allowed. |
| `wait_if_reset_within` | `30m` | When the provider reports a limit reset this soon, wait instead of handing off. |
| `report_wait_after` | `6h` | v0.2.0. When a limit is being waited out with no handoff possible and its reported reset is further away than this, send one `limit_wait` report for that wait. The loop is not held and no decision changes. There is no off value: leaving it out or setting `0` uses the default. To receive nothing, give `report.events` an explicit list without `limit_wait`. See [a long limit wait](operations.md#a-long-limit-wait-with-no-way-out). |

The re-prompt budget resets when the session is seen idle, or when it is active and the Git fingerprint changes; seeing it active is not enough, because a re-prompt itself makes it active. An `auth` error goes to awaiting user unless a handoff happens, because retrying cannot fix it. For a class listed in `failover_on`, a reported reset time is honored: the loop waits for the reset instead of spending re-prompts when no fallback is available. For other classes the reset time is ignored. `failover_on: []` turns handoff off.

## Validation and defaults

- Durations use Go syntax such as `90s`, `10m`, or `4h`.
- `stall_after` and `min_wake_interval` must be at least `1m`. `daemon.interval` must be at least `10s`, and `daemon.control_poll` at least `100ms`.
- A zero or omitted number means its default. `max_reprompts: 0` means 3, not zero.
- A window span whose start equals its end is rejected.
- The BB CLI is given 30 seconds per call.

## network

The connectivity check made before an error re-prompt, a handoff, a stall nudge and fallback probes, among the situations listed under network outages. See [network outages](operations.md#network-outages).

| Field | Default | Meaning |
| --- | --- | --- |
| `check` | `on` | `off` disables the check; nothing is gated. Quote the value, since YAML reads bare `on` and `off` as booleans. |
| `url` | none | An `http` or `https` URL with a host that answers a GET itself when the network is up. Any status counts except a redirect or `511`; redirects are not followed. With neither `url` nor `command`, the `KEEPGOING_NET_PROBE` (a shell command line) and `KEEPGOING_NET_PROBE_URL` variables apply, then `https://api.github.com/zen`. |
| `command` | none | An argv whose exit 0 means online. Set this or `url`, not both. |
| `timeout` | `8s` | Between `1s` and `30s`. It applies to every kind of check. |
| `verify_after` | `1h` | Once the check has failed this long, ask the provider directly with one probe and attempt a `network_wait` report; then again after twice as long each time, up to a week. If the probe passes or is refused with `rate_limit` or `overloaded`, the check is set aside for that outage. At least `1m`. |

## report, yield, daemon

See [operations.md](operations.md).

| Field | Default |
| --- | --- |
| `report.url_env`, `report.secret_env` | `KEEPGOING_WEBHOOK_URL`, `KEEPGOING_WEBHOOK_SECRET` |
| `report.secret_file` | none |
| `report.source` | the loop name |
| `report.scheme` | `standard-webhooks` (or `bearer`) |
| `report.format` | `standard` (or `text`) |
| `report.events` | `handoff, switch_back, degraded, recovered, await_user, limit_wait, network_wait, child_error, child_recovery, complete, stop, error` (the `complete` event requires v0.1.3 or newer; `limit_wait`, `network_wait`, `child_error` and `child_recovery` are v0.2.0; `child_recovery` is sent only by a loop with `bb.children.retry_errored`; `check_gap`, also v0.2.0, is not a default and must be named) |
| `report.timeout` | `20s` |
| `yield.busy_command`, `yield.timeout` | required, `10s` |
| `daemon.interval`, `daemon.control_poll` | `5m`, `2s` |
| `tick_timeout` | `3m`, or `probe.timeout` plus `90s` when that is longer. A check ends within this plus 90 seconds. Minimum `2m40s`; the connectivity check needs `network.timeout` more than that to have its full time; with fallbacks an explicit value must be at least `probe.timeout` plus `90s`. See [check time budget](operations.md#check-time-budget) |

## Tick output

Each tick prints one JSON line:

```json
{"at":"2026-11-09T12:00:00Z","loop":"example-loop","target":"primary","control":"enabled","state":"error","errorClass":"rate_limit","action":"handoff","reason":"error: rate_limit","window":"no window configured","handoff":{"from":"primary","to":"codex-thread","reason":"rate_limit","tried":["codex-thread"]}}
```

`action` is one of `none`, `wake`, `nudge`, `reprompt`, `await_user`, `stop`, `handoff`, or `switch_back`. Optional fields include `resetsAt`, `redundancy`, `probes`, `yielded`, `dryRun`, `warnings`, `outcome` (`succeeded`, `failed` or `dry_run`; absent when the check could not be recorded) and `missingWorktrees`. A probe entry with `skipped` produced no result. With `--dry-run`, the tick decides and records but sends nothing and runs no probes.
