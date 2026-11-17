#!/usr/bin/env bash
# Table test for the file rules of scripts/check-contributions.sh: a change
# may not add or change a file that is larger than 1 MB or begins like a
# compiled executable, or a text file with git conflict markers, unless its
# path is allowlisted. Run from the
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
cp "$root/scripts/changelog-release.sh" "$repo/scripts/"
printf '# test allowlist\nfixtures/allowed.bin\n' > "$repo/scripts/contribution-file-allowlist.txt"
printf '# test allowlist\nfixtures/merge-example.txt\n' > "$repo/scripts/conflict-marker-allowlist.txt"
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

# Conflict markers: lines a merge committed unresolved leaves behind.
printf 'intro\n<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> origin/main\nend\n' > docs/merged.md
expect "an unresolved merge fails, naming the lines" 1 "docs/merged.md has git conflict markers at line 2,4,6"
printf 'a\n<<<<<<< HEAD\nb\n' > docs/start.md
expect "an opening marker alone fails" 1 "docs/start.md has git conflict markers at line 2"
printf 'a\n>>>>>>> feature\n' > docs/end.md
expect "a closing marker alone fails" 1 "docs/end.md has git conflict markers at line 2"
printf 'a\n=======\nb\n' > docs/divider.md
expect "a divider line alone fails" 1 "docs/divider.md has git conflict markers at line 2"
printf 'Title\n========\n\nOther\n===\n' > docs/headings.md
expect "a heading underline of another length passes" 0 ""
printf 'text <<<<<<< HEAD inside a line\n  =======\n<<<<<<<HEAD\n>>>>>>>\n' > docs/mentions.md
expect "markers inside a line, indented, or without the space pass" 0 ""
printf '\x00binary\n<<<<<<< HEAD\n=======\n>>>>>>> x\n' > fixtures/blob.bin
expect "a binary file is not read for markers" 0 ""
printf '<<<<<<< HEAD\nexample\n=======\nexample\n>>>>>>> branch\n' > fixtures/merge-example.txt
expect "an allowlisted file with markers passes" 0 ""
printf '<<<<<<< HEAD\n' > fixtures/merge-example.txt.orig
expect "the marker allowlist is exact" 1 "fixtures/merge-example.txt.orig has git conflict markers"

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

# The whole-tree scan finds it, and conflict markers too.
printf 'a\n=======\nb\n' > docs/scanned.md
git add -A && git commit -q -s -m "test: markers for the scan"
out=$(scripts/check-contributions.sh --scan-tree HEAD 2>&1); code=$?
if [ "$code" != 1 ] || ! grep -qF "docs/already-there.bin (1048577 bytes) is larger than 1 MB" <<<"$out" || ! grep -qF "docs/scanned.md has git conflict markers at line 2" <<<"$out"; then
	echo "FAIL: --scan-tree: exit $code: $out"; fail=1
fi
git reset -q --hard HEAD~1

# The older rules still hold beside the new one.
echo "unsigned" > docs/unsigned.md
git add -A && git commit -q -m "test: no sign-off"
out=$(scripts/check-contributions.sh "$base" HEAD 2>&1); code=$?
if [ "$code" != 1 ] || ! grep -qF "needs a sign-off matching its author" <<<"$out"; then
	echo "FAIL: a commit without a sign-off: exit $code: $out"; fail=1
fi
git reset -q --hard "$base"

# Unknown revisions must fail before an empty commit/file list can pass.
for args in "missing-base HEAD" "HEAD missing-head" "--scan-tree missing-tree"; do
	out=$(scripts/check-contributions.sh $args 2>&1); code=$?
	if [ "$code" != 1 ] || ! grep -qF "is not a commit" <<<"$out"; then
		echo "FAIL: unknown revision ($args): exit $code: $out"; fail=1
	fi
done

unrelated=$(printf 'test: unrelated history\n' | git commit-tree "HEAD^{tree}")
if scripts/check-contributions.sh "$unrelated" HEAD >/dev/null 2>&1; then
	echo "FAIL: revisions without a common ancestor passed"; fail=1
fi

