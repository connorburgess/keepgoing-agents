# Fallbacks

`fallbacks.yaml` is the maintained list of agents the loop may move to when its current agent cannot continue. Point the loop at it with `loop.fallbacks`.

## Add a fallback with guided setup

Unreleased source; v0.1.5 does not include this command. For an existing loop:

```sh
keepgoing fallbacks add -c /private/example-loop/keepgoing.yaml
```

The guide runs four steps, then a preview and a confirmation. An answer that cannot be used is explained and asked again; nothing is written until the end.

1. **Provider.** It lists the agent CLIs found on `PATH` (looked up, never run) and which can be a fallback: headless `claude` and `codex`, or `bb` for an existing BB thread (see [BB threads](#bb-threads)).
2. **Account.** An account directory you have already signed in to (`CLAUDE_CONFIG_DIR` or `CODEX_HOME`; a leading `~/` is expanded), or blank for the CLI's current login, and the quota group. The suggested group is the directory's name, otherwise the provider, with a numeric suffix when that label is taken.
3. **Model.** The model for the fallback's turns and, optionally, a cheaper one for scheduled probes. Blank keeps the CLI's configured model. Names are not looked up or validated.
4. **Handoff.** The place in the handoff order when other fallbacks exist, and whether to turn on automatic handoff when it is not already on. The default is no.

The preview shows the candidate, the full handoff order with the new entry marked, the handoff trigger, the redundancy rule (with a warning when the listed candidates cannot meet it), the probe policy and the files to be changed. Nothing is written before `yes`; the default answer is `no`.

Scripts use the same path:

```sh
keepgoing fallbacks add -c /private/example-loop/keepgoing.yaml --non-interactive \
  --agent claude --account-dir /private/accounts/claude-b --group pool-b \
  --model example-model --position 1 --dry-run --json
```

`--non-interactive` requires `--agent` and writes without asking unless `--dry-run` is set. Other flags: `--probe-model`, `--id`, `--workdir`, `--enable-handoff`, and `--probe-daily-cap`, which is accepted only when the command creates the fallbacks file. `--json` prints a `fallback_add` object with `schemaVersion: 1`: the candidate, order, redundancy, changed files, `warnings`, `notes` and the follow-up commands as argument arrays. Its `handoff` object reports `automatic` and `wasAutomatic` (whether a handoff can happen after and before the change), `changed`, the resulting `failoverOn` and whether `failoverOnWritten`. Account-directory and agent executable paths are withheld from JSON.

### BB threads

A fallback can be an existing BB thread: this loop's own conversation continued on another model, or a different thread on another provider.

```sh
keepgoing fallbacks add -c /private/example-loop/keepgoing.yaml --non-interactive \
  --agent bb --thread thr_example123 --provider claude-code \
  --model example-model --reasoning high --dry-run
```

A thread has one provider, so to continue on another provider without preparing a thread by hand, let the handoff start one (unreleased source). Nothing is created when you add the fallback:

```sh
keepgoing fallbacks add -c /private/example-loop/keepgoing.yaml --non-interactive \
  --agent bb --new-thread --provider codex --model example-model --dry-run
```

In the guide, type `new` at the thread question. See [hand off to a new conversation](#hand-off-to-a-new-conversation) for what happens at the handoff.

In the guide, steps 2 and 3 become:

2. **Thread.** If you agree (the default answer is no), keepgoing runs `bb thread list --json` and shows live, visible threads nine at a time by title, provider and status, this loop's own thread first; `more` shows the next page. Pick a number or paste a thread ID; an ID that is not in BB's list is used as typed, with a note. Thread IDs are not printed unless you type `ids` or pass `--show-ids`. If you decline, or the listing is unavailable, you enter the ID and the provider.
3. **Model.** With the same agreement, `bb provider models <provider> --json` supplies a pick list and the reasoning levels each model accepts. A name you type instead is not checked. Blank keeps the thread's own model, except for a thread the loop already uses, where a model different from every recorded use is required (compared without regard to case). Then the quota group (one usage limit; the suggestion is the model's name, otherwise the provider), a cheaper model for scheduled checks, and an optional account directory for the local check.

Those two listings are the only commands this path can run, only in the guided mode and only after `yes`. They are read-only: no message is sent, no thread is created, moved or changed, and no model is called. Flags never run `bb`. Only the structured output is read; an error reply or unexpected shape means "unavailable", and a listing is abandoned after 20 seconds or 8 MiB. Text taken from a listing (titles, providers, statuses, display names) is stripped of control and format characters, including escape sequences and bidirectional overrides, and shortened before it is printed; a listed model name containing such characters is not offered.

What the preview states, because keepgoing cannot verify it:

- **The account.** BB chooses the account for a thread; keepgoing cannot see or switch it. A fallback must have a quota group of its own, so giving it one is your claim that it has a separate usage limit. keepgoing cannot check that claim. A thread that shares a limit with a target already in the loop cannot help and should not be added.
- **The check.** A BB fallback is confirmed with the local `claude` or `codex` CLI, as [probes](#probes) describe. A pass shows that CLI's login works; it shows BB's account works only if BB uses the same login. `--probe-account-dir` points the check at another existing login.
- **The same conversation.** Adding a thread the loop already uses means a handoff switches that thread's model (and reasoning level, if set) with `bb thread update` and continues in the same conversation, with the same history. It is very likely the same account, so it helps only if the other model has its own limit, which only the check before a handoff tests. The provider must be the one the loop already records for that thread. If the primary's `bb.model` is unset, returning to the primary leaves the thread on the fallback's model; the preview says so.
- **The model.** Marked as from BB's list or not checked. With no model set, the check before a handoff uses the local CLI's default model, which may differ from the thread's.

Refused without writing: a missing or malformed thread ID; a provider other than `claude-code` or `codex` (no built-in probe); an unknown reasoning level; a thread the loop already uses with no model, the same model or a different provider; in the guide, a `--provider` that contradicts what BB lists for the picked thread; `--account-dir` or `--workdir`; a missing probe account directory; and a missing local `claude` or `codex` CLI, since the fallback could then never be confirmed. `--thread`, `--provider`, `--reasoning`, `--probe-account-dir` and `--show-ids` are refused for headless agents. In JSON, `candidate.thread` is `same_as_primary`, `same_as_fallback` or `other`, `candidate.modelSource` is `bb_list` or `unchecked`, and `candidate.threadId` appears only with `--show-ids`.

### Handoff stays a choice

A handoff can happen only when fallbacks exist and `errors.failover_on` is not empty. Because an absent `errors.failover_on` means `rate_limit`, adding the first fallback to such a loop would start handoff on its own. The command decides it explicitly instead:

- Handoff already on (fallbacks exist and the setting is not empty): nothing changes. `--enable-handoff` has no effect and the preview says so.
- You choose handoff: an empty or absent setting is written as `[rate_limit]`; an existing non-empty setting is kept.
- You do not choose it and the loop's setting would start handoff with the first fallback: `errors.failover_on: []` is written, and the preview names the setting it replaces.

### What it writes

- With an existing fallbacks file, the candidate is inserted at the chosen position. Other candidates, rules and comments are kept. The file is re-emitted, so indentation is normalized, CRLF line endings become LF and blank lines between entries are dropped.
- Without one, it creates `fallbacks.yaml` beside the config (redundancy `min_healthy: 1`, `span: group`; scheduled probes every 12 hours, 24-hour TTL, daily cap 2) and sets `loop.fallbacks`. `keepgoing.yaml` is re-emitted with the same normalization.
- `keepgoing.yaml` is not rewritten when neither `loop.fallbacks` nor `errors.failover_on` changes. Replaced files keep their permission bits; a created file is owner-only.

Writes happen under the loop's tick lock with the guards `retarget` uses: the caller must be the owning BB thread when the loop has one, no BB transfer may be pending, and the files must be unchanged since the preview. A refusal exits 1 for ownership and 75 for a running check, a pending transfer or changed sources. A running daemon uses the new order after its next configuration reload.

### What it refuses

Without writing anything:

- Cursor, process or unknown agents, and any loop with a Cursor primary.
- A quota group equal to the primary's or another candidate's.
- The same login as an existing target with the same model. Logins are compared by provider and account directory (symlinks resolved); the CLI's current login counts as one login. With a different model the candidate is accepted with a warning, since it helps only when that model has its own limit. Only headless targets can be compared: a BB primary's account is not visible, which the preview states. Other credentials in the environment are not inspected.
- A config path or fallbacks file that is a symbolic link, because relative paths resolve against the directory the file is opened from.
- A repeated or invalid id, a missing account or project directory, a CLI that is not on `PATH`, an out-of-range position, a model containing spaces or starting with a hyphen, and `--probe-daily-cap` when the fallbacks file already exists.

### Afterwards

The command never runs an agent or a probe, logs in, creates an account or copies credentials. It cannot tell whether the directory is signed in, which subscription it uses, or whether the model exists. It prints the commands to check that yourself:

```sh
keepgoing fallbacks check -c /private/example-loop/keepgoing.yaml --id claude-pool-b --exact --dry-run   # free preview
keepgoing fallbacks check -c /private/example-loop/keepgoing.yaml --id claude-pool-b --exact             # one real request
keepgoing fallbacks list  -c /private/example-loop/keepgoing.yaml
```

Until a probe passes, the candidate is not healthy and is not used; `fallbacks list` names candidates that were never probed.

Not covered yet: detecting accounts or logins, listing models for headless CLIs, BB threads on providers other than `claude-code` and `codex`, process/Cursor candidates, removing a loop's last fallback, and a BB plugin UI. Tests use fake CLI discovery and temporary loops; no provider was called. See the [roadmap](roadmap.md#11-useful-supervision-features-from-product-analysis).

## Change or remove a fallback

Unreleased source; v0.1.5 does not include these commands. They need a loop that already has fallbacks.

```sh
keepgoing fallbacks handoff on  -c /private/example-loop/keepgoing.yaml   # or: off
keepgoing fallbacks move claude-pool-b --to 1 -c /private/example-loop/keepgoing.yaml
keepgoing fallbacks remove claude-pool-b -c /private/example-loop/keepgoing.yaml
```

Each command prints what will change, the handoff order (marking the target in use now) and the files it will update, then asks; the default answer is `no`. `--yes` writes without asking, `--dry-run` only previews, and `--json` (with `--yes` or `--dry-run`) prints a `fallback_handoff`, `fallback_remove` or `fallback_move` object with `schemaVersion: 1` and an `outcome` of `preview`, `changed` or `unchanged`. Nothing is run: no agent, model or check. The guards are those of `fallbacks add`: the owning BB thread, no pending transfer, the tick lock, unchanged files since the preview, and no symbolic links.

- **`handoff on|off`.** `on` makes rate limits hand off: an empty or absent list becomes `[rate_limit]`, and a list of other error classes gains `rate_limit` and keeps the rest (JSON reports `rateLimitAdded`). It does nothing only when `rate_limit` is already listed. `off` writes `[]`, and does nothing when handoff is already off. A custom list is not remembered: the preview says that turning handoff on again restores rate limits only. Turning handoff off does not move a loop that is running on a fallback back to its primary; use `keepgoing retarget primary` for that. Scheduled checks of the fallbacks continue either way.
- **`move ID --to N`** changes only the order, which decides which fallback is tried first at the next handoff. Recorded health is kept and the loop stays on its current target.
- **`remove ID`** takes the entry out. It refuses the fallback the loop is running on (move the loop first with `keepgoing retarget primary`) and a loop's only fallback, because a fallbacks file cannot be empty; turn handoff off instead. If the fallbacks left cannot meet the redundancy rule, the preview warns that handoffs will be refused.

What `remove` does with the fallback's records, decided again under the lock when the change is written, not from the preview:

| Record | What happens |
| --- | --- |
| Saved check results in `health.json` | deleted |
| Count of rejected handoff messages in `state.json` | deleted |
| Saved session and turn files in `targets/<id>/` | moved to `removed-targets/<id>-<time>/` |
| Saved output from failed checks in `probe-failures/<id>/` | kept |

So a fallback added later under the same id starts a new session with no check results, and nothing is thrown away that cannot be recovered except those two records. If a step after the file edit fails, the command says which, and running the same `remove ID` again finishes the cleanup even though the id is no longer in the file.

How the files are edited:

- `move` and `remove` cut and paste the entry's own lines. A comment block directly above an entry, with no blank line between, belongs to it and moves or goes with it; the preview lists those lines. Headings and notes set apart by a blank line stay where they are. Every other byte is left as it was, including line endings, blank lines and inline `{...}` values. A candidates list written inline on one line is refused; edit it by hand.
- `handoff` changes only the list itself when `errors.failover_on` is written inline (`failover_on: [...]`), keeping a comment after it. When the key is absent or written as a block list, `keepgoing.yaml` is re-emitted as described for `fallbacks add`: comments and permission bits are kept, indentation is normalized, CRLF becomes LF and blank lines between entries are dropped.

A running daemon rereads its configuration on its interval or at the next control change and uses the new setup from then on; until it has, its checks are skipped because the files no longer match what it loaded. See [loaded configuration checks](operations.md#loaded-configuration-checks).

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

In unreleased source a BB candidate may instead set `bb: {new_thread: true, ...}` and name no thread. See [hand off to a new conversation](#hand-off-to-a-new-conversation).

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

A failed probe also saves its raw output to `probe-failures/<candidate>/` in the state directory. Captures are owner-only, capped at 64 KB, and only the newest five per candidate are kept. In unreleased source the cap keeps the end of the output, where a stream's result or error event is, instead of the beginning. With the `stream-json` Claude probe a capture holds the event stream instead of a single result object, without the CLI's `system`/`init` message: that message describes the local installation and is omitted whole, so its contents were not inspected. They exist so an error shape keepgoing does not yet recognise, such as a provider's usage-limit event, can be mapped into a structured class later. keepgoing never reads them to make a decision. A candidate is healthy while its last probe passed within `probe.ttl`.

## Usage readings

Unreleased source records how much of an account's quota a provider says is used, when a probe that already runs happens to carry that information. It is a display value only. No decision reads it: health, redundancy, handoff and switch-back behave exactly as before, and a candidate shown at 100% that passes its probe is still healthy. Switching at a threshold is [planned](roadmap.md#quota-monitoring-planned), not implemented.

| Probe kind | Source | When a reading is recorded |
| --- | --- | --- |
| `claude` | `rate_limit_event` messages in the probe's own `stream-json` output | Whenever the probe's output contains the event with usable windows |
| `codex` | The app-server's `account/rateLimits/read` reply | Only when that read already happens: after a probe fails without a recognized code. A healthy Codex probe carries no reading. |
| `command`, and every other provider including Cursor | none | Never; shown as unavailable |

No provider call is added for this. keepgoing does not read stored credentials or call a provider usage endpoint. To get the event, the Claude probe now asks for `--output-format stream-json --verbose` instead of `json`; it is still one request, judged from the same final result object. That equivalence is checked with fixtures and one live probe on one CLI version (see the end of this section); a CLI that rejects those flags would fail its probe.

The Claude event is not a documented contract. Its shape was observed by the project coordinator on Claude Code 2.1.318 with live one-turn probes on two models, where each window under `rate_limit_info.unifiedWindows` carried a `utilization` between 0 and 1, a `resetsAt` time and a `status`. Parsing is deliberately narrow:

- No event, another shape, or a window without a numeric `utilization` between 0 and 1 gives no reading for that window. It is never an error and never changes the probe result.
- Only `utilization` is required. A `resetsAt` or `status` that is missing, of another type or implausible is left out and the window is kept, shown without that field.
- Window names are opaque keys. keepgoing does not assume which windows exist or what they mean.
- When a probe emits several events, later ones override earlier ones per window.
- Names and status values must be short identifiers (letters, digits, `_`, `-`, `.`, at most 64 characters); anything else is dropped. At most eight windows are kept.

One reading per candidate is stored in `health.json` under `usage`: the observation time, the source, an account identity, and for each window its key, used fraction, optional reset time and optional status. No raw output is stored there. A later probe without a reading leaves the earlier one in place, so a reading can be older than the last probe; its own time is always shown. Health files written before this field existed, and stored values that are not a valid reading, load as unavailable.

A reading belongs to the account it was read from. The identity is a hash of the candidate's provider, quota group, probe kind, probe executable and probe environment; no path or value is stored. When a candidate ID is pointed at another account, the stored reading stops being shown at once and is dropped by the next probe. Renaming a candidate, or changing its model or target thread, keeps it.

A reading also goes out of date. A window whose reset time has passed is shown as `stale` with that time and no percentage, since the recorded figure describes a window that has since reset. When every window is stale, the reading's state is `stale`. A window with no reset time cannot be judged this way and stays as recorded until a newer reading replaces it, so check its age.

`keepgoing fallbacks list` prints a `usage <id>:` line per candidate. `keepgoing status` prints a `usage:` line for the primary and each candidate when fallbacks are configured, and none otherwise, because only probed targets can have a reading. Both show each window's percentage (rounded down, so only a full window shows 100%), reset time and status, then the reading's age and source, or `unavailable`. Their JSON carries `usage` with `state` (`available`, `stale` or `unavailable`), `observedAt`, `ageSeconds`, `source` and `windows`, each window with a `stale` flag.

What a reading does not tell you:

- `unavailable` and `stale` mean there is no current figure. They do not mean usage is low.
- A reading describes the login the probe ran with. For a BB candidate that is the account BB uses only when they share the login, as with probe health.
- It is as fresh as the probe that carried it. Nothing refreshes it between probes, and BB does not expose the event for turns it runs.
- The primary is probed only for switch-back, so it usually shows unavailable.
- Headless Claude turns still use `json` output and record no reading.

Verification uses fictional fixtures only: malformed, partial, multi-event and absent events, out-of-range values, older health files, repointed candidates, passed reset times, and CLI output. Live evidence is one live manual probe by the project coordinator, with a binary built from this source, of one headless Claude candidate on Claude Code 2.1.318 and macOS arm64: `fallbacks check --id` exited 0, the candidate was recorded healthy with its credential expiry, one `seven_day` window was recorded and shown by `fallbacks list` and `status`, and no failure capture was written. Scheduled and exact tiers, a failing probe, Codex readings, other CLI versions and Windows have not been exercised live.

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

A candidate is healthy while its last probe passed within `probe.ttl`. That holds whether or not its next scheduled probe is due: a due probe is a refresh, not a precondition. So when a handoff is due and the rule is met on results inside their ttl, keepgoing probes the handoff's target exactly and hands off without first refreshing the others (unreleased; before, the due scheduled probes ran first, and a candidate found down by one could turn the handoff into a refusal). The candidate that receives the loop is always confirmed by an exact probe in the same check; the others count toward the rule on evidence up to `probe.ttl` old. Shorten `probe.ttl` if that is too old for you.

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

The candidate then receives a brief and becomes the active target. If `loop.handoff_file` exists, its text is sent as is. Otherwise keepgoing generates a brief that names the loop, what happened, and the repositories. The brief asks the agent to honor stop and pause first, inspect current state, and recover work without duplicating it. The goal from the loop's prompt follows. In unreleased source, when the conversation being left had child conversations still working, either brief also carries one fixed sentence with their number, saying they are not supervised or moved; see [BB child awareness](operations.md#bb-child-awareness).

In unreleased source, a candidate that passes its exact probe but then rejects the takeover (its adapter cannot be built, its model cannot be applied, or the brief send fails) is skipped for `errors.backoff`, and for the rest of the error episode after `errors.max_reprompts` rejections. Other candidates keep their own counts. See [rejected takeover briefs](operations.md#rejected-takeover-briefs).

## Hand off to a new conversation

Unreleased source; **offline fixtures with a fake `bb` only, nothing live**. A BB fallback normally names a thread that already exists, and a BB thread cannot change provider. So moving from, say, a Codex primary to Claude inside BB needed a thread someone had created beforehand. A candidate can now say "start a new thread on this provider and model" instead:

```yaml
candidates:
  - id: claude-new-conversation
    adapter: bb
    provider: claude-code            # required: the new thread's BB provider
    group: claude-example-account
    bb: {new_thread: true, model: example-model, reasoning: high, permission_mode: accept-edits}
    probe: {model: example-probe-model}
```

This is the only case in which keepgoing creates a conversation. It is opt-in per candidate; a configuration without `new_thread` behaves exactly as before, and keepgoing never asks BB to create or list a thread for it.

**What happens at the handoff.** The candidate is probed like any other. After its exact probe passes, keepgoing runs one `bb thread spawn` with:

- the project and environment of the conversation being continued, read with `bb thread show` (or `bb.project` and `bb.environment` when you set them; they are required when the loop is leaving a target that is not a BB thread);
- the candidate's `provider`, `model` and `reasoning`;
- an explicit `--permission-mode` (see below), so the new thread never takes a project default;
- the title `keepgoing: <loop name> (continued, <token>)`, so you can tell keepgoing started it;
- the handoff brief as the first prompt, on stdin, followed by the line `Continue from @thread:<id>.` naming the earlier conversation, and a fixed paragraph on loop ownership (see below).

The new thread then becomes the active target and is supervised like any other. Each handoff to such a candidate creates a fresh thread; an earlier one is left where it is.

**The conversation that was left** is not stopped, archived, re-parented or sent anything by keepgoing. It stays as it was. Two things follow from that:

- BB may itself retry that conversation later, for example when its provider limit resets. keepgoing does not prevent this and no longer observes that thread, so two conversations can then be active on the task.
- The new thread works in the **same environment and checkout** as the one it continues. Nothing isolates the two.

**Permission mode.** `bb.permission_mode` is `accept-edits` (the default), `auto` or `full`, and is always passed to `bb thread spawn`. With `accept-edits` the new thread asks before anything but file edits, so an unattended loop may stop on a permission question until a person answers; that is the conservative choice. `auto` lets BB decide, and `full` acts without asking. `fallbacks add` asks for it and shows it in the preview.

**Exactly one thread per handoff, and every attempt counts.** Before `bb thread spawn` runs, keepgoing saves its intent to `state.json`, including the title with its random token, and counts the attempt against the cap in the same write. Then:

- If bb returns the new thread, it is recorded and the move completes.
- If the `bb` CLI reports rejecting the invocation itself (its `unknown_command`, `unknown_option`, `missing_required`, `unexpected_argument`, `invalid_value` or `missing_command` codes) **and** ended on its own **and** no thread with the title is listed, nothing was created. This is the only outcome treated that way: the attempt is uncounted, it counts as a [rejected takeover brief](operations.md#rejected-takeover-briefs) for that candidate under the same per-fallback cap and spacing, and the next candidate is tried in the same check. The code alone is not trusted: the thread is looked up first, and a command that was killed or timed out is never a refusal, whatever it printed.
- After **any** failure (bb was killed or timed out, printed nothing usable, reported an error from BB or the provider, or reported a refusal), keepgoing looks for a thread with that exact title in that project and environment straight away. Exactly one match is the thread, whatever bb went on to report, and the handoff completes.
- If it is not found and bb did not report such a refusal, that proves nothing: the thread may have been renamed, archived or not listed yet. The attempt **stays counted**, the loop is **held for a person** with an `await_user` report naming the title, the spawn is not retried, and the loop is handed to no other fallback, because a second conversation could be started on the same task. The same applies when BB cannot be asked or more than one thread has the title, and when the supervisor stopped between asking bb and recording the answer (the next check does the lookup).
- If the `bb` CLI could not be started at all (missing or not executable), the hold says so and that most likely nothing was created. It is still held and counted, because nothing confirmed it.
- `resume` looks again: found, the handoff completes; otherwise the loop is held again.
- `retarget` settles it by hand. It looks for the thread first. Found, the thread is put on record for its candidate. In every other case (not listed, BB cannot be asked, more than one match) `retarget` refuses and changes nothing, unless you add `--discard-pending-thread`.

  **Before you discard, find the thread in BB and stop or archive it.** Not being listed under its title proves nothing: it may have been renamed or archived. Once discarded, keepgoing no longer knows about it. The loop can then hand off to another fallback, and after an hour create another thread, so an orphan you left running means two live conversations on the same task. The discarded attempt stays counted against the cap.

  ```sh
  keepgoing retarget primary --discard-pending-thread -c /private/example-loop/keepgoing.yaml
  ```

  This is also the way out when BB can never settle it (two threads with the title, or BB permanently unreachable); no file needs editing.

**Cap and spacing.** A loop attempts at most `loop.limits.max_new_threads` thread creations (default 3, never more than 10) over the life of its state directory, counting every attempt not proven refused, and never two less than **one hour** apart. So a loop creates at most 10 threads over its whole life, which is also the most it can create in one day, and never more than one in an hour. At the cap a new-thread candidate is skipped without a probe; if another fallback can take the loop it does, and if none can the loop is held for a person. Raise the cap and `resume` to continue; `resume` alone holds again. Inside the hour the candidate is skipped the same way (`new_thread_spacing`) but no hold is raised for spacing alone: the ordinary error rules apply.

**Bounds and holds.** The creation is one send against `max_sends`. Stop, pause, disabled, awaiting-user and completed win: a held loop asks BB nothing, creates nothing, and does not settle a pending creation until it is resumed. A dry run previews the handoff as `wouldCreateThread` and creates nothing. Reading the place (10s), the spawn (30s) and the lookup (20s) together take the 60 seconds a check reserves for a model switch and send, and the lookup at the start of a later check comes out of the observation's 90 seconds, so the [check time limit](operations.md#check-time-budget) is unchanged.

**Switch-back** works as for any fallback: the loop returns to its primary thread when that is configured and the primary passes its probe. The created thread is left as it was.

**Ownership.** The move does not change who owns the loop: the owner recorded before the handoff still owns it afterwards, even if that owner is the thread that hit its limit. Moving ownership as part of the handoff was considered and not done, because with the current ownership checks it would stop supervision: the running daemon exits when the owner or its generation changes under it, and a scheduled check that runs with the earlier owner's identity would be refused. Until those checks can follow a handoff the supervisor made itself, ownership moves only by an explicit `enable --take-over` or `transfer apply`.

The new thread's first prompt therefore ends with fixed wording: it does not own the loop; it may run `keepgoing pause`, `stop`, `await-user` and `complete` for it; and it must not run `resume` or `enable`, or use `--take-over`, unless a person asks. Work accounting, the goal and holds are kept; observations about the thread that was left are cleared, as on any handoff.

**Where it shows.** The tick output's `handoff` has `newThread` and `continues`. `status` shows the created thread as the target's thread and a `new threads:` line with the count against the cap (`newThreads` in JSON). `keepgoing runs` shows both conversations on the run, the new one with source `created_thread`. `timeline` marks the check as `NEW THREAD` without the thread ID. The `handoff` report carries `newThread: true` and no thread ID.

**What BB records about the origin.** `bb thread spawn` in BB 0.48.1 accepts a title but no origin other than `fork`, so BB does not record these threads as started by the keepgoing plugin. The plugin SDK has an origin plugin field for threads a plugin spawns, but the supervisor is a separate CLI process with no channel to the plugin host. The title and keepgoing's own records are what identify the thread.

**The `@thread:<id>` reference.** BB's guide documents `Continue from @thread:<id>` in a spawned thread's prompt as the way to hand off, and describes the result as a new thread that references the source. It does not say whether the referenced thread is notified or woken, and that has not been established here: no BB documentation or read-only command settles it, and nothing was run live. Treat it as unverified until a supervised live handoff shows the earlier thread is left alone.

**Limits to know about.**

- A thread whose title was changed before keepgoing could find it stays unrecorded. keepgoing never creates a replacement on its own: the loop is held and the attempt stays counted.
- keepgoing cannot see which account BB uses for the new thread. The probe tests the local CLI's login, as for every BB candidate.
- Without `model`, BB uses the project's remembered default for that provider.
- `retarget <id>` to a new-thread candidate works only once a thread exists for it; `retarget` never creates one.
- Once a created thread is recorded, a build from before this change does not know the candidate's thread and refuses the run graph's new source value. Do not run an older build against the same state directory.

With failover disabled (`errors.failover_on: []`), a rate-limit error with a structured future reset waits without spending recovery retries. Ordinary checks continue observing the target, so an earlier structured recovery can take effect on the next check. Once the reset passes, a still-errored target uses bounded recovery. Without a known reset, bounded recovery applies immediately. This correction is available in v0.1.4 and later. Existing awaiting-user holds require explicit `resume`; stop, pause, completion and configured loop-wide bounds retain precedence. This does not add account polling or schedule an extra check at the reset timestamp. In unreleased source, a wait whose reset is further away than `errors.report_wait_after` (default `6h`) sends one `limit_wait` report without holding the loop; see [a long limit wait](operations.md#a-long-limit-wait-with-no-way-out). `timeline --kind limit_wait` shows the waiting checks.

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
