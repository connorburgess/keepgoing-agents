# Run and conversation discovery (planned)

Keepgoing should answer “what is running, where is its conversation, and which run is blocking it?” through durable local references. Users should be able to follow those references immediately in CLI/JSON output, without reconstructing a scheduled run from timestamps, Git history or directory searches. The optional menu and BB plugin should consume the same contract.

This is a proposed data and navigation model. Only the [identity and run-boundary contract](#identity-and-run-boundary-contract-d1-internal-unreleased) below exists in source, as an internal package with no runtime callers. No graph commands, run index, provider-link resolver or blocker lookup described here are implemented. The [D1–D6 roadmap criteria](roadmap.md#priority-run-and-conversation-discoverability-planned) track delivery; D2–D6 remain unchecked. D1–D3 are the next new feature after the concrete Windows control-file replacement fix; owner-only/live reliability checks do not gate them, and new provider expansion must not delay them. The already-open ACP work may finish. The plan creates no scheduler, agent orchestrator, new dashboard, graph database or server.

## Routine, run and chat are different nodes

A routine is a useful top-level view when an external scheduler supplies its identity. A keepgoing loop can exist without a routine. Each execution run links to the loop and goal revision it belongs to, and to its provider chats/sessions. A run may use several conversations; successive runs may resume the same conversation. These are recorded relationships, not BB parent/child ownership and not permission to create or re-parent threads.

The hierarchy is a convenient default view of a graph: routine → runs → chats. Shared chats and blocked-by edges prevent a strict tree from expressing every valid relationship. A missing external routine or provider chat ID remains unavailable, rather than being invented from a timestamp or process ID.

## Identity and run-boundary contract (D1, internal, unreleased)

`internal/rundiscovery` defines the identities below, what opens or closes a run, and which relationships a run may record. It is a pure package: values in, values out. Nothing in keepgoing calls it yet, so no record is written, no command shows a run and no provider is queried. IDs and timestamps are supplied by the caller; the package performs no IO, generates no IDs and never authorizes a send or control change. Its table tests establish the rules on this page for in-memory values only. Storage, indexes and real restart behavior are D2 and D5.

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

### What the contract leaves to later items

The package keeps no history. Callers must prevent reuse of discarded run and attempt IDs, verify provenance and target bindings, check current holds and authorization, and serialize and persist each transition, with intent persisted before a send. Registry mapping, enrollment of existing loops, atomic completion/reopen records, superseded-run archives, invocation deduplication, late receipts for superseded runs and recoverable attempt receipts are D2 work. Which target is current after a transfer is D5. Restrictive controls must stay writable when discovery records are unavailable; the runtime integration must settle that transaction boundary before claiming durable identity.

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
