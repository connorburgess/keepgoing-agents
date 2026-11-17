#!/usr/bin/env bash
# Prepare CHANGELOG.md for a release: open "## VERSION (DATE)" under an empty
# "## Unreleased", holding every fragment in changelog.d/ (newest first, by the
# commit that added it; uncommitted ones first) and then the entries already
# under Unreleased. The fragments are deleted. Run it in the release-prep pull
# request; the release workflow itself does not read CHANGELOG.md.
#
#   scripts/changelog-release.sh 0.2.1 2026-11-20
set -euo pipefail
version=${1:?a version, such as 0.2.1}; date=${2:?a date, YYYY-MM-DD}
cd "$(git rev-parse --show-toplevel)"
log=CHANGELOG.md
grep -qx '## Unreleased' "$log" || { echo "$log has no '## Unreleased' heading" >&2; exit 1; }
if grep -q "^## $version " "$log"; then echo "$log already has a $version heading" >&2; exit 1; fi

frags=()
while IFS= read -r f; do frags+=("$f"); done < <(
  for f in changelog.d/*.md; do
    [ -f "$f" ] && [ "${f##*/}" != README.md ] || continue
    t=$(git log -1 --diff-filter=A --format=%ct -- "$f" 2>/dev/null)
    echo "${t:-9999999999} $f"
  done | sort -k1,1nr -k2 | cut -d' ' -f2-)

# The new section: fragments, then the old Unreleased entries, one blank line
# between paragraphs and none at either end.
body=$( { for f in ${frags[@]+"${frags[@]}"}; do cat "$f"; printf '\n\n'; done
          awk '/^## /{n++; next} n==1' "$log"; } | cat -s | sed '/./,$!d')
[ -n "$body" ] || { echo "nothing to release: no fragments and no Unreleased entries" >&2; exit 1; }

tmp=$(mktemp "${TMPDIR:-/tmp}/changelog.XXXXXX")
{
  awk '/^## /{exit} {print}' "$log"
  printf '## Unreleased\n\n## %s (%s)\n\n%s\n\n' "$version" "$date" "$body"
  awk '/^## /{n++} n>=2' "$log"
} > "$tmp"
mv "$tmp" "$log"
for f in ${frags[@]+"${frags[@]}"}; do git rm -q --cached --ignore-unmatch -- "$f"; rm -f -- "$f"; done
echo "CHANGELOG.md: ${#frags[@]} fragments and the Unreleased entries are now under $version ($date)"
