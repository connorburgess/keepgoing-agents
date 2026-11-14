#!/usr/bin/env bash
# Say, in the job summary, what a cancelled CI job was doing when it stopped,
# and whether it stopped at its time limit. A job that runs into
# `timeout-minutes` is reported by GitHub as cancelled, with the step it was
# in marked cancelled and the rest skipped: at a glance that looks the same
# as a job cancelled by a newer push, and nothing says "time limit".
#
# Run as a last step with `if: cancelled()`:
#   LIMIT_MINUTES=<the job's timeout-minutes> GH_TOKEN=<token> scripts/ci-timeout-note.sh
# It needs `actions: read`. It never fails the job.
#
# For tests: CI_TIMEOUT_JOBS_JSON names a file holding the API answer, and
# CI_TIMEOUT_NOW is the time to treat as now (seconds since 1970).
set -u
limit=${LIMIT_MINUTES:?set LIMIT_MINUTES to the timeout-minutes of the job}
out=${GITHUB_STEP_SUMMARY:-/dev/stdout}

say() {
	printf '%s\n' "$1" | tee -a "$out"
}

if [ -n "${CI_TIMEOUT_JOBS_JSON:-}" ]; then
	jobs=$(cat "$CI_TIMEOUT_JOBS_JSON")
else
	jobs=$(gh api "repos/${GITHUB_REPOSITORY}/actions/runs/${GITHUB_RUN_ID}/attempts/${GITHUB_RUN_ATTEMPT:-1}/jobs?per_page=100" 2>/dev/null) || jobs=""
fi
now=${CI_TIMEOUT_NOW:-$(date -u +%s)}

# This job is the one running on this runner. The step that was running is
# the one GitHub shows in progress or cancelled; failing that, the first one
# that has not finished. This step itself does not count.
filter='[.jobs[]? | select(.runner_name == $runner)] | first // empty
	| (.started_at | fromdate) as $started
	| ([.steps[] | select(.name != $self)]) as $steps
	| ( ($steps | map(select(.status == "in_progress" or .conclusion == "cancelled")) | first)
	    // ($steps | map(select(.status != "completed")) | first)
	    // {name: "(not known)"} ) as $running
	| "\($started) \($running.name)"'
found=$(printf '%s' "$jobs" | jq -r --arg runner "${RUNNER_NAME:-}" --arg self "${CI_TIMEOUT_STEP_NAME:-Say so if the job ran out of time}" "$filter" 2>/dev/null) || found=""
started=${found%% *}
step=${found#* }

if [ -z "${started:-}" ]; then
	say "**This job was cancelled.** What it was doing could not be read from GitHub, so it is not known whether it reached its limit of ${limit} minutes."
	exit 0
fi

elapsed=$((now - started))
took="$((elapsed / 60))m$((elapsed % 60))s"
# GitHub stops the job at the limit; allow for the time it takes to get here.
if [ "$elapsed" -ge $((limit * 60 - 30)) ]; then
	say "**This job ran out of time: it is not a failing test.** It was stopped after ${took}, at its limit of ${limit} minutes, while running the step \"${step}\". Steps after that one did not run. If it keeps happening, the job has outgrown its limit: see \"CI time limits\" in docs/testing.md."
else
	say "**This job was cancelled after ${took}, before its limit of ${limit} minutes**, for example by a newer push to the same pull request. It was running the step \"${step}\". It did not run out of time."
fi
exit 0
