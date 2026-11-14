# Run and conversation discovery (planned)

Keepgoing should answer “what is running, where is its conversation, and which run is blocking it?” through durable local references. Users should be able to follow those references immediately in CLI/JSON output, without reconstructing a scheduled run from timestamps, Git history or directory searches. The optional menu and BB plugin should consume the same contract.

This is a data and navigation model in progress. Three parts exist in source and are in v0.2.0: the [identity and run-boundary contract](#identity-and-run-boundary-contract-d1-internal), the [persisted reference graph](#persisted-reference-graph-d2) that records it in each loop's state directory, and the read-only [`keepgoing runs` command](#cli-navigation-d3) that shows it. It also shows [what a loop is waiting on](#what-a-loop-is-waiting-on-d4-partial): child conversations and the owning conversation. There is no provider-link resolver, lock-holder lookup or GUI navigation. The [D1–D6 roadmap criteria](roadmap.md#priority-run-and-conversation-discoverability-planned) track delivery; D1 and D3 are checked, D2 and D4 are partial, and D5–D6 are planned. D3 has one [live check](#live-check) on a BB target; the rest of its evidence is from fixtures. The plan creates no scheduler, agent orchestrator, new dashboard, graph database or server.

## Routine, run and chat are different nodes

A routine is a useful top-level view when an external scheduler supplies its identity. A keepgoing loop can exist without a routine. Each execution run links to the loop and goal revision it belongs to, and to its provider chats/sessions. A run may use several conversations; successive runs may resume the same conversation. These are recorded relationships, not BB parent/child ownership and not permission to create or re-parent threads.

The hierarchy is a convenient default view of a graph: routine → runs → chats. Shared chats and blocked-by edges prevent a strict tree from expressing every valid relationship. A missing external routine or provider chat ID remains unavailable, rather than being invented from a timestamp or process ID.

<a id="identity-and-run-boundary-contract-d1-internal-unreleased"></a>

## Identity and run-boundary contract (D1, internal)

`internal/rundiscovery` defines the identities below, what opens or closes a run, and which relationships a run may record. It is a pure package: values in, values out. IDs and timestamps are supplied by the caller; the package performs no IO, generates no IDs and never authorizes a send or control change. Its only caller is the [persisted reference graph](#persisted-reference-graph-d2), which validates every stored record against it. The D1 criterion that restart preserves the run reference is verified there: a run recorded by one process is still the current run for later separate processes, including a daemon started after the previous one was killed. Which target is current after a transfer remains D5.

A run is one opened episode of supervision for a loop. It can span many checks, wakes, provider turns, holds, goal revisions and target changes. A loop has at most one current run. Only an explicit completion closes it, recorded as **completion declared**, never verified success. Only an explicit resume after completion opens the next run, which keeps a predecessor reference when one is known. A run can be open while supervision is disabled or held; `open` is not a claim of activity. Removing a loop's enrollment is not defined yet.

| Identity | Form | Meaning and boundary |
| --- | --- | --- |
| External routine and invocation (optional) | Source, routine and optional invocation tokens supplied by a structured integration | Names the scheduler entry and one trigger. Never derived from a timestamp. An invocation ID is a reference on a run, not the run's ID. |
| Registered loop reference | Existing registry `loop_` plus 32 hex digits | A removable reference to a config/runtime binding. Re-registering creates a new one. It maps to a durable loop only through an explicit, caller-verified record. |
| Durable loop | `supervision_` plus 32 hex digits | Enrollment identity that owns runs. Distinct from the registry reference, loop name and runtime path. Allocated by the first graph write for a state directory; see [where identity lives](#where-identity-lives). |
| Goal and revision | Existing `goal_` ID and revision 1–128 | A run records each revision in effect; an attempt carries a revision only when it was composed into that prompt. |
| Execution run | `run_` plus 32 hex digits | One supervision episode. Never a daemon process, PID, poll, scheduled tick, provider turn or wake. |
| Attempt | `attempt_` plus 32 hex digits | One adapter-send intent in one run: a wake, re-prompt, handoff or switch-back, or a queued nudge that may not start a provider turn. A daemon process is not an identity. |
| Provider chat/session | Provider token plus the provider's opaque ID | A recorded reference only. It has no parent, owner or URL, and does not say the conversation exists or can be resumed or opened. |

External tokens are 1–128 characters from letters, digits and `. _ : @ -`, starting with a letter or digit. Anything else, including a URL, is refused and stays unavailable; it is not repaired or truncated.

### Run boundaries

| Case | Effect on the run | Recorded alongside |
| --- | --- | --- |
| Enroll | None. Records only the durable loop identity; a different identity is refused. | |
| First enable or start | Opens a run if none exists and completion has not been declared. | The opening routine invocation, when the operation carries one |
| Repeated enable or start | Keeps the run and its recorded time. Does not clear completion or any hold. | |
| Scheduled trigger | Never opens, reopens or replaces a run. A trigger opens a run only by arriving as the explicit start or resume above. | An invocation link, only while a run is open. With no run or after completion it relates to no run. |
| Check, repeated wake or nudge | Keeps the run. | Each send is a new attempt in the same run |
| Retry | Keeps the run. | A new attempt; the earlier intent stays unresolved and is never replayed or merged |
| Daemon restart | Keeps the run for the same recorded values. | The first send afterwards is a new attempt; an intent with no recorded return stays an intent |
| Pause, stop, disable or await-user | Keeps the run. Holds belong to the separate control system. | |
| Resume from a hold | Keeps the run. With no run and no completion it opens nothing. | |
| Complete | Closes the run once. With no run, records completion without inventing one. | Late adapter returns and session/goal links may still attach to the closed run |
| Resume after completion (reopen) | Opens a new run with a new ID and a predecessor reference when known. Does not enable a disabled loop. | The opening routine invocation, when supplied |
| Goal revision | Keeps the run. | A goal link per revision in effect |
| Target transfer | Keeps the run. | A session link for the destination; the source link remains. No link is marked current. |
| Legacy adoption | Opens an `adopted_existing` run only when the caller states it verified eligibility. The recorded time is not a historical start, and no routine is attached. | |
| Dry run | None. Validates the prospective transition and returns the unchanged state. | |

### Attempts and relationships

`BeginAttempt` validates an intent against the open run and refuses an attempt ID already present in the attempts the caller retains. `FinishAttempt` records only `adapter_returned` or `adapter_error`; neither means delivered or executed. A late result may attach to its original attempt after completion without reopening the run. Repeating an identical result keeps the first timestamp; a conflicting outcome is refused. A missing result remains an intent and never authorizes replay.

`Relate` validates a link from the current run to a provider chat/session, a goal revision or a routine invocation. Links are history: one run may record several chats, and the same chat may be recorded by successive runs. A session link may name the attempt whose send targeted or returned it. There is no session-to-session or run-to-parent edge, so a shared chat never implies BB thread parentage; the only run-to-run reference is the predecessor recorded by an explicit reopen. A link naming any run other than the current one is refused, including a late receipt for a run that has since been superseded.

Caller-supplied timestamps may move backwards; they are not identity or ordering authority.

### What the contract leaves to its caller

The package keeps no history. Its caller must prevent reuse of discarded run and attempt IDs, verify provenance and target bindings, check current holds and authorization, and serialize and persist each transition, with intent persisted before a send. The graph below does the storage part. Invocation deduplication, late receipts for superseded runs and the current target after a transfer are still open.

<a id="persisted-reference-graph-d2-unreleased"></a>

## Persisted reference graph (D2)

Each loop's state directory holds one private record, `graph.json`, written by `internal/rungraph`. It stores the durable loop identity, runs, send attempts and the links from a run to provider chats/sessions, goal revisions and routine invocations. It is diagnostic history only. No decision, hold, allowance or send reads it. [`keepgoing runs`](#cli-navigation-d3) prints it.

### Where identity lives

| Identity | Where it lives | How it is created |
| --- | --- | --- |
| Durable loop `supervision_…` | `graph.json` in the loop's state directory | Allocated by the first graph write for that directory. It follows the directory, not the config path, loop name or registry. |
| Registry reference `loop_…` | The registry; the graph keeps a list of references it has recorded | Recorded by `registry add` when the loop already has a graph, and when a control operation reaches the loop through that reference (registered client pause, stop or resume). |
| Routine and invocation | `graph.json`, on the run they opened or triggered | Supplied by the caller with `--routine` on `enable`, `resume` or `tick`. Never generated or inferred. |
| Run `run_…`, attempt `attempt_…` | `graph.json` | Allocated when a run opens or a send intent is recorded. |

A registry reference maps to a durable loop through its entry's runtime directory: the durable loop is the one recorded in that directory's `graph.json`. The mapping is **recorded** only once the graph lists that reference. `registry add` records it when the loop already has a graph, and writes nothing otherwise: registry commands never create a runtime file, so a loop registered before its first run resolves as **not recorded** until `registry add` is repeated after that run, or a registered control operation uses the reference. `registry add` is safe to repeat; it returns the same reference. Removing and adding again creates a new reference, recorded as its own entry with its own time; the earlier one stays as history. A reader shows the not-recorded state instead of presenting a loop's runs as belonging to a reference the graph has not recorded. The graph's list says a reference was recorded then, not that it is still registered; the registry answers that, and `registry remove` does not edit the graph. The list keeps 16. When it is full the oldest reference recorded only by `registry add` goes first, so repeated remove and add cannot push out a reference that a control operation used.

### What is recorded, and when

| Record | Written when | Source label |
| --- | --- | --- |
| Run opened, explicit | An accepted `enable` or `start` finds no run and no declared completion | `control_command` |
| Run opened, `adopted_existing` | A check finds an enabled loop with no hold, no pending transfer and no run. This is how a loop started by an older build gets its first run. | `control_state` |
| Run closed | Saved control state shows completion and the run is open | `control_command`, or `control_state` when a later check records it |
| Run opened, `reopened` | Saved control state no longer shows a completion the graph recorded | as above |
| Send attempt, `intent` | Before every automatic adapter send | `adapter_call` |
| Attempt outcome `adapter_returned` or `adapter_error` | After that adapter call returns | `adapter_call` |
| Run → goal revision | A check finds a valid `goal.json` while the run is open | `goal_record` |
| Run → chat/session | A check's target is a BB thread (its configured thread ID, or the thread keepgoing created for a handoff to a `bb.new_thread` candidate) or a headless Claude, Codex or Cursor runner with a saved UUID-shaped resume ID | `configured_target`, `created_thread` or `runner_session_file` |
| Run's opening invocation | An accepted `enable` or `resume` that opens a run carries `--routine` | the run's own source |
| Run → routine invocation (trigger) | `tick`, `enable` or `resume` carries `--routine` while a run is already open | `external` |
| Registry reference | `registry add` finds an existing graph | `registry_add` |
| Registry reference | A registered client control operation succeeds; this also upgrades a reference first recorded by an add | `registered_control` |
| Waiting record | A check observes a BB target: the verified listing of its direct child threads, by key, replacing the previous one. Cleared when the run closes. | `child_listing` |
| Unrecorded note | A check's target has a chat/session identifier outside the recordable characters, such as one containing `/` or a space | none; kept once per run and target |

Run boundaries are derived from saved control state, read inside the graph lock, so a writer never applies control state older than the graph it updates. If the write after a control command fails, the next check records the same boundary with the `control_state` source. This recovers the state, not every event: if a completion and the resume after it both lose their graph writes before any check runs, control looks unchanged and neither boundary is recorded, so that work stays in the earlier run. An attempt left as `intent` means the process stopped, or the graph write failed, between recording the intent and recording the return. It is unconfirmed, not proof that anything was or was not sent. A session link carries the configured target alias it was recorded for. It is history: neither a session link nor the graph says which target is active now. Target moves (handoff, switch-back and manual retarget) are recorded once, in the incident journal read by [`keepgoing timeline`](diagnostics.md#offline-incident-timeline); the graph does not duplicate them. A process-adapter target has no recorded session identity. An identifier the contract cannot store is not altered to fit: the run gets an unrecorded note naming the target alias, the check warns once (`target "<alias>": its chat/session identifier cannot be recorded…`), and later checks stay quiet. The rejected identifier is not stored. Dry runs record nothing.

### Supplying a scheduler identity

A scheduler that calls keepgoing can say which entry and which trigger it is:

```sh
keepgoing tick   -c loop.yaml --routine example-scheduler/nightly/2026-11-13
keepgoing enable -c loop.yaml --routine example-scheduler/nightly/2026-11-13
keepgoing resume -c loop.yaml --routine example-scheduler/nightly/2026-11-14
```

A BB script automation that runs `keepgoing tick` should pass `--routine bb-automation/<automation id>`: `status`, `doctor`, the panel and the `check_gap` report then name that automation when checks stop. The check also records the identity it was started with in `state.json` (`startedBy`), replaced at every check.

The value is `source/routine` or `source/routine/invocation`. Each part is 1–128 letters, digits and `. _ : @ -`, starting with a letter or digit. A timestamp with `+00:00`, a name with a space or an ID containing `/` does not fit; pass an identifier that does. `start`, `daemon` and the other commands do not take the flag.

A value that cannot be recorded, including an empty one, is handled by what the command is for:

- **`tick` runs the check anyway.** The identity is diagnostic only, so a scheduler sending an unusable value must not stop supervision. The check behaves exactly as it would without the flag, records no identity, and adds one warning to its output and its `runs.jsonl` event: `routine identity ignored: …`. The value is not echoed.
- **`enable` and `resume` refuse with a usage error** that changes nothing and does not echo the value. These are explicit control operations, usually run by a person or a setup script, where a wrong identity should be corrected.

The identity is recorded and nothing more. It never reaches a decision, and a trigger never opens, reopens or closes a run:

- `enable` or `resume` that opens a run records the invocation on that run.
- `tick`, `enable` or `resume` while a run is already open records a trigger link on it. A repeated invocation is one link. A routine that supplies no invocation ID has one link per run, because its triggers cannot be told apart.
- With no open run, including after completion and on a disabled or held loop with no run, nothing is recorded. Dry runs record nothing.

Triggers have their own bound. A run keeps its first trigger and its newest seven; the ones between are dropped and counted as `triggers`. A scheduler that sends a new ID on every check therefore cannot push out a chat/session link, a goal link, a send or an earlier run. If chat and goal links alone fill the shared 256-link bound, trigger links are removed first.

### Lookup

`Lookup` takes an exact reference and returns that node with its adjacent recorded references, from an index built once per loaded graph. It reads nothing else: no history scan, provider query, filesystem search or timestamp comparison. References are the run, attempt, durable loop and registry IDs themselves, `session:<provider>/<id>` for a chat/session, `<goal id>@<revision>` for a goal revision, `routine:<source>/<routine>` for a routine and `routine:<source>/<routine>/<invocation>` for one invocation. An invocation reference always has three parts, ending in `/` when no invocation ID was supplied, so it never equals a routine reference. The parts are joined with `/`, which no recorded token can contain, so different identities never share a reference. Every node and edge carries its source label and recorded time; that is the only freshness the graph has, and nothing in it is a live observation.

The paths D2 requires resolve from recorded IDs: loop → current run → chat/session, run → goal revision, run → attempts, run → predecessor, chat/session → every run that recorded it, registry reference → loop, and routine → its invocations and runs → their chats.

### Evidence that is not available

| State | Meaning | Behavior |
| --- | --- | --- |
| `missing` | No graph, including every loop last run by an older build | Reads return nothing. The next change creates the record. |
| `unsupported` | Written by a newer schema | Not read and never rewritten. |
| `corrupt` | Unreadable, oversized, not a regular file, or failing validation against the contract | Not read and never rewritten or reset. Remove the file to start a new record; the earlier history is gone. |
| `not_recorded` | The reference is not in the graph | Nothing is inferred. |
| `not_recorded_or_expired` | The reference is not in the graph and retention has dropped runs, attempts, links, triggers, registry references or unrecorded notes | Dropped records are counted per kind, never recreated. |
| Unrecorded note on a run | A chat/session identifier existed but could not be stored | The run lists the target alias and reason; no session link exists for it. |
| Conflict `completion_not_recorded` or `reopen_not_recorded` | Saved control state and the recorded run disagree because a boundary write was lost | Reported to the reader; the next check records the boundary. |

### Storage and failure behavior

- **Private and atomic.** `graph.json` is written with mode 0600 through the control writer's temporary-file replacement, which on Windows replaces a file that a reader still has open. The temporary file is flushed to disk before it replaces the record, so a power loss leaves either the old record or the new one, not a truncated file. The directory entry is not flushed, so the newest write can still be lost. A separate `graph.lock` serializes writers; it is independent of the tick and control locks.
- **One write per observing check.** A check that observes a BB target rewrites the record once, to stamp the child listing with that check's time. Other checks write only when something changed.
- **Bounded.** At most 32 runs, 128 attempts, 256 links, 8 trigger links per run, 200 children in the waiting record, 16 registry references, 16 unrecorded notes and 256 KiB. The oldest closed run is dropped first, together with its attempts and links. Within one long run the oldest attempts and links go.
- **Versioned.** `schemaVersion` is 2, which adds the waiting record. A version 1 file is read as it is and upgraded by its next write. A build that only knows version 1 reports a version 2 file as unsupported and leaves it alone. Unknown fields are rejected.
- **Never in the way.** A graph that cannot be read or written does not fail a check, change its decision, block a send or fail a control command. The check adds one warning, `run reference record unavailable; discovery evidence is incomplete`, to its output and its `runs.jsonl` event. Control commands stay silent. A corrupt record keeps warning on every check until the file is removed by hand, because it is never reset automatically.
- **No private text.** The record holds IDs, target aliases, action names and times. It holds no prompt, goal text, note, path, URL or error text.

Older builds ignore the file. Copying or deleting runtime files is outside these guarantees.

<a id="cli-navigation-d3-unreleased"></a>

## CLI navigation (D3)

`keepgoing runs` shows what the graph recorded: the current run, earlier runs, their sends and the chat/session references they used. It is available in source builds only. It reads private files and nothing else:

- It runs no adapter, provider command or `bb` command, and needs no provider installed.
- It takes no lock, including the tick lock, so it works while a check, a daemon or another command is running.
- It creates no file or directory and changes no control. It works the same for a paused, stopped, disabled, awaiting-user or completed loop.
- Each file is read as its own snapshot. A check that finishes during a read can make the graph, control and saved target differ by one step.

Everything it prints was recorded earlier. Nothing is a live observation, and a session reference does not show that the conversation exists or can be resumed.

### Selecting a loop

```sh
keepgoing runs -c loop.yaml                       # one loop, by its configuration
keepgoing runs --state-dir /private/state         # one loop, without loading configuration
keepgoing runs --registry-id loop_0123456789abcdef0123456789abcdef
keepgoing runs --all                              # every registered loop with its current run
```

Use exactly one selector; the default is `-c looptender.yaml`, or `keepgoing.yaml` when only that is in the working directory ([configuration](configuration.md)). `--directory` names a private registry directory for `--registry-id` and `--all`. Two registered loops can share a name: their registry references keep them apart, and each shows only its own records. With a registry selector the report also says whether the graph has recorded that reference (`recorded`) or not (`not_recorded`); see [where identity lives](#where-identity-lives).

A registry selector checks the binding first, as registered status does: the registered configuration must still load and still name the same loop and runtime directory. If it does not, the entry shows `binding: unavailable`, nothing is read from the registered directory, and the command exits 1. A lookup through such an entry answers `unavailable`.

Add `--json` for the versioned report, `--limit N` (1–256, default 10) and `--stale-after D` (default 30m) for how old the last check's child listing may be. The limit bounds earlier runs, each run's chat/session references and goal revisions, a reference's links, a chat's sends, the children a loop is waiting on and registered loops. Omitted counts are always printed.

### Following a reference

```sh
keepgoing runs -c loop.yaml --id run_0123456789abcdef0123456789abcdef
keepgoing runs -c loop.yaml --id session:bb/thr_example
```

`--id` takes one exact reference from earlier output and prints that node with every reference it links to. Following them in turn walks registry reference → durable loop → runs → sends → chat/session, and back from a chat/session to every run that recorded it and the sends made to it. References are matched exactly. A loop name, a bare thread ID or a partial ID is not searched for; it reports `not_recorded`. `--id` accepts only the characters a recorded reference can contain (letters, digits and `. _ : @ / -`); anything else is a usage error. Text mode prints the same links as JSON, including each send's run and chat/session, so every path below can be followed without `--json`.

| Reference | Links shown |
| --- | --- |
| `loop_…` (registry reference) | the durable loop |
| `supervision_…` (durable loop) | current run, earlier runs, registry references |
| `run_…` | sends, chats/sessions with their target alias, goal revisions, predecessor run, opening invocation |
| `attempt_…` | its run, the goal revision composed into it, and its chat/session |
| `session:<provider>/<id>` | every run that recorded it, and the sends recorded for it |
| `<goal id>@<revision>` | every run that recorded it |
| `routine:<source>/<routine>` | its invocations, and every run they opened or triggered |
| `routine:<source>/<routine>/<invocation>` | the run it opened or triggered, and its routine |
| `child:<key>` | the run it held (`blocks`), or the run that lists it (`child`); `unavailable` when the listing is not current |

A send's chat/session is derived from two recorded fields: the send's run and its target alias. If that alias named more than one chat during the run, for example because the configured chat was edited, the send's chat is `ambiguous` and no chat is picked. Each of those chats then reports how many sends could not be attributed (`ambiguousAttempts`).

### Evidence labels

| Label | Meaning |
| --- | --- |
| `configured` | The open run's link for the saved active target, and still the chat that target's configuration names. Configuration, not an observation. |
| `observed` | The open run's link for the saved active target, and still the resume ID in the headless runner's saved file. Not a live session check. |
| `historical` | Everything else that was recorded: a closed run, a target that is not the saved active target, a chat that configuration or the runner file no longer names, or any chat when no active target is saved. Never presented as current. |
| `unavailable` | Nothing recorded: no session for the run, an identifier that could not be stored, or a missing provider link. |

With `--state-dir` no configuration is loaded, so a configured chat cannot be compared with it. It is then labelled `configured` only when it is the single chat recorded for that target in the run; if the target has several, all are `historical`.

A record file that does not exist is reported as `not_recorded`, never as the default it would be read as. With no `control.json` the control state is not printed as `disabled`, and with no `state.json` no active target is printed as `primary`.

The saved active target is the alias the last check wrote to `state.json`. Target moves are shown by [`keepgoing timeline`](diagnostics.md#offline-incident-timeline). Every session's `link` is `unavailable`: no provider link format is validated yet. What a loop is waiting on is described [below](#what-a-loop-is-waiting-on-d4-partial).

A lookup answers `found`, `not_recorded`, `not_recorded_or_expired` when retention has dropped records, or `unavailable` when the graph exists but cannot be read (corrupt or a newer schema): the reference may be in it, so it is not reported as unrecorded. A graph that is `missing` is an ordinary answer for a loop that has not recorded a run. A `conflict` line means saved control and the recorded run disagree until the next check records the boundary.

### JSON report, schema 2

| Field | Content |
| --- | --- |
| `schemaVersion`, `readAt`, `view` | `2`, the read time, and `loop`, `node` or `registry`. Schema 1 had a `blocker` that was always `unavailable`. |
| `loops[]` | One entry per selected loop |
| `loops[].registrationId`, `registration`, `binding`, `loop` | With a registry selector: the reference, its mapping state, and whether its configuration binding still holds; loop name when known |
| `loops[].graph` | `availability` (`available`, `missing`, `unsupported`, `corrupt`), `loopId`, `updatedAt`, `dropped` counts |
| `loops[].control`, `conflict`, `activeTarget` | Saved control summary, any boundary conflict, and the saved active target alias; each has `availability` `not_recorded` when its file does not exist |
| `loops[].current`, `recent[]`, `omittedRuns` | The current run and earlier runs, newest first |
| run fields | `id`, `state` (`open` or `completion_declared`), `origin`, `recordedAt`, `source`, `closedAt`, `reopens`, `invocation` (the one that opened it), `triggers[]` and `omittedTriggers` (later invocations while open), `sessionEvidence`, `sessions[]`, `omittedSessions`, `goalRevisions[]`, `omittedGoalRevisions`, `attempts`, `unrecorded[]` |
| `sessions[]` | `ref`, `provider`, `id`, `target`, `evidence`, `source`, `recordedAt`, `link` |
| `attempts` | `total`, `unconfirmed`, and the newest three in `latest[]` with `outcome` (`intent`, `adapter_returned`, `adapter_error`) and `session` |
| `loops[].blocker` | `availability` (`blocked`, `held`, `listed`, `none`, `unknown`), `reason`, `checkedAt`, `ageSeconds`, `stale`, `target`, `lastCheckCause`, `children[]` with `ref`, `status`, `holds` and `supervised` (`registrationId`, `loop`, `run`), `omittedChildren`, `owner`, and `lock` (always `unavailable`); see [what a loop is waiting on](#what-a-loop-is-waiting-on-d4-partial) |
| `node` | With `--id`: `ref`, `status`, `type`, `recordedAt`, `source`, `edges[]`, `omittedEdges`, and the run or send detail; for a chat/session, its `attempts[]`, `omittedAttempts` and `ambiguousAttempts` |
| `omitted`, `limitations[]` | Registered loops not shown, and fixed statements of what the report cannot establish |

Output holds identifiers, target aliases, action names, sources and times. It holds no prompt, goal text, control note, path or URL. Chat and session IDs are the ones `status` and registered status already show.

Exit codes: 0 for an answer, including a missing graph, a hold or a reference that is not recorded; 1 when a graph, control or state record exists but cannot be read, a registry binding no longer holds, or the registry or registration is unavailable, with whatever was readable still printed; 2 for usage or unreadable configuration.

<a id="what-a-loop-is-waiting-on-d4-partial-unreleased"></a>

### What a loop is waiting on (D4, partial)

`keepgoing runs` answers "which conversation is this loop waiting on, and is it working, stuck or failed" from structured records: the child listing a check verified, the reason that same check recorded, and the ownership claim. It reads no PID and matches no names. Showing a blocker contacts nobody and grants no control: following it only reads more records.

**Child conversations.** Each check that observes a BB target lists the target's direct child threads and verifies each one, as it always has. The graph now keeps that picture for the open run:

| Status | Meaning | Can hold the run |
| --- | --- | --- |
| `active` | The child is working | Yes. The check does not prompt the parent for its task; it can still send the one notice that other children are in error. |
| `awaiting_input` | The child is stuck on a person | Yes. The check puts the loop on an awaiting-user hold. |
| `error` | The child failed | No. It is listed because the check reported it. |

A child is said to have **held the run** only when its status can hold a parent *and* the last check recorded child work or child input as its reason. The answer is one of five:

| Answer | Meaning |
| --- | --- |
| `blocked` | The last check verified every child and recorded child work (`child_busy`) or child input (`child_input`) as its reason. |
| `held` | The loop is on an awaiting-user hold that began when a check found a child stuck on a person. Held checks do not observe, so this is the last verified listing, shown with its age. The children have not been verified since. |
| `listed` | The last check verified every child, but recorded another reason, such as a stop request. The children are shown and none is said to hold the run. |
| `none` | The last check verified every child and none could hold the run. |
| `unknown` | The evidence does not support an answer. No child is listed. |

Reasons for `unknown`:

| Reason | When |
| --- | --- |
| `child_listing_incomplete` | The check could not verify every child, or its observation failed. Children it did verify are not listed either: a partial list would read as the answer. |
| `not_refreshed_by_the_last_check` | The last recorded check is not the one that wrote the record: the loop was held for another reason, the target changed, or the graph write failed. |
| `last_check_older_than_stale_after` | The listing is from the last check, but that check is older than `--stale-after` (default 30 minutes, as for `status`). Checks may have stopped. Set it above your scheduler interval. |
| `no_child_evidence_recorded` | No check has recorded a listing for this run: a headless or process target, or no check since the run opened. |
| `last_check_not_recorded` | There is no `state.json` or it records no check. |
| `no_open_run`, `graph_unavailable`, `binding_unavailable` | There is no open run, or the records cannot be read. |

Every answer that has a listing shows the time of the check it came from and its age, and marks it `stale` past `--stale-after`. `lastCheckCause` is the last check's reason in a fixed vocabulary; the reason text itself is not printed.

A child is shown by key, `child:<key>`, a short hash of its thread ID. A child's thread ID is never written to the graph and never printed in the parent's view: the checks that already keep errored children do the same in `state.json`. `--id child:<key>` and a run's `blocked_by` links follow the same answer as the loop view: when the answer is `unknown` the child reference answers `unavailable` and the run shows no child link, and when it is `listed` or `none` the links are shown as `child`, not `blocked_by`.

**The blocking loop.** A child conversation is often supervised itself. When a registry is consulted, each child is matched against the registered loops whose configuration binding validates and whose configured BB thread has that key:

- **Exactly one match:** the report names that loop's registry reference, its name, and its open run when that run recorded the same conversation. If it has no such run, the loop is named and the run is `unavailable`. The child's conversation is not printed here. Follow it with `keepgoing runs --registry-id <reference>`, where that loop's own chat reference is shown.
- **No match:** `not_registered`. The child is still shown by key.
- **More than one match:** `ambiguous`. None is picked.

A loop that merely shares a name is never a match, and a loop is never its own blocker. A registry is consulted with `--registry-id` or `--all`, or when `--directory` is passed with `-c` or `--state-dir`; otherwise the answer is `registry_not_consulted`. A registry that cannot be read is an error, not "nothing registered".

**The owning conversation.** `control.json` records which BB thread owns a loop and its generation; control commands from another conversation are refused. `runs` reports that claim when it validates:

- With `-c` or `--state-dir` the owner is shown as `session:bb/<thread>`, to any caller, including one that is not the owner. With a registry selector it is shown as `owner:<key>`, because registered status withholds the owning thread.
- `blocksCaller` is true when the command ran from a different conversation (`BB_THREAD_ID`). The caller's identity is compared and never printed. Reading as a non-owner claims nothing.
- If the owner is itself a registered loop, that loop and its run are named by the same rule as for a child.
- An invalid claim or a pending transfer is `unknown`. No claim is `not_recorded`.

**Which thread IDs are printed.** One rule covers the target, the owner and children: a thread ID is printed in a loop's view only when it is part of that loop's own records *and* an existing view of the same loop, reached the same way, already prints it.

| Conversation | With `-c` or `--state-dir` | With a registry selector | Why |
| --- | --- | --- | --- |
| The loop's own target | Printed | Printed | `status` and registered status both print it. |
| The loop's owner | Printed | Key only | The claim is this loop's own control record. `status` prints it, and the refusal a non-owner receives names the owning thread, because a refused caller needs to know which conversation to ask. Registered status withholds it. |
| A child | Key only | Key only | It is another conversation's identity, held only as a hash, and no existing view of this loop prints it. |

So the owner line shown to a non-owner is consistent with withholding children: the caller already receives that thread ID from the command that refused it, while nothing gives it a child's.

**Not covered.** Who holds `tick.lock` or `daemon.lock` when a check is skipped is not identified: `lock` is always `unavailable`. That needs a holder record that does not exist yet. A child of a child is not followed from here; follow it from the child's own loop.

### Live check: waiting on

One live check has been run, on 2026-11-13, by the project coordinator on a real supervised BB loop whose checks come from an external scheduler. Its installed binary was upgraded to a source build of `main` at `e69e10e` on macOS arm64. No real identifiers are recorded here.

What it showed:

- **Upgrade.** The first scheduled check on the new build upgraded the existing graph and recorded the child listing. The check itself was unchanged: action none, reason child work running, exit 0.
- **The blocker.** `keepgoing runs -c <config>`, twelve seconds after that check, reported `waiting on: blocked`, with the check's time and age and the last check recorded as `child_busy`. It showed one child, by key, as active and as having held the run at that check.
- **It matched the provider.** At that moment the supervised conversation had six child threads: one working and five idle. The view listed the one working child and none of the idle ones, which matched BB's own thread list.
- **No registry.** The child's loop was reported as unavailable because no registry was consulted, as designed.
- **Owner.** The owner was shown as a chat reference with its generation, and the view said the calling conversation was not the owner and its control commands would be refused. That was true: the caller was a different conversation from the recorded owner.
- **Lock holder.** Unavailable (`lock_holder_not_identified`).
- **Following and JSON.** `--id child:<key>` answered `found` and linked `blocks` to the run. `--json` returned schema 2 with `stale` false, and contained two thread IDs in total, the supervised chat and the owner, and none for the child.

What it did not cover, and so rests on fixtures only:

- **A failed or stuck child** in this view. Later the same day a child did fail during an outage and `status` reported one child in error, but `runs` was not captured at that moment.
- **An incomplete listing** and **a stale listing**.
- **A child that is itself a registered supervised loop**, and any registry view.
- **Windows and Linux.**

### Live check

One live check has been run, on 2026-11-13, by the project coordinator against a real supervised loop. The loop's installed binary was upgraded to a source build of `main` at `0d1e967` on macOS arm64. Its active target was a BB Claude Code thread, reached earlier by a handoff from a rate-limited primary, and its checks came from an external scheduler. No real identifiers are recorded here.

What it showed:

- **Adoption on upgrade.** The first scheduled check on the new build created the graph in the existing state directory and recorded one open run with origin `adopted_existing`. The check itself was unchanged: action none, exit 0.
- **Current work and its chat.** `keepgoing runs -c <config>` showed the loop, the durable loop reference, control available (enabled), the saved active target recorded as the fallback, the current run, and one chat/session reference labelled `configured` for that target. The coordinator compared that reference with the thread's own ID: it was the conversation actually being supervised.
- **Following references.** `--id session:bb/<thread>` answered `found` and linked to the run. `--id run_<id>` answered `found` and linked to the loop. An unknown run reference did not answer `found`.
- **JSON and read-only.** `--json` returned `schemaVersion` 1 (the report schema at that build), and `control.json` was byte-identical before and after the reads.

What it did not cover, and so rests on fixtures only:

- **Sends.** None had been recorded on the new build yet, so send → chat was not walked live.
- **A headless target** and its `observed` label.
- **Two loops with one name**, and any registry view.
- **History before adoption.** The earlier handoff predates the graph, so the run shows nothing before it was adopted.
- **Windows and Linux.**

## Record once, resolve by reference

D2 should define versioned node and relationship types with stable local IDs, source identity, observation time and freshness. The minimum useful paths are loop → current run → provider chat/session, run → goal revision, and blocked run → blocking run. Routine → run is available when the caller or integration supplies a verifiable routine identity. Registration identity and durable loop identity need an explicit mapping; removing and re-adding today's registry entry must not silently claim the same run history.

Record start intent before launching work, then attach structured provider receipts and verified transition evidence as they arrive. A crash between intent and receipt must leave an explicit pending or unconfirmed attempt, not proof that a chat exists or work ran. Neither a returned send nor a saved session ID proves successful execution or resumability. Do not read prompts or transcripts to populate the graph.

An indexed local resolver should find an ID and its recorded adjacent references directly. Lookup must not require scanning all history, Git commits, task files or provider conversation directories. This is an access-pattern requirement, not a latency or literal constant-time promise. Bound records, traversal and retention; define migrations, interrupted updates, missing nodes, unsupported schema versions, cycles and conflicting identities. Unknown remains explicit, and expired history is not silently recreated.

D3 delivers CLI/JSON views for active and recent runs and direct reference traversal before GUI work; see [CLI navigation](#cli-navigation-d3). D4 adds what a loop is waiting on; see [below](#what-a-loop-is-waiting-on-d4-partial).

## Links and blockers need evidence

A provider chat/session reference is useful even when no openable URL exists. A validated link requires a supported provider link format, permitted scheme/destination and binding to the recorded target; arbitrary model text, task URLs and guessed local ports are not link authority. Missing support should show the provider and available session identity with an explicit unavailable-link reason. Opening a validated link must not invoke a shell command from stored text. Any provider refresh should be explicit, bounded and separately authorized; ordinary local traversal reads recorded evidence only.

A lock or permission blockage needs the exact responsible run only when a structured, fresh owner binding supplies it. A PID, a recently modified log or the newest chat is insufficient. The known blocker may itself be held or unavailable, and some external blockers have no run reference. Report that limitation instead of attaching an unrelated conversation. Inspecting or opening a blocker never authorizes killing it, deleting its lock, clearing its hold or taking ownership.

Restart must retain the appropriate run reference while recording a new process/turn attempt where applicable. Transfer must preserve source/destination history and make current edges reflect only a verified transition. Stale or interrupted transitions must not send a user to an old conversation labelled current. Historical links can remain explicitly historical; disappearance, provider deletion and reuse of a conflicting ID require unavailable/conflict evidence.

## Existing foundations and remaining gaps

| Existing evidence | What it establishes | What it does not establish |
| --- | --- | --- |
| Explicit registry ID | A registered config/name/runtime binding, revalidated by client reads | An execution run or permanent filesystem identity; removing and registering again creates a new ID |
| Registered status | Controls, configured target identity, goal metadata, recorded action and freshness | Live provider identity, an active/recent run graph or an exact blocker link |
| Saved headless session ID | A locally recorded resume reference when valid | Provider existence, resumability, a UI URL or successful execution |
| Last automatic prompt provenance | The latest returned send's target, goal revision and outcome | Pre-send intent, complete attempt history or confirmed provider delivery |
| Incident journal and `runs.jsonl` | Structured diagnostic observations and events | A durable run-episode ID merely because the file is called `runs.jsonl` |
| Native menu preview | A view of registered status, with pause, stop and resume after a confirmation | Current-run/chat traversal or open-thread support |
| `keepgoing runs` (v0.2.0) | Recorded runs, sends and chat/session references by exact ID; the child conversations and owner a loop is waiting on | Live session state, an openable link, who holds a lock, or which chat a send reached when its alias named several |

D4–D6 must preserve these evidence boundaries. Local registration remains explicit; graph records live outside repositories. Separate identifiers and safe display labels from private task text, paths, credentials and URLs containing sensitive components. Private display is opt-in, and graph relationships or provider links must not silently extend webhook/report payloads or introduce central analytics. A future client uses the same versioned resolver; it must not infer more certainty than CLI output provides.

Each implementation PR must review README, setup, operations, local-client and integration claims as applicable. Record what is implemented, released, fixture-tested and live-verified separately. Test restart, interrupted transition, duplicate names, shared sessions, dangling references, corrupt records, stale blockers and privacy with fictional identities first; provider link behavior and UI accessibility need their own evidence. Documentation alone closes none of these criteria.

## README artwork publication gate

The approved diagram direction remains private until the feature is implemented. Publish the artwork with the feature only when the routine/run/chat, shared-session and blocked-by relationships it depicts have matching implementation and acceptance evidence. If a relationship remains unimplemented, omit it from published artwork. Review the final README alongside the command/schema contract, fixture results, release availability and remaining live-verification gaps. Visual approval is not capability verification, and this planning change publishes no diagram.
