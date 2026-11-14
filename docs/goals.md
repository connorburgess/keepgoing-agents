# Private goal metadata

In v0.1.4, `keepgoing goal create|revise|show` records what a loop is working toward. The record has a generated stable ID, an owning loop name and an ordered revision history. Its only state is `recorded`: this means a specification was saved, not that work is running, accepted or complete. Loop controls remain the authority for supervision.

This is a metadata feature. It does not generate a plan, modify the saved prompt, start a turn, execute a next action, validate a reference or run a reviewer. An explicit `loop.prompt_goal: true` setting can include the latest specification in automatic messages; see below. Completion declarations, verification results and review stages are not yet bound to goal revisions. `complete` and `resume` keep their existing behavior and do not change the goal record. Do not treat an old completion hold as acceptance of a newly revised goal.

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

`state_dir/goal.json` is a separate private record (0600 on Unix, inside the private runtime directory), written by atomic replacement using the existing control writer. This does not promise power-loss durability or filesystem access isolation from an operator who can edit runtime files. Missing records are compatible with existing loops; corrupt, unsupported-version or non-regular records, including symlinks, refuse edits. Status reports a generic goal error without printing raw input. With the default `prompt_goal: false`, missing or invalid goal metadata does not change tick/control behavior. Opted-in messages require a valid goal for this loop before sending; stop and existing holds still win.

Input must be one JSON object, at most 32 KiB, with a nonblank title (up to 256 bytes) and nonempty `objectiveRefs` and `acceptanceRefs` arrays. Unknown fields and trailing JSON are rejected. Each reference array has at most 32 entries, each nonblank and at most 2,048 bytes. Optional `nextAction` needs a nonblank summary of at most 1,024 bytes. The stored history is capped at 128 revisions and 1 MiB. Reaching either cap refuses the edit without discarding history; archival/replacement workflows are not implemented.

Offline storage and command fixtures cover reload, competing edits, ownership, holds, history limits, malformed records, private output, lock contention and zero provider calls. No live goal adoption or provider walkthrough has been performed.

## Current goal context in wake messages

With v0.1.4, set `loop.prompt_goal: true` to explicitly share the current private goal specification with the agent on each wake, nudge, recovery re-prompt, handoff and switch-back. In v0.2.0 a BB recovery made by [retrying the failed turn](operations.md#recovering-a-failed-bb-turn-by-retry) sends no text, so it carries no goal. This sends the title, all current reference lists and next-action metadata, plus the goal ID/revision. Prior revision contents are excluded. The default is false, so existing loops never begin sharing private goals automatically. Goal create/revise/show still send nothing themselves.

```yaml
loop:
  # Other required loop fields are omitted here.
  prompt_file: wake-policy.md
  prompt_goal: true
```

Keep `wake-policy.md` focused on stable operating instructions. For example: “Read the current authorized roadmap and evidence before selecting the highest-priority unfinished work. Honor newer user steering.” Store the task specification once in the goal record and revise it with the expected ID/revision. Avoid copying a temporary milestone or “start with X” into the scaffold. A saved next action can also become outdated: the agent must check the current sources. Referenced files are not embedded, read or verified by keepgoing; relative references have no automatic base resolution, so use unambiguous references the agent can access.

The renderer reloads the latest saved revision when composing each message. Cooperative goal edits use the same tick lock as production sends. A custom handoff file replaces the ordinary scaffold but still receives the opted-in current goal context. This transports descriptive metadata, not authority to widen scope, proof of acceptance, a planner or automatic goal revision. No goal text is added to keepgoing's reports or incident history, but the receiving agent/provider may retain messages. Headless runner request artifacts may also contain the sent prompt under their existing private storage behavior.

Missing, corrupt or wrong-loop goal records refuse the send before reserving its send allowance; no stale cached goal is substituted. Normal status observations or probes may already have occurred. A dry-run previews decisions without validating/composing the goal message. Stop, pause, disabled, awaiting-user and completed controls keep their existing precedence. This does not update already-running turns or queued messages, fence external writers that ignore locks, detect semantic drift in a roadmap, or bind completion to the goal revision.

Offline fixtures cover revised goals across ticks, opt-in privacy, legacy behavior, unavailable records, handoff variants, holds, and logs/reports without private goal text. Live provider adoption remains unverified. In v0.1.5, `init --with-goal` and the wizard choice can create revision 1 and enable this transport during disabled setup; see [goal setup](setup.md#optional-goal-during-setup). The rebuilt BB setup UI exposes this choice and displays only recorded identity/revision in ordinary diagnostics. Live setup verification and revision-bound results remain unfinished.


## Recorded send provenance

Available in v0.1.5. Ordinary `keepgoing status` and `status --json` show the latest **recorded automatic send call** independently of the current goal record. `promptSend.last` includes the goal ID/revision composed into that message, the target alias, action (`wake`, `nudge`, `reprompt`, `handoff` or `switch_back`), requested delivery mode, and the tick's check timestamp. The timestamp is not an exact send or provider-receipt time. A message sent without `prompt_goal` has no goal ID/revision, even if a goal record exists.

- `adapter_returned`: the adapter returned without error. A queued message may still be waiting; a process adapter may only have started a command. This does not establish receipt, agent adoption, execution or acceptance.
- `adapter_error`: the adapter returned an error, including an unsupported operation. Delivery remains uncertain; a failure does not prove that no side effect occurred. Raw error text is excluded from this record.
- `availability: unavailable`: no supported valid record was retained. This includes older binaries and states without a captured send; it does not prove that nothing was sent.

`currentGoalComparison` compares only the saved ID/revision to the current goal metadata: `same_revision`, `different_revision`, `different_goal`, `not_included` or `unavailable`. It is not a semantic drift detector. Revising a goal does not rewrite already queued prompts or their evidence. Current goal and state are independent reads without a lock; the comparison is advisory, not an authorization to resume or replay anything.

The bounded latest-call record lives in `state.json`, saved after the tick's action path. A crash or failed state write can lose newer attempt evidence and leave an older record. Only the last returned call survives when one tick tries multiple candidates; earlier attempts are not a complete history. Dry runs, failed composition/reservation and checks without sends do not invent or refresh a record. Holds preserve it across later checks and restarts. Primary transfer clears it along with old thread observations; other target aliases refer to the configuration at that historical check, not verified current session identity. No prompt text, source path, reference contents or raw adapter error is added to this metadata.

Offline fixtures cover goal changes across fresh ticks, failed and unsupported sends, queued revision history, handoff/switch-back composition, transfer reset, legacy/malformed state, status comparison and privacy. This does not add source-file revision tracking, durable pre-send intents, a full attempt ledger, confirmed delivery, incident-timeline or registered-client projection, or live provider verification. These remain separate F2 acceptance work.
