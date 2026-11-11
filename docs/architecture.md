# Architecture

`keepgoing` is a supervisor, not an agent. It sits beside a running agent session and answers one question on every tick: should anything be sent, stopped, escalated, or moved right now?

## Rules that do not bend

- **Never edit repositories.** The tool reads Git metadata to detect progress. It never writes to a checkout, commits, pushes, or merges.
- **Structured signals only.** Session state and error class come from exit codes, JSON fields, HTTP status, or provider events. Transcript and message text is never read for a decision. Stderr from agent commands is never parsed.
- **Unknown is conservative.** An unknown state means no action. An unknown error class means bounded re-prompts and then a person, never a handoff.
- **Confirmed before relied on.** A fallback receives the loop only after passing an exact-target probe in the same tick, and only while the redundancy rule is met.
- **Stop and pause win.** Once the loaded configuration is verified, a stop is handled before anything else on every tick, even while the loop is paused, disabled, completed, or awaiting a person, and even when the fingerprint or health files cannot be read. Paused, disabled, completed, or awaiting-user without a stop means the provider is not even queried and no probe runs.
- **Missing control means disabled.** A tick must never wake an agent nobody asked for.
- **Pure decisions.** The decision engine is a function from inputs to one action, with a table test per rule. Side effects live in adapters and the tick orchestrator.

## Packages

```
cmd/keepgoing               CLI: start, service, tick, daemon, status, timeline, goal, control, fallbacks, report, ci-wait, net-retry
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
internal/report             signed webhook reports and diagnostic delivery history
internal/incident           bounded typed tick/control history; diagnostic only
internal/evidence           pure, allowlisted diagnostic source/code metadata
internal/control            control.json and goal.json: holds, BB ownership, private goal revisions, serialized updates
internal/state              state.json, sends.json, progress.json, runs.jsonl, and the progress fingerprint
internal/window             weekly time windows
internal/lock               named advisory locks for ticks, daemon, controls, and services
internal/daemoninfo         private loopback diagnostics for a running daemon
internal/netretry           retry only while the network is down
internal/ciwait             wait for CI on an exact commit
```

## A tick, end to end

1. Take the tick lock. If another tick holds it, exit 75. The command layer verifies the calling BB owner or the running daemon's bound ownership generation, then compares the loaded config/fallback source bytes with their private in-memory SHA256 snapshots before provider work. A mismatch or unverifiable source skips the operation, including stop execution, without changing runtime records. Early polls, retargets, and manual probe execution share this guard. Reload and retry; see [loaded configuration checks](operations.md#loaded-configuration-checks). This serializes only with writers sharing the tick lock, not arbitrary external edits.
2. Load control, state, and the active target. The target is `primary` or a fallback id.
3. If a stop is requested, observe the active target and stop it once if it is running. Record and end the tick.
4. If disabled, paused, completed, or awaiting a person, record and stop here.
5. If the configured absolute deadline is reached at tick start, persist/report an awaiting-user hold before provider work. Existing controls and stop win; in-flight checks and running turns are not cancelled. Then check the configured send allowance before provider work; exhaustion holds, unavailable accounting fails closed. When a no-progress timeout is configured, verify and persist Git evidence before provider work; elapsed time without changes holds, unavailable evidence fails closed. Stop and existing holds skip this observation. Otherwise, if a yield command reports other work, record and exit 75.
6. Observe the active target through its adapter. If it cannot be observed, record and take no action. Verified pending child input is decided and escalated here, before probes or fingerprinting; it persists an awaiting-user hold and reports child IDs without touching the parent.
7. Inside the time window, run any scheduled probes that are due, within the daily cap. The active target and candidates in its quota group are skipped, so probes never spend the pool the loop is using.
8. Evaluate redundancy among eligible fallbacks. Eligible means enabled, not the active target, and not in the active target's quota group. Report a change into or out of the degraded state.
9. Update the progress fingerprint, then run the decision engine.
10. Act. A wake or re-prompt starts or steers a turn. A nudge is queued behind the live turn. Awaiting-user flips the control file. A handoff tries healthy candidates in order. For each one it writes the brief, runs an exact probe, runs the adapter's activate step, and sends the brief. A brief that cannot be written ends the handoff before any candidate is touched. The first success becomes the active target. If no candidate can take over, the ordinary error rules apply to the current target.
11. Save state and health, append an event to `runs.jsonl`, send any subscribed reports, and print the result.

## Adapters

