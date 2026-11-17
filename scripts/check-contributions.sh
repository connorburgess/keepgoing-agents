#!/usr/bin/env bash
# Verify that every commit in BASE..HEAD carries a Signed-off-by line matching
# its author and that neither commits nor PR_TEXT carry a tool attribution.
# A sign-off records contribution origin. This check does not verify that a
# contributor accepted the contributor agreement: a maintainer does that
# before merging (see CONTRIBUTING.md).
# Merge commits are exempt: GitHub generates them when a pull request is
# merged and never adds a sign-off. The commits they merge are still checked.
#
# It also refuses a change that adds or changes a file which, at HEAD, is
# larger than 1 MB or begins like a compiled executable (Mach-O, ELF or PE),
# unless its path is listed in scripts/contribution-file-allowlist.txt. A
# built binary or a large capture committed by mistake is expensive to remove
# from history once merged.
#
# It also refuses a text file the change adds or changes that has a line
# beginning "<<<<<<< " or ">>>>>>> ", or a line that is exactly "=======":
# git's conflict markers, left by a merge that was committed unresolved.
# Paths listed in scripts/conflict-marker-allowlist.txt are let through.
#
# It also refuses a pull request title or body (PR_TEXT) that names a BB
# thread ID or a person's home directory, which a public pull request should
# not carry:
#   - "thr_" and exactly ten lowercase letters or digits, not followed by a
#     letter or digit (an underscore may follow, as in a file name), the form
#     of a BB thread ID, unless it contains "example" (the fictional IDs the docs use,
#     such as thr_example123);
#   - an absolute home path, "/Users/<name>" or "/home/<name>", with or
#     without a trailing slash, not preceded by a letter, digit, ".", "_",
#     "-" or "~", unless the name is example
#     (the docs' fictional home), runner (a CI runner's home), linuxbrew
#     (Homebrew's prefix on Linux) or Shared (macOS's shared folder).
# The message gives the line number, title first, never the match itself.
# There is no rule for a body pasted from another repository's pull request:
# nothing in such a text reliably says where it came from.
#
# Changelog entries go in changelog.d/, one file per change, so pull requests
# never edit the same lines (see changelog.d/README.md). It refuses a line
# added under "## Unreleased" in CHANGELOG.md, and a changelog.d/ fragment
# that is not a non-empty, lower-case-named .md file directly in that
# directory, or that has a heading. A release-prep change from
# scripts/changelog-release.sh adds its lines under a version heading, so it
# passes.
#
#   check-contributions.sh BASE HEAD          the commits and files of BASE..HEAD
#   check-contributions.sh --scan-tree REF    every file in REF, for auditing
set -euo pipefail

max_bytes=1048576
allowlist="$(dirname "$0")/contribution-file-allowlist.txt"

marker_allowlist="$(dirname "$0")/conflict-marker-allowlist.txt"
markers='^(<<<<<<< |>>>>>>> |=======$)'

listed() { # listed FILE PATH: PATH is a line of FILE, comments aside
  [ -f "$1" ] && grep -qxF -- "$2" <(grep -vE '^[[:space:]]*(#|$)' "$1")
}

allowed() { listed "$allowlist" "$1"; }

# check_markers REF PATH: prints the line numbers and returns 1 when the text
# file at REF has a conflict marker line and is not allowlisted. git grep -I
# skips binary files.
check_markers() {
  local ref=$1 file=$2 lines
  lines=$(git grep -I -h -n -E "$markers" "$ref" -- "$file" 2>/dev/null | cut -d: -f1 | paste -sd, -) || true
  if [ -z "$lines" ] || listed "$marker_allowlist" "$file"; then
    return 0
  fi
  echo "$file has git conflict markers at line $lines; resolve the merge, or list the path in scripts/conflict-marker-allowlist.txt"
  return 1
}

# check_file REF PATH: prints a line and returns 1 when the file at REF is
# too large or begins like an executable and is not allowlisted.
check_file() {
  local ref=$1 file=$2 mode size magic kind=""
  mode=$(git ls-tree "$ref" -- "$file" | awk '{print $1}')
  # A symbolic link's content is its target; a submodule has no content here.
  case "$mode" in ""|120000|160000) return 0 ;; esac
  size=$(git cat-file -s "$ref:$file")
  # head closes the pipe early on a large file; that is not a failure.
  magic=$( (git cat-file blob "$ref:$file" 2>/dev/null || true) | head -c 4 | od -An -tx1 | tr -d ' \n')
  case "$magic" in
    feedfacf|cffaedfe|feedface|cefaedfe) kind="Mach-O" ;;
    cafebabe|bebafeca|cafebabf|bfbafeca) kind="Mach-O universal" ;;
    7f454c46) kind="ELF" ;;
    4d5a*) kind="PE" ;;
  esac
  if [ -z "$kind" ] && [ "$size" -le "$max_bytes" ]; then
    return 0
  fi
  if allowed "$file"; then
    return 0
  fi
  if [ -n "$kind" ]; then
    echo "$file ($size bytes) begins like a compiled executable ($kind); build it, do not commit it, or list the path in scripts/contribution-file-allowlist.txt"
  else
    echo "$file ($size bytes) is larger than 1 MB ($max_bytes bytes); keep it out of the repository, or list the path in scripts/contribution-file-allowlist.txt"
  fi
  return 1
}

