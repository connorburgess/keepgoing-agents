# Architecture

`keepgoing` is a supervisor, not an agent. It sits beside a running agent session and answers one question on every tick: should anything be sent, stopped, escalated, or moved right now?

## Rules that do not bend

- **Never edit repositories.** The tool reads Git metadata to detect progress. It never writes to a checkout, commits, pushes, or merges.
- **Structured signals only.** Session state and error class come from exit codes, JSON fields, HTTP status, or provider events. Transcript and message text is never read for a decision. Stderr from agent commands is never parsed.
- **Unknown is conservative.** An unknown state means no action. An unknown error class means bounded re-prompts and then a person, never a handoff.
- **Confirmed before relied on.** A fallback receives the loop only after passing an exact-target probe in the same tick, and only while the redundancy rule is met.
- **Stop and pause win.** A stop is handled before anything else on every tick, even while the loop is paused, disabled, completed, or awaiting a person, and even when the fingerprint or health files cannot be read. Paused, disabled, completed, or awaiting-user without a stop means the provider is not even queried and no probe runs.
- **Missing control means disabled.** A tick must never wake an agent nobody asked for.
- **Pure decisions.** The decision engine is a function from inputs to one action, with a table test per rule. Side effects live in adapters and the tick orchestrator.

## Packages

```
cmd/keepgoing               CLI: tick, daemon, status, control, fallbacks, report, ci-wait, net-retry
internal/config             keepgoing.yaml and fallbacks.yaml: loading, defaults, validation
internal/decide             the pure decision engine
internal/supervise          one tick: observe, probe, decide, act, record
internal/adapter            the Adapter interface and the code-to-class mapping
internal/adapter/bb         BB threads over the bb CLI's JSON
internal/adapter/process    any session described by three commands
internal/adapter/runner     headless Claude Code and Codex turns, run detached
internal/adapter/factory    builds the adapter for an agent definition
internal/probe              one tiny real request to confirm a candidate works
internal/health             probe evidence and the redundancy rule
internal/handoff            the brief a fallback receives
internal/report             signed webhook reports
internal/control            control.json: holds, persistent BB ownership, serialized updates
internal/state              state.json, runs.jsonl, and the progress fingerprint
internal/window             weekly time windows
internal/lock               named advisory locks for ticks and the daemon
internal/daemoninfo         private loopback diagnostics for a running daemon
internal/netretry           retry only while the network is down
internal/ciwait             wait for CI on an exact commit
```

## A tick, end to end

1. Take the tick lock. If another tick holds it, exit 75. The command layer verifies the calling BB owner or the running daemon's bound ownership generation before provider work.
2. Load control, state, and the active target. The target is `primary` or a fallback id.
3. If a stop is requested, observe the active target and stop it once if it is running. Record and end the tick.
4. If disabled, paused, completed, or awaiting a person, record and stop here.
5. If a yield command reports other work, record and exit 75.
6. Observe the active target through its adapter. If it cannot be observed, record and take no action. Verified pending child input is decided and escalated here, before probes or fingerprinting; it persists an awaiting-user hold and reports child IDs without touching the parent.
7. Inside the time window, run any scheduled probes that are due, within the daily cap. The active target and candidates in its quota group are skipped, so probes never spend the pool the loop is using.
8. Evaluate redundancy among eligible fallbacks. Eligible means enabled, not the active target, and not in the active target's quota group. Report a change into or out of the degraded state.
9. Update the progress fingerprint, then run the decision engine.
10. Act. A wake or re-prompt starts or steers a turn. A nudge is queued behind the live turn. Awaiting-user flips the control file. A handoff tries healthy candidates in order. For each one it writes the brief, runs an exact probe, runs the adapter's activate step, and sends the brief. A brief that cannot be written ends the handoff before any candidate is touched. The first success becomes the active target. If no candidate can take over, the ordinary error rules apply to the current target.
11. Save state and health, append an event to `runs.jsonl`, send any subscribed reports, and print the result.

