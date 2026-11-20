# PR backlog safeguard (unreleased)

A coordinator can keep starting branches while reviews, fixes and integration wait. An optional WIP policy puts structured GitHub evidence into an ordinary wake, so the coordinator does not have to remember to ask for it. It does not enforce branch creation: looptender has no branch-creation hook, never edits or merges repositories and grants no permission.

## Opt in

The existing `gh` CLI must already have read access to the configured repository on **github.com**. This feature neither logs in nor requests additional scopes. With no `pr_backlog` block there are no GitHub reads, cache updates or prompt changes.

```yaml
loop:
  # Other required loop settings omitted.
  pr_backlog:
    repo: example/project
    max_open: 5
    refresh_interval: 15m
    max_age: 1h
    # Optional exact repository labels, declared by your workflow:
    held_label: workflow-held
    superseded_label: workflow-superseded
```

`repo` and `max_open` are required. The threshold is inclusive: **five open PRs reaches a threshold of five**. All open PRs count, including drafts and those labelled held or superseded. This avoids hiding unfinished work by changing its label. `max_open` accepts 1–5000; acquisition is capped at 500 rows, so a larger threshold never turns a capped inventory into an all-clear. `refresh_interval` defaults to 15 minutes and accepts 5 minutes–24 hours. `max_age` defaults to one hour and must be at least the refresh interval and at most 24 hours. Labels are optional, distinct, exact, case-sensitive strings of at most 100 bytes. Absent label configuration is reported explicitly, so zero matches never establishes that there is no held or superseded work. They declare workflow status only; a label is not an approval, runtime hold, verified supersession or permission.

## What an ordinary wake consults

Only an already-selected ordinary wake consults this policy. Active/idle timing, send allowances, operating windows, lane waiting and all existing gates continue to decide whether there is a wake. There are no extra wakes, reports, background polling or tasks. Notice-only lane-error messages, nudges, re-prompts, turn retries and handoff/return briefs do not consult it. Dry runs and held, disabled, completed, stopped or superseded loops make no backlog query.

When a refresh is due, at most five sequential GraphQL pages of 100 open PRs are read, under one five-second deadline and a two-MiB stdout cap per page. Process cleanup is bounded by one additional second. There are no query retries. Optional work uses only the check's remaining optional budget, preserving send/report reserves. It also reserves that cleanup second. With less than two seconds available it records `budget_unavailable`. The effective control gate is checked again after consultation; a newly saved stop, pause, hold, disable or retirement suppresses that ordinary wake. This does not cancel an already-sent message.

Success and failure attempts are both spaced by `refresh_interval`, measured at eligible wakes. The private `state.json` cache retains only policy binding, attempt/observation time, bounded aggregates and notice bookkeeping. It retains no PR title, body, review text, branch name, label text, raw output or credentials. A failed refresh preserves the last complete aggregate but marks it historical immediately. Expired, future, malformed, incomplete or differently bound evidence cannot establish a below-threshold result. A missing/null field, changing count, repeated PR/cursor, pagination cap, partial label listing or GraphQL error is unavailable evidence, never zero. Individual reads are not an atomic snapshot; changes that preserve counts can escape detection.

## Notice and evidence

At or above the threshold the fixed notice asks the coordinator to **finish reviews, fixes and integration of authorized existing work before starting more branches**. If it exceeds the policy, it must state an explicit reason and its existing authority. This is an instruction in the configured workflow, not a mechanically enforced branch prohibition or a verified exception. Below-threshold evidence grants no permission. Unknown/stale evidence asks the coordinator to inspect the backlog before expanding work.

The notice includes the observation date, count, oldest creation date, aggregate additions/deletions/files, draft count, declared held/superseded counts, structured blockers, unknown review/merge states and observed branch-dependency count. Categories overlap; file counts are summed PR file counts, not distinct files. Blockers reflect observed drafts, review requirements/changes requested, and GitHub `BLOCKED`, `DIRTY`, `BEHIND` or `UNSTABLE` merge states. Other states are not a readiness claim. A clean merge state is not proof of exact-head/current-base checks, resolved reviews, merge permission or merge-queue acceptance.

An observed branch dependency means another open PR from the same repository has a head branch matching this PR's base branch. Fork branches do not establish that match; multiple matches remain multiple candidates. This is not a full dependency graph: body references and unstated dependencies are not read, and cycles do not establish an integration order.

The notice is appended once per known conversation/target and semantic evidence change. Observation timestamps and age drift alone do not repeat it. A fresh failure/unknown transition is distinct from a prior below-threshold notice. At most three returned-send attempts carry an unchanged notice; later ordinary wakes continue without it. A changed status or known conversation resets that allowance. An adapter return is not proof the coordinator acted; a crash before the state save can repeat a notice. No suppression record changes wake cadence or grants permission. Unknown conversation changes cannot be discovered from transcript text.

## Inspect explicitly

```sh
looptender pr-backlog --repo example/project --max-open 5
looptender pr-backlog --repo example/project --max-open 5 --json
```

Optional `--held-label` and `--superseded-label` use the same exact-label convention. The command needs no loop configuration, writes no loop records and makes only the fixed read-only GitHub query. It prints PR numbers, creation/update dates, size, draft/declared states, recognized merge/review states and observed branch dependency numbers, plus the aggregate notice. Titles, branch names, labels and raw errors are withheld. Exit 0 means a complete observed count below threshold; exit 1 means threshold reached or evidence unavailable; exit 2 means invalid arguments. These exits gate only this command, not arbitrary shell commands or branches. There is no override or GitHub write operation.

GitHub's structured fields and merge-state meanings are documented in the [GraphQL pull request reference](https://docs.github.com/en/graphql/reference/pulls); the transport uses [gh api](https://cli.github.com/manual/gh_api).

## What is verified

Fictional tests cover bounded acquisition, zero versus unavailable/capped inventories, count/page changes, required fields, private projection, policy binding, freshness/failure retention, threshold boundaries, fork dependency exclusion, command exits, send suppression and failed-send caps. Supervisor fixtures cover ordinary-wake attachment, cached/failure/repair transitions, optional budget and controls saved during the read. These tests use injected structured reads and stand-in subprocesses, not live GitHub, BB or provider calls.

This is source-only and unreleased until distributed. It does not activate existing loops or update installed binaries. Native platform execution, live GitHub credentials/rate limiting, a deployed coordinator following the notice and actual branch-creation prevention are separate acceptance; compilation or documentation does not establish them. Pending approval must also leave independent authorized work moving; see the separate [coordination acceptance scenario](coordinating-lanes.md#approval-dependent-work).