# check_unreleased BASE HEAD: prints and returns 1 when the change adds a
# non-blank line under "## Unreleased" in CHANGELOG.md.
check_unreleased() {
  local base=$1 head=$2 range lines
  git cat-file -e "$head:CHANGELOG.md" 2>/dev/null || return 0
  # The Unreleased section is the lines strictly between its heading and the
  # next "## " heading.
  range=$(git show "$head:CHANGELOG.md" | awk '/^## /{ if (s && !e) e = NR; if ($0 == "## Unreleased") s = NR } END { print s + 0, (e ? e : 999999999) }')
  lines=$(git diff -U0 --no-renames "$base...$head" -- CHANGELOG.md | awk -v r="$range" '
    BEGIN { split(r, b, " ") }
    /^@@/ { split($3, a, ","); n = substr(a[1], 2) + 0; next }
    /^\+\+\+/ { next }
    /^\+/ { if (b[1] && n > b[1] && n < b[2] && substr($0, 2) ~ /[^[:space:]]/) print n; n++ }
  ' | paste -sd, -)
  [ -z "$lines" ] && return 0
  echo "CHANGELOG.md gains an entry under Unreleased at line $lines; put it in a file of its own under changelog.d/ instead. To move an open pull request: (1) merge main into it; (2) move your Unreleased entry into changelog.d/<topic>.md; (3) restore CHANGELOG.md to main's version (git checkout origin/main -- CHANGELOG.md). See changelog.d/README.md."
  return 1
}

# check_fragment REF PATH: prints and returns 1 when a changelog.d/ file the
# change adds or changes is not a well-formed fragment.
check_fragment() {
  local ref=$1 file=$2 text
  case "$file" in changelog.d/*) ;; *) return 0 ;; esac
  [ "$file" = changelog.d/README.md ] && return 0
  if ! [[ "${file#changelog.d/}" =~ ^[a-z0-9][a-z0-9.-]*\.md$ ]]; then
    echo "$file: a changelog fragment is one lower-case .md file directly in changelog.d/ (letters, digits, '.', '-')"; return 1
  fi
  text=$(git cat-file blob "$ref:$file" 2>/dev/null) || text=""
  if ! grep -q '[^[:space:]]' <<<"$text"; then
    echo "$file: the changelog fragment is empty"; return 1
  fi
  if grep -qE '^#' <<<"$text"; then
    echo "$file: a changelog fragment has no heading; scripts/changelog-release.sh adds the version heading"; return 1
  fi
  return 0
}

if [ "${1:-}" = "--scan-tree" ]; then
  ref=${2:?a commit}
  found=0
  while IFS= read -r -d '' file; do
    check_file "$ref" "$file" || found=1
    check_markers "$ref" "$file" || found=1
  done < <(git ls-tree -r -z --name-only "$ref")
  exit $found
fi

base=${1:?base sha}; head=${2:?head sha}
credit='^[[:space:]*_>-]*(co-authored-by:[[:space:]]*(codex|claude|chatgpt|cursor|copilot|github copilot)|(generated|written|created|powered)[[:space:]]+(with|by)[[:space:]]+(codex|claude|chatgpt|cursor|copilot|github copilot|anthropic|openai))'
fail=0
for c in $(git rev-list --no-merges "$base..$head"); do
  author=$(git show -s --format='%an <%ae>' "$c")
  msg=$(git show -s --format='%B' "$c")
  if ! grep -qiF "Signed-off-by: $author" <<<"$msg"; then
    echo "${c:0:12} needs a sign-off matching its author ($author)"; fail=1
  fi
  if grep -qiE "$credit" <<<"$msg"; then
    echo "${c:0:12} contains a tool attribution credit"; fail=1
  fi
done
check_unreleased "$base" "$head" || fail=1
# Files the change adds or changes, as they are at HEAD. The three dots
# compare HEAD with where it left BASE, so files that only changed on BASE
# are not charged to this change.
while IFS= read -r -d '' file; do
  check_file "$head" "$file" || fail=1
  check_markers "$head" "$file" || fail=1
  check_fragment "$head" "$file" || fail=1
done < <(git diff --name-only --no-renames --diff-filter=AM -z "$base...$head")
if [ -n "${PR_TEXT:-}" ]; then
  # The workflow joins the title and body with a literal backslash-n; read it
  # as a line break so the body's first line is a line of its own.
  text=${PR_TEXT//\\n/$'\n'}
  if grep -qiE "$credit" <<<"$text"; then
    echo "the pull request text contains a tool attribution credit"; fail=1
  fi
  thread='thr_[a-z0-9]{10}([^A-Za-z0-9]|$)'
  home='(^|[^A-Za-z0-9_.~-])/(Users|home)/[A-Za-z0-9_-][A-Za-z0-9._-]*([^A-Za-z0-9._-]|$)'
  lines=$(grep -nE "$thread" <<<"$text" | while IFS=: read -r n line; do
    grep -oE "$thread" <<<"$line" | grep -qv example && echo "$n"
  done | paste -sd, -) || true
  if [ -n "$lines" ]; then
    echo "the pull request title and body name a BB thread ID (thr_ and ten letters or digits) at line $lines; describe the thread instead, or use a fictional ID such as thr_example123"; fail=1
  fi
  lines=$(grep -nE "$home" <<<"$text" | while IFS=: read -r n line; do
    grep -oE "$home" <<<"$line" | grep -qvE '/(Users|home)/(example|runner|linuxbrew|Shared)([^A-Za-z0-9._-])?$' && echo "$n"
  done | paste -sd, -) || true
  if [ -n "$lines" ]; then
    echo "the pull request title and body have an absolute home path (/Users/<name> or /home/<name>) at line $lines; use a relative path, ~, or /Users/example/"; fail=1
  fi
fi
exit $fail
