# Testing

For a built or unpacked executable, [release verification](release-verification.md) provides a provider-free setup smoke check, a [local recovery smoke check](release-verification.md#check-recovery-with-local-stand-ins), and a separate live walkthrough. Both scripts run in macOS/Linux CI. Recovery uses stand-in providers, a loopback webhook and actual temporary daemon processes; neither script certifies real provider sessions.

## On a machine with Go

```sh
go vet ./...
go test -race ./...
```

The end-to-end tests in `cmd/keepgoing` drive the real CLI against fake `bb`, `claude`, `codex`, and `gh` executables and a local webhook receiver. They need a POSIX shell, so they run on macOS and Linux only.

Guided-setup tests use temporary Git repositories and fake CLI discovery. They cover wizard/flag equivalence, private disabled output, task-file handling, ordered account routing, operating limits, cancellation, existing-file preservation, and checkout/symlink rejection. Structured setup fixtures also cover JSON preview/create consistency, ordered account metadata without account paths, stdin bounds/UTF-8/errors/cancellation, and failed creation without a success report. They make no model calls and do not establish live account or IDE compatibility.

LaunchAgent tests use an injected fake launchctl and temporary private directories. They cover install/status/remove, unchanged holds and ownership, conflicting installers, duplicate refusal, failed bootstrap, interrupted removal, unknown registration, modified/symlinked files, checkout rejection, config-independent recovery, XML escaping, and state-directory binding. macOS also validates the generated plist with `/usr/bin/plutil -lint`. A read-only macOS 26.4 check confirmed exit 113 for a fictional missing service and exit 0 for its accessible GUI domain. These checks do not install a real service or establish login/logout/reboot behavior; live lifecycle and soak checks remain outstanding.

## On a machine or VM without Go

Build a bundle on a machine that has Go, copy it over, and run it there:

```sh
scripts/build-test-bundle.sh linux arm64 out/linux      # writes out/linux/bundle.tar.gz
scripts/build-test-bundle.sh windows arm64 out/windows  # writes out/windows/bundle.zip
```

Unpack the archive in a scratch directory on the target. Then run `src/run-tests.sh` on Linux or macOS, or this on Windows:

```powershell
powershell -ExecutionPolicy Bypass -File src\run-tests.ps1
```

Each package's test binary runs from its own directory, so fixtures resolve as they do under `go test`. The bundle also holds a `keepgoing` binary for smoke checks such as `keepgoing validate -c examples/keepgoing.bb.yaml`.

## Platform results

These results come from runs of the full bundle. A VM result covers that configuration only.

| Platform | How | Result | Not covered |
| --- | --- | --- | --- |
| macOS 26, Apple silicon | `go test -race ./...` | all packages pass | none |
| Ubuntu 26.04 LTS ARM64 | test bundle in a Parallels VM, run as an ordinary user | all 18 test packages pass, including end-to-end, headless runner, and lock tests | x86-64 |
| Windows 11 Arm (build 26300) | test bundle in a Parallels VM | all 18 test packages pass, including the Windows lock and the headless runner's lifecycle, error, and stop tests | the end-to-end CLI tests (POSIX only), x64 |

CI runs the suite on Ubuntu x86-64 and macOS and cross-compiles for Windows. Check the Actions tab for the latest run.

Systemd user-service tests use a fake manager with structured show properties and temporary private unit files. They cover lifecycle and unchanged controls/ownership, duplicate/competing installs, dry runs, interrupted install/removal (including failure after deleting the unit), broken-config recovery, unknown/malformed/oversized replies, foreign fragments/drop-ins, XDG paths, changed files, and escaping. A native Linux fixture runs `systemd-analyze verify --generators=no` on a generated unit without installing or starting it. No live user bus, lingering change, login/logout/reboot check, agent call, or soak is part of this evidence.

Service-health assessment uses pure table tests plus fake launchd/systemd and local diagnostic responders. Fixtures verify missing registration despite a responder, failed/disabled units, unknown data, build differences, overdue waiting checks and the exact grace boundary, long-running checks, unchanged holds/ownership, no provider calls, and text/JSON redaction. Assessment performs no repair. Recorded, loaded Linux service status adds a bounded structured execution query; separate fixtures cover result/exit mappings, zero versus missing exits, unknown codes, malformed/oversized/error replies, changed identity/state, redaction, unchanged private files and holds, and removal despite unavailable diagnostic fields. All execution-failure evidence uses fake managers, not a live installed service. These fixtures do not verify installed-service identity or live recovery.

Service-executable inspection uses native temporary-file fixtures on macOS/Linux for matching/different content, symlinks, missing files, absent execute bits, denied reads (non-root), FIFO rejection/nonblocking open, size bounds, cancellation, and identity/metadata changes. A native fixture hashes the test executable; script fixtures have a marker proving they were never run. Fake-manager status tests verify both service backends, unchanged controls/recovery files, no provider calls, redaction, and removal with a missing executable. The comparison concerns on-disk files only; loaded process images, executable validity, signing, effective execute access, live service startup/recovery, and adversarial filesystem changes are not verified.

Linux service-to-daemon binding uses pure tables and fake-manager queries to cover matching identities, mismatches, reused PIDs with changed invocation IDs, old/manual daemons without identity, malformed/oversized/failed manager responses, and changed unit identity/state. A Linux-only integration fixture compares fake manager metadata with a real local authenticated responder using a fictional invocation environment; it checks human/JSON redaction, absent/stopped responders, unchanged private files/holds, and zero provider calls. Shared daemon-info tests cover optional metadata, invalid invocation redaction, backward-compatible responsiveness, and fresh challenge verification. This is not evidence of a live installed service, process-image identity, or macOS binding.

Child-input precedence is tested through the real fake-CLI adapter and supervisor poll with a three-second test deadline, separately from command-level control-cycle timing. It no longer assumes that notification scanning plus parent/child subprocess inspection always fits a 500 ms poll under race/build load. Existing daemon fixtures still verify early timer advancement, bounded slow commands, holds, tick-lock contention, and saved-cursor deduplication. A deterministic supervisor fixture cancels during observation after consuming a one-shot signal: no early check, replay, prompt, or hold mutation occurs, and the next ordinary tick still records the child's awaiting-user hold without sending or probing. Production polling limits and at-most-once notification semantics are unchanged.

## BB setup plugin

Run `npm ci`, `npm run typecheck`, and `npm test` in `integrations/bb-plugin-keepgoing`; `bb plugin build` checks bundle generation when the BB CLI is available. The plugin CI job runs typechecking and tests. SDK fake-host tests cover metadata resolution, host routing, private reference persistence and reload, and absence of thread creation/send calls. Controller fixtures cover preview/creation/start separation, stale input, repeated/concurrent requests, holds, failed diagnostics, and error redaction. A real local Node subprocess fixture verifies stdin and bounded output; it starts no agent. DOM fixtures cover the three explicit user actions and visible failure states. The public-SDK scanner checks package boundaries. None of this verifies live deployment, remote host connectivity, paid turns, or install-to-completion behavior.
