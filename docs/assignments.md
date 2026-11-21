# Explicit assignment handoffs (unreleased)

For a BB coordinator, `loop.assignment_lifecycle: true` enables a private assignment review and a precondition on the explicit `await-user` command. It does not schedule lanes. Existing loops retain their behavior until a person opts in. Record a goal first and establish the loop's existing BB ownership; the review binds the goal revision, configuration/runtime, coordinator and owner generation.

BB execution (`active`, `idle`, and other structured statuses), declared assignment outcome, and declared lane availability are separate. An idle child with no handoff remains unknown. Turn end is not completion. A saved handoff remains unknown until the coordinator acknowledges its exact revision. The acknowledgement records an explicit coordinator judgment; it is not independent verification of the linked evidence, authorization, or global goal completion.

## Reconcile existing direct children

The coordinator or a person invokes the review. No initial import happens automatically on upgrade:

```sh
keepgoing assignment inventory -c /private/example/looptender.yaml --json
```

This bounded read lists hidden and visible **direct children**, then reads only their structured identity, parent, execution and archive metadata. No descendants, Git queries, provider probes, events or message text are read. At most 200 lanes, 1 MiB listing output, 64 KiB per show, two seconds per query and twenty seconds total are allowed. A capped/failed/changed listing or unverifiable child yields partial coverage and exit 1, retaining observed rows. Bracketed reads detect some races; they are not an atomic snapshot. A matching listing does not prove future coverage.

Prepare a private plan outside a public checkout. Every observed direct child needs a row, including idle and intentionally dormant children. Scope and authorization are references supplied explicitly, never interpreted as new permission:

```json
{"assignments":[
  {"id":"docs","lane":"thr_example_docs","scopeRefs":["private/docs-task.md"],"authorizationRefs":["private/owner-direction.md"]},
  {"id":"preview","lane":"thr_example_preview","scopeRefs":["private/preview-task.md"],"authorizationRefs":["private/owner-direction.md"]}
]}
```

```sh
keepgoing assignment review -c /private/example/looptender.yaml --file /private/example/plan.json
keepgoing assignment show -c /private/example/looptender.yaml --private
```

`review` takes a fresh inventory and saves coordinator-declared mappings. A partial review remains partial; it cannot establish full coverage. Every listed child must be accounted for. Prior unfinished assignment IDs cannot be dropped when a child disappears or is archived. One assignment is mapped to each lane. Inspect the private record for its ID, current record revision and each assignment revision; subsequent mutations require `--id` and `--revision` to reject stale updates. Ordinary output contains counts, coverage and dates, with no private IDs, text or refs.

