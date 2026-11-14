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
cmd/keepgoing               CLI: start, service, tick, daemon, status, timeline, goal, registry, client status/pause/stop/prepare-resume/resume, dock, transfer, control, fallbacks, report, ci-wait, net-retry
internal/config             keepgoing.yaml and fallbacks.yaml: loading, defaults, validation
internal/prompt             opt-in current-goal context for wake and handoff messages
internal/decide             the pure decision engine
internal/supervise          one tick: observe, probe, decide, act, record
internal/adapter            the Adapter interface and the code-to-class mapping
internal/adapter/bb         BB threads over the bb CLI's JSON
internal/adapter/process    any session described by three commands
internal/adapter/runner     headless turns, run detached; one Spec per agent (Claude Code, Codex)
internal/adapter/factory    builds the adapter for an agent definition
internal/probe              one tiny real request to confirm a candidate works
internal/health             probe evidence and the redundancy rule
internal/handoff            the brief a fallback receives
internal/report             signed webhook reports and diagnostic delivery history
internal/incident           bounded typed tick/control/retarget history; diagnostic only
internal/evidence           pure, allowlisted diagnostic source/code metadata
internal/registry           explicit private local loop references; independent of loop controls
internal/rundiscovery       pure run identity/boundary contract; no IO
internal/rungraph           graph.json: persisted run references, attempts and links; diagnostic only
internal/control            control.json and goal.json: holds, BB ownership/transfer fences, private goal revisions, serialized updates
internal/state              state.json, sends.json, progress.json, runs.jsonl, and the progress fingerprint
internal/window             weekly time windows
internal/lock               named advisory locks for ticks, daemon, controls, and services
internal/snapshot           read-only opens and atomic replacement that do not block each other on Windows; used for the records in the state directory that one command replaces while another reads
internal/testexec           test-only installer for shell stand-in CLIs; see testing.md
internal/scenario           test-only scenario suite that drives the built binary, and its stand-in for bb, claude and codex; see testing.md
internal/daemoninfo         private loopback diagnostics for a running daemon
internal/netretry           retry only while the network is down
internal/ciwait             wait for CI on an exact commit
```

## A tick, end to end

1. Take the tick lock. If another tick holds it, exit 75. The command layer verifies the calling BB owner or the running daemon's bound ownership generation, then compares the loaded config/fallback source bytes with their private in-memory SHA256 snapshots before provider work. A mismatch or unverifiable source skips the operation, including stop execution, without changing runtime records. Early polls, retargets, and manual probe execution share this guard. Reload and retry; see [loaded configuration checks](operations.md#loaded-configuration-checks). This serializes only with writers sharing the tick lock, not arbitrary external edits.
2. Load control, state, and the active target. The target is `primary` or a fallback id.
3. If a stop is requested, observe the active target and stop it once if it is running. If the stop was asked to cover the lanes (`stop --lanes`), also walk the target's verified descendants within fixed bounds and stop the running ones (`internal/supervise/lanestop.go`; the rules are pure in `internal/decide/lanestop.go`), recording each stop first. Record and end the tick.
4. If disabled, paused, completed, or awaiting a person, record and stop here.
5. If the configured absolute deadline is reached at tick start, persist/report an awaiting-user hold before provider work. Existing controls and stop win; in-flight checks and running turns are not cancelled. Then check the configured send allowance before provider work; exhaustion holds, unavailable accounting fails closed. When a no-progress timeout is configured, verify and persist Git evidence before provider work; elapsed time without changes holds, unavailable evidence fails closed. Stop and existing holds skip this observation. Otherwise, if a yield command reports other work, record and exit 75.
6. Observe the active target through its adapter. If it cannot be observed, record and take no action. Verified pending child input is decided and escalated here, before probes or fingerprinting; it persists an awaiting-user hold and reports child IDs without touching the parent.
7. Inside the time window, run any scheduled probes that are due, within the daily cap. In unreleased source they run only in time the [check time budget](operations.md#check-time-budget) has free, are skipped while the [connectivity check](operations.md#network-outages) fails, and wait until after the decision when a handoff is already due. The active target and candidates in its quota group are skipped, so probes never spend the pool the loop is using.
8. Evaluate redundancy among eligible fallbacks. Eligible means enabled, not the active target, and not in the active target's quota group. Report a change into or out of the degraded state.
9. Update the progress fingerprint, then run the decision engine.
10. Act. A wake or re-prompt starts or steers a turn. For an adapter that implements `adapter.TurnRetrier` (BB), a re-prompt of a session in a `network`, `overloaded` or `unknown` error re-runs the failed turn by reference instead of sending the prompt; the pure rule is `decide.RecoverBy`. A nudge is queued behind the live turn. Awaiting-user flips the control file. A handoff tries healthy candidates in order. For each one it writes the brief, runs an exact probe, runs the adapter's activate step, and sends the brief. A brief that cannot be written ends the handoff before any candidate is touched. The first success becomes the active target. If no candidate can take over, the ordinary error rules apply to the current target.
11. Save state and health, append an event to `runs.jsonl`, send any subscribed reports, and print the result.

## Adapters

An adapter implements `Name`, `Observe`, `Send`, and `Stop`. It may implement `WakePoller` to consume structured lifecycle notices between daemon ticks under the same tick lock. The supervisor honors control holds and checks the resulting observation with the pure decision engine before requesting an early tick; polling itself never sends or probes. It may also implement `Activate`, which runs before the first message after a handoff. The BB adapter uses it to switch the thread's model. An adapter that reports saved evidence of a cancelled interaction (`DeferredInteraction`) may implement `AdoptInteraction`; the supervisor saves the awaiting-user hold first and calls it only afterwards, so unadopted evidence holds again after a restart. No registered adapter reports it yet; see the [Antigravity foundation](antigravity.md#saved-interaction-hold).

- **bb** reads `bb thread show --json`, discovers direct children with a parent-filtered listing including hidden threads, verifies each child with `thread show`, checks its structured interaction status, and reads the parent's new log events when no confirmed child input already requires a hold. Verified pending child input requires an awaiting-user hold. For a parent that is not in error, verified active children and incomplete child discovery or interaction queries suppress prompts and switch-back in the pure decision engine; a parent in error gets its error rules (handoff, limit wait, re-prompts) regardless. Stop and control holds retain precedence. No child is spawned or managed. Separately, and only for a handoff to a candidate with `bb.new_thread`, the adapter can create one thread with `bb thread spawn`, read a thread's project and environment, and list a place's threads to find one by its generated title; `internal/supervise/newthread.go` saves and counts the intent before the spawn, looks the thread up by title after any failure that does not prove the request never reached BB, and holds the loop for a person when it cannot be found. bb returns events oldest first, so the adapter keeps a cursor in `targets/<id>/bb-cursor.json`. On the first tick it pages through the whole log once. After that it reads only events newer than the last one it saw, folding them into a running summary: the latest turn start, the latest error since then, the latest rate-limit window, pending interactions, and the latest structured child outcome notice. The daemon uses new child outcomes for early idle checks, with the persisted sequence preventing replay. It classifies errors from `provider/error` events, using their category, HTTP status, and provider code, and from `system/error` codes. Only errors after the latest turn started count. A `willRetry` flag means BB is already retrying, including while status remains active, so keepgoing waits. The flag counts only while fresh: within `bb.retry_signal_ttl` of the newest notice on an errored thread, of the newest provider or system log event on an active one (requested input does not count), or of a rate-limit notice's structured reset. After that the observation reports the notice as expired and ordinary error or stall handling applies. The supervisor passes its check time to the adapter, so the observation and the decision share one clock. Queued input does not clear that flag; matching provider message/terminal metadata or a new turn does. Incomplete retry scans suppress prompts and target moves without hiding an active stop target. A versioned cursor rebuild handles old summaries once without replaying child wakes. The supervisor persists target-scoped ordinary stall grace after observed active recovery; it leaves Git evidence, spent nudges and hard bounds unchanged. See [retry handling](operations.md#bb-retry-and-stall-handling). A blocked `provider/rateLimits/updated` window with a future reset marks the thread rate limited and supplies the reset time.
- **process** runs a status command that prints a small JSON object. A non-zero exit maps through `exit_codes`.
- **cursor** (unreleased) reuses the detached runner for standalone primary loops, with a bounded NDJSON lifecycle parser, matching initialization/terminal session identity and known zero exit required for turn completion. It reads prompts through stdin and preserves exact resume IDs. Missing or ambiguous identity blocks resumption. Print failures stay unknown; no fallback or switch-back path is enabled. See [Cursor contract](cursor.md).
- **claude** and **codex** start one headless turn per send as a detached process. The process writes its stdout and exit code into the target's directory, so a short-lived tick never waits for a turn. The next tick reads the result: the `is_error`, `subtype`, and `api_error_status` fields for Claude Code, and the event types for Codex. Each new turn resumes the saved session. On macOS, Linux and Windows, a detached copy of keepgoing runs the turn. Each wrapper receives a fresh random stop key, withheld from the agent environment. Stop writes a bounded private request and waits for its matching fresh acknowledgement; it never signals a saved PID. The live Unix wrapper is its own process-group leader and signals that group; the Windows wrapper retains an unnamed job handle containing itself and its descendants and terminates that job. Both avoid a saved-PID validation-to-signal race. See [owned cancellation](operations.md#owned-headless-cancellation) for legacy, crash and receipt limits.

## Runner contract

Unreleased source (roadmap K1). A command-line agent that runs one turn per process is described by one specification, `runner.Spec` in `internal/adapter/runner/spec.go`. Claude Code and Codex are on it. This section lists what the code shares and what it keeps per agent, as the code is.

**Common to every runner** (`runner.Dir` and `runner.Headless`; nothing here names an agent):

- The detached turn: a copy of the keepgoing binary runs the agent, so a check never waits for a turn. It records `run.json`, `stdout`, `stderr` and the exit code in `targets/<id>/`.
- The prompt, written to a private file and given to the agent on standard input. It is never in argv.
- Session storage: one saved identifier per target, read before a turn and saved after one.
- The phases `never_run`, `running`, `finished` and `crashed`, and what each means: never run is idle, running is active, crashed is an unknown error.
- For a finished turn: healthy only when the agent reported completion **and** the exit code is zero; otherwise an error with the class the output gave, or `unknown`.
- Refusing to send into a running turn, the owned stop through the live wrapper, environment overrides, output bounds and the runner's own exit evidence.

**Per agent** (the fields of `runner.Spec`):

| Part | Claude Code | Codex |
| --- | --- | --- |
| Name, default provider, default CLI | `claude`, `claude-code`, `claude` | `codex`, `codex`, `codex` |
| Account directory variable | `CLAUDE_CONFIG_DIR` | `CODEX_HOME` |
| Settings read from the loop | `cli`, `workdir`, `model`, `effort`, `permission_mode`, `max_budget_usd`, `extra_args`, `env` | `cli`, `workdir`, `model`, `sandbox`, `skip_git_repo_check`, `extra_args`, `env` |
| Start argv | `-p --output-format json` and flags | `exec --json` and flags, `-` last |
| Resume argv | adds `--resume <id>` before the extra arguments | inserts the `resume` subcommand, turns `-s` into a `-c sandbox_mode=` override, and puts the id before `-` |
| Prompt | standard input | standard input |
| Result reader | the last `result` object: `is_error`, `subtype`, `api_error_status`, `terminal_reason`, `session_id` | the JSONL event stream: `thread.started`, `turn.completed`, `turn.failed`, `error` and their codes |
| Structured limit lookup | none | the app-server's rate-limit snapshot, asked only when a failure is unclassified |
| Probe | `-p` with `stream-json`, no tools, one turn; judged by the result object; the initialization message is dropped from kept output | `exec --json --ephemeral`, read-only sandbox; judged by `turn.completed` or `turn.failed` |

**The reading.** A result reader returns a `Reading`: the session identity, whether the agent reported completion, whether it reported waiting on a person, the error class (or `unknown`) and structured evidence. It reads structured fields and the exit code only, never prose, and has no side effects. The shared runner turns a reading into an observation the same way for every agent. `Waiting` is carried to the observation's awaiting-input flag only for an agent that declares structured waiting-on-person; neither ported agent does, and a stand-in specification tests both cases.

**Capabilities.** Each specification declares what the agent does itself and reports in a structured form: retries itself, fails over itself, schedules itself, has child work, exact resume, can be stopped, structured errors, structured waiting-on-person. False means "not established", not "known absent". Both ported agents declare exact resume, can be stopped and structured errors, and nothing else: the repository holds no evidence for the rest. Two parts of a specification already have an effect: structured waiting gates the awaiting-input flag, and the [capability matrix](adapters.md) takes each ported agent's decision inputs from `Spec.ObservationInputs()` (state, error class and evidence for both; the reset time for Codex, which has the limit lookup). Its tests fail when a specification gains an input the table does not list, or when the shared runner refers to an observation field no specification can declare. Every declaration is tied to the code it describes by a test, so it cannot drift before K2 relies on it:

- the setting names are exactly the YAML keys of the agent's settings struct;
- the provider and default CLI are what configuration gives a loop that sets neither;
- the shared runner builds nothing for a prompt it cannot pass, and refuses to stop an agent that does not declare it can be stopped;
- exact resume means the resume command carries the saved session and the first one does not;
- structured errors means a coded failure is classified and comes with evidence;
- child work, stop, exact resume, structured errors and structured waiting agree with the capability matrix's rows;
- each ported agent's real runner is run over a healthy turn, a coded failure, an uncoded failure and unreadable output, with the limit lookup answering "blocked", and the observation fields it set must be exactly the table's inputs.

Retries itself, fails over itself and schedules itself are vocabulary for agents that supervise themselves (K3). Nothing acts on them, and a test refuses a registered agent that declares one.

**Registry.** `runner.Specs()` lists the built-in specifications; `runner.Resolve` also builds one from a definition. These read from it: the adapter factory, `internal/probe` (argv, verdict, kept output and the limit lookup), the saved-session check in `status`, the run graph's session link, and guided `fallbacks add` (account variable, existing logins, default quota group and the probe for a BB provider).

These still name agents and were left, with the reason:

- `internal/config` (the adapter list, defaults, validation, probe-kind defaults and probe settings) and both JSON schemas. `runner` imports `config`, so `config` cannot read the registry, and each agent's settings are a typed struct with its own YAML keys.
- `init` and the guided `fallbacks add` flow: they build those typed settings and carry per-agent wording.
- `internal/incident`'s allowed adapter names: a storage allowlist that should change deliberately.
- `doctor`'s version pattern, and the account variables `CLAUDE_CONFIG_DIR` and `CODEX_HOME` written out in `doctor` (where it reads an agent's CLI, account and working directory) and in `init` (where it builds an agent from an account directory). Both switch on the typed settings, so they were left with them.

**Not on the contract.** Cursor (`cursor.go`) keeps its own adapter and is the next to adopt it. From its code as it is, adopting needs three additions to the contract that neither ported agent uses: an output size limit on the turn's stream, a strict session rule (a returned session that differs from the saved one is refused, and so is a new conversation after a turn with no verified session), and a reading that can say the output is unsafe to act on, which the runner reports as an unknown state with an error. It also reads a crashed turn's output, where the shared runner does not. Antigravity has an unregistered runner of its own that no loop can select; see the [Antigravity foundation](antigravity.md#detached-runner). BB and process targets are not one-turn-per-process agents.

**Agents defined in configuration (roadmap K2, unreleased).** A loop or fallback may set `adapter: custom` and describe an agent keepgoing has no built-in runner for. `internal/config/custom.go` holds the definition and its validation; `internal/adapter/runner/definition.go` turns it into a `runner.Spec`, so it runs through the same shared runner, probe and factory as Claude Code and Codex. See [an agent defined in configuration](configuration.md#an-agent-defined-in-configuration) for the format.

To make room for it the contract no longer assumes a built-in agent:

- A specification never sees the configuration. `runner.Resolve` reads a registered agent's typed settings, or a definition's values, into `runner.Settings` (the four every runner needs, the rest by name), and `Spec.Argv` takes those.
- A limit lookup returns the general `runner.Limits`; `CodexLimits` is an alias for it.
- A probe's usage reading and its source are fields of the specification, not a table keyed by agent name.
- A probe with no configured executable uses the specification's default, not the probe kind. The usage identity keeps its earlier fingerprint, so stored readings stay valid.
- The exit code is part of each reader's "completed", so a definition can name its own healthy exit codes.
- A specification may bound the output that is read (`MaxOutput`) and may take its prompt from the private prompt file with an empty standard input (`PromptFile`). What a turn's standard input is, is an explicit start parameter that the environment cannot change.
- A turn that ended waiting on a person is reported as a deferred interaction until the supervisor has saved the awaiting-user hold (the existing `InteractionAdopter`), then no longer. After a person resumes, the turn is read by its other rules and the next send continues the session.

What a definition cannot express, by design or for now:

- Anything read from prose. A field is compared for equality with a declared code, and a value that is not shaped like a code is absent.
- State spread over several event types. One object is selected (the last, or the last whose field has a given value) and every rule reads that object; the session may select its own.
- Codex's limit lookup (a JSON-RPC exchange with a second process) and Claude's usage and credential-expiry readings. A definition's limit lookup is one command printing one JSON object; no usage reading or credential expiry is taken from a defined agent.
- Retries, failover, scheduling and child work. Validation refuses those capabilities because a definition gives no way to observe them. They are the subject of K3.
- A resume that needs a setting passed differently, beyond what a second argv can say.

## State directory

Runtime records live in `state_dir`, outside any repository. Managed service definitions additionally live in `~/Library/LaunchAgents` on macOS or `$XDG_CONFIG_HOME/systemd/user` (default `~/.config/systemd/user`) on Linux:

| File | Owner | Purpose |
| --- | --- | --- |
| `control.json` | operator, via control commands | enabled, paused, stop, stopLanesAt, awaitingUser, completed, note, ownership, pendingTransfer, bbTarget |
| `goal.json` | operator, via goal commands | bounded private specification history, stable goal identity and owning loop; independent of controls/completion |
| `sends.json` | the tool | versioned total reserved send attempts while `max_sends` is enabled; independent of diagnostic state, saved before adapter sends under the tick lock |
| `progress.json` | the tool | versioned Git source/fingerprint hashes, last verified change and observation time for the optional no-progress timeout; independent of diagnostic/per-turn counters |
| `state.json` | the tool | fingerprint, last progress, last send, re-prompt budget, active target, retry/recovery stall grace, last check decision/outcome and build label; latest returned automatic prompt provenance |
| `health.json` | the tool | probe evidence, the latest usage reading and its history per candidate |
| `probe-failures/<id>/` | the tool | raw output of the last few failed probes, for mapping new error shapes; never read for decisions |
| `runs.jsonl` | the tool | append-only events |
| `graph.json`, `graph.lock` | run reference writer | versioned run, attempt and link records and the last verified child listing (32 runs/128 attempts/256 links/200 children/256 KiB); independent lock and flushed atomic replacement; a failed write never fails a tick; never a decision input |
| `incidents.json`, `incident.lock` | tick diagnostic writer | versioned rolling snapshots, capped at 256 records/512 KiB with reserved key/condition/activity/routine tiers and collapsed runs of identical checks; independent lock and atomic replacement; never a decision input |
| `deliveries.json`, `delivery.lock` | report diagnostic writer | bounded typed delivery evidence (64 records/128 KiB); separate short lock; never send/replay authority |
| `daemon-endpoint.json` | daemon | private loopback endpoint and random verification key; removed on clean exit, stale records do not prove liveness |
| `service-launchd.json`, `service-systemd.json` | service commands | private backend-specific plan/recovery record; no task text or credentials |
| `tick.lock`, `daemon.lock`, `control.lock`, `service.lock` | the tool | advisory locks, released by the OS on exit |
| `targets/<id>/` | the tool | headless turn files (`run.json` with private stop key, stop request/acknowledgement, prompt, stdout, stderr, exit code, session id) the BB log cursor (`bb-cursor.json`), and, from internal fixtures only, create-once ACP interaction-hold records (`acp-prompt-reserved.json`, `acp-needs-user.json`, `acp-hold-adopted.json`) |

## Ownership and control writes

`enable` records `BB_THREAD_ID` when present. Ownership is persistent metadata; idle, missing, archived, or unavailable thread activity does not expire it. A different BB caller needs an explicit takeover. Calls without a BB identity use the existing claim without replacing it. This is a cooperative guard for local operators and schedulers, not authentication against a caller that can edit runtime files or environment variables.

Claims and takeover hold the tick lock, then the control lock. Every runtime control read-modify-write uses the short control lock; pause and stop do not wait for the tick lock. The one exception is a stop asked to run now (`stop --immediately`, `client stop --immediately`): after its control save it takes the tick lock, waiting at most five seconds, and runs one stop-only check (`supervise.Options.StopOnly`). That check skips the owner verification of step 1. It does only what the next check would do for the saved stop. If a resume has lifted the stop, it records a check that did nothing else. Control contention returns exit 75 for retry. No provider query occurs under the control lock. An update callback can reject a claim without writing control. Low-level `control.Save` is for initialization/fixtures, not concurrent runtime updates.

A daemon binds to the current owner ID and generation (including no owner). It checks the binding under the tick lock before ticks and early polls, and exits when it changes. Takeover cannot overlap an in-flight tick; it must retry after that tick releases the lock. Generations prevent a transfer back to a previous thread from reviving its old daemon. No BB thread is stopped, spawned, or re-parented by ownership handling.

Between daemon passes, the command layer compares control metadata and an in-memory SHA-256 digest of the opened regular file's observed bytes. The streamed digest prevents equal size/mtime from masking a change, without adding a persisted counter or exposing control content. Unreadable/non-regular observations remain unavailable; the signature only requests a full check and never authorizes provider work. Cooperating control writers use atomic replacement; arbitrary concurrent in-place or hostile filesystem changes are outside this snapshot guarantee.

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

## BB transfer integration

The plugin routes preview/apply/recovery through the recorded host and the CLI transfer protocol. Browser input contains only a registered setup ID, destination thread ID and one-use preview ticket. The server resolves both identities through BB, checks known destination conflicts, revalidates CLI metadata/controls/goal/labels, and persists a private `moves.v1` request before mutation. Only operational references are stored; no task text, raw CLI output or control notes enter this record.

Setup, transfer and resume requests share a plugin-instance queue; pause/stop bypass it and retain the CLI’s short control lock. A pending plugin request blocks start/resume and is visible on both associated threads. Managed resume also pins the expected thread, refusing stale source-view clicks after a move. Status reconciliation requires the expected primary/provider, no fallbacks, awaiting-user, final BB target binding and destination ownership before updating `setups.v1` and removing the request. These two saves are not atomic: recovery handles either boundary without replaying a verified completed move. Explicit recovery can continue a CLI pending ID or retry the saved source/destination request when no CLI move began; check-status never retries a command. The CLI remains the authority for locks, config snapshots, holds and target fencing.

This first flow supports plugin-created setups only, with no import, cancellation UI or external timer management. Plugin reload discards preview tickets but retains requests. External writers, other plugin instances and BB metadata changes are outside the in-memory queue; CLI guards still apply, and unknown state requires host inspection. Fakes verify the wire/controller/DOM paths; no live deployment is implied.

## Completion declarations

The command layer detects a new completion episode inside the serialized control update, saves `completed` before any delivery, then appends the control log and submits the subscribed `complete` report outside the lock. The saved flag is the deduplication marker; ticks never emit completion reports. Resume explicitly reopens the episode. Payloads describe an unverified declaration without private notes. The report transport uses one attempt without redirects for this event; other events retain their existing retry behavior. This favors avoiding duplicate notifications over guaranteed delivery after crashes, log/config failures or uncertain HTTP results. No independent acceptance verifier or goal revision binding is implemented yet.

## Private goal records

The control package also owns a separate, versioned `goal.json`. Goal edits acquire tick then control locks, verify the loaded config sources and current owner, and replace the private history atomically without rewriting controls. Revisions require the expected ID and current revision. Status exposes only generated identity, revision and the fixed `recorded` metadata state. References and next actions are opaque descriptions, never executable input or authorization/acceptance evidence. The decision engine does not consume this record. With explicit `prompt_goal`, the message renderer transports the latest specification to the adapter as prompt text; adapters do not read goal storage themselves. Goal-bound completion and stage results remain future work; see [private goals](goals.md).

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

A long limit wait is reported by the tick orchestrator after the decision and its action, never by the decision engine: `internal/supervise` reads the decision's typed limit-wait rule, the observed reset time and `errors.report_wait_after`, saves the wait to `state.json` and then requests its `limit_wait` report, so a restart cannot repeat it; the wait is cleared only on positive recovery evidence. It writes no control and returns the tick's decision unchanged. The `child_error` report for child threads in error follows the same shape: the pure rule in `internal/decide` says whether newly errored children are reported, and the orchestrator saves the episode before the one request.

Incident schema 5 records the hashed target alias after each check, typed probe results and saved manual retargets, so handoffs, switch-backs and retargets show both ends. `timeline` labels each event with fixed kinds and filters by kind, target key and time before applying the display limit; summaries ignore filters. A limit wait is labelled from a typed rule on the engine's decision, which no rule reads back, not from its reason text. Schema 6 reserves room for key events, ongoing conditions, activity and routine checks separately and folds runs of identical no-op checks, including repeated scheduled-probe results but never an attempted move or new probe result, into a count and time span on the run's latest record; tiers and run matching are derived from typed fields at the storage boundary, and `timeline` reports per-tier pruning and flags ranges that reach pruned history. Target keys remain alias hashes. Run, chat and conversation identity belongs to the [run discovery](run-discovery.md) graph (unreleased source) and is not defined here.

The view retains normalized source/sequence references, local timestamp bases, historical build labels, typed decision inputs and delivery attempts. Sorting by local timestamps is presentation only, not causal inference. Source-wide summaries remain independent of the bounded display limit. Missing, invalid, stale, future or unsupported evidence remains explicit; partial failures print usable sources before returning failure. No diagnostic conclusion authorizes sends, control changes or replay. See the [timeline contract](diagnostics.md#offline-incident-timeline).

## Transfer preview

The command layer implements `transfer preview` as an advisory read-only view. It loads bounded control/state/goal records and optionally makes two bounded `bb thread show --json` queries through the diagnostic subprocess runner. Only validated identity/provider/status fields are projected; environment paths are compared internally and withheld. It rechecks configuration source snapshots at the end, but independent reads are not an atomic snapshot and no lock or durable ticket is acquired. It never invokes adapters, controls, model changes, scheduling or thread creation. `compatible_metadata` describes only the checked subset, while `applySupported` identifies command availability rather than authorization. Model/account identity remains unavailable; see [transfer preview](transfer.md).

## Applying a BB transfer

The command layer serializes apply/recovery with ticks and performs bounded BB metadata checks before short control updates. `config.BBTransferEdit` retains private source/replacement bytes in memory, patches only a literal BB thread scalar, then checks and atomically replaces a regular private config. Checkout paths and YAML indirection are refused. `control.PendingBBTransfer` persists only identity, before/after digests and targets inside the first control save, alongside awaiting-user. A partial operation is held for explicit roll-forward recovery; no background retry or transaction coordinator is introduced.

The command guards combine source snapshots with the pending or final target binding. Pending records suppress automatic work and permissive controls; stop may use only an exact before/after config. Restrictive controls retain their short independent lock. Recovery repeats the primary observation reset without changing goal or allowance files, commits the config if still at the before version, and finishes with a new owner generation and persistent `bbTarget`. This fences old daemons and copied configs even after the pending record is cleared. Begin/finish use existing saved-control incident capture; diagnostic failure never undoes a control save. Existing bindings are checked before tick/poll, manual probes/retarget, enable/resume, validate and startup.

This is a cooperative, process-crash protocol, not a multi-file atomic commit or power-loss guarantee. Model/account identity, external BB metadata changes, old binaries and writers ignoring locks are outside its guarantees. See [transfer scope, recovery and preserved state](transfer.md).

## Local client registry

`internal/registry` owns a bounded schema-1 registry of explicit config/runtime references and a separate short advisory lock. The command layer canonicalizes registry/runtime paths while retaining config-relative path semantics, refuses checkout/project destinations, loads and validates configs for add, and checks their source snapshots before registering. Reads never query registered configs or create files. Add/remove never acquire loop locks or change controls. IDs are discovery references, not authorization or durable filesystem identities; future status/control requests must revalidate their bindings. Atomic replacement preserves a previous record on failed writes, without promising power-loss durability or coordination with external writers. See [local clients](local-clients.md) for privacy, compatibility and planned control requirements.

## Registered client status

The command layer composes registry/config binding checks, bounded local control/state reads, validated goal metadata and the existing runtime/daemon diagnostics into a separate schema-1 projection. It does not serialize ordinary status wholesale: notes, paths, owner/account identities, goal references and free-text check reasons are withheld. Explicit private display contains only the current goal title and next-action summary. Binding/source checks run again after the bounded local daemon query; changed bindings discard the snapshot. Reads take no locks and form independent evidence, not an atomic snapshot or a control authorization. Providers, schedulers, transcripts and session artifacts are never queried. See [registered status](local-clients.md#registered-status-schema-1) for outcome and freshness semantics; prepared resume is a separate explicit operation below.


## Registered restrictive controls

`client pause|stop` resolves an explicit registration and uses `registry.WithEntry` to fence cooperating add/remove writers through a short local operation. The command takes the registry lock before the control lock, both nonblocking; it never acquires the tick lock. Existing registry add/remove operations take no loop locks, and ordinary loop controls take no registry lock. The callback revalidates config/runtime/source bindings and bounded control evidence before adding one hold. The command preserves ownership, notes, other holds and pending transfer state, matching ordinary pause/stop semantics rather than claiming or transferring ownership.

The existing control incident recorder and run history receive the operation. Confirmed control saves remain confirmed when diagnostic writes fail; rejected preconditions and uncertain saves have separate versioned output. No provider, daemon, scheduler, notification or thread command runs. The saved flag is not evidence that a provider has stopped. Cooperative locks cannot fence arbitrary external file replacements; a changed config that cannot be revalidated is refused, and callers can use the original CLI with a known configuration for recovery. Registered resume uses the separate observation protocol below.


## Prepared registered resume

The command layer owns one bounded private `client-resume.json` ticket slot per runtime directory. `prepare-resume` acquires registry, tick and control locks in that order without waiting, validates owner/transfer bindings and snapshots current sources/control/state/goal evidence. It brackets the offline status projection with matching evidence reads and atomically saves a five-minute random ticket. `config.SourcesDigest` identifies config and fallback snapshots, including their paths, only for private comparison. No source digest or owner identity is projected to clients. Status remains lock-free and read-only; preparing is explicitly a write.

`resume` takes the same registry/tick locks and uses the existing checked control update/history recorder for the short control lock. It compares the ticket to fresh validated evidence and the callback's control record, persists consumption before mutating controls, then checks evidence again. Stale, expired, clock-reversed, consumed or superseded tickets are refused. A crash after consumption loses that ticket and may leave holds intact; only a new explicit preparation/action can proceed. Successful resume has ordinary CLI semantics without enabling, claiming, resetting allowances or starting work. Diagnostic failures cannot undo a saved control change. No provider, scheduler, transcript, task-file or live daemon lookup occurs under these locks.

All saved state fields are bound, so an intervening ordinary tick may require another reviewed preparation. Files referenced by the config and external session/BB metadata are not bound. This is a cooperative local stale-action guard, not authentication, an approval ledger, or a power-loss transaction. See [registered resume](local-clients.md#registered-resume) for output, privacy and recovery limits.

## Dock page (unreleased)

`cmd/keepgoing` implements `dock` as three separate layers so that remote reach can be added later without a rewrite. `dockGuard` accepts only loopback `Host` values, same-origin requests and `GET`/`HEAD`. `dockAuth` is an interface that decides who may read; its only implementation requires a per-run random token as the first path segment, compared in constant time. `dockReader` builds one versioned report (`/api/v1/dock`) that the embedded page consumes; ages are computed against the report's read time, not the browser's clock. The listener binds `127.0.0.1` only. There is no write endpoint and no control path.

The reader composes existing reads and no new evidence: `registry.Load`, `readClientStatus` (with its bounded query of the loop's existing daemon endpoint, reused for the same loop's status read), `readStatus` (the function behind `status --json`), `readRegisteredRunsWith` (behind `runs --registry-id`), `summarize` (behind `fallbacks list`) and `readTimeline` (behind `timeline`) for recent history, the record of a hold starting and how long the last result has repeated. A read runs on the command's context with its own time limit, never a request's; each registered configuration is validated once per read and shared with the runs read; an incomplete read is marked, never cached, and its unread loops are listed as needing a look. It takes no lock, creates no file, and runs no adapter, provider, `bb`, scheduler or Git command.

`dock_route_view.go` adds one loop's last decision, hold, device, lanes and recent history from the same status, runs and `readTimeline` values, wording reasons only from the fixed cause vocabulary. A loop that is not on its first route carries what its state records of that (the handoff time, the reported reset, the last switch-back attempt, and whether switch-back is configured), and the report groups routes into allowances by account group. The newest usage reading and test result any loop holds for a route are shown on every loop that lists it, marked as another loop's. `dock_view.go` is a pure projection from those values to plain-language fields with explicit unknown and stale markers and caps on every list and string. `TestDockPhrasesFollowEvidence` maps each record to the exact phrase shown, so wording that says more than its record has to be added there. `allClear` is true only when every registered loop was read and none needs a person. A loop's current route is "in use" only when the loop is on with no hold and its last check is recent and recorded that route. Each loop carries its ordered routes (`primary`, then fallbacks), and the report groups the same entries by route (provider, quota group, model and the probe's usage identity) for the view across loops, so every view reads one feed. Identifiers and hold notes are withheld unless requested, and paths are never included. The token keeps other web origins out; it is not authentication against a local process that can read runtime files. `panel_changes.go` is the only write path, present only on a background panel server started with `--allow-changes`: two-step prepare and confirm requests with their own guard and anti-forgery values, calling `applyClientHoldFrom` and `clientResume` in-process with no BB caller and the origin `panel`, which `updateControlFromAnnotated` records in the operation's value. See [the panel](dock.md).

## Goal-aware prompt composition

`internal/prompt` combines a configured scaffold with the latest `control.LoadGoal` specification only when `loop.prompt_goal` is true. `supervise` uses it for wake/nudge/re-prompt; `handoff` uses it for generated and custom briefs, including switch-back. Configuration parsing remains independent of goal storage. Production sends and cooperating goal edits share the tick lock; arbitrary external writers and already queued messages are outside that guarantee. Invalid/missing/wrong-loop goals return fixed errors before send reservation. No references are read or executed, no goal is revised, and no task text enters decision inputs or reports. Context transport is not approval, acceptance or semantic drift detection.


## Recorded automatic prompt provenance

Prompt and handoff composition return a private message plus the ID/revision of the exact goal read while rendering. Supervision passes that text to the adapter and captures its return as `state.PromptSend`, without rereading the goal or persisting task text. The latest returned wake, nudge, re-prompt, handoff or switch-back call is saved with ordinary tick state. Goal-disabled messages explicitly have no goal ID/revision. The record is diagnostic only; it never changes decisions, allowance reservation, retry policy or reporting.

There is no pre-send intent or complete attempt ledger: process termination or a failed final state write can leave an older record, and multiple candidate attempts retain only the last returned call. Adapter nil/error outcomes neither confirm delivery nor establish no side effects. Ordinary status compares recorded ID/revision to the independently read current goal, never to transcript text. Invalid/unsupported provenance is withheld without invalidating other state fields. Primary transfer clears the record with thread observations to avoid attributing old-primary sends to the destination. Other target aliases describe configuration at the historical check; arbitrary config edits are not tracked. Registered client status and incident journals do not yet project this evidence. See [recorded send provenance](goals.md#recorded-send-provenance).

## Run discovery (unreleased)

`internal/rundiscovery` defines a pure value contract separate from the decision engine and controls. It distinguishes an external routine invocation, a registry reference, a durable loop, a goal revision, an execution run, a send attempt and a provider chat/session. Explicit transition types and caller-supplied IDs/timestamps keep checks, wakes, retries, restarts, holds, goal revisions, transfers and scheduled triggers from opening or closing a run. It performs no IO. The [contract tables](run-discovery.md#identity-and-run-boundary-contract-d1-internal-unreleased) describe boundaries and validation.

`internal/rungraph` persists that contract as `graph.json` in the state directory and validates every stored record against it. The command layer records a run boundary after a saved control change. A tick records the boundary implied by saved control state before it runs, a send intent before each adapter send and its return afterwards, and the goal revision and chat/session reference known without a provider query. These calls sit beside the tick, not in it: they return nothing to the decision path, take only the graph lock, and a failure adds one warning. The decision engine never reads the graph. See [the persisted graph](run-discovery.md#persisted-reference-graph-d2-unreleased).

Current-target resolution after transfer and lock-holder identification remain unimplemented. Graph records and adapter-return outcomes grant no authority or proof of delivery.
