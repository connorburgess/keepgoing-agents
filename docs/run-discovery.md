# Run and conversation discovery (planned)

Keepgoing should answer “what is running, where is its conversation, and which run is blocking it?” through durable local references. Users should be able to follow those references immediately in CLI/JSON output, without reconstructing a scheduled run from timestamps, Git history or directory searches. The optional menu and BB plugin should consume the same contract.

This is a proposed data and navigation model. No graph commands, run index, provider-link resolver or blocker lookup described here are implemented. The [D1–D6 roadmap criteria](roadmap.md#priority-run-and-conversation-discoverability-planned) track delivery; all remain unchecked. D1–D3 are the next new feature after the concrete Windows control-file replacement fix; owner-only/live reliability checks do not gate them, and new provider expansion must not delay them. The already-open ACP work may finish. The plan creates no scheduler, agent orchestrator, new dashboard, graph database or server.

## Routine, run and chat are different nodes

A routine is a useful top-level view when an external scheduler supplies its identity. A keepgoing loop can exist without a routine. Each execution run links to the loop and goal revision it belongs to, and to its provider chats/sessions. A run may use several conversations; successive runs may resume the same conversation. These are recorded relationships, not BB parent/child ownership and not permission to create or re-parent threads.

The hierarchy is a convenient default view of a graph: routine → runs → chats. Shared chats and blocked-by edges prevent a strict tree from expressing every valid relationship. A missing external routine or provider chat ID remains unavailable, rather than being invented from a timestamp or process ID.

Before implementation, D1 must define run-episode boundaries. An execution run must not accidentally mean each daemon process, poll, provider turn or wake. The design needs a transition table for initial start, an externally identified scheduled invocation, retries, pause/resume, completion/reopen, changed goal revisions and target transfer. It must distinguish a continuing run from a new attempt after restart, and a new run from resumed conversation history. An external routine's run ID is a reference supplied by its structured integration, not necessarily the keepgoing run's ID.

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