# The pull request title and body: BB thread IDs and home paths. Each case
# checks the text alone (no commits), as the workflow passes it: title, a
# literal backslash-n, then the body.
text_case() { # text_case NAME WANT_EXIT WANT_TEXT PR_TEXT
	local name=$1 want=$2 text=$3 out code
	out=$(PR_TEXT=$4 scripts/check-contributions.sh "$base" "$base" 2>&1)
	code=$?
	if [ "$code" != "$want" ] || { [ -n "$text" ] && ! grep -qF -- "$text" <<<"$out"; }; then
		echo "FAIL: $name: exit $code, want $want; output: $out"
		fail=1
	fi
	if grep -qE "k3m9x2q7wz|0a1b2c3d4e|a1b2c3d4e5|alice|bob" <<<"$out"; then
		echo "FAIL: $name: the message repeats the match: $out"; fail=1
	fi
}
tid="name a BB thread ID"
hp="absolute home path"
text_case "a plain title and body pass" 0 "" 'fix(bb): read status\nThe body.'
text_case "a thread ID in the body fails, with its line" 1 "$tid (thr_ and ten letters or digits) at line 3" 'fix: x\nIntro.\nSee thr_k3m9x2q7wz for context.'
text_case "a thread ID in the title fails" 1 "$tid (thr_ and ten letters or digits) at line 1" 'fix: as asked in thr_0a1b2c3d4e'
text_case "a thread ID first in the body, after the joining backslash-n, is its own line" 1 "at line 2" 'fix: x\nthr_k3m9x2q7wz asked for this'
text_case "a thread link with a message anchor fails" 1 "$tid" 'fix: x\nSee @thread:thr_k3m9x2q7wz#msg=42.'
text_case "a thread ID inside a branch or path fails" 1 "$tid" 'fix: x\nbranch bb/ci-fast-pushes-thr_k3m9x2q7wz'
text_case "two lines are both named" 1 "at line 2,4" 'fix: x\nthr_k3m9x2q7wz\nok\nand thr_a1b2c3d4e5.'
text_case "the docs' fictional ten-character ID passes" 0 "" 'fix: x\nfor example thr_example123 and thr_example456'
text_case "a fictional ID beside a real one still fails" 1 "at line 2" 'fix: x\nthr_example123 then thr_k3m9x2q7wz'
text_case "longer and shorter fictional IDs pass" 0 "" 'fix: x\nthr_example_owner thr_destination thr_example0001 thr_abc123 thr_example'
text_case "eleven characters is not a thread ID" 0 "" 'fix: x\nthr_k3m9x2q7wza'
text_case "a thread ID used as a file-name prefix fails" 1 "$tid" 'fix: x\nsaved as thr_k3m9x2q7wz_notes.md'
text_case "a fictional ID as a file-name prefix passes" 0 "" 'fix: x\nsaved as thr_example123_notes.md'
text_case "upper case is not a thread ID" 0 "" 'fix: x\nTHR_K3M9X2Q7WZ'
text_case "a macOS home path fails, with its line" 1 "$hp (/Users/<name> or /home/<name>) at line 2" 'fix: x\nRan in /Users/alice/src/repo.'
text_case "a Linux home path fails" 1 "$hp" 'fix: x\nlog at /home/bob/.cache/x'
text_case "a home path in a file URL fails" 1 "$hp" 'fix: x\nfile:///Users/alice/notes.md'
text_case "the docs' fictional home passes" 0 "" 'fix: x\n/Users/example/repo and /home/example/repo'
text_case "a CI runner, Homebrew and the shared folder pass" 0 "" 'fix: x\n/home/runner/work/x /Users/runner/work /home/linuxbrew/.linuxbrew /Users/Shared/x'
text_case "placeholders, ~ and a bare /Users pass" 0 "" 'fix: x\n/Users/<name>/ /home/$USER/ ~/src /Users/ /home/./x'
text_case "a home path without a trailing slash fails" 1 "$hp" 'fix: x\nthe /Users/alice directory'
text_case "a home path ending the text fails" 1 "$hp" 'fix: x\ncd /Users/alice'
text_case "an allowed home without a trailing slash passes" 0 "" 'fix: x\ncd /home/example and /Users/runner'
text_case "a home path in backticks or brackets fails" 1 "$hp" 'fix: x\nsee `/Users/alice/x` (/home/bob/y)'
text_case "home inside a list of names is not a path" 0 "" 'fix: x\nfour entries (studio/home/lab/spare) and ~/home/notes/'
text_case "a credit still fails beside the new rules" 1 "tool attribution credit" 'fix: x\nIntro.\nGenerated with Claude Code'
text_case "a credit first in the body, after the joining backslash-n, fails" 1 "tool attribution credit" 'fix: x\nGenerated with Claude Code'

# Changelog entries: a line under Unreleased is refused; a fragment in
# changelog.d/ is the way, and must be well formed.
mkdir -p changelog.d
printf '# Changelog\n\n## Unreleased\n\n- An entry from before fragments.\n\n## 0.1.0 (2026-01-01)\n\n- The first release.\n' > CHANGELOG.md
printf '# Fragments\n\nOne file per change.\n' > changelog.d/README.md
git add -A && git commit -q -s -m "test: changelog on the base" && base=$(git rev-parse HEAD)
mkdir -p docs fixtures bin changelog.d

