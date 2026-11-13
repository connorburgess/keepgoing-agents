#!/usr/bin/env bash
# Table test for the file rules of scripts/check-contributions.sh: a change
# may not add or change a file that is larger than 1 MB or begins like a
# compiled executable, unless its path is allowlisted. Run from the
# repository root. It works in a scratch repository and touches nothing here.
set -u
root=$(pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

# A scratch repository with its own copy of the script and allowlist.
repo="$tmp/repo"
mkdir -p "$repo/scripts"
cp "$root/scripts/check-contributions.sh" "$repo/scripts/"
printf '# test allowlist\nfixtures/allowed.bin\n' > "$repo/scripts/contribution-file-allowlist.txt"
cd "$repo" || exit 2
git init -q
git config user.name "Example Author"
git config user.email "author@example.com"
git config commit.gpgsign false
echo "base" > README.md
git add -A && git commit -q -s -m "test: base"
base=$(git rev-parse HEAD)

bytes() { # bytes N: N bytes of text
	head -c "$1" /dev/zero | tr '\0' 'a'
}

# expect NAME WANT_EXIT WANT_TEXT: commit whatever is staged or changed, run
# the check against the base, compare, then go back to the base.
expect() {
	local name=$1 want=$2 text=$3 out code
	git add -A && git commit -q -s -m "test: $name"
	out=$(scripts/check-contributions.sh "$base" HEAD 2>&1)
	code=$?
	if [ "$code" != "$want" ] || { [ -n "$text" ] && ! grep -qF -- "$text" <<<"$out"; }; then
		echo "FAIL: $name: exit $code, want $want; output: $out"
		[ -n "$text" ] && echo "      wanted to see: $text"
		fail=1
	fi
	git reset -q --hard "$base"
	mkdir -p docs fixtures bin # the reset removes directories it emptied
}

mkdir -p docs fixtures bin

echo "plain text" > docs/note.md
expect "a small text file passes" 0 ""

bytes 1048576 > docs/exactly-1mb.txt
expect "exactly 1 MB passes" 0 ""

bytes 1048577 > docs/big.txt
expect "one byte over 1 MB fails, with path and size" 1 "docs/big.txt (1048577 bytes) is larger than 1 MB"

printf '\xcf\xfa\xed\xfe rest' > bin/macho64
expect "Mach-O 64-bit" 1 "bin/macho64 (9 bytes) begins like a compiled executable (Mach-O)"
printf '\xfe\xed\xfa\xcf rest' > bin/macho64be
expect "Mach-O 64-bit, big-endian" 1 "bin/macho64be (9 bytes) begins like a compiled executable (Mach-O)"
printf '\xce\xfa\xed\xfe rest' > bin/macho32
expect "Mach-O 32-bit" 1 "bin/macho32 (9 bytes) begins like a compiled executable (Mach-O)"
printf '\xfe\xed\xfa\xce rest' > bin/macho32be
expect "Mach-O 32-bit, big-endian" 1 "begins like a compiled executable (Mach-O)"
printf '\xca\xfe\xba\xbe rest' > bin/fat
expect "Mach-O universal" 1 "bin/fat (9 bytes) begins like a compiled executable (Mach-O universal)"
printf '\xbe\xba\xfe\xca rest' > bin/fatle
expect "Mach-O universal, little-endian" 1 "begins like a compiled executable (Mach-O universal)"
printf '\xca\xfe\xba\xbf rest' > bin/fat64
expect "Mach-O universal, 64-bit" 1 "begins like a compiled executable (Mach-O universal)"
printf '\x7fELF rest' > bin/elf
expect "ELF" 1 "bin/elf (9 bytes) begins like a compiled executable (ELF)"
printf 'MZ\x90\x00 rest' > bin/tool.exe
expect "PE" 1 "bin/tool.exe (9 bytes) begins like a compiled executable (PE)"

printf '\x7fELF rest' > "bin/café tool"
expect "a path with a space and an accent is named" 1 "bin/café tool (9 bytes) begins like a compiled executable (ELF)"

printf '#!/bin/sh\necho hello\n' > bin/script.sh && chmod +x bin/script.sh
expect "an executable shell script passes" 0 ""
printf 'ELF and MZ in the text, not at the start: \x7fELF MZ\n' > docs/mentions.txt
expect "the bytes later in a file pass" 0 ""
: > docs/empty.txt
expect "an empty file passes" 0 ""

printf '\x7fELF rest' > fixtures/allowed.bin
expect "an allowlisted executable passes" 0 ""
bytes 1048577 > fixtures/allowed.bin
expect "an allowlisted large file passes" 0 ""
printf '\x7fELF rest' > fixtures/not-allowed.bin
expect "the allowlist is exact, not a directory" 1 "fixtures/not-allowed.bin"

ln -s /usr/bin/true bin/link
expect "a symbolic link passes" 0 ""

bytes 1048577 > docs/big.txt
printf '\x7fELF rest' > bin/elf
expect "two offenders are both named" 1 "bin/elf (9 bytes)"

# A file that becomes too large when changed.
echo "small" > docs/grows.txt
git add -A && git commit -q -s -m "test: small file on the base" && base=$(git rev-parse HEAD)
bytes 1048577 > docs/grows.txt
expect "a file changed to over 1 MB fails" 1 "docs/grows.txt (1048577 bytes) is larger than 1 MB"

# A large file already on the base is not charged to a change that leaves it alone,
# and removing it passes.
bytes 1048577 > docs/already-there.bin
git add -A && git commit -q -s -m "test: large file already on the base" && base=$(git rev-parse HEAD)
echo "another note" > docs/other.md
expect "a large file the change does not touch passes" 0 ""
git rm -q docs/already-there.bin
expect "deleting a large file passes" 0 ""

# The whole-tree scan finds it.
out=$(scripts/check-contributions.sh --scan-tree HEAD 2>&1); code=$?
if [ "$code" != 1 ] || ! grep -qF "docs/already-there.bin (1048577 bytes) is larger than 1 MB" <<<"$out"; then
	echo "FAIL: --scan-tree: exit $code: $out"; fail=1
fi

# The older rules still hold beside the new one.
echo "unsigned" > docs/unsigned.md
git add -A && git commit -q -m "test: no sign-off"
out=$(scripts/check-contributions.sh "$base" HEAD 2>&1); code=$?
if [ "$code" != 1 ] || ! grep -qF "needs a sign-off matching its author" <<<"$out"; then
	echo "FAIL: a commit without a sign-off: exit $code: $out"; fail=1
fi
git reset -q --hard "$base"

[ "$fail" = 0 ] && echo "check-contributions: all file cases pass"
exit "$fail"
