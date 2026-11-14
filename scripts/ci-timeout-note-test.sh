#!/usr/bin/env bash
# Table test for scripts/ci-timeout-note.sh. Run from the repository root.
set -u
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

# jobs STEPS_JSON: an API answer with two jobs; this runner's is the second.
jobs() {
	cat > "$tmp/jobs.json" <<JSON
{"jobs":[
 {"name":"Build and test (ubuntu-24.04)","runner_name":"other-runner","started_at":"2026-01-05T09:00:00Z",
  "steps":[{"name":"Run go test","status":"in_progress","conclusion":null}]},
 {"name":"Windows access diagnostics (processes)","runner_name":"this-runner","started_at":"2026-01-05T09:00:00Z",
  "steps":$1}
]}
JSON
}

# expect NAME SECONDS_AFTER_START WANT...: every WANT must be in the note.
expect() {
	local name=$1 after=$2 out want
	shift 2
	out=$(RUNNER_NAME=this-runner LIMIT_MINUTES=15 CI_TIMEOUT_JOBS_JSON="$tmp/jobs.json" \
		CI_TIMEOUT_NOW=$((1767603600 + after)) GITHUB_STEP_SUMMARY="$tmp/summary" scripts/ci-timeout-note.sh 2>&1)
	code=$?
	for want in "$@"; do
		if [ "$code" != 0 ] || ! grep -qF -- "$want" <<<"$out"; then
			echo "FAIL: $name: exit $code; wanted \"$want\" in: $out"
			fail=1
		fi
	done
	grep -qF -- "$1" "$tmp/summary" || { echo "FAIL: $name: the note was not written to the job summary"; fail=1; }
	: > "$tmp/summary"
}

jobs '[{"name":"Set up job","status":"completed","conclusion":"success"},
       {"name":"Verify native headless runner lifecycle","status":"in_progress","conclusion":null},
       {"name":"Verify Cursor command recovery","status":"queued","conclusion":null},
       {"name":"Say so if the job ran out of time","status":"in_progress","conclusion":null}]'
expect "stopped at the limit, step still shown in progress" 903 \
	"This job ran out of time: it is not a failing test." "after 15m3s, at its limit of 15 minutes" 'while running the step "Verify native headless runner lifecycle"'
expect "just inside the allowance before the limit" 872 "This job ran out of time"
expect "cancelled early, for example by a newer push" 240 \
	"cancelled after 4m0s, before its limit of 15 minutes" 'It was running the step "Verify native headless runner lifecycle"' "It did not run out of time."

jobs '[{"name":"Set up job","status":"completed","conclusion":"success"},
       {"name":"Verify the run reference graph store","status":"completed","conclusion":"cancelled"},
       {"name":"Verify offline run navigation","status":"completed","conclusion":"skipped"},
       {"name":"Say so if the job ran out of time","status":"in_progress","conclusion":null}]'
expect "the step already marked cancelled" 905 'while running the step "Verify the run reference graph store"'

jobs '[{"name":"Set up job","status":"completed","conclusion":"success"},
       {"name":"Verify guided fallback setup","status":"queued","conclusion":null},
       {"name":"Say so if the job ran out of time","status":"in_progress","conclusion":null}]'
expect "no step marked running: the first unfinished one" 905 'while running the step "Verify guided fallback setup"'

jobs '[{"name":"Set up job","status":"completed","conclusion":"success"},
       {"name":"Say so if the job ran out of time","status":"in_progress","conclusion":null}]'
expect "nothing but this step unfinished" 905 'while running the step "(not known)"'

# GitHub could not be asked, or this runner's job is not in the answer.
echo 'not json' > "$tmp/jobs.json"
expect "an unreadable answer" 905 "What it was doing could not be read from GitHub"
echo '{"jobs":[{"name":"x","runner_name":"someone-else","started_at":"2026-01-05T09:00:00Z","steps":[]}]}' > "$tmp/jobs.json"
expect "this runner's job is missing" 905 "What it was doing could not be read from GitHub"

[ "$fail" = 0 ] && echo "ci-timeout-note: all cases pass"
exit "$fail"