For parked, errored or stale lanes, follow the [coordinator reconciliation guide](coordinating-lanes.md#reconcile-existing-lanes-before-parking-the-coordinator). A newer owner direction must be explicitly represented in the affected authorization references and reviewed; changed references invalidate the prior handoff and acknowledgement without clearing controls. Contents behind unchanged references are not reinterpreted or revision-tracked.

The coordinator explicitly requests a handoff where evidence is missing, using its existing authority to communicate with that lane. **looptender does not send that request**, spawn/restart/archive a lane, or infer permission. An unassigned observed child cannot disappear from review simply because it is idle.

## Lane document, coordinator import, acknowledgement

A lane prepares this bounded private document from explicit facts and references:

```json
{
  "assignment":"docs","assignmentRevision":1,
  "declaredBy":"thr_example_docs","declaredAt":"2030-01-02T03:04:05Z",
  "outcome":"unfinished","availability":"available","blocker":"none",
  "evidenceRefs":["private/review-result.md"],
  "remainingRefs":["private/remaining-fix.md"],"dependencyRefs":[],
  "nextOwner":"coordinator"
}
```

The lane can validate and emit it without accessing the coordinator's runtime:

```sh
keepgoing assignment handoff --draft --file /private/example/lane-handoff.json > /private/example/validated-handoff.json
```

The existing coordinator owner imports the received declaration, then separately acknowledges it. Here `assignment_example` and revision numbers stand for values inspected in the private record:

```sh
keepgoing assignment handoff -c /private/example/looptender.yaml --file /private/example/validated-handoff.json --id assignment_example --revision 1
# Private acknowledgement JSON: {"assignment":"docs","handoffRevision":1}
keepgoing assignment acknowledge -c /private/example/looptender.yaml --file /private/example/ack.json --id assignment_example --revision 2
```

Attribution is a coordinator-recorded statement about the lane declaration, not authenticated host/worker identity. A worker does not gain write access to another loop by naming its assignment. Existing ownership checks apply to every publication. The coordinator must receive the declaration through an authorized channel; no transcript is parsed to infer it.

Outcomes are `complete`, `unfinished`, `waiting`, or `superseded`; availability is independently `available`, `busy`, or `waiting`. Every outcome needs evidence refs. Unfinished work needs remaining refs and next owner. Waiting also needs dependency refs and an explicit blocker: `person` or `dependency`. Superseded work needs remaining refs, next owner and a successor ref; this does not fence or act on either loop. Complete requires no remaining work; it does not declare the whole loop complete. Future timestamps are refused when publishing. A new handoff invalidates the previous acknowledgement. Changed scope/lane or goal/owner binding requires a review and new handoff/acknowledgement; history survives.

## Explicit awaiting-user precondition

For opted-in loops, explicit coordinator `await-user` requires a bound review of observed direct children, no unknown/unacknowledged assignments, and at least one unfinished assignment, with **all unfinished assignments explicitly acknowledged as waiting on a person**. Actionable unfinished work, dependency-waiting, superseded remaining work, partial/unavailable/stale evidence and missing handoffs refuse that declaration without changing controls. All-complete assignments do not manufacture a global completion declaration; the coordinator evaluates the whole task separately.

For example, a merge awaits owner approval while an independent authorized review/fix remains available. The coordinator continues that work and asks the approval question again only when material information or the decision changes. Waiting on CI is a dependency, not necessarily waiting on a person. An explicit declaration cannot prove that all work or permissions have been inventoried.

Pause and stop remain unconditional safety controls. Automatic provider/input/budget/safety holds retain precedence and bypass this explicit-command precondition. Existing holds never clear from a handoff, acknowledgement, goal revision, inventory or upgrade. The supervised-agent self-resume restriction is unchanged.

## Status, bounds and recovery

`assignment show`, ordinary status and the read-only panel show missing, unavailable, stale-binding or available evidence separately. Unknown evidence has no zero counts. The panel shows evidence/review dates, observed-direct-child or partial coverage and unknown/unacknowledged/unfinished counts. The evidence is historical, not a live activity oracle. Staleness here means changed goal/ownership/config binding or assignment/handoff revision, not age-based permission or hold clearing. A later new child is outside the earlier dated observation until explicitly reviewed.

`assignments.json` is a private versioned record, capped at 200 assignments, 32 full revisions and 1 MiB. Inputs are capped at 100 KiB; refs at 32 per field and 2048 bytes each. Storage rejects symlinks/nonregular files, malformed/copied records and lost unfinished history. Full history refuses further writes instead of silently evicting obligations. Interrupted atomic publication leaves the old record; stale revisions fail without replacement. Inspect unavailable records privately, restore a verified backup if appropriate, and re-review explicitly; reads never repair or rebind them.

Older strict configuration readers reject `assignment_lifecycle`; do not activate it while an older reader still owns the loop. No configuration is rewritten on read. Verify all associated readers, ownership and gates before coordinated upgrades. Registry references in the BB plugin and the per-user CLI registry are different inventories; neither alone proves all installed instances have been covered.

## Opt-in coordinator wake for independent work

`assignment_lifecycle` alone keeps the existing child-active suppression. A separate opt-in permits one coordinator wake for an explicit batch of independently actionable assignments:

```yaml
loop:
  assignment_lifecycle: true
  assignment_coordination:
    enabled: true
    max_evidence_age: 30m
```

The age defaults to 30 minutes and must be between 1 minute and 24 hours. Freshness counts the entire check, including initial observation and optional revalidation. Expiry during revalidation withholds the busy-child exception; a baseline ordinary wake retires its valid local grant without a stale-evidence notice. It only withholds scheduling when evidence is too old; it never clears a hold or discards obligations. Missing configuration adds no supervision reads or queries. A disabled object also preserves the ordinary prompt and query sequence. Configuration requires a BB primary; this exception does not operate on a fallback.

First complete the initial review, import each lane's explicit handoff, and acknowledge its exact revision using the preceding workflow. Include the busy CI assignment as well as the idle docs and transfer-preview assignments. Missing, stale, unacknowledged, absent or partial evidence anywhere withholds this first exception: it does not automatically recover missing handoffs. Execution alone never supplies a declaration. A person-blocked merge does not block independently authorized review work, but the coordinator must explicitly declare that independence.

Create `ready.json` privately, naming exact assignment and handoff revisions from `assignment show --private`:

```json
{"independentlyActionable":true,"selections":[
  {"assignment":"docs","assignmentRevision":1,"handoffRevision":1},
  {"assignment":"preview","assignmentRevision":1,"handoffRevision":1}
]}
```

Each selected handoff must be acknowledged, `unfinished`, `available`, and have blocker `none`. The explicit declaration attests that its remaining work is already authorized and independent of ongoing lanes. Looptender does not interpret scope, authorization, dependency or next-owner text to reach that conclusion. Set the following variables from the current private record identity/revision, then run:

```sh
keepgoing assignment ready -c /private/example/looptender.yaml --file /private/example/ready.json \
  --id "$ASSIGNMENT_RECORD_ID" --revision "$ASSIGNMENT_RECORD_REVISION"
```

This saves one batch grant bound to the complete reviewed rows, handoff acknowledgements and inventory, including unselected busy lanes. Any change to that evidence requires a new declaration; acknowledging a changed unselected handoff cannot reactivate an old grant. The private freshness digest is not projected in ordinary output. The declaration does not send anything. The ordinary scheduled check may use it only for the existing configured coordinator, with exact current idle direct-lane evidence. One healthy busy CI lane can then coexist with a single coordinator notice about the independent docs/preview work. No message, retry, spawn, archive, reparent or stop is sent to a child. The coordinator chooses its next authorized action and preserves healthy workers.

The pure decision exception bypasses only active-child suppression. Every existing stop, pause, disabled/awaiting-user/completed/retired gate, interaction, retry, window, spacing, network, deadline and send/progress bound retains precedence. The notice uses the ordinary Wake action and send allowance. It contains fixed counts and the automatic/non-person-instruction disclosure, without private references or stale task text. It adds no extra wake cadence or permission. The independent-work exception never triggers a route change.

Immediately before consuming a grant, one bounded structured child scan revalidates identities, statuses and pending interactions. It uses the existing optional-check allowance, at most 20 seconds, with send/report/cleanup reserves retained. No retry loop or provider probe is added. Changed, partial or failed evidence or insufficient optional budget withholds the busy-child exception. The observation and BB tell are not a transaction: changes after the final scan cannot be cancelled by this contract.

A normal no-active-child wake keeps its configured task prompt and may append the fixed counts notice. It retires an exact valid local grant in that same attempt, preventing a later busy-child exception from repeating it. If optional revalidation is unavailable or changes evidence, the normal wake proceeds under its existing gates without a current-assignment claim, and retires only the still-bound local grant as `superseded_by_ordinary_wake_attempt`. The same descriptive retirement applies when a strictly loaded current-binding grant has unusable readiness (for example, a changed unacknowledged handoff); it adds no evidence notice or scheduling authority. Corrupt, changed-binding or full records are not rewritten. Unknown or stale assignment evidence never suppresses baseline ordinary wakes by policy. Actual source/ownership/storage failures at the pre-send safety boundary can still prevent delivery.

Before any adapter call, sends.json reserves the ordinary allowance and the assignment record atomically records the exact grant attempt. If allowance reservation fails, no grant is consumed. If assignment publication fails, no prompt is sent, and the reserved allowance is conservatively spent. A crash may consume allowance/grant without delivery. A failed or unknown send, restart, failed final diagnostic save, provider return, time passing, or unrelated metadata update does not rearm that grant. This is durable attempt bookkeeping, **not delivery evidence**.

After reviewing the attempt and remaining assignment obligations, the coordinator prepares `reconciliation.json` using the grant identity from private show:

```json
{"grant":"assignment_00000000000000000000000000000000","reviewed":true}
```

```sh
keepgoing assignment reconcile-wake -c /private/example/looptender.yaml \
  --file /private/example/reconciliation.json \
  --id "$ASSIGNMENT_RECORD_ID" --revision "$ASSIGNMENT_RECORD_REVISION"
```

Refresh the record revision variables before each write. This declares coordinator review, not authenticated receipt or completion. `assignment show`, status and the panel distinguish readiness, stale evidence and attempted/unreconciled work with dates and the next supported command. A new readiness batch requires another explicit declaration; an attempted grant cannot be replaced before reconciliation. Handoff/assignment/goal/owner/binding changes make old readiness unusable without erasing the attempt. Reads never repair evidence. Scheduling data explicitly promotes assignments.json to version 2; version 1 remains readable, but older binaries refuse the promoted record. The existing 32-revision/1-MiB bound applies to declarations, attempts and reconciliations too; full history withholds further coordination notices rather than evicting unfinished obligations; baseline ordinary wakes remain available. There is no supported history compaction/rotation in this slice. Preserve the full record and explicitly disable assignment_coordination if necessary. Never restore a pre-attempt backup to rearm a grant: retain the attempt evidence and reconcile it explicitly.

## Remaining acceptance

This implements explicit initial reconciliation, handoff import/acknowledgement, visibility and the declared hold precondition. It does **not** require a handoff before BB ends a turn: the available idle event is post-turn and the dispatch hook is not a turn-completion checkpoint. The separate coordinator-wake opt-in above covers explicitly declared independent work alongside a healthy worker. It does not dispatch ready lanes, authenticate worker publication, enforce turn-end handoffs or resume a held coordinator after owner continuation. Unknown or missing evidence still requires explicit reconciliation; passing this slice does not close the whole idle-work incident.

Fixture evidence is separate from deployed acceptance. After reviewed artifacts and exact-head checks, the authorized rollout must inventory all BB-associated instances (including newly discovered associated apps), preserve identity/controls/accounting, satisfy ordinary owner-terminal execution constraints, and verify installed behavior. No source test proves that upgrade or live coordinator scheduling happened.

## Whole-objective completion review (unreleased)

A milestone is not the whole objective. Before delegating, the coordinator records
an explicit goal with objective, acceptance and scope references, then accounts
for direct children and their assignments. References remain opaque declarations:
looptender never reads a roadmap or transcript to decide the scope or whether
acceptance was met. A worker's idle state, disappearance or completed handoff
cannot establish the coordinator's whole-objective completion.

Separately opt in with `loop.assignment_completion: true`, requiring
`assignment_lifecycle` and a BB primary. Default is false; existing loops keep
their completion behavior and make no additional queries. Old strict readers
reject this new key. Verify all associated readers before enabling it; it adds
no record schema, migration, permission or automatic completion.

For opted-in loops, bare `complete` refuses. Inspect the current private goal and
assignment record, evaluate the whole objective under existing authority, and
prepare a private regular file outside configuration/runtime/progress directories:

```json
{
  "wholeObjectiveComplete": true,
  "goal": "goal_0123456789abcdef0123456789abcdef",
  "goalRevision": 1,
  "assignments": "assignment_0123456789abcdef0123456789abcdef",
  "assignmentRevision": 4
}
```

```sh
keepgoing goal show -c /private/example/looptender.yaml --private
keepgoing assignment show -c /private/example/looptender.yaml --private
keepgoing complete -c /private/example/looptender.yaml --review-file /private/example/completion.json
```

Use the exact current IDs/revisions, not those example values. The file is bounded
to 4 KiB and rejects unknown, duplicate, null or missing fields and symlink inputs.
Its explicit true statement is required even for a reviewed zero-child objective;
zero children and completed assignments are preconditions, never proof of global
acceptance. No new attestation ledger is saved: success uses existing completion
control/history/report semantics, while the goal stays descriptive metadata.

The current strict assignment record must match the goal, config/runtime,
coordinator, owner generation and durable target. The current diagnostic target
must remain primary; damaged diagnostic state or a fallback route refuses this
primary-coordinator review. Every retained obligation must
have an explicit complete handoff acknowledged at that exact revision. Unknown,
unacknowledged, unfinished, person/dependency-waiting and superseded remaining
work refuse completion. Partial/unavailable inventories and stale identities or
revisions refuse without changing runtime evidence. Review and reconcile first.

Coordination grants in a version-2 record remain historical evidence. Readiness,
an attempted or uncertain notification, and ordinary-wake retirement never prove
whole-objective completion. Conversely, an old unreconciled notification attempt
does not block completion once every current obligation is explicitly complete
and acknowledged and the exact whole-objective attestation passes. Unfinished,
unacknowledged or changed-revision work still refuses. Completion neither erases
nor reconciles nor rearms grants; the completed control gate prevents a pending
unattempted grant from sending. Strict version-2 history validation still applies.

Previously completed and acknowledged historical assignments may remain absent
**only after an explicit assignment review records that absence**. Retain their
rows in the plan to keep the completion evidence visible; old revisions remain
in history. A disappearance after review refuses until reviewed again. An absent
unfinished or unknown obligation never becomes completed or disappears by this
rule. Current children must all be accounted for and idle; active, pending,
starting, stopping and error states refuse. Separate bounded interaction reads
must establish no pending or unknown child interaction.

Only this explicit command queries BB metadata: two bracketed inventories and
interaction lists, under a single twenty-second ceiling, existing 200-child
listing bounds, two seconds per query, 64 KiB per show/interaction response and
fewer than 500 interaction entries. No provider model, Git, events or transcript
query is made. The command revalidates sources, goal, owner, target and the exact
loaded assignment record under the existing tick/control locks before saving.
External BB observation and control publication are not atomic; a change after
the final observation cannot be cancelled by this contract.

Pause and stop remain unconditional and survive successful completion. Automatic
safety holds retain precedence; an explicit attestation is no self-resume or
hold-clear authorization. When completion would end awaiting-user, the existing
supervised-caller hold-clear refusal applies. Refusal leaves controls, assignment
history and goal evidence unchanged. No lane is messaged, started, stopped,
archived or retried. Missing handoff recovery, host-enforced publication and live
rollout remain separate acceptance.