An adapter implements `Name`, `Observe`, `Send`, and `Stop`. It may implement `WakePoller` to consume structured lifecycle notices between daemon ticks under the same tick lock. The supervisor honors control holds and checks the resulting observation with the pure decision engine before requesting an early tick; polling itself never sends or probes. It may also implement `Activate`, which runs before the first message after a handoff. The BB adapter uses it to switch the thread's model.

- **bb** reads `bb thread show --json`, discovers direct children with a parent-filtered listing including hidden threads, verifies each child with `thread show`, checks its structured interaction status, and reads the parent's new log events when no confirmed child input already requires a hold. Verified pending child input requires an awaiting-user hold. Verified active children and incomplete child discovery or interaction queries suppress parent prompts and target moves in the pure decision engine; stop and control holds retain precedence. No child is spawned or managed. bb returns events oldest first, so the adapter keeps a cursor in `targets/<id>/bb-cursor.json`. On the first tick it pages through the whole log once. After that it reads only events newer than the last one it saw, folding them into a running summary: the latest turn start, the latest error since then, the latest rate-limit window, pending interactions, and the latest structured child outcome notice. The daemon uses new child outcomes for early idle checks, with the persisted sequence preventing replay. It classifies errors from `provider/error` events, using their category, HTTP status, and provider code, and from `system/error` codes. Only errors after the latest turn started count. A `willRetry` flag means BB is already retrying, including while status remains active, so keepgoing waits. Queued input does not clear that flag; matching provider message/terminal metadata or a new turn does. Incomplete retry scans suppress prompts and target moves without hiding an active stop target. A versioned cursor rebuild handles old summaries once without replaying child wakes. The supervisor persists target-scoped ordinary stall grace after observed active recovery; it leaves Git evidence, spent nudges and hard bounds unchanged. See [retry handling](operations.md#bb-retry-and-stall-handling). A blocked `provider/rateLimits/updated` window with a future reset marks the thread rate limited and supplies the reset time.
- **process** runs a status command that prints a small JSON object. A non-zero exit maps through `exit_codes`.
- **claude** and **codex** start one headless turn per send as a detached process. The process writes its stdout and exit code into the target's directory, so a short-lived tick never waits for a turn. The next tick reads the result: the `is_error`, `subtype`, and `api_error_status` fields for Claude Code, and the event types for Codex. Each new turn resumes the saved session. On macOS and Linux a small shell wrapper runs the turn. On Windows, where there is no POSIX shell, keepgoing starts a detached copy of its own binary as the wrapper, and stop ends the turn's process tree with `taskkill`.

## State directory

Runtime records live in `state_dir`, outside any repository. Managed service definitions additionally live in `~/Library/LaunchAgents` on macOS or `$XDG_CONFIG_HOME/systemd/user` (default `~/.config/systemd/user`) on Linux:

| File | Owner | Purpose |
| --- | --- | --- |
| `control.json` | operator, via control commands | enabled, paused, stop, awaitingUser, completed, note, ownership |
| `goal.json` | operator, via goal commands | bounded private specification history, stable goal identity and owning loop; independent of controls/completion |
| `sends.json` | the tool | versioned total reserved send attempts while `max_sends` is enabled; independent of diagnostic state, saved before adapter sends under the tick lock |
| `progress.json` | the tool | versioned Git source/fingerprint hashes, last verified change and observation time for the optional no-progress timeout; independent of diagnostic/per-turn counters |
| `state.json` | the tool | fingerprint, last progress, last send, re-prompt budget, active target, retry/recovery stall grace, last check decision/outcome and build label |
| `health.json` | the tool | probe evidence per candidate |
| `probe-failures/<id>/` | the tool | raw output of the last few failed probes, for mapping new error shapes; never read for decisions |
| `runs.jsonl` | the tool | append-only events |
| `incidents.json`, `incident.lock` | tick diagnostic writer | versioned rolling snapshots, capped at 256 records/512 KiB; independent lock and atomic replacement; never a decision input |
| `deliveries.json`, `delivery.lock` | report diagnostic writer | bounded typed delivery evidence (64 records/128 KiB); separate short lock; never send/replay authority |
| `daemon-endpoint.json` | daemon | private loopback endpoint and random verification key; removed on clean exit, stale records do not prove liveness |
| `service-launchd.json`, `service-systemd.json` | service commands | private backend-specific plan/recovery record; no task text or credentials |
| `tick.lock`, `daemon.lock`, `control.lock`, `service.lock` | the tool | advisory locks, released by the OS on exit |
| `targets/<id>/` | the tool | headless turn files (`run.json`, prompt, stdout, stderr, exit code, session id) and the BB log cursor (`bb-cursor.json`) |

