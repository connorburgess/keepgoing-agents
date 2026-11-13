#!/usr/bin/env bash
# Decide how much of CI a change needs. Prints one line for $GITHUB_OUTPUT:
#
#   scope=docs   every changed file is documentation: the Ubuntu job runs
#                everything, and the macOS and Windows jobs skip their work
#   scope=full   anything else, and any doubt: every job runs everything
#
# The reason goes to standard error. See "What runs for a documentation-only
# change" in docs/testing.md.
#
# Usage:
#   EVENT=pull_request scripts/ci-scope.sh            in a checkout of the merge commit
#   EVENT=workflow_dispatch PREVIEW_BASE=<commit> ... a manual preview against a base
#   scripts/ci-scope.sh --classify                    paths on standard input, one per line
set -u

full() {
	echo "ci-scope: full: $1" >&2
	echo "scope=full"
	exit 0
}

# is_doc PATH: true when the file is documentation and nothing else.
is_doc() {
	case "$1" in
	# Never documentation, whatever the file is called: code, tests and their
	# fixtures, configuration, schemas, examples that tests load, and CI.
	.github/* | cmd/* | internal/* | scripts/* | schema/* | examples/* | .bb/*) return 1 ;;
	testdata/* | */testdata/* | */fixtures/* | */__fixtures__/* | */node_modules/*) return 1 ;;
	esac
	case "$1" in
	docs/* | LICENSES/*) return 0 ;;
	*.md | *.markdown) return 0 ;;
	*/*) return 1 ;;
	# Top-level text files only: a pattern's * also matches a slash.
	LICENSE | LICENSE.* | LICENSE-* | LICENSING | LICENSING.* | NOTICE | NOTICE.* | COPYING | COPYING.* | AUTHORS | AUTHORS.*) return 0 ;;
	esac
	return 1
}

# classify: reads paths, one per line, and decides.
classify() {
	local n=0 path
	while IFS= read -r path; do
		[ -n "$path" ] || continue
		n=$((n + 1))
		is_doc "$path" || full "$path is not documentation"
	done
	[ "$n" -gt 0 ] || full "no changed files were found"
	echo "ci-scope: docs: all $n changed files are documentation" >&2
	echo "scope=docs"
}

if [ "${1:-}" = "--classify" ]; then
	classify
	exit 0
fi

case "${EVENT:-}" in
pull_request)
	# The checkout is the merge of the pull request into its base. Its first
	# parent is the base, so this is exactly what the pull request changes.
	git rev-parse -q --verify 'HEAD^2' >/dev/null 2>&1 || full "the checkout is not a pull request merge commit"
	base=$(git rev-parse -q --verify 'HEAD^1') || full "the base commit is not available"
	;;
workflow_dispatch)
	[ -n "${PREVIEW_BASE:-}" ] || full "a manual run"
	base=$(git rev-parse -q --verify "${PREVIEW_BASE}^{commit}" 2>/dev/null) || full "the preview base is not a commit in this checkout"
	;;
*)
	full "event ${EVENT:-unknown}"
	;;
esac

# core.quotePath=false keeps non-ASCII names as they are. A name with a
# newline or a quote is still quoted by git, starts with a double quote, and
# so matches no documentation pattern. --no-renames lists both names of a
# renamed file, so moving code to a .md name is not documentation.
files=$(git -c core.quotePath=false diff --name-only --no-renames "$base" HEAD) || full "the changed files could not be listed"
printf '%s\n' "$files" | classify
