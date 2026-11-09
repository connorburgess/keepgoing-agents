# Testing

## On a machine with Go

```sh
go vet ./...
go test -race ./...
```

The end-to-end tests in `cmd/keepgoing` drive the real CLI against fake `bb`, `claude`, `codex`, and `gh` executables and a local webhook receiver. They need a POSIX shell, so they run on macOS and Linux only.

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
| Windows 11 Arm (build 26300) | test bundle in a Parallels VM | all 16 test packages pass, including the Windows lock | the end-to-end tests and headless runners (POSIX only), x64 |

CI runs the suite on Ubuntu x86-64 and macOS and cross-compiles for Windows. Check the Actions tab for the latest run.