## Ownership and control writes

`enable` records `BB_THREAD_ID` when present. Ownership is persistent metadata; idle, missing, archived, or unavailable thread activity does not expire it. A different BB caller needs an explicit takeover. Calls without a BB identity use the existing claim without replacing it. This is a cooperative guard for local operators and schedulers, not authentication against a caller that can edit runtime files or environment variables.

Claims and takeover hold the tick lock, then the control lock. Every runtime control read-modify-write uses the short control lock; pause and stop do not wait for the tick lock. Control contention returns exit 75 for retry. No provider query occurs under the control lock. An update callback can reject a claim without writing control. Low-level `control.Save` is for initialization/fixtures, not concurrent runtime updates.

A daemon binds to the current owner ID and generation (including no owner). It checks the binding under the tick lock before ticks and early polls, and exits when it changes. Takeover cannot overlap an in-flight tick; it must retry after that tick releases the lock. Generations prevent a transfer back to a previous thread from reviving its old daemon. No BB thread is stopped, spawned, or re-parented by ownership handling.

## Starting a daemon

The command layer implements `start`: hold the daemon lock, acquire the tick lock, validate local setup and ownership, open diagnostics, then claim/enable under the control lock without clearing holds. It releases the tick lock and enters the same daemon loop as `daemon`, retaining the daemon lock and owner generation for its lifetime. A competing start cannot enable or change an existing daemon's controls.

Background start re-executes the binary with an absolute configuration path, a detached process session/group, and a one-shot stdout pipe. The child acknowledges only after setup succeeds; the parent bounds startup time and checks the returned PID against its own child. Raw daemon output is discarded after acknowledgement; structured tick history remains in the runtime directory. No shell, provider process, or BB thread is created by validation. The daemon's subsequent ticks use the existing adapters normally. This starts a supervisor process, not a service or an external schedule.

## Managed LaunchAgents

The command layer owns service installation and removal; the decision engine and adapters remain unchanged. A deterministic label binds a LaunchAgent to the canonical state directory. The generated private plist invokes a service entrypoint that checks the configured state directory against that binding before starting the ordinary daemon. A private JSON record retains the exact definition inputs for recovery and comparison, even when the config becomes unreadable. Only PATH is captured from the installer environment; BB caller identity is explicitly cleared for the scheduler.

`service.lock` serializes install/remove. Install also acquires the daemon and tick locks for owner checks and preparation, then releases them before bootstrap so the launched daemon can acquire its locks. Controls and ownership are never rewritten by service management. A concurrent manual start can win the daemon lock after release; registration alone does not imply this service owns a healthy daemon.

Removal verifies the recorded definition, disables the exact service target, boots it out, verifies absence and daemon-lock availability, and then removes its files. Unknown manager results and changed definitions retain recovery files and return failure. Numeric launchctl status is consumed; raw output, environment values, and prose are never parsed. Filesystem and scheduler changes are not one atomic transaction: partial installation/removal is explicit and recoverable. This guard coordinates cooperative local callers; it is not authentication against someone who can edit private files.

## Managed systemd user units

Linux uses the same service command, locks, private plan format, state-bound daemon entrypoint, and control preservation. Unit generation uses systemd argument quoting, escapes percent specifiers, disables ExecStart environment expansion, and handles WorkingDirectory as a raw path. Only PATH is captured; BB_THREAD_ID is cleared. A user manager may supply other inherited environment values.

The backend consumes bounded `systemctl --user show` properties for the exact unit ID, loaded fragment path, empty drop-ins, load/active states, and enablement. Unknown, masked, aliased, overridden, or foreign units cannot be managed. Install writes its record/unit, reloads the manager, checks identity before enable/start, and verifies active/enabled state. Removal disables/stops, checks the daemon lock, deletes the exact definition, reloads, verifies absence, and only then removes the record. Missing definitions remain recoverable after an interrupted final reload. Command failures retain recovery metadata. External manual changes are not serialized by service.lock; these are cooperative local guards.

The unit uses KillMode=process to keep service removal from killing detached agent turns. It does not guarantee survival across user-manager shutdown. It never enables login lingering, changes another timer, or creates a login session.

