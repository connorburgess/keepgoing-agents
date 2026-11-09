#!/usr/bin/env bash
# Build a self-contained test bundle for another OS, for machines or VMs
# without a Go toolchain. It writes bundle.tar.gz (or bundle.zip for
# Windows) next to the unpacked tree. The bundle holds the source tree (tests read
# fixtures relative to their package), one compiled test binary per package,
# the keepgoing binary, and a runner.
#   scripts/build-test-bundle.sh linux arm64 out/dir
#   scripts/build-test-bundle.sh windows arm64 out/dir
set -euo pipefail
goos=${1:?GOOS}; goarch=${2:?GOARCH}; out=${3:?output directory}
root=$(cd "$(dirname "$0")/.." && pwd)
rm -rf "$out" && mkdir -p "$out/src"
git -C "$root" archive HEAD | tar -x -C "$out/src"
ext=""; [ "$goos" = windows ] && ext=".exe"
cd "$root"
for pkg in $(go list ./...); do
  rel=${pkg#github.com/connorburgess/keepgoing-agents}; rel=${rel#/}; [ -z "$rel" ] && rel=.
  name=$(basename "$pkg")
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go test -c -ldflags "-s -w" -o "$out/src/$rel/$name.test$ext" "$pkg" 2>/dev/null || true
done
CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -ldflags "-s -w -X main.version=$(git rev-parse --short HEAD)" -o "$out/src/keepgoing$ext" ./cmd/keepgoing
cat > "$out/src/run-tests.sh" <<'RUN'
#!/bin/sh
# Run every compiled test binary from its own package directory.
cd "$(dirname "$0")"; root=$(pwd); fail=0
for t in $(find . -name '*.test' ! -name '._*' -type f | sort); do
  dir=$(dirname "$t"); name=$(basename "$t")
  if out=$(cd "$dir" && "./$name" -test.timeout 300s 2>&1); then echo "ok   $dir"; else echo "FAIL $dir"; echo "$out" | tail -25; fail=1; fi
done
exit $fail
RUN
cat > "$out/src/run-tests.ps1" <<'RUN'
# Run every compiled test binary from its own package directory. Each runs as
# its own process with output to files and a hard time limit; on timeout its
# whole process tree is killed, so a hung test or a leftover child process
# can never hold this script open.
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$fail = 0
$limit = 360
Get-ChildItem -Path $root -Recurse -Filter *.test.exe | Where-Object { $_.Name -notlike '._*' } | Sort-Object FullName | ForEach-Object {
  $rel = $_.DirectoryName.Substring($root.Length)
  $out = Join-Path $env:TEMP ("kg-" + $_.BaseName + ".out")
  $err = Join-Path $env:TEMP ("kg-" + $_.BaseName + ".err")
  $p = Start-Process -FilePath $_.FullName -ArgumentList "-test.timeout","300s" -WorkingDirectory $_.DirectoryName -NoNewWindow -PassThru -RedirectStandardOutput $out -RedirectStandardError $err
  # Read the handle now: without it, PowerShell reports no exit code later.
  $null = $p.Handle
  if (-not $p.WaitForExit($limit * 1000)) {
    & taskkill /PID $p.Id /T /F | Out-Null
    "FAIL .$rel (killed after ${limit}s)"
    $fail = 1
  } elseif (($p.WaitForExit() -or $true) -and $p.ExitCode -eq 0) {
    "ok   .$rel"
  } else {
    "FAIL .$rel"
    (Get-Content $out, $err -ErrorAction SilentlyContinue | Select-Object -Last 25) -join "`n"
    $fail = 1
  }
}
exit $fail
RUN
chmod +x "$out/src/run-tests.sh"
# Archive without macOS metadata files.
if [ "$goos" = windows ]; then
  (cd "$out" && rm -f bundle.zip && zip -qrX bundle.zip src -x '*/._*' '*.DS_Store')
else
  COPYFILE_DISABLE=1 tar -C "$out" --no-xattrs -czf "$out/bundle.tar.gz" src 2>/dev/null || COPYFILE_DISABLE=1 tar -C "$out" -czf "$out/bundle.tar.gz" src
fi
echo "bundle: $out/src ($(find "$out/src" -name '*.test*' | wc -l | tr -d ' ') test binaries)"
