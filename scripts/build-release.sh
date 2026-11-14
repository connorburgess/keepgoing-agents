#!/usr/bin/env bash
# Build release archives and checksums into dist/.
#   scripts/build-release.sh v0.1.0
set -euo pipefail
version=${1:?version, for example v0.1.0}
root=$(cd "$(dirname "$0")/.." && pwd)
dist="$root/dist"
# A release never ships without the notices of everything in the executable:
# this fails when a module was added and THIRD_PARTY_NOTICES was not updated.
"$root/scripts/third-party-notices.sh" --check
rm -rf "$dist" && mkdir -p "$dist"
targets="darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64"
for t in $targets; do
  os=${t%/*}; arch=${t#*/}
  name="keepgoing_${version#v}_${os}_${arch}"
  work="$dist/$name"
  mkdir -p "$work"
  ext=""; [ "$os" = windows ] && ext=".exe"
  (cd "$root" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$work/keepgoing$ext" ./cmd/keepgoing)
  # LICENSE is the Sustainable Use License: everyone who gets the executable
  # gets its terms. The file points to LICENSING.md and the preserved Apache
  # text, so both ship beside it. THIRD_PARTY_NOTICES carries the terms of
  # the Go runtime and the modules inside the executable.
  cp "$root/LICENSE" "$root/LICENSING.md" "$root/THIRD_PARTY_NOTICES" "$root/README.md" "$work/"
  mkdir -p "$work/LICENSES" && cp "$root/LICENSES/Apache-2.0.txt" "$work/LICENSES/"
  if [ "$os" = windows ]; then
    (cd "$dist" && zip -qr "$name.zip" "$name")
  else
    # macOS tar otherwise adds AppleDouble files containing local metadata.
    COPYFILE_DISABLE=1 tar -C "$dist" -czf "$dist/$name.tar.gz" "$name"
  fi
  rm -rf "$work"
done
(cd "$dist" && shasum -a 256 keepgoing_* > checksums.txt)
ls -1 "$dist"