Linux service status may bind a loop responder to the recorded user unit. New daemon diagnostic snapshots include the process PID and a validated Linux invocation ID in their authenticated response, without persisting or publicly reporting those fields. The command layer brackets a fresh challenge with strict structured manager observations and uses a pure comparison to require an unchanged active main PID/invocation and matching response. Missing, changed, or unsupported data is unavailable; a valid mismatch is reported separately. This check never controls lifecycle or failover and is not a security boundary against local writers of runtime state. macOS binding remains unavailable.

## BB setup integration

The BB plugin resolves existing thread/environment/host metadata on its server and runs setup through a typed host entry. The CLI remains responsible for configuration generation, private destinations, diagnostics, ownership, and background daemon startup. Browser input supplies a task and loop name, never a host, provider, executable, config path, or arbitrary command arguments. Preview tickets bind a task hash and resolved metadata/plan for one creation attempt; mutation requests are serialized within the plugin instance. Task text travels over the host RPC and subprocess stdin, not argv or plugin storage. Private plugin storage retains only setup references for subsequent host-routed status and controls.

Creating a configuration, saving its reference, and starting a daemon are separate operations. The first two can fail independently and require inspection instead of automatic retries; startup is a separate explicit UI action with fresh metadata/status/doctor checks. Those observations are not atomic with manual configuration edits or external scheduling. Holds, competing daemon locks, and owner claims remain enforced by keepgoing. The plugin never spawns/re-parents threads or makes loop decisions.

## Completion declarations

The command layer detects a new completion episode inside the serialized control update, saves `completed` before any delivery, then appends the control log and submits the subscribed `complete` report outside the lock. The saved flag is the deduplication marker; ticks never emit completion reports. Resume explicitly reopens the episode. Payloads describe an unverified declaration without private notes. The report transport uses one attempt without redirects for this event; other events retain their existing retry behavior. This favors avoiding duplicate notifications over guaranteed delivery after crashes, log/config failures or uncertain HTTP results. No independent acceptance verifier or goal revision binding is implemented yet.

## Private goal records

The control package also owns a separate, versioned `goal.json`. Goal edits acquire tick then control locks, verify the loaded config sources and current owner, and replace the private history atomically without rewriting controls. Revisions require the expected ID and current revision. Status exposes only generated identity, revision and the fixed `recorded` metadata state. References and next actions are opaque descriptions, never executable input or authorization/acceptance evidence. The decision engine and adapters do not consume this record. Goal-bound completion and stage results remain future work; see [private goals](goals.md).

## Tick incident snapshots

After the tick returns from its normal decision/action/recording path, `internal/incident` writes a typed snapshot under its own nonblocking lock. It never acquires the tick or control lock, so an unavailable diagnostic writer cannot prevent an already-requested stop or undo a saved hold. A write failure adds a fixed warning and returns a joined error; earlier tick effects and ordinary records remain. Pre-tick lock, ownership and source-snapshot refusals never enter this path. Process termination can leave no snapshot for an attempted action.

The storage boundary allowlists strings, omits raw errors and task/provider content, and hashes the configured target alias for correlation. It records control flags read at the tick's beginning and after its action; concurrent or between-tick changes are not a complete transition audit. BB active error classification is exposed alongside retry intent, without changing pure decision rules. Adapters attach `evidence.Detail` to the observation; the pure decision engine ignores it. Both adapters and journal storage normalize source/code/category strings through a finite diagnostic vocabulary, separate from error-classification policy. BB persists sanitized details with its error cursor, clears them on recovery and withholds them after incomplete scans. Headless results and process status use already-read structured output/exit evidence; no extra command is run. Unsupported codes, source timestamps and headless/process retry intent remain unavailable. This first R2 slice is a diagnostic foundation; it does not implement alert receipts or a timeline command. See [retention, failures and privacy](diagnostics.md#bounded-tick-incident-history).

Incident journal schema 2 adds source, category, HTTP status and exit status and fills the existing provider-code field. Read-only loading also accepts legacy versions 1 and 2; appending upgrades them to the current schema under the journal lock, preserving sequence/retention history and marking old source details unavailable. Older binaries refuse unsupported versions rather than rewriting them; downgrade does not guarantee incident-history writes. Source labels identify the structured input used, not a cause such as Wi-Fi or a provider outage. Numeric runner exit evidence is recorded only when the existing exit artifact parses successfully; this availability check does not change the runner's classification behavior.

