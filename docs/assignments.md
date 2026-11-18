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

## Remaining acceptance

This implements explicit initial reconciliation, handoff import/acknowledgement, visibility and the declared hold precondition. It does **not** require a handoff before BB ends a turn: the available idle event is post-turn and the dispatch hook is not a turn-completion checkpoint. It does not automatically wake the parent alongside a healthy worker, dispatch ready lanes, authenticate worker publication or resume a held coordinator after owner continuation. Existing child-active suppression remains. These scheduling/host compliance paths require separate typed anti-duplicate and host-hook acceptance; passing the precondition does not close the whole idle-work incident.

Fixture evidence is separate from deployed acceptance. After reviewed artifacts and exact-head checks, the authorized rollout must inventory all BB-associated instances (including newly discovered associated apps), preserve identity/controls/accounting, satisfy ordinary owner-terminal execution constraints, and verify installed behavior. No source test proves that upgrade or live coordinator scheduling happened.
