# Private goal metadata

On `main` after v0.1.3, `keepgoing goal create|revise|show` records what a loop is working toward. The record has a generated stable ID, an owning loop name and an ordered revision history. Its only state is `recorded`: this means a specification was saved, not that work is running, accepted or complete. Loop controls remain the authority for supervision.

This is a metadata feature. It does not generate a plan, modify the loop's prompt, start a turn, execute a next action, validate a reference or run a reviewer. Completion declarations, verification results and review stages are not yet bound to goal revisions. `complete` and `resume` keep their existing behavior and do not change the goal record. Do not treat an old completion hold as acceptance of a newly revised goal.

## Create and inspect

Save a JSON specification in a private file outside the checkout. Keep sensitive text out of shell arguments and public repositories. For example, a fictional specification could contain:

```json
{
  "title": "Deliver the approved roadmap",
  "objectiveRefs": ["private/roadmap.md"],
  "acceptanceRefs": ["private/acceptance.md"],
  "scopeRefs": ["private/scope.md"],
  "authorizationRefs": ["private/authorization.md"],
  "nextAction": {
    "summary": "Verify the next approved milestone",
    "refs": ["private/next-action.md"]
  },
  "evidenceRefs": ["private/verification-notes.md"]
}
```

```sh
keepgoing goal create -c loop.yaml --file private-goal.json
keepgoing goal show -c loop.yaml --json
keepgoing status -c loop.yaml --json
```

Creation refuses an existing goal. Output and ordinary status contain only `id`, `revision` and `state`; titles, next-action text, the owning-loop field and references from the goal record are withheld. Existing status fields retain their existing behavior. To intentionally inspect the full private record, including all prior specifications:

```sh
keepgoing goal show -c loop.yaml --private
```

This prints private JSON to stdout. Use it only in a private context. No goal text is appended to run history, sent in reports or passed to a provider by these commands.

## Revise explicitly

Supply the ID and current revision from `goal show`, with a complete replacement specification:

```sh
keepgoing goal revise -c loop.yaml \
  --id goal_0123456789abcdef0123456789abcdef --revision 1 \
  --file revised-private-goal.json
```

The result is revision 2 of the same goal. Every accepted edit appends a revision, even if its contents are identical. A mismatched ID or revision is rejected without changing the record. Omitted optional fields are cleared in the new specification; earlier revisions remain intact. Evidence and authorization references are descriptive pointers only, not verified evidence, approval tokens or permission to act. No file or URL is read or executed through these references, and relative paths have no automatic resolution semantics.

Creating and revising acquire the tick lock, verify the loaded config/fallback sources, then acquire the control lock to check BB ownership and write `goal.json`. Another BB owner must first use the existing explicit takeover flow. Identity-free terminal/scheduler calls can use the current claim, as other controls do. This is cooperative local coordination, not authentication. The goal's owning loop name must still match the config; a rename/migration command is not implemented.

All holds, owner claims, counters and active targets are preserved. A new goal never enables a loop or clears an existing completion/awaiting-user hold. Metadata survives process restart; ordinary target changes do not edit it. Full transfer, revision-bound acceptance and stale-result invalidation remain separate roadmap work. Older binaries do not understand goal metadata, and deleting/copying runtime files is outside these guarantees.

## Storage and limits

`state_dir/goal.json` is a separate private record (0600 on Unix, inside the private runtime directory), written by atomic replacement using the existing control writer. This does not promise power-loss durability or filesystem access isolation from an operator who can edit runtime files. Missing records are compatible with existing loops; corrupt, unsupported-version or non-regular records, including symlinks, refuse edits. Status reports a generic goal error without printing raw input. Goal metadata is not a supervision gate: missing or invalid goal metadata does not change tick/control behavior.

Input must be one JSON object, at most 32 KiB, with a nonblank title (up to 256 bytes) and nonempty `objectiveRefs` and `acceptanceRefs` arrays. Unknown fields and trailing JSON are rejected. Each reference array has at most 32 entries, each nonblank and at most 2,048 bytes. Optional `nextAction` needs a nonblank summary of at most 1,024 bytes. The stored history is capped at 128 revisions and 1 MiB. Reaching either cap refuses the edit without discarding history; archival/replacement workflows are not implemented.

Offline storage and command fixtures cover reload, competing edits, ownership, holds, history limits, malformed records, private output, lock contention and zero provider calls. No live goal adoption or provider walkthrough has been performed.
