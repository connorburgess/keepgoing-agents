# Managed user services

`keepgoing service install|status|remove` manages one macOS GUI LaunchAgent or Linux systemd user unit per canonical loop state directory. Both backends are implemented on `main`, after v0.1.1. Windows scheduling is not implemented by this command yet; use `start --background` or an external scheduler there. Existing scheduling options remain available. The [Windows investigation](windows-services.md) describes the proposed Task Scheduler backend and the gates that must pass before Windows service commands can ship.

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

Status consumes only the bounded machine-readable properties of [`systemctl --user show`](https://www.freedesktop.org/software/systemd/man/latest/systemctl.html#show%20%5BPATTERN%E2%80%A6%7CJOB%E2%80%A6%5D). Reports distinguish loaded/absent/unknown registration, active state, and enablement. The exact ID, expected fragment path, empty drop-ins, and recognized property values must match. Aliased, masked, overridden, foreign, malformed, inaccessible, or unfamiliar units return unknown/failure without exposing raw manager output. Each manager command has a ten-second deadline and output bound. A separate daemon challenge verifies loop responsiveness, not that this unit owns that responder. Manager registration alone does not establish provider access or daemon health.

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

Raw daemon stdout/stderr goes to `/dev/null`. Structured decisions and outcomes stay in the loop's private runtime history. Use `status`, `doctor`, and `service status`; run in foreground for raw daemon errors. The service does not queue missed ticks for replay after sleep. Network failures retain the ordinary structured-error policy. Live login/logout/reboot recovery and prolonged service operation remain unverified.

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