## Adapters

An adapter implements `Name`, `Observe`, `Send`, and `Stop`. It may implement `WakePoller` to consume structured lifecycle notices between daemon ticks under the same tick lock. The supervisor honors control holds and checks the resulting observation with the pure decision engine before requesting an early tick; polling itself never sends or probes. It may also implement `Activate`, which runs before the first message after a handoff. The BB adapter uses it to switch the thread's model.

- **bb** reads `bb thread show --json`, discovers direct children with a parent-filtered listing including hidden threads, verifies each child with `thread show`, checks its structured interaction status, and reads the parent's new log events when no confirmed child input already requires a hold. Verified pending child input requires an awaiting-user hold. Verified active children and incomplete child discovery or interaction queries suppress parent prompts and target moves in the pure decision engine; stop and control holds retain precedence. No child is spawned or managed. bb returns events oldest first, so the adapter keeps a cursor in `targets/<id>/bb-cursor.json`. On the first tick it pages through the whole log once. After that it reads only events newer than the last one it saw, folding them into a running summary: the latest turn start, the latest error since then, the latest rate-limit window, pending interactions, and the latest structured child outcome notice. The daemon uses new child outcomes for early idle checks, with the persisted sequence preventing replay. It classifies errors from `provider/error` events, using their category, HTTP status, and provider code, and from `system/error` codes. Only errors after the latest turn started count. A `willRetry` flag means BB is already retrying, so keepgoing waits. A blocked `provider/rateLimits/updated` window with a future reset marks the thread rate limited and supplies the reset time.
- **process** runs a status command that prints a small JSON object. A non-zero exit maps through `exit_codes`.
- **claude** and **codex** start one headless turn per send as a detached process. The process writes its stdout and exit code into the target's directory, so a short-lived tick never waits for a turn. The next tick reads the result: the `is_error`, `subtype`, and `api_error_status` fields for Claude Code, and the event types for Codex. Each new turn resumes the saved session. On macOS and Linux a small shell wrapper runs the turn. On Windows, where there is no POSIX shell, keepgoing starts a detached copy of its own binary as the wrapper, and stop ends the turn's process tree with `taskkill`.

## State directory

Everything the tool writes lives in `state_dir`, outside any repository:

| File | Owner | Purpose |
| --- | --- | --- |
| `control.json` | operator, via control commands | enabled, paused, stop, awaitingUser, completed, note, ownership |
| `state.json` | the tool | fingerprint, last progress, last send, re-prompt budget, active target, last check decision/outcome and build label |
| `health.json` | the tool | probe evidence per candidate |
| `probe-failures/<id>/` | the tool | raw output of the last few failed probes, for mapping new error shapes; never read for decisions |
| `runs.jsonl` | the tool | append-only events |
| `daemon-endpoint.json` | daemon | private loopback endpoint and random verification key; removed on clean exit, stale records do not prove liveness |
| `tick.lock`, `daemon.lock`, `control.lock` | the tool | advisory locks, released by the OS on exit |
| `targets/<id>/` | the tool | headless turn files (`run.json`, prompt, stdout, stderr, exit code, session id) and the BB log cursor (`bb-cursor.json`) |

## Ownership and control writes

`enable` records `BB_THREAD_ID` when present. Ownership is persistent metadata; idle, missing, archived, or unavailable thread activity does not expire it. A different BB caller needs an explicit takeover. Calls without a BB identity use the existing claim without replacing it. This is a cooperative guard for local operators and schedulers, not authentication against a caller that can edit runtime files or environment variables.

Claims and takeover hold the tick lock, then the control lock. Every runtime control read-modify-write uses the short control lock; pause and stop do not wait for the tick lock. Control contention returns exit 75 for retry. No provider query occurs under the control lock. An update callback can reject a claim without writing control. Low-level `control.Save` is for initialization/fixtures, not concurrent runtime updates.

