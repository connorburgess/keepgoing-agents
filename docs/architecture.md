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
internal/control            control.json: enabled, paused, stop, awaitingUser, completed
internal/state              state.json, runs.jsonl, and the progress fingerprint
internal/window             weekly time windows
internal/lock               named advisory locks for ticks and the daemon
internal/netretry           retry only while the network is down
internal/ciwait             wait for CI on an exact commit
```

## A tick, end to end

1. Take the tick lock. If another tick holds it, exit 75.
2. Load control, state, and the active target. The target is `primary` or a fallback id.
3. If a stop is requested, observe the active target and stop it once if it is running. Record and end the tick.
4. If disabled, paused, completed, or awaiting a person, record and stop here.
5. If a yield command reports other work, record and exit 75.
6. Observe the active target through its adapter. If it cannot be observed, record and take no action.
7. Inside the time window, run any scheduled probes that are due, within the daily cap. The active target and candidates in its quota group are skipped, so probes never spend the pool the loop is using.
8. Evaluate redundancy among eligible fallbacks. Eligible means enabled, not the active target, and not in the active target's quota group. Report a change into or out of the degraded state.
9. Update the progress fingerprint, then run the decision engine.
10. Act. A wake or re-prompt starts or steers a turn. A nudge is queued behind the live turn. Awaiting-user flips the control file. A handoff tries healthy candidates in order. For each one it writes the brief, runs an exact probe, runs the adapter's activate step, and sends the brief. A brief that cannot be written ends the handoff before any candidate is touched. The first success becomes the active target. If no candidate can take over, the ordinary error rules apply to the current target.
11. Save state and health, append an event to `runs.jsonl`, send any subscribed reports, and print the result.

## Adapters

An adapter implements `Name`, `Observe`, `Send`, and `Stop`. It may also implement `Activate`, which runs before the first message after a handoff. The BB adapter uses it to switch the thread's model.

- **bb** reads `bb thread show --json` and the thread's new log events. bb returns events oldest first, so the adapter keeps a cursor in `targets/<id>/bb-cursor.json`. On the first tick it pages through the whole log once. After that it reads only events newer than the last one it saw, folding them into a running summary: the latest turn start, the latest error since then, the latest rate-limit window, and pending interactions. It classifies errors from `provider/error` events, using their category, HTTP status, and provider code, and from `system/error` codes. Only errors after the latest turn started count. A `willRetry` flag means BB is already retrying, so keepgoing waits. A blocked `provider/rateLimits/updated` window with a future reset marks the thread rate limited and supplies the reset time.
- **process** runs a status command that prints a small JSON object. A non-zero exit maps through `exit_codes`.
- **claude** and **codex** start one headless turn per send as a detached process. The process writes its stdout and exit code into the target's directory, so a short-lived tick never waits for a turn. The next tick reads the result: the `is_error`, `subtype`, and `api_error_status` fields for Claude Code, and the event types for Codex. Each new turn resumes the saved session. On macOS and Linux a small shell wrapper runs the turn. On Windows, where there is no POSIX shell, keepgoing starts a detached copy of its own binary as the wrapper, and stop ends the turn's process tree with `taskkill`.

## State directory

Everything the tool writes lives in `state_dir`, outside any repository:

| File | Owner | Purpose |
| --- | --- | --- |
| `control.json` | operator, via control commands | enabled, paused, stop, awaitingUser, completed, note |
| `state.json` | the tool | fingerprint, last progress, last send, re-prompt budget, active target, last check decision/outcome and build label |
| `health.json` | the tool | probe evidence per candidate |
| `probe-failures/<id>/` | the tool | raw output of the last few failed probes, for mapping new error shapes; never read for decisions |
| `runs.jsonl` | the tool | append-only events |
| `tick.lock`, `daemon.lock` | the tool | advisory locks, released by the OS on exit |
| `targets/<id>/` | the tool | headless turn files (`run.json`, prompt, stdout, stderr, exit code, session id) and the BB log cursor (`bb-cursor.json`) |
