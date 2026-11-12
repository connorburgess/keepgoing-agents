# Run and conversation discovery (planned)

Keepgoing should answer “what is running, where is its conversation, and which run is blocking it?” through durable local references. Users should be able to follow those references immediately in CLI/JSON output, without reconstructing a scheduled run from timestamps, Git history or directory searches. The optional menu and BB plugin should consume the same contract.

This is a data and navigation model in progress. Two parts exist in source and are unreleased: the [identity and run-boundary contract](#identity-and-run-boundary-contract-d1-internal-unreleased) and the [persisted reference graph](#persisted-reference-graph-d2-unreleased) that records it in each loop's state directory. No command reads the graph yet: there are no graph commands, provider-link resolver or blocker lookup. The [D1–D6 roadmap criteria](roadmap.md#priority-run-and-conversation-discoverability-planned) track delivery; D1 is checked, D2 is partial and D3–D6 are planned. The plan creates no scheduler, agent orchestrator, new dashboard, graph database or server.

## Routine, run and chat are different nodes

A routine is a useful top-level view when an external scheduler supplies its identity. A keepgoing loop can exist without a routine. Each execution run links to the loop and goal revision it belongs to, and to its provider chats/sessions. A run may use several conversations; successive runs may resume the same conversation. These are recorded relationships, not BB parent/child ownership and not permission to create or re-parent threads.

The hierarchy is a convenient default view of a graph: routine → runs → chats. Shared chats and blocked-by edges prevent a strict tree from expressing every valid relationship. A missing external routine or provider chat ID remains unavailable, rather than being invented from a timestamp or process ID.

## Identity and run-boundary contract (D1, internal, unreleased)

`internal/rundiscovery` defines the identities below, what opens or closes a run, and which relationships a run may record. It is a pure package: values in, values out. IDs and timestamps are supplied by the caller; the package performs no IO, generates no IDs and never authorizes a send or control change. Its only caller is the [persisted reference graph](#persisted-reference-graph-d2-unreleased), which validates every stored record against it. The D1 criterion that restart preserves the run reference is verified there: a run recorded by one process is still the current run for later separate processes, including a daemon started after the previous one was killed. Which target is current after a transfer remains D5.

A run is one opened episode of supervision for a loop. It can span many checks, wakes, provider turns, holds, goal revisions and target changes. A loop has at most one current run. Only an explicit completion closes it, recorded as **completion declared**, never verified success. Only an explicit resume after completion opens the next run, which keeps a predecessor reference when one is known. A run can be open while supervision is disabled or held; `open` is not a claim of activity. Removing a loop's enrollment is not defined yet.

| Identity | Form | Meaning and boundary |
| --- | --- | --- |
| External routine and invocation (optional) | Source, routine and optional invocation tokens supplied by a structured integration | Names the scheduler entry and one trigger. Never derived from a timestamp. An invocation ID is a reference on a run, not the run's ID. |
| Registered loop reference | Existing registry `loop_` plus 32 hex digits | A removable reference to a config/runtime binding. Re-registering creates a new one. It maps to a durable loop only through an explicit, caller-verified record. |
| Durable loop | `supervision_` plus 32 hex digits | Enrollment identity that owns runs. Distinct from the registry reference, loop name and runtime path. Nothing allocates it yet. |
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

## Persisted reference graph (D2, unreleased)

Each loop's state directory holds one private record, `graph.json`, written by `internal/rungraph`. It stores the durable loop identity, runs, send attempts and the links from a run to provider chats/sessions, goal revisions and routine invocations. It is diagnostic history only. No decision, hold, allowance or send reads it, and no command prints it yet; D3 adds the CLI/JSON views.

### Where identity lives

| Identity | Where it lives | How it is created |
| --- | --- | --- |
| Durable loop `supervision_…` | `graph.json` in the loop's state directory | Allocated by the first graph write for that directory. It follows the directory, not the config path, loop name or registry. |
| Registry reference `loop_…` | The registry; the graph keeps a list of references it has seen used | Recorded when a control operation reaches the loop through that reference (registered client pause, stop or resume). |
| Run `run_…`, attempt `attempt_…` | `graph.json` | Allocated when a run opens or a send intent is recorded. |

A registry reference maps to a durable loop through its entry's runtime directory: the durable loop is the one recorded in that directory's `graph.json`. The mapping is **recorded** only once the graph lists that reference. `registry add` and `registry remove` never write into a runtime directory, so a reference that has only been added, or one created by removing and adding again, resolves to the directory's loop as **not recorded**. A reader must show that state instead of presenting the loop's earlier runs as belonging to the new reference. The graph's list says a reference was used then, not that it is still registered; the registry answers that. The list keeps the newest 16.

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
| Run → chat/session | A check's target is a BB thread (its configured thread ID) or a headless Claude, Codex or Cursor runner with a saved UUID-shaped resume ID | `configured_target` or `runner_session_file` |
| Run → routine invocation | Store API only. No command accepts a routine identity yet. | `external` |
| Registry reference | A registered client control operation succeeds | `registered_control` |

Run boundaries are derived from saved control state, so they converge. If the write after a control command fails, the next check records the same boundary with the `control_state` source. An attempt left as `intent` means the process stopped, or the graph write failed, between recording the intent and recording the return. It is unconfirmed, not proof that anything was or was not sent. A session link carries the configured target alias it was recorded for. It is history: neither a session link nor the graph says which target is active now. Target moves are recorded once, in the incident journal read by `keepgoing timeline` ([diagnostics](diagnostics.md)); the graph does not duplicate them. A process-adapter target has no recorded session identity. Dry runs record nothing.

### Lookup

`Lookup` takes an exact reference and returns that node with its adjacent recorded references, from an index built once per loaded graph. It reads nothing else: no history scan, provider query, filesystem search or timestamp comparison. References are the run, attempt, durable loop and registry IDs themselves, `session:<provider>:<id>` for a chat/session, `<goal id>@<revision>` for a goal revision and `routine:<source>:<routine>:<invocation>` for an invocation. Every node and edge carries its source label and recorded time; that is the only freshness the graph has, and nothing in it is a live observation.

The paths D2 requires resolve from recorded IDs: loop → current run → chat/session, run → goal revision, run → attempts, run → predecessor, chat/session → every run that recorded it, and registry reference → loop.

### Evidence that is not available

| State | Meaning | Behavior |
| --- | --- | --- |
| `missing` | No graph, including every loop last run by an older build | Reads return nothing. The next change creates the record. |
| `unsupported` | Written by a newer schema | Not read and never rewritten. |
| `corrupt` | Unreadable, oversized, not a regular file, or failing validation against the contract | Not read and never rewritten or reset. Remove the file to start a new record; the earlier history is gone. |
| `not_recorded` | The reference is not in the graph | Nothing is inferred. |
| `not_recorded_or_expired` | The reference is not in the graph and retention has dropped records | Dropped records are counted, never recreated. |
| Conflict `completion_not_recorded` or `reopen_not_recorded` | Saved control state and the recorded run disagree because a boundary write was lost | Reported to the reader; the next check records the boundary. |

### Storage and failure behavior

- **Private and atomic.** `graph.json` is written with mode 0600 through the control writer's temporary-file replacement, which on Windows replaces a file that a reader still has open. A separate `graph.lock` serializes writers; it is independent of the tick and control locks.
- **Bounded.** At most 32 runs, 128 attempts, 256 links, 16 registry references and 256 KiB. The oldest closed run is dropped first, together with its attempts and links. Within one long run the oldest attempts and links go.
- **Versioned.** `schemaVersion` is 1. Unknown fields are rejected.
- **Never in the way.** A graph that cannot be read or written does not fail a check, change its decision, block a send or fail a control command. The check adds one warning, `run reference record unavailable; discovery evidence is incomplete`. Control commands stay silent.
- **No private text.** The record holds IDs, target aliases, action names and times. It holds no prompt, goal text, note, path, URL or error text.

Older builds ignore the file. Copying or deleting runtime files is outside these guarantees.

## Record once, resolve by reference

D2 should define versioned node and relationship types with stable local IDs, source identity, observation time and freshness. The minimum useful paths are loop → current run → provider chat/session, run → goal revision, and blocked run → blocking run. Routine → run is available when the caller or integration supplies a verifiable routine identity. Registration identity and durable loop identity need an explicit mapping; removing and re-adding today's registry entry must not silently claim the same run history.

Record start intent before launching work, then attach structured provider receipts and verified transition evidence as they arrive. A crash between intent and receipt must leave an explicit pending or unconfirmed attempt, not proof that a chat exists or work ran. Neither a returned send nor a saved session ID proves successful execution or resumability. Do not read prompts or transcripts to populate the graph.

An indexed local resolver should find an ID and its recorded adjacent references directly. Lookup must not require scanning all history, Git commits, task files or provider conversation directories. This is an access-pattern requirement, not a latency or literal constant-time promise. Bound records, traversal and retention; define migrations, interrupted updates, missing nodes, unsupported schema versions, cycles and conflicting identities. Unknown remains explicit, and expired history is not silently recreated.

D3 should deliver CLI/JSON views for active and recent runs and direct reference traversal before GUI work. A user should be able to follow current run → chat even with duplicate loop names. D3 reports blocker navigation as unavailable until D4 supplies a validated relationship; blocked run → blocker → chat is verified with D4, not a prerequisite for checking off D3. CLI names are intentionally undecided here. Current commands and output schemas remain those documented in [local clients](local-clients.md) and [operations](operations.md).

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
| Read-only native menu preview | A view of registered status | Current-run/chat traversal or open-thread support |

D4–D6 must preserve these evidence boundaries. Local registration remains explicit; graph records live outside repositories. Separate identifiers and safe display labels from private task text, paths, credentials and URLs containing sensitive components. Private display is opt-in, and graph relationships or provider links must not silently extend webhook/report payloads or introduce central analytics. A future client uses the same versioned resolver; it must not infer more certainty than CLI output provides.

Each implementation PR must review README, setup, operations, local-client and integration claims as applicable. Record what is implemented, released, fixture-tested and live-verified separately. Test restart, interrupted transition, duplicate names, shared sessions, dangling references, corrupt records, stale blockers and privacy with fictional identities first; provider link behavior and UI accessibility need their own evidence. Documentation alone closes none of these criteria.

## README artwork publication gate

The approved diagram direction remains private until the feature is implemented. Publish the artwork with the feature only when the routine/run/chat, shared-session and blocked-by relationships it depicts have matching implementation and acceptance evidence. If a relationship remains unimplemented, omit it from published artwork. Review the final README alongside the command/schema contract, fixture results, release availability and remaining live-verification gaps. Visual approval is not capability verification, and this planning change publishes no diagram.
