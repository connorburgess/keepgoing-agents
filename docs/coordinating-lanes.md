# Coordinating lanes

A convention for a [coordinator](glossary.md): a conversation that hands out lane work and reviews it, and is the parent of those lanes. It is written for agents and the people who set them up. The usual shape is one coordinator per project: the session the person talks to, which hands work to lanes and is kept going by a loop while the person is away. Coordinators of different projects are peers and send each other requests, which can be declined and never carry approval.

**looptender enforces none of this.** It does not read a ledger, check a report's length, record approvals, choose a lane's model or limit nesting. Each rule below is something a coordinator does by itself. What looptender does for lanes today is listed [at the end](#what-looptender-does-for-lanes-today).

## Create a lane when its work is assigned

Do not start a lane ahead of the work. Creating a conversation runs a turn, and every later wake of that conversation reads its whole history again, so a lane created early costs a turn now and context later, even if it never does anything.

Until a lane's work is assigned, keep it as a row in a ledger file the coordinator owns:

| Lane | Handoff file | Worktree or base |
| --- | --- | --- |
| `example-app · docs page` | `briefs/docs-page.md` | new worktree from `origin/main` |
| `example-app · usage reading` | `briefs/usage-reading.md` | after `docs page` merges |

Create the conversation when the row is assigned, and point it at its handoff file. Keep the ledger outside the repositories the lanes work in: it is the coordinator's record, and it is what lets another conversation take over coordination.

## Use lanes to advance independent work

Before substantial work, identify concrete deliverables and dependencies. When the
person or project instructions authorize delegation, assign independent work now;
do not keep all implementation, evidence gathering and review in the coordinator.
Respect configured concurrency and spending bounds. Prefer reusing a suitable
lane to creating another, and avoid delegation overhead for a small or sequential
task.

In BB, a request for child lanes means visible child conversations parented to the
coordinator. Internal subagents and named research topics are not equivalent to
that visible roster. Record the actual child ID, assignment and result pointer.
Give each lane a bounded scope, expected output, verification and separate file or
worktree ownership. Keep a shared signed-in browser or another exclusive resource
with one owner; other lanes can independently review exported evidence.

While a lane or CI is running, advance another authorized deliverable. When all
remaining work depends on a pending result, wait for that result rather than
repeatedly querying it. Review new evidence once; repeat a check only when changed
source, a failed check or an unresolved concern warrants it. Required checks on a
changed merge head or base still apply. An idle lane with an unfinished assignment
needs reconciliation, not an automatic completion claim.

New setup prompts contain this guidance. Updating a binary or restarting a daemon
does not rewrite existing saved prompts. Existing coordinators need an explicit,
reviewed prompt update preserving their task, limits and holds. Guidance does not
make looptender create conversations, dispatch work, grant permission, infer
completion from transcripts or clear a runtime hold.

Acceptance remains open: observe a coordinator assign two authorized independent
deliverables to visible child lanes, advance a third while they run, reconcile a
completed lane once, and preserve a later person-only gate. Include a dependent
single-task control where no extra lane is created. Record output and elapsed
progress; a changed prompt alone does not establish faster completion.

## Report by pointer

A lane reports with a path and a few lines: the pull request or file, the commit, what was checked, and what is open. Anything longer goes in a file whose path the report names. Do not restate a brief or a diff in a report; the coordinator reads the file if it needs the detail, and every restatement is read again on every later turn of the coordinator.

## Record approvals verbatim, with their source

When a person approves something, copy their exact words into the ledger with where they said it (the conversation and message, or the pull request comment). Then:

- A request is not an approval. "Could you look at merging this?" asks for a look.
- Nothing transfers by implication. Approval to merge one pull request does not approve the next, approval given to one lane does not pass to another, and an approval does not survive a change of coordinator unless the ledger records it.
- A lane acts on an approval only when the coordinator hands it the recorded words and their source.

## Choose each lane's model and effort when starting it

Decide the model and the effort level as part of creating the lane, from the work in its handoff file, and record the choice in the ledger row. A lane does not inherit a sensible choice by default, and looptender does not route lanes: its [routes](glossary.md) apply to the loop's own conversation only.

How to choose, also a convention that looptender does not enforce:

- The coordinator runs on the strongest model among itself and its lanes, and chooses each lane's model by the task.
- A lane's model is never stronger than its coordinator's.
- Match the model to the work. Code and design-heavy work gets the strongest lane model. Docs, wording, mechanical edits and simple reviews get a mid model. Lookups and status checks get the smallest. Reviews of code changes stay on the strongest lane model or with the coordinator.
- The coordinator steps in when a lane is stuck, has been wrong twice, or the task has turned out harder than its model. It does the work itself or re-runs the lane one level up, rather than letting it grind.

### Antigravity and Gemini 4 model tiers

As of November 2026, when running coordinators and lanes on Antigravity or the Gemini 4 API (also a convention looptender does not enforce; check live limits in the provider's console or status because plans and pricing change):

- **Multi-file feature PRs & refactors (daily driver):**
  - **In Antigravity (Pro/Ultra):** Use **Orchestrator (Neon)** — it plans with `gemini-4-neon` and delegates file edits and test loops to the cheaper `gemini-4-hydrogen`, stretching the 5-hour/weekly compute pool while keeping frontier accuracy.
  - **Over the API:** Plan and review with `gemini-4-neon` (`medium` or `high`), then hand execution to `gemini-4-hydrogen` (`high` or `medium`) via `previous_interaction_id`.
  - **Do not push `gemini-4-hydrogen` to `xhigh`:** `gemini-4-neon` `medium` scores higher, runs nearly 2× faster, and costs less per task than `gemini-4-hydrogen` `xhigh`. Keep Hydrogen at `medium` or `high` for routine edits and test fixes.
- **Hard debugging / system design (escalation):** Use `gemini-4-carbon` or `gemini-4-neon` at `high` / `xhigh` (Neon `high`/`xhigh` matches Carbon `high` on benchmarks in less than half the time).
- **High-volume PR chores (summaries, release notes, lint/triage, lookups):** `gemini-3.5-flash-lite` (or `gemini-3.8-flash` where available).
- **`gemini-4-argon`:** Superseded by Neon for general coding and PR work; reserve for tasks that specifically need its lower hallucination rate.

Quota-saving habits for Antigravity loops and lanes:

- Google AI Pro and Ultra meter Antigravity from a shared compute pool across Gemini models (5-hour refresh up to a weekly cap; partner Claude and GPT models draw from a separate pool). Heavier models, higher thinking levels (`xhigh` > `high` > `medium` > `low`), and long conversation histories drain the pool fastest.
- Start a fresh thread/lane per PR or ticket rather than re-sending huge chat contexts across unrelated work.
- Explicitly set `thinking_level` in API calls so model swaps do not silently spike output tokens (`gemini-4-neon` and `gemini-4-hydrogen` default to `high` when unset, whereas `gemini-4-carbon` and `gemini-4-argon` default to `medium`).
- Avoid the free Gemini API tier for private or work repositories (free-tier content may be used to improve provider products, and free projects have tight per-project rate caps).

## Keep nesting shallow

Prefer two levels: a coordinator and its lanes. A lane coordinates lanes of its own only when the work needs it, and then it follows this page as their coordinator.

Each level costs a full turn at that level's context. A result from a lane's own lane reaches a person only after its parent lane runs a turn to relay it and the coordinator runs another to read it, and each of those turns reads that conversation's whole history. A relay is never cheaper than the conversation relaying it, and detail is lost at each one.

## Where the text lives

The full rules live on this page. A coordinator's wake prompt carries at most three short lines, because the wake prompt is sent again on every wake:

```text
Start a lane only when its work is assigned; until then it is a ledger row.
Lanes report by pointer: a path and at most five lines.
Act only on approvals recorded verbatim with their source; a request is not an approval.
```

Point the coordinator at this page, or a copy of it, from its handoff file rather than pasting the rules into the prompt.

## What looptender does for lanes today

looptender supervises the loop's own conversation. For a BB loop it also knows the loop's direct lanes by count and state, and acts on them in two cases only. The rules are in [architecture](architecture.md#rules-that-do-not-bend) and the [glossary](glossary.md); in short:

- **Does:** count lanes and their states; let a working lane keep the loop from looking idle, and wake the loop when a lane finishes; escalate a lane's pending question to a person; report lanes in error; retry a failed lane's turn when the loop opts in with `bb.children.retry_errored` ([lane recovery](operations.md#lane-recovery)); stop lanes when a person asks with `stop --lanes`; say in a handoff brief how many lanes were working. See [BB child awareness](operations.md#bb-child-awareness).
- **Does not:** create, message or move a lane; read a lane's message text; maintain an automatic assignment ledger or know about lanes not yet created; record or check approvals; choose a lane's model or effort; limit nesting or report size.

The unreleased opt-in record below adds explicit assignment evidence and an explicit awaiting-user precondition; it does not add automatic scheduling or approval authority. Remaining planned coordinator support is listed in the [roadmap](roadmap.md#coordinating-lanes-planned).

## Explicit assignment lifecycle (unreleased)

For an opted-in BB coordinator, use the [assignment lifecycle commands](assignments.md)
for an initial direct-child review and subsequent lane handoff imports and acknowledgements.
Keep BB execution separate from outcome and declared availability. Missing or unacknowledged
handoffs require reconciliation, even for an idle lane. A pending merge approval holds that
action; continue independent authorized review/fix work and repeat the question only with
material new information. Automatic scheduling and mandatory end-of-turn handoffs remain
unverified host/runtime acceptance; the explicit awaiting-user guard does not supply them.

## Finish work before expanding the backlog

The unreleased opt-in [PR backlog safeguard](pr-backlog.md) consults structured GitHub evidence during an ordinary wake. Configure an explicit repository and WIP threshold; counts include held work and drafts. At the threshold, finish authorized reviews, fixes and integration before opening more branches. If more work is necessary, record the explicit reason and the authority already granted. A threshold notice is not permission to merge, combine PRs, dismiss reviews or change a hold. Unknown evidence is not an empty backlog. This feature supplies evidence and a fixed reminder; these coordination decisions remain the coordinator's.

## Approval-dependent work

Hold the action that needs approval and continue independent authorized tasks. Repeat an approval question only when material new information arrives or the decision changes. Enter awaiting-user only when all remaining authorized work requires a person. A recorded pending approval is neither an approval nor a reason to abandon unrelated work.

For example, fictional PR42 awaits the owner's merge approval, while PR43 has an already-authorized independent review and PR44 has an already-authorized test fix. Keep PR42's merge held, perform the PR43 review and PR44 fix, then report their evidence. Do not repeatedly ask to merge PR42 without new information. Ask a person when only approval-dependent work remains. A runtime hold already in place still wins: the supervised session must never clear it or resume itself to perform this scenario.

This is a separate coordination acceptance case. Observed coordinator stalling is not proof of a looptender runtime defect. The backlog safeguard does not inspect assignments or approval text, decide which work is independent, set awaiting-user based on this scenario, infer permission from transcripts, or bypass self-resume restrictions. Automatic child dispatch/approval and host handoff compliance remain open. The separate [explicit independent-work coordinator wake](assignments.md#opt-in-coordinator-wake-for-independent-work) uses typed readiness, existing gates and durable attempt consumption; the assignment record alone does not supply those capabilities.

### Completing the whole objective

When a phase finishes, verify its result and reassess the remaining objective
before deciding what to do next. If an independent authorized step can proceed,
take one concrete next action; repeated terminal receipts are not new progress.
For example, a fictional implementation phase has verified results and all its
lanes are idle. An offline integration review is authorized, while a later live
release needs a person. Continue the review and keep the release held. Missing
authorization or a runtime hold still prevents the affected action; idle lanes
alone establish neither completion nor permission.

New `init` prompts include this guidance. Existing saved prompts are not rewritten.
This is a coordinator instruction, not automatic dispatch or a runtime guarantee.
The phase-settlement acceptance case remains open until observed coordinator
behavior verifies continuation, duplicate-receipt handling and preserved holds.

Establish the goal's objective/acceptance/scope references before assigning lanes;
record each lane's explicit scope and reconcile its handoff. A finished milestone,
an idle roster or a worker disappearing is not a declaration that the whole
objective is done. Use the separate opt-in [completion review](assignments.md#whole-objective-completion-review-unreleased)
to require complete acknowledged obligations, fresh direct-child coverage and an
explicit whole-objective attestation bound to the current goal/review. For a
zero-child objective the attestation remains required; child inventory cannot
verify acceptance. This safeguard does not schedule independent work, manufacture
approval, enforce host turn-end handoffs or resume a held coordinator.