A daemon binds to the current owner ID and generation (including no owner). It checks the binding under the tick lock before ticks and early polls, and exits when it changes. Takeover cannot overlap an in-flight tick; it must retry after that tick releases the lock. Generations prevent a transfer back to a previous thread from reviving its old daemon. No BB thread is stopped, spawned, or re-parented by ownership handling.

## One owner per loop

When `keepgoing enable` runs inside BB, it records `BB_THREAD_ID` in `control.ownership`, with a generation number. The owner is the thread operating the loop; it is separate from the configured agent target. Claiming or transferring ownership never retargets the loop or changes a BB parent/child relationship. `status` shows the owner and latest handoff, and `status --json` includes the ownership metadata.

A different BB thread is refused by `validate`, `enable`, and `daemon`, as well as direct ticks, resume, retarget, and executing manual probes. Read-only status/doctor and probe previews remain available. Disable, pause, stop, complete, and await-user remain available from any thread. Missing `BB_THREAD_ID` identifies an external scheduler or ordinary terminal: commands use the existing claim without changing it. Legacy loops without ownership metadata continue working; their first enable from a BB thread records the claim. This relies on cooperative callers and private runtime files, not an authentication boundary.

From the BB thread that should become the new owner:

```sh
keepgoing validate -c /path/to/keepgoing.yaml --take-over
keepgoing enable -c /path/to/keepgoing.yaml --take-over
keepgoing status -c /path/to/keepgoing.yaml
```

Validation only previews eligibility; it never claims ownership or enables a loop. Validation against an existing runtime directory may create its private control lock; it creates no missing runtime directory. When replacing an existing owner, `enable --take-over` transfers only the claim, preserving disabled, pause, stop, awaiting-user, completion, and the existing note unless a new note is supplied. If the loop was disabled, a subsequent `enable` from the new owner is required; use `resume` separately to clear other holds. First claims and same-owner enables set `enabled` as usual. The previous owner is recorded as `handed_off` with a timestamp; only the latest handoff is retained. A same-owner enable leaves the generation unchanged. Takeover requires a valid `BB_THREAD_ID` and is never inferred from inactivity or a failed status lookup. No owner-status provider query is made: even an idle, archived, or missing owner retains its claim until an explicit transfer. Malformed ownership metadata is refused rather than silently discarded.

A running daemon is bound to its starting ownership generation. After a successful transfer it exits before further provider work, normally at its next control check, and releases its daemon lock. This also applies to a daemon started by an identity-free scheduler and to a previously unowned daemon when the loop is first claimed. `enable --take-over` does not start a replacement daemon; after the old daemon exits, start `daemon` from the new owner or scheduler. `daemon --take-over` can claim and start when no daemon already holds the process lock, preserving disabled and other held state. It cannot evict an existing daemon. `daemon --take-over --dry-run` is rejected; use validation to preview a transfer.

Ownership and transfers use the runtime state directory, so multiple config paths or directory symlinks sharing that directory share the same owner and locks. A `control.json` file symlink is refused: use the original state directory instead. Distinct runtime directories are distinct loops; moving a config to another state directory is not an ownership transfer. Do not alias individual runtime files with hard links or mix old binaries that discard ownership metadata. Removing or replacing runtime state also removes its ownership history and safeguards.

Claims and transfers serialize with ticks and early polls. If a tick is in flight, takeover exits 75 and must be retried; it never interrupts or rolls back that tick's work. Control updates have a separate short lock, preserving concurrent pause/stop requests without waiting for long provider calls. Control-lock contention also exits 75. No thread is created, stopped, re-parented, or messaged by ownership operations.

Offline fixtures verify conflicting identities, preview and explicit takeover, shared-state config paths, simultaneous claiming processes, directory aliases, control-file symlink refusal, hold preservation, scheduler access, and old-daemon exit including a transfer back to its original owner. Live transfers between real threads remain unverified; the tests make no live model calls.
