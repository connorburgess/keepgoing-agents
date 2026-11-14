# Managed user services

`keepgoing service install|status|remove` manages one macOS GUI LaunchAgent or Linux systemd user unit per canonical loop state directory. Both backends are implemented on `main`, after v0.1.1. Windows scheduling is not implemented by this command yet; use `start --background` or an external scheduler there. Existing scheduling options remain available. The [Windows investigation](windows-services.md) describes the proposed Task Scheduler backend and the gates that must pass before Windows service commands can ship.

## The recommended way to keep checks running

On macOS and Linux, `keepgoing service install` is the recommended way to keep a loop's checks running: the operating system restarts the service, and it does not depend on another scheduler's failure counting. `init` prints the command. A `start --background` daemon and an external scheduler, including a BB automation, stay supported; an external scheduler can disable itself, and the [overdue rule](operations.md#external-schedulers-can-stop-on-their-own) says so when it does.

**Moving a loop from a BB automation to the service.** Nothing does this for you. For a loop whose checks a BB script automation runs:

1. Preview: `keepgoing service install -c /private/example-loop/keepgoing.yaml --dry-run`.
2. Disable the BB automation in BB's Automations. Keep it, rather than deleting it, so you can go back.
3. Install: `keepgoing service install -c /private/example-loop/keepgoing.yaml`.
4. Check: `keepgoing service status -c /private/example-loop/keepgoing.yaml` reports the service healthy, and within one interval `keepgoing status` shows a new check.
5. To go back: `keepgoing service remove -c /private/example-loop/keepgoing.yaml`, then enable the automation again.

Run one scheduler per loop. A short overlap during the move is safe: the tick lock makes a second check at the same moment exit 75 without acting.

If you keep the automation, have it identify itself, `keepgoing tick -c <config> --routine bb-automation/<automation id>`, so a stopped automation is named in `status`, the panel and the `check_gap` report.

## Health findings in service status

`service status` now adds a read-only assessment in both text and JSON. The JSON `health` object contains `status` and a `findings` array; each finding has a stable `code`, a `status` (`warning` or `unavailable`), a message, and a suggested next step. Install/remove previews do not claim a health assessment.

| Health status | Meaning |
| --- | --- |
| `not_installed` | No recovery record or definition exists and the manager confirms absence. A manual daemon or external timer may still run. |
| `ok` | No warnings in the available observations. Consult `binding` separately for Linux service-to-daemon identity; health status alone does not establish it, provider health, or future execution. |
| `warning` | At least one observation needs attention; inspect all findings for details. |
| `unavailable` | No warning was established, but some assessment data could not be verified. |

Findings distinguish missing recovery files/registration, unknown manager responses, failed/inactive/transitioning or disabled systemd units, and an unverified loop daemon. A responding daemon does not cancel a missing-registration or unit-failure warning: it may have been started manually. macOS disabled overrides and detailed launch failures remain unavailable. Recorded, loaded macOS services now explicitly report the unsupported identity and execution checks described below.

For a responding loop daemon, findings also flag a different/unknown build label, dry-run mode, and a waiting-phase normal check more than one minute overdue. The overdue comparison uses the daemon's own observation and next-check timestamps, with a fixed one-minute grace. It does not time out an in-progress check or declare the process stalled. Sleep, scheduling delays, and clock changes can explain an overdue timestamp. Historical heartbeat freshness remains separate in `status` and `doctor`, using their `--stale-after` setting.

Daemon build-label comparison concerns the responding loop daemon and the invoking CLI. It does not prove that the responder belongs to the service or say which build is newer. The separate executable-file inspection below compares on-disk contents without interpreting build labels. Empty/development labels remain unavailable. A healthy agent cannot be inferred from these checks.

The assessment uses structured manager and daemon observations without writes, provider probes, restarts, or control changes. Recorded, loaded Linux units receive one additional bounded execution-details query. Recorded services also receive the read-only executable-file inspection below. Recorded, loaded Linux services additionally check process/invocation identity around the daemon challenge as described below. Holds are never cleared as a repair. Existing exit-code behavior is preserved: warnings can accompany exit 0; unknown registration, a loaded unrecorded service, and existing file/config validation errors still fail. Scripts should inspect `health.status` and `health.findings`, not treat exit 0 as a health certificate. Modified/unreadable definitions still fail validation before a health report can be produced.

Pure table tests and fake-manager/local-diagnostic fixtures cover combined failures, grace boundaries, redaction, human/JSON output, and unchanged controls/provider-call counts. These tests do not establish live service startup, logout/reboot, or soak behavior.

## macOS diagnostic gaps

For a recorded, loaded LaunchAgent, status explicitly emits `binding.state: unavailable` and `execution.state: unavailable`. The execution object contains no invented result, exit code, or signal. Health includes `service_binding_unavailable` and `launchd_result_unavailable`; otherwise clear observations produce `health.status: unavailable`, while any warning still takes precedence. These capability declarations require no additional manager command. Absent or unknown registration keeps its existing findings instead of attaching execution/binding assessments.

A responsive loop daemon may have been launched manually. Registration, matching executable files, and a matching daemon build label cannot prove that the LaunchAgent owns that responder. Restarting, upgrading a daemon's identity metadata, or clearing a hold does not resolve this unsupported macOS check. Inspect the exact LaunchAgent manually when diagnosing a failure; keepgoing does not turn diagnostic prose into a decision.

The [macOS API investigation](macos-diagnostics.md) records why launchctl text and legacy job dictionaries are unsuitable for reliable automatic classification, and separates launch approval from process identity and exit history. The explicit unavailable reports are implemented and covered by fake-manager/local-responder fixtures. Startup/exit inspection, service-to-daemon binding, approval inspection, and live service recovery are not implemented or verified by this change.

## Systemd execution details

For a recorded, loaded Linux service, `service status` makes a second read-only `systemctl show` query for `Result`, `ExecMainCode`, and `ExecMainStatus`. It rechecks the exact unit ID, loaded state, canonical fragment path, absence of drop-ins, and the previously observed active state. A changed identity/state, failed query, unexpected field, unknown value, malformed reply, or oversized output makes execution details `unavailable`. This query does not participate in install/remove decisions.

The text report and JSON `execution` object expose:

- `state`: `available` or `unavailable`.
- `result`: an allowlisted manager result such as `success`, `exit-code`, `timeout`, `resources`, `start-limit-hit`, or `oom-kill`, with a fixed explanation in `message`. A non-success result adds a `unit_result` warning, including the `exec-condition` skipped-service result. Unknown details add `unit_result_unavailable`.
- `mainExit`: `not_reported`, `exited`, `killed`, or `dumped`. `mainStatus` is present only for a reported exit: a numeric exit code or signal number. Zero with `exited` means a clean exit; `not_reported` does not distinguish a running process from one that has never run. Unrecognized signal numbers remain unavailable.

These properties can describe a previous run. They are not evidence that the current process failed or that it belongs to the loop's diagnostic responder. A `success` result alone does not prove startup or healthy operation. Exit code 203 is shown numerically rather than attributed conclusively to an executable failure: a program can return that code itself. No journal, transcript, service-supplied status text, command arguments, or raw error output is read into the report. Manager results never become provider-limit or failover signals, and no restart or hold-clearing action follows.

The field meanings follow the [systemd service interface](https://github.com/systemd/systemd/blob/v255/man/org.freedesktop.systemd1.xml) and [result enumeration](https://github.com/systemd/systemd/blob/v255/src/core/service.c). Offline fixtures cover reported failures and exits, redaction, identity changes, unknown schemas, and unchanged controls/files/provider-call counts. Live installed-service failure and recovery checks remain unverified. macOS launch failure details and macOS service-to-daemon identity remain planned. Executable-file inspection is described below.

## Recorded executable inspection

For recorded macOS and Linux services, `service status` inspects the executable named in the private recovery record, even if registration is absent or unavailable. It never runs that path, including for a version query. JSON adds `executable` with `state`, `comparison`, and `message`; text shows the same observations. An unrecorded service has no executable assessment, and install/remove decisions do not use it.

| State | Meaning |
| --- | --- |
| `available` | A stable regular file was readable within the size bound and had at least one Unix execute bit. This does not establish effective execute access or a valid binary format. |
| `missing` | The recorded path, including any symlink target, was absent at inspection. |
| `non_regular` | The path resolved to a directory, FIFO, or another non-regular file. |
| `no_execute_bits` | The regular file had no Unix execute bits. |
| `unavailable` | Inspection failed, was cancelled, detected a file change, or exceeded the 128 MiB per-file size bound. Raw errors are withheld. |

For an available recorded file, `comparison` is `match` or `different` when its SHA-256 content digest can be compared with the invoking CLI's file on disk; otherwise it is `unknown`. The `executable` object never exposes digests, file contents, paths, build settings, or raw errors. It does not use a version subprocess, provider call, or model request. This is an on-disk content comparison, not proof of authenticity, build age, loaded process image, or service-to-daemon identity. In particular, replacing a file after the CLI started can make its current on-disk contents differ from the running image, and matching files do not establish a valid executable format.

Missing/non-regular/non-executable files and differing contents add warnings with codes `executable_missing`, `executable_non_regular`, `executable_no_execute_bits`, and `executable_differs`. Unavailable inspection or comparison adds `executable_unavailable` or `executable_comparison_unknown`. No repair, reinstall, restart, or control change follows; warnings retain the existing status exit-code behavior.

Inspection follows installation symlinks, opens nonblocking to avoid waiting on a concurrently substituted FIFO, checks descriptor and resolved-path identity/size/mode/modification time before and after bounded reads, and checks cancellation between chunks. These are best-effort observations for cooperative local installations, not an atomic filesystem snapshot or a security boundary. Filesystem calls themselves may wait on storage; the size bound is not a wall-clock timeout. Effective ACL execution permission, executable format/platform, code signing, build labels, and the binary actually loaded by a service are not verified.

Temporary-file fixtures cover matching copies, changed contents, symlinks and broken targets, missing files, permissions, FIFOs, size bounds, cancellation, identity/metadata changes, redaction, unchanged controls/files, and removal with a missing executable. Native macOS/Linux tests read the test executable without executing it. Fake-manager status tests cover both backends; no live service failure, restart, or recovery is part of this evidence.

## Linux service-to-daemon binding

For a recorded, loaded Linux service, `service status` now brackets its fresh authenticated daemon challenge with bounded `systemctl show` observations of the exact unit ID, loaded/active state, canonical fragment path, empty drop-ins, `MainPID`, and `InvocationID`. The main PID and invocation must be valid, nonzero, and unchanged across both observations. The signed daemon snapshot must report the same PID and invocation. No journal, transcript, process command line, or raw manager error is parsed.

The text report and JSON `binding` object contain `state` and a fixed `message`:

- `verified`: the authenticated responder matches the stable active systemd main process and runtime cycle observed around the challenge.
- `different`: stable manager metadata and a responsive daemon with valid metadata identify different processes or invocations. This adds the `service_daemon_differs` warning.
- `unavailable`: missing/unknown metadata, a nonresponsive or older daemon, a manual launch without invocation metadata, inactive/transitional units, failed queries, malformed replies, foreign identity, or a change between manager observations prevents verification. This adds `service_binding_unavailable`; responsiveness can still be reported separately.

A recycled PID alone cannot verify a binding: a changed invocation invalidates it. New daemons capture their own PID and, on Linux, a valid `INVOCATION_ID` at startup. The fields are covered by the existing fresh-challenge MAC and state-directory binding. They are not stored in the endpoint record or exposed in public status/doctor/service output. Older daemons remain queryable, but lack the optional identity metadata needed for verification. Non-Linux platforms do not claim this binding, and unrecorded or unloaded services receive no binding assessment.

This is a point-in-time observation for cooperative local processes. It does not establish which executable image is loaded, authenticate against a local user who can modify the private endpoint key or process environment, or prove provider health/future operation. Manager snapshots are not a continuous monitor or an atomic OS transaction; identity changes yield unavailable observations, and execution can stop after a successful check. Namespace or environment differences can prevent verification. Binding findings do not stop a process, restart a service, clear holds, or participate in failover or install/remove decisions.

Systemd documents the per-runtime-cycle [invocation environment](https://github.com/systemd/systemd/blob/v255/man/systemd.exec.xml) and structured [unit/service properties](https://github.com/systemd/systemd/blob/v255/man/org.freedesktop.systemd1.xml). The [`show` property formatter](https://github.com/systemd/systemd/blob/v255/src/shared/bus-print-properties.c) supplies decimal PID and hexadecimal invocation values. Verification uses pure comparison tables, fake-manager queries, and a native Linux local diagnostic responder with a fictional invocation environment. These fixtures cover PID reuse, invocation changes, old/absent identity, mismatches, malformed replies, redaction, unchanged holds/files, and no provider calls. **No live installed systemd service has been bound or restarted for this change.** macOS binding and live service lifecycle/soak checks remain outstanding.

## Linux systemd user services

Use a stable installed binary and private configuration outside the checkout. End any existing daemon and explicitly remove any external timer for the loop before switching scheduling methods. These commands do not discover or change BB automations, cron jobs, or other units:

```sh
keepgoing service install -c /private/example-loop/keepgoing.yaml --dry-run
keepgoing service install -c /private/example-loop/keepgoing.yaml
keepgoing service status -c /private/example-loop/keepgoing.yaml --json
keepgoing service remove -c /private/example-loop/keepgoing.yaml
```

Installation requires Linux, `/usr/bin/systemctl`, and an already accessible user manager. systemd does not accept quote or backslash characters in the executable path; install the binary at a stable path without those characters. Argument paths are escaped separately. It writes a mode-0600 unit under `$XDG_CONFIG_HOME/systemd/user` (default `~/.config/systemd/user`) and a private `service-systemd.json` recovery record in the loop state directory. XDG_CONFIG_HOME must be absolute, and status/removal must use the same home/config directory as installation. Destinations inside Git checkouts, existing records, and unrecorded/changed unit files are refused. Preview runs only local setup checks and makes no manager calls or writes.

After local validation, install reloads the user manager, checks the exact unit identity and fragment path, enables it for `default.target`, and starts it. A competing daemon or conflicting BB owner prevents installation. **Installation never enables or resumes the loop itself:** all control flags, notes, ownership, and targets stay unchanged. An enabled loop can perform its normal work as soon as the daemon starts. Validation runs no provider, login, or probe commands.

The generated unit invokes the state-directory-bound keepgoing entrypoint without a shell. It quotes argument values, escapes percent specifiers, and disables dollar expansion in ExecStart. It captures PATH and clears BB_THREAD_ID, but does not copy other shell values into files. The user manager can supply its own inherited environment; this is not an isolated credential environment. Explicit account settings still belong in the existing agent/probe configuration. See systemd's [command-line rules](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html#Command%20Lines) and [execution environment](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html#Environment=).

The unit uses `Type=exec`, `Restart=on-failure`, and a 30-second restart delay. Raw daemon stdout/stderr goes to the null device; structured loop history remains available through status and doctor. User-manager lifetime depends on host login policy and existing lingering settings. keepgoing does not enable lingering, create a login session, install a system service, or promise work during logout, sleep, or host shutdown. A hold keeps the daemon alive while suppressing work; no missed ticks are queued for replay.

Status consumes only the bounded machine-readable properties of [`systemctl --user show`](https://www.freedesktop.org/software/systemd/man/latest/systemctl.html#show%20%5BPATTERN%E2%80%A6%7CJOB%E2%80%A6%5D). Reports distinguish loaded/absent/unknown registration, active state, and enablement. The exact ID, expected fragment path, empty drop-ins, and recognized property values must match. Aliased, masked, overridden, foreign, malformed, inaccessible, or unfamiliar units return unknown/failure without exposing raw manager output. Each manager command has a ten-second deadline and output bound. A separate daemon challenge verifies loop responsiveness. The optional Linux binding assessment below checks whether that responder matches the unit main process. Manager registration alone does not establish provider access or daemon health.

Removal disables and stops the exact recorded unit, verifies inactive/disabled state and the loop's free daemon lock, deletes the matching unit file, reloads the manager, and verifies absence before deleting the recovery record. Modified or symlinked files and surviving enable links are refused. The record remains after a failed final reload, so removal can be retried even when the unit file is already gone. Unknown results never count as absence. A concurrent manual daemon is not killed; end it explicitly if its lock prevents cleanup. External edits are not serialized by keepgoing's service lock.

The unit deliberately uses [`KillMode=process`](https://www.freedesktop.org/software/systemd/man/latest/systemd.kill.html#KillMode=): removing the supervisor must not implicitly kill detached agent turns. systemd generally discourages this mode because descendants can remain after the service stops; those turns remain the agent adapter's responsibility. Use `keepgoing stop` and verify its result first if the agent should stop too. Survival across logout or user-manager shutdown is not guaranteed.

An interrupted install/removal returns failure and retains its private record for inspection/retry. For a missing or damaged config, use `service status --state-dir /private/example-loop/state` or `service remove --state-dir /private/example-loop/state`. Restore the original XDG_CONFIG_HOME if it changed. Missing/corrupt recovery records, foreign definitions, and drop-ins require manual inspection; keepgoing does not guess which service to stop.

Offline fixtures cover manager failures, recovery, identity checks, control preservation, and quoting; Linux tests validate the generated unit with systemd-analyze without starting it. **No live systemd user service has been installed or removed for this implementation.** Login/logout/reboot, manager environment, detached-turn survival, and prolonged service operation remain unverified.

## macOS LaunchAgents

The following sections describe the macOS backend.

## Install and inspect

Use a stable installed keepgoing executable and a private configuration outside the checkout. Review the task and remove any existing external timer for this loop before changing scheduling methods. End an existing foreground/background daemon first; installation refuses a held daemon lock. It does not discover or remove cron jobs, BB automations, or manually installed service definitions.

```sh
keepgoing service install -c /private/example-loop/keepgoing.yaml --dry-run
keepgoing service install -c /private/example-loop/keepgoing.yaml
keepgoing service status -c /private/example-loop/keepgoing.yaml --json
```

The preview validates local setup and shows the proposed managed definition path without writing files or running launchctl. Actual installation checks ownership, local setup, and that its service label is absent in an accessible GUI domain. It writes a private plan record under `state_dir` and a mode-0600 plist under `~/Library/LaunchAgents`, then enables and bootstraps that exact service. Existing service records or definitions are never overwritten. After moving the binary/config or changing the captured PATH, remove and reinstall explicitly.

Installation **does not enable or resume the loop**. It preserves all control flags, notes, ownership, and targets. A disabled loop stays disabled; use `keepgoing enable -c ...` explicitly when ready. A paused, stopped, completed, or awaiting-user loop keeps its hold; only `resume` clears those holds. An already enabled loop can perform normal checks, sends, or configured probes as soon as bootstrap starts its daemon. Installation validation itself does not run providers, probes, or login commands.

The plist invokes keepgoing directly with separate arguments, without a shell, task text, or credentials. The label hashes the canonical state directory. The service refuses startup if the config now resolves to another state directory; remove using the old state directory before reinstalling for a new one. Different configs sharing state cannot install competing managed services. An existing BB owner is respected during installation. The scheduled daemon runs without BB caller identity and uses the persistent owner claim; it cannot transfer ownership. The service may restart after an ownership change, using the new recorded owner and current controls.

## Environment and lifecycle

The generated definition captures only the installing shell's `PATH` and explicitly clears `BB_THREAD_ID`. It runs from the configuration directory. It does not copy the shell's credentials, webhook secrets, account variables, proxy settings, or other environment values. Provider commands must be available from that PATH or configured with absolute executable paths. Configure explicit account directories through the existing agent/probe environment settings; shell-only login selection is not automatically preserved. Reports whose required secret variables are unavailable may be disabled by the ordinary daemon. A successful registration does not establish login validity or provider access.

The LaunchAgent runs at load and requests restart after an unsuccessful daemon exit, with a 30-second launch throttle. A clean daemon exit does not request immediate restart. Loop completion holds future work while leaving the daemon alive. As described in [Apple's launchd guide](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html), per-user LaunchAgents are loaded for the user's login session and receive termination on logout. Login after reboot can load an installed agent; there is no promise of execution before login, during sleep, or while the host is off. A GUI domain must already be accessible; this command does not create a login session or install a privileged daemon.

Raw daemon stdout/stderr goes to `/dev/null`. Structured decisions and outcomes stay in the loop's private runtime history. Use `status`, `doctor`, and `service status`; run in foreground for raw daemon errors. The service does not queue missed ticks for replay after sleep. Network failures retain the ordinary structured-error policy; in unreleased source, recovery [waits while the connectivity check fails](operations.md#network-outages). Live login/logout/reboot recovery and prolonged service operation remain unverified.

## Status and removal

`service status` reports whether a private record and matching definition exist, and whether launchctl reports the label loaded, absent, or unknown. A loaded registration is not proof that its daemon is healthy or running. A separate daemon challenge reports loop liveness and build comparison, but does not establish that this particular service owns the responder. Disabled launchd overrides, process IDs, detailed exit reasons, other timers, and the next OS launch are not inferred from launchctl's human-readable output.

Only launchctl exit status is consumed: exit 0 from the service query means loaded; exit 113 means absent only when a second query confirms the GUI domain is accessible. Other exits, execution failures, and timeouts are unknown. Each manager call has a ten-second deadline, and raw output is discarded. The missing-service/domain behavior was checked read-only on macOS 26.4; unfamiliar results fail conservatively.

```sh
keepgoing service remove -c /private/example-loop/keepgoing.yaml --dry-run
keepgoing service remove -c /private/example-loop/keepgoing.yaml
```

Removal requires the private record to match this user/state directory and any existing plist to match the exact recorded definition. Symlinked, modified, oversized, and unrecorded files are refused. Removal disables the exact label first, boots out a loaded service, verifies absence, waits up to ten seconds for the loop’s daemon lock to become available, and then deletes its plist and record. The disabled launchd override remains; a later explicit installation enables that label again. Successful removal leaves no registration or plist for this managed service. It does not remove other timers or terminate a separately started daemon. A daemon still holding the loop lock makes cleanup incomplete; end that process explicitly and retry removal.

Removal preserves loop controls and sends no agent stop command. Booting out the supervisor cancels any in-flight supervisor command; an already running detached agent turn may continue. To stop the agent too, request `keepgoing stop` and verify the stop before removing its supervisor.

## Recover an interrupted operation

Filesystem and launchctl operations are not atomic together. An unsuccessful install may leave its record/plist and may already have registered the job. It returns failure with an incomplete-install message; inspect it or remove it explicitly. A failed removal retains recovery files. Once disable succeeds, that exact label stays disabled even if a later step fails. No unknown registration result is treated as success or absence, and no edited definition is silently deleted.

If the config is broken, missing, or now names another state directory, use the original runtime directory directly:

```sh
keepgoing service status --state-dir /private/example-loop/state --json
keepgoing service remove --state-dir /private/example-loop/state
```

`--state-dir` is available only for status/remove and cannot be combined with `-c`. If the recovery record itself is missing, corrupt, or no longer matches the definition, automatic removal refuses. Restore the recorded files from a known backup or inspect the exact LaunchAgent manually; the command never guesses which unrelated service to unload. All runtime records and definitions belong outside Git checkouts and must remain private.

Offline manager fixtures cover these operations and failures. Native macOS tests validate plist syntax, and a read-only check verified the missing-service exit code. No live LaunchAgent was installed or removed for this implementation, and no real provider call was used. Installed-service lifecycle and soak verification remain roadmap acceptance work.
