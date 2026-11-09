# Configuration

`keepgoing.yaml` describes one loop. Relative paths resolve against the directory holding the file, and a leading `~/` expands to the home directory. Unknown fields are errors.

```yaml
version: 1
loop:
  name: example-loop
  state_dir: ~/.local/state/keepgoing/example-loop
  adapter: bb                 # bb | process
  bb:
    thread: thr_example123
    cli: bb                   # optional, default bb
  prompt: Continue the loop.  # or prompt_file: HANDOFF.md
  progress:
    repositories: [~/src/example-app]
    stall_after: 30m
    min_wake_interval: 60m
  window:
    tz: UTC
    days:
      Mon: ["08:00", "23:00"]
      Fri: ["22:00", "02:00"]  # runs past midnight into Saturday
  errors:
    max_reprompts: 3
    backoff: 10m
```

## loop

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | Lowercase letters, digits, hyphens. Appears in every event. |
| `state_dir` | yes | Where control, state, log, and lock files live. Keep it outside any repository. |
| `adapter` | yes | `bb` or `process`. |
| `prompt` / `prompt_file` | one of them | The text sent on wake, nudge, and re-prompt. The file is read on every send, so you can edit it while the loop runs. |

## bb adapter

| Field | Default | Meaning |
| --- | --- | --- |
| `thread` | required | The BB thread ID to supervise. |
| `cli` | `bb` | Path to the bb executable. |

The adapter reads `bb thread show --json`. On an error status it reads the newest log events and classifies a structured `code`, `status`, or similar field if one is present. Message text is never used. A thread that is archived, deleted, or not found is reported as gone, and nothing is sent.

## process adapter

| Field | Default | Meaning |
| --- | --- | --- |
| `status_command` | required | argv. Prints a JSON status object on stdout. |
| `resume_command` | required | argv. Receives the prompt on stdin and the delivery mode (`auto` or `queue`) in `KEEPGOING_MODE`. |
| `stop_command` | none | argv. Stops the session. Without it, a stop request just means nothing more is sent. |
| `exit_codes` | none | Map of status-command exit code to error class, for example `{75: rate_limit}`. Any other non-zero exit is an error of class `unknown`. |
| `env` | none | Extra environment variables for all three commands. |
| `timeout` | `20s` | Per command. |

The status object:

```json
{"state": "idle", "errorClass": "rate_limit", "queued": 0, "background": 0, "gone": false}
```

`state` is one of `active`, `idle`, `error`, `stopping`, `pending`; anything else is `unknown`. `errorClass` is read only when state is `error` and must be one of `rate_limit`, `auth`, `overloaded`, `network`, `unknown`, or a well-known code such as `429`. Stderr is never captured, so a command may print whatever it likes there.

## progress

| Field | Default | Meaning |
| --- | --- | --- |
| `repositories` | none | Repositories whose worktrees are fingerprinted. Without this, stall detection is off and an active session is left alone. |
| `stall_after` | `30m` | How long the fingerprint may stay unchanged while the session is active before a nudge. |
| `min_wake_interval` | `60m` | Minimum gap between anything the tool sends. |

The fingerprint covers every worktree of each repository: `HEAD`, `git status --porcelain` for tracked files, and the size and modification time of tracked files that differ from `HEAD`. Untracked files, file contents, and transcripts are never read.

## window

Optional. `tz` is an IANA zone name. `days` maps `Mon` through `Sun` to a `["HH:MM", "HH:MM"]` span. The end is exclusive. A span whose end is earlier than its start runs past midnight into the next day; the key is the day it starts. Days that are not listed are closed.

## errors

| Field | Default | Meaning |
| --- | --- | --- |
| `max_reprompts` | `3` | Re-prompts allowed while the session stays in error, before the loop is marked awaiting user. |
| `backoff` | `10m` | Minimum gap between re-prompts. |

The budget resets as soon as the session is seen active or idle. An `auth` error skips the budget and goes straight to awaiting user, because retrying cannot fix it.

## Control commands

`enable`, `disable`, `pause`, `resume`, `stop`, and `await-user` edit `control.json` atomically and append a `control` event to `runs.jsonl`. `resume` clears pause, stop, and awaiting-user together. Every command accepts `--note "text"` to leave a reason. The next `tick` honors the new state before anything else.

## Tick output

One JSON line per tick:

```json
{"at":"2026-11-09T12:00:00Z","loop":"example-loop","control":"enabled","state":"idle","action":"wake","reason":"idle","window":"no window configured"}
```

`action` is one of `none`, `wake`, `nudge`, `reprompt`, `await_user`, `stop`. A `warning` field appears when the adapter could not stop a running turn. `--dry-run` decides and records without sending.

Exit codes: `0` ok, `1` failed (the provider could not be observed or the send failed), `2` usage or config error, `75` another tick holds the lock.
