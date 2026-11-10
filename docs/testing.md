# Testing

## On a machine with Go

```sh
go vet ./...
go test -race ./...
```

The end-to-end tests in `cmd/keepgoing` drive the real CLI against fake `bb`, `claude`, `codex`, and `gh` executables and a local webhook receiver. They need a POSIX shell, so they run on macOS and Linux only.

Guided-setup tests use temporary Git repositories and fake CLI discovery. They cover wizard/flag equivalence, private disabled output, task-file handling, ordered account routing, operating limits, cancellation, existing-file preservation, and checkout/symlink rejection. They make no model calls and do not establish live account or IDE compatibility.

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

Service-health assessment uses pure table tests plus fake launchd/systemd and local diagnostic responders. Fixtures verify missing registration despite a responder, failed/disabled units, unknown data, build differences, overdue waiting checks and the exact grace boundary, long-running checks, unchanged holds/ownership, no provider calls, and text/JSON redaction. Assessment adds no manager queries and performs no repair. These fixtures do not verify installed-service identity or live recovery.
