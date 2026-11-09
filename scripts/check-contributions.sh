#!/usr/bin/env bash
# Verify that every commit in BASE..HEAD carries a Signed-off-by line matching
# its author and that neither commits nor PR_TEXT carry a tool attribution.
set -euo pipefail
base=${1:?base sha}; head=${2:?head sha}
credit='^[[:space:]*_>-]*(co-authored-by:[[:space:]]*(codex|claude|chatgpt|cursor|copilot|github copilot)|(generated|written|created|powered)[[:space:]]+(with|by)[[:space:]]+(codex|claude|chatgpt|cursor|copilot|github copilot|anthropic|openai))'
fail=0
for c in $(git rev-list "$base..$head"); do
  author=$(git show -s --format='%an <%ae>' "$c")
  msg=$(git show -s --format='%B' "$c")
  if ! grep -qiF "Signed-off-by: $author" <<<"$msg"; then
    echo "${c:0:12} needs a sign-off matching its author ($author)"; fail=1
  fi
  if grep -qiE "$credit" <<<"$msg"; then
    echo "${c:0:12} contains a tool attribution credit"; fail=1
  fi
done
if [ -n "${PR_TEXT:-}" ] && grep -qiE "$credit" <<<"$PR_TEXT"; then
  echo "the pull request text contains a tool attribution credit"; fail=1
fi
exit $fail