sed -i.bak 's/^## Unreleased$/## Unreleased\n\n- A new entry in the old place./' CHANGELOG.md && rm CHANGELOG.md.bak
expect "a line added under Unreleased fails, with the way to move it" 1 "CHANGELOG.md gains an entry under Unreleased at line 5"
sed -i.bak 's/^## Unreleased$/## Unreleased \n\n- A new entry behind a changed heading./' CHANGELOG.md && rm CHANGELOG.md.bak
expect "a changed Unreleased heading fails" 1 "keep the exact ## Unreleased heading"
sed -i.bak '/^## Unreleased$/d' CHANGELOG.md && rm CHANGELOG.md.bak
expect "removing the Unreleased heading fails" 1 "keep the exact ## Unreleased heading"
git rm -q CHANGELOG.md
expect "removing the changelog fails" 1 "keep the exact ## Unreleased heading"
sed -i.bak 's/^## Unreleased$/## Unreleased\n\n++ A new entry that resembles a diff header./' CHANGELOG.md && rm CHANGELOG.md.bak
expect "added plus content alone fails" 1 "CHANGELOG.md gains an entry under Unreleased at line 5"
sed -i.bak 's/^## Unreleased$/## Unreleased\n\n++ A new entry that resembles a diff header.\n- Another new entry./' CHANGELOG.md && rm CHANGELOG.md.bak
expect "added plus content is checked and counted" 1 "CHANGELOG.md gains an entry under Unreleased at line 5,6"
sed -i.bak 's/^- An entry from before fragments\.$/- An entry from before fragments, reworded./' CHANGELOG.md && rm CHANGELOG.md.bak
expect "a reworded Unreleased line fails too" 1 "restore CHANGELOG.md to main's version"
sed -i.bak '/^- An entry from before fragments\.$/d' CHANGELOG.md && rm CHANGELOG.md.bak
expect "removing an Unreleased line passes" 0 ""
sed -i.bak 's/^- The first release\.$/- The first release, with a typo fixed./' CHANGELOG.md && rm CHANGELOG.md.bak
expect "a correction under a released version passes" 0 ""
echo "- A new entry as a fragment." > changelog.d/new-entry.md
expect "a fragment passes" 0 ""
printf '#324 fixes an issue.\n' > changelog.d/issue-reference.md
expect "a fragment starting with an issue reference passes" 0 ""
ln -s ../README.md changelog.d/link.md
expect "a symbolic link fragment fails" 1 "changelog.d/link.md: a changelog fragment must be a regular file with mode 100644"
echo "- An executable fragment." > changelog.d/executable.md
chmod +x changelog.d/executable.md
expect "an executable fragment fails" 1 "changelog.d/executable.md: a changelog fragment must be a regular file with mode 100644"
printf '## Added\n\n- An entry.\n' > changelog.d/with-heading.md
expect "a fragment with a heading fails" 1 "changelog.d/with-heading.md: a changelog fragment has no heading"
printf '\n  \n' > changelog.d/empty.md
expect "an empty fragment fails" 1 "changelog.d/empty.md: the changelog fragment is empty"
mkdir -p changelog.d/sub && echo "- x" > changelog.d/sub/x.md
expect "a fragment in a subdirectory fails" 1 "changelog.d/sub/x.md: a changelog fragment is one lower-case .md file"
echo "- x" > changelog.d/Entry.md
expect "an upper-case fragment name fails" 1 "changelog.d/Entry.md"
echo "- x" > changelog.d/entry.txt
expect "a fragment that is not .md fails" 1 "changelog.d/entry.txt"
printf '# Fragments\n\nChanged.\n' > changelog.d/README.md
expect "the README is not a fragment" 0 ""

# The release script gathers fragments, newest first, above the old entries,
# and its result passes the check.
echo "- Older fragment." > changelog.d/older.md
git add -A && git commit -q -s -m "test: older fragment"
echo "- Newer fragment." > changelog.d/newer.md
GIT_COMMITTER_DATE="2030-01-01T00:00:00Z" git add -A
GIT_COMMITTER_DATE="2030-01-01T00:00:00Z" git commit -q -s -m "test: newer fragment"
pre=$(git rev-parse HEAD)
# Use a non-default mode so restoring a hard-coded 0644 also fails.
chmod 640 CHANGELOG.md
scripts/changelog-release.sh 0.2.0 2026-02-02 >/dev/null || { echo "FAIL: the release script failed"; fail=1; }
# -perm works with both BSD and GNU find.
if [ "$(find CHANGELOG.md -perm 0640 -print)" != CHANGELOG.md ]; then
	echo "FAIL: release script changed CHANGELOG.md permissions"; fail=1
fi
want='# Changelog

## Unreleased

## 0.2.0 (2026-02-02)

- Newer fragment.

- Older fragment.

- An entry from before fragments.

## 0.1.0 (2026-01-01)

- The first release.'
if [ "$(cat CHANGELOG.md)" != "$want" ]; then echo "FAIL: release script output:"; cat CHANGELOG.md; fail=1; fi
if [ -e changelog.d/older.md ] || [ -e changelog.d/newer.md ] || [ ! -e changelog.d/README.md ]; then
	echo "FAIL: the release script should delete the fragments and keep the README"; fail=1
fi
git add -A && git commit -q -s -m "chore: release 0.2.0"
out=$(scripts/check-contributions.sh "$pre" HEAD 2>&1) || { echo "FAIL: a release-prep change was refused: $out"; fail=1; }
if scripts/changelog-release.sh 0.2.0 2026-02-03 >/dev/null 2>&1; then echo "FAIL: releasing a version twice should fail"; fail=1; fi
if scripts/changelog-release.sh 0.3.0 2026-02-03 >/dev/null 2>&1; then echo "FAIL: a release with nothing in it should fail"; fail=1; fi
git reset -q --hard "$base"

[ "$fail" = 0 ] && echo "check-contributions: all file, text and changelog cases pass"
exit "$fail"
