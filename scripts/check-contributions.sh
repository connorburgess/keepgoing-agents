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
# Files the change adds or changes, as they are at HEAD. The three dots
# compare HEAD with where it left BASE, so files that only changed on BASE
# are not charged to this change.
while IFS= read -r -d '' file; do
  check_file "$head" "$file" || fail=1
  check_markers "$head" "$file" || fail=1
done < <(git diff --name-only --no-renames --diff-filter=AM -z "$base...$head")
if [ -n "${PR_TEXT:-}" ] && grep -qiE "$credit" <<<"$PR_TEXT"; then
  echo "the pull request text contains a tool attribution credit"; fail=1
fi
exit $fail
