# Configuration

`keepgoing.yaml` describes one loop. Relative paths resolve against the directory holding the file, and a leading `~/` expands to the home directory. Unknown fields are errors. Editors that support JSON Schema can use [schema/keepgoing.schema.json](../schema/keepgoing.schema.json). Fallbacks have their own file, described in [fallbacks.md](fallbacks.md).

```yaml
version: 1
loop:
  name: example-loop
  state_dir: ~/.local/state/keepgoing/example-loop
  adapter: bb                     # bb | process | claude | codex
  provider: claude-code           # optional for the primary; used in reports
  group: claude-example-account   # quota pool; same-group fallbacks are skipped on a limit
  bb: {thread: thr_example123}
  prompt: Continue the loop.      # or prompt_file
  handoff_file: HANDOFF.md        # optional
  progress: {repositories: [~/src/example-app], stall_after: 30m, min_wake_interval: 60m}
  window: {tz: UTC, days: {Mon: ["08:00", "23:00"]}}
  errors: {max_reprompts: 3, backoff: 10m, failover_on: [rate_limit], wait_if_reset_within: 30m}
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
| `adapter` | yes | `bb`, `process`, `claude`, or `codex`, with the matching block below. |
| `provider`, `group` | no | `group` defaults to `provider`, then to `primary`. Set `group` so a limit on this account skips fallbacks that share it. |
| `prompt` or `prompt_file` | one of them | The text sent on wake, nudge, and re-prompt. The file is read on every send, so you can edit it while the loop runs. |
| `handoff_file` | no | Sent to a fallback when it takes over. When the file is missing, a brief is generated. |

## bb

| Field | Default | Meaning |
| --- | --- | --- |
| `thread` | required | The BB thread id. |
| `cli` | `bb` | Path to the bb executable. |
| `model`, `reasoning` | none | Applied with `bb thread update` before the first message after a handoff to this agent. |

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
{"state": "idle", "errorClass": "rate_limit", "queued": 0, "background": 0, "gone": false}
```

`state` is one of `active`, `idle`, `error`, `stopping`, or `pending`. Anything else is `unknown`. `errorClass` is read only when the state is `error`. Stderr is never captured.

## claude

keepgoing runs `claude -p --output-format json` itself, one turn per send. Each turn after the first resumes the saved session.

| Field | Default | Meaning |
| --- | --- | --- |
| `workdir` | required | Where the agent runs. |
| `cli` | `claude` | Path to the executable. |
| `model`, `effort`, `permission_mode` | none | Passed through as flags. |
| `max_budget_usd` | none | Passed as `--max-budget-usd`. |
| `extra_args` | none | Appended as is. |

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

A running headless turn cannot be steered or queued into. A nudge to a stalled headless turn is recorded as a warning instead.

## progress

| Field | Default | Meaning |
| --- | --- | --- |
| `repositories` | none | Repositories whose worktrees are fingerprinted. Without this, stall detection is off and an active session is left alone. |
| `stall_after` | `30m` | How long the fingerprint may stay unchanged while the session is active before a nudge. |
| `min_wake_interval` | `60m` | Minimum gap between nudges. |

The fingerprint covers every worktree of each repository. It includes `HEAD`, `git status --porcelain` for tracked files, and the size and modification time of tracked files that differ from `HEAD`. Untracked files, file contents, and transcripts are never read.

## window

This is optional. `tz` is an IANA zone name. `days` maps `Mon` through `Sun` to a `["HH:MM", "HH:MM"]` span, and the end is exclusive. A span whose end is earlier than its start runs past midnight. Its key is the day it starts. Days that are not listed are closed. Scheduled probes also run only inside the window.

## errors

| Field | Default | Meaning |
| --- | --- | --- |
| `max_reprompts` | `3` | Re-prompts allowed while the session stays in error, before the loop waits for a person. |
| `backoff` | `10m` | Minimum gap between re-prompts. |
| `failover_on` | `[rate_limit]` | Error classes that may trigger a handoff: `rate_limit`, `auth`, `overloaded`, or `network`. `unknown` is not allowed. |
| `wait_if_reset_within` | `30m` | When the provider reports a limit reset this soon, wait instead of handing off. |

The re-prompt budget resets as soon as the session is seen active or idle. An `auth` error that is not in `failover_on` goes straight to awaiting user, because retrying cannot fix it. When the provider reports a reset time and no fallback is available, the loop waits for the reset instead of spending re-prompts.

## report, yield, daemon

See [operations.md](operations.md).

| Field | Default |
| --- | --- |
| `report.url_env`, `report.secret_env` | `KEEPGOING_WEBHOOK_URL`, `KEEPGOING_WEBHOOK_SECRET` |
| `report.secret_file` | none |
| `report.source` | the loop name |
| `report.scheme` | `standard-webhooks` (or `bearer`) |
| `report.format` | `standard` (or `text`) |
| `report.events` | `handoff, degraded, recovered, await_user, stop, error` |
| `report.timeout` | `20s` |
| `yield.busy_command`, `yield.timeout` | required, `10s` |
| `daemon.interval`, `daemon.control_poll` | `5m`, `2s` |

## Tick output

Each tick prints one JSON line:

```json
{"at":"2026-11-09T12:00:00Z","loop":"example-loop","target":"primary","control":"enabled","state":"error","errorClass":"rate_limit","action":"handoff","reason":"error: rate_limit","window":"no window configured","handoff":{"from":"primary","to":"codex-thread","reason":"rate_limit","tried":["codex-thread"]}}
```

`action` is one of `none`, `wake`, `nudge`, `reprompt`, `await_user`, `stop`, or `handoff`. Optional fields include `resetsAt`, `redundancy`, `probes`, `yielded`, `dryRun`, and `warnings`. With `--dry-run`, the tick decides and records but sends nothing and runs no probes.