Incident journal schema 3 also captures each tick's calls to the pure decision engine through a supervisor-local wrapper. The wrapper returns the unmodified decision and stores a bounded typed projection: no child IDs, free-text reasons or diagnostic provider payloads. It records inputs before actions can change counters and retains repeated evaluations after failed moves. These values describe engine arguments, not independently verified conditions; early paths have default unused fields. Empty captured history identifies bypassed engine calls, while schema-1/2 records have unavailable decision history. The loader retains schema-1/2/3 compatibility and append upgrades to the current schema. Diagnostic records never feed back into policy or allowance accounting.

## Saved control incident records

The command layer wraps serialized control updates for standalone commands, start and daemon takeover. It captures gate flags and owner-presence/change booleans inside the update callback, then appends a schema-4 control record only after a successful save, outside the control lock. Notes, owner IDs/generations and arbitrary callback errors never enter the journal. Capture uses callback-start and post-save observation times, not an invented exact save timestamp. Journal ordering is append order; concurrent writers, crashes, external changes and retention prevent a complete audit. Legacy records remain ticks and are not backfilled.

A distinct saved-control/history-failure error prevents callers from confusing a failed diagnostic append with an uncommitted mutation. Ordinary control commands preserve their existing log/report sequence and return failure after it; completion deduplication and delivery semantics stay intact. Startup and daemon takeover instead stop before running a daemon when their new control record cannot be written, explicitly preserving the already-saved enable/claim and all holds. Diagnostic code never acquires a control or tick lock. Automatic holds remain represented by their tick snapshots; initialization and low-level external writers are outside command capture.


## Notification delivery evidence

The command layer supplies `report.NewRecorded` to supervision and standalone report-producing controls. A failed setup still returns a reporter that records `setup_failed` only when a subscribed event is actually requested. Disabled, filtered and dry-run events create no delivery record. The explicit `report test` command records its forced test event. The webhook transport emits typed snapshots before request preparation, before each `client.Do`, and after each result. These snapshots contain fixed event/outcome labels, observation timestamps and numeric HTTP statuses, never payloads, endpoint identities, notes, secrets or raw errors.

`internal/report` saves a bounded versioned ledger under its own short advisory lock. It releases that lock before HTTP and never acquires the control/tick locks. A local sequence ties updates to a retained record; concurrent declarations get separate records. Corruption, lock contention or eviction of an in-flight record returns an explicit diagnostic error, without suppressing the original send, adding a retry or rolling back a saved hold. A crash or failed final write can leave prepared/unconfirmed evidence; it cannot authorize replay. Missing evidence does not establish that a request never occurred. Retention and external state edits prevent a complete audit.

Status and doctor read the ledger independently of the tick's last decision. Warnings describe retained historical failures or unconfirmed attempts, even if the loop is held or a later notification succeeded. HTTP acceptance does not prove application or human receipt. Ordinary retries/redirects and the completion flag's single-attempt policy remain unchanged. See [delivery evidence](diagnostics.md#notification-delivery-evidence) for gaps and read/write behavior.


## Offline incident view

`cmd/keepgoing` implements `timeline` as a read-only composition of `incident.Load`, `report.LoadDeliveries` and a bounded control-file projection. No new decision or storage package is introduced. The command reads these files independently, without locks or provider/daemon/scheduler/CI calls, and keeps current flags separate from historical hold entries. `--state-dir` bypasses normal config loading for copied records or broken configuration. It never consumes transcript text, raw logs or provider output.

The view retains normalized source/sequence references, local timestamp bases, historical build labels, typed decision inputs and delivery attempts. Sorting by local timestamps is presentation only, not causal inference. Source-wide summaries remain independent of the bounded display limit. Missing, invalid, stale, future or unsupported evidence remains explicit; partial failures print usable sources before returning failure. No diagnostic conclusion authorizes sends, control changes or replay. See the [timeline contract](diagnostics.md#offline-incident-timeline).

## Transfer preview

The command layer implements `transfer preview` as an advisory read-only view. It loads bounded control/state/goal records and optionally makes two bounded `bb thread show --json` queries through the diagnostic subprocess runner. Only validated identity/provider/status fields are projected; environment paths are compared internally and withheld. It rechecks configuration source snapshots at the end, but independent reads are not an atomic snapshot and no lock or durable ticket is acquired. It never invokes adapters, controls, model changes, scheduling or thread creation. `compatible_metadata` describes only the checked subset, while `applySupported` remains false. Model/account identity and transactional application are future work; see [transfer preview](transfer.md).
