# Adapter capabilities

keepgoing is meant to supervise every popular coding agent, not only BB threads. This page says what each adapter can do today, how strong the evidence is, and what is missing. It documents the source on `main`; see [integrations](integrations.md) for what is in a published release.

An adapter is how the supervisor talks to one kind of agent. The decision engine is the same for all of them, but it can only act on what an adapter reports and can only do what an adapter can carry out.

## How to read the matrix

Each cell is a status and an evidence level:

| Status | Meaning |
| --- | --- |
| `yes` | Implemented. |
| `partial` | Implemented in part; the detail says which part. |
| `missing` | Not implemented. |
| `n/a` | Cannot apply to this kind of agent; the detail says why. |

| Evidence | Meaning |
| --- | --- |
| `live` | Observed at least once with the real provider. It is not a claim about every version or platform. |
| `fixture` | Tests with stand-in executables or fake adapters only. |
| `none` | Nothing exercises it. |

Antigravity has internal protocol code and no registered adapter, so a loop cannot use it and every row is `missing`.

The last column is every [agent defined in configuration](configuration.md#an-agent-defined-in-configuration). What such an agent can do depends on what its definition declares, so most of its cells are `partial` and say which declaration the behaviour needs. All of its evidence is `fixture`: no real agent has been run from a definition.

## How the matrix is kept true

The matrix below is generated from one Go table, [`internal/adapter/capability`](../internal/adapter/capability/capability.go). `go test ./internal/adapter/capability` fails when:

- `decide.Observation` gains a field the table does not list. A new decision input cannot be introduced silently.
- An adapter's source refers to an Observation field the table does not list for it, or stops referring to one it lists. An adapter cannot start reporting a new input without its column changing.
- The adapter factory builds an adapter the table does not register, or the other way round.
- An adapter gains or loses the optional activate or wake-poll interface, or a check records a session reference for a different set of adapters than the table says.
- A cell is empty, an implemented cell has no evidence or no reference, or a reference names a file or test that does not exist.
- An agent on the runner contract, built in or defined in configuration, sets a field the table does not list for it: its real runner is run over sample turns and the fields it set are compared with the table. For defined agents a definition that declares everything must produce exactly the column's inputs, and one that declares nothing must produce only state, error class and evidence.
- This document's matrix differs from the table.

What the test cannot check is whether a cell's status is true where no code difference would show it, such as `live` evidence or a behaviour that is the same code for every adapter. Those rest on the linked tests and records.

To change the matrix, edit the table and run `KEEPGOING_UPDATE_ADAPTERS_DOC=1 go test ./internal/adapter/capability -run TestDocumentMatchesTheTable`.

## Matrix

<!-- capability-matrix:begin -->
| Behaviour | BB thread | Claude Code headless | Codex headless | Cursor headless | Antigravity | Generic process | Agent defined in configuration |
| --- | --- | --- | --- | --- | --- | --- | --- |
| [Observe state](#observe-state) | yes · live | yes · fixture | yes · fixture | yes · live | missing · none | yes · fixture | yes · fixture |
| [Structured error classes](#structured-error-classes) | yes · live | yes · fixture | yes · live | missing · live | missing · none | yes · fixture | partial · fixture |
| [Refusals continue, restrictions hold](#refusals-continue-restrictions-hold) | partial · fixture | yes · fixture | partial · fixture | missing · none | missing · none | yes · fixture | partial · fixture |
| [Stale retry-notice expiry](#stale-retry-notice-expiry) | yes · live | n/a | n/a | n/a | missing · none | n/a | n/a |
| [Recover a failed turn: re-prompt](#recover-a-failed-turn-re-prompt) | yes · fixture | yes · fixture | yes · fixture | yes · live | missing · none | yes · fixture | yes · fixture |
| [Recover a failed turn: exact session resume](#recover-a-failed-turn-exact-session-resume) | n/a | yes · fixture | yes · fixture | yes · live | missing · none | n/a | partial · fixture |
| [Recover a failed turn: retry the turn](#recover-a-failed-turn-retry-the-turn) | yes · fixture | missing · none | missing · none | missing · none | missing · none | missing · none | missing · none |
| [Connection loss and the network check](#connection-loss-and-the-network-check) | yes · fixture | yes · fixture | yes · fixture | yes · fixture | missing · none | yes · fixture | yes · fixture |
| [Child-work awareness](#child-work-awareness) | yes · live | n/a | n/a | n/a | missing · none | partial · fixture | n/a |
| [Child failure notice and report](#child-failure-notice-and-report) | yes · fixture | n/a | n/a | n/a | missing · none | n/a | n/a |
| [Lane recovery: retry a failed child's turn](#lane-recovery-retry-a-failed-childs-turn) | yes · fixture | missing · none | missing · none | missing · none | missing · none | missing · none | missing · none |
| [Handoff away from it](#handoff-away-from-it) | yes · live | yes · fixture | yes · fixture | missing · none | missing · none | yes · fixture | yes · fixture |
| [Handoff to it](#handoff-to-it) | yes · live | yes · fixture | yes · fixture | missing · none | missing · none | yes · fixture | partial · fixture |
| [Switch-back](#switch-back) | yes · fixture | yes · fixture | yes · fixture | missing · none | missing · none | yes · fixture | yes · fixture |
| [Probes](#probes) | yes · live | yes · live | yes · live | missing · none | missing · none | yes · fixture | partial · fixture |
| [Quota readings](#quota-readings) | partial · live | yes · live | partial · live | missing · none | missing · none | missing · none | missing · none |
| [Handoff into a new conversation](#handoff-into-a-new-conversation) | yes · fixture | yes · fixture | yes · fixture | missing · none | missing · none | n/a | yes · fixture |
| [Run graph and keepgoing runs](#run-graph-and-keepgoing-runs) | yes · live | yes · fixture | yes · fixture | yes · fixture | missing · none | partial · fixture | partial · fixture |
| [Stop and owned cancellation](#stop-and-owned-cancellation) | yes · live | yes · fixture | yes · fixture | yes · live | missing · none | partial · fixture | partial · fixture |
| [Stop the lanes too](#stop-the-lanes-too) | yes · fixture | n/a | n/a | n/a | missing · none | n/a | n/a |
| [Permission prompts](#permission-prompts) | yes · live | missing · none | missing · none | missing · none | missing · fixture | yes · fixture | partial · fixture |

### Observe state

Report idle, active, error or unknown from structured status, never from transcript text.

- **BB thread:** yes, evidence live. Thread status, log events and the interaction list. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestObserveIdle`](../internal/adapter/bb/bb_test.go)
- **Claude Code headless:** yes, evidence fixture. The turn's process state, exit code and result object. [`TestClaudeTurnLifecycle`](../internal/adapter/runner/runner_test.go)
- **Codex headless:** yes, evidence fixture. The turn's process state, exit code and event stream. [`TestCodexTurn`](../internal/adapter/runner/runner_test.go)
- **Cursor headless:** yes, evidence live. The turn's process state, exit code and bounded event stream. Live once, on one CLI version on macOS. [cursor.md](cursor.md) [`TestCursorLifecycleOnThisPlatform`](../internal/adapter/runner/cursor_test.go)
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. The JSON the status command prints. [`TestObserveJSON`](../internal/adapter/process/process_test.go)
- **Agent defined in configuration:** yes, evidence fixture. The turn's process state, its exit code and the JSON fields the definition names. Output over the definition's limit is not read. [`TestDefinedAgentTurnLifecycle`](../internal/adapter/runner/definition_unix_test.go) [`TestDefinedAgentOutputBound`](../internal/adapter/runner/definition_unix_test.go)

### Structured error classes

Classify a failure as rate_limit, auth, restricted, refused, overloaded or network from structured fields. Anything else is unknown, and unknown never fails over.

- **BB thread:** yes, evidence live. From real provider error and rate-limit events. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestObserveStructuredRateLimit`](../internal/adapter/bb/bb_test.go)
- **Claude Code headless:** yes, evidence fixture. From api_error_status, terminal_reason and subtype. A real usage-limit result has not been captured. [`TestClaudeErrorClassification`](../internal/adapter/runner/runner_test.go)
- **Codex headless:** yes, evidence live. Live for a usage limit, read from the app-server's structured snapshot because the event stream carries it only as prose. Other classes are fixture-only. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestCodexTurnLimitClassifiedFromAppServerNotProse`](../internal/adapter/runner/runner_test.go)
- **Cursor headless:** missing, evidence live. Every failure is unknown: the CLI has no structured error contract. A live usage limit and an untrusted directory were both an exit code and stderr text. Connection records add a diagnostic category only. [cursor.md](cursor.md) [`TestCursorFailuresRemainUnknown`](../internal/adapter/runner/cursor_test.go) [`TestCursorConnectionRecordsAreDiagnosticOnly`](../internal/adapter/runner/cursor_test.go)
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. From errorClass in the status JSON or the exit_codes map. [`TestObserveExitCodeMap`](../internal/adapter/process/process_test.go)
- **Agent defined in configuration:** partial, evidence fixture. With structured_errors: the codes, HTTP status field and exit codes the definition maps. Everything else, and every failure without that declaration, is unknown. [`TestDefinedAgentErrorClassification`](../internal/adapter/runner/definition_unix_test.go) [`TestDefinitionReadings`](../internal/adapter/runner/definition_test.go)

### Refusals continue, restrictions hold

Recognise, from a structured field, a provider denying permission (restricted) or a model or provider declining the turn (refused). A restriction holds for a person at once and a person's resume asks for exactly one more try. A refusal is continued in the same session with spacing that grows to 10 minutes, and after refusal.ask_after refusals in a row asks a person; until a turn finishes without a refusal, no error on that route moves the work. A restriction outranks a refusal. Neither ever causes a handoff. A refusal no structured field reports stays unknown or looks like a finished turn.

- **BB thread:** partial, evidence fixture. Restricted: a provider error with HTTP 403, a permission code or oauth_org_not_allowed, in any field, including BB's category "unauthorized" with status 403. Refused: BB's category "policy". Shapes read from BB 0.48.1's bundled providers, not captured live. A model refusal that ends a turn normally is not reported by BB in a structured form and looks idle. When the provider answers with another model after a refusal (provider/modelFallback with reason refusal), that is a timeline note only: no hold and no continue. An errored turn is named by its thread and the log position of its start, so a refused turn is counted once. [`TestObserveForbiddenIsRestricted`](../internal/adapter/bb/bb_test.go) [`TestObserveNamesTheErroredTurn`](../internal/adapter/bb/bb_test.go) [`TestObserveModelFallbackAfterARefusal`](../internal/adapter/bb/bb_test.go) [`TestObservePolicyBlockIsRefused`](../internal/adapter/bb/bb_test.go) [`TestRestrictedHoldsAndNeverMoves`](../internal/decide/restricted_test.go) [`TestRefusedContinuesWithSpacingAndNeverMoves`](../internal/decide/restricted_test.go)
- **Claude Code headless:** yes, evidence fixture. Restricted: HTTP 403 in api_error_status, or a permission code in any result field. Refused: stop_reason "refusal", which Claude Code documents for a declined turn, whether or not the result is marked as an error. Neither has been captured from the real CLI. [`TestClaudeErrorClassification`](../internal/adapter/runner/runner_test.go) [`TestRestrictedHoldsAndNeverMoves`](../internal/decide/restricted_test.go) [`TestRefusedContinuesWithSpacingAndNeverMoves`](../internal/decide/restricted_test.go)
- **Codex headless:** partial, evidence fixture. Restricted: an event carrying HTTP 403 or a permission code. Refused: the codes cyberPolicy and misalignmentPolicyViolation, or the type policy. Whether codex exec prints those codes was not captured; a failure carried only as a message is unknown or looks like a finished turn. [`TestParseCodexEventsRestricted`](../internal/adapter/runner/runner_test.go) [`TestRestrictedHoldsAndNeverMoves`](../internal/decide/restricted_test.go) [`TestRefusedContinuesWithSpacingAndNeverMoves`](../internal/decide/restricted_test.go)
- **Cursor headless:** missing, evidence none. Every failure is unknown.
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. When the status command reports errorClass restricted or refused, or an exit code is mapped to either. [`TestObserveRestricted`](../internal/adapter/process/process_test.go) [`TestRestrictedHoldsAndNeverMoves`](../internal/decide/restricted_test.go) [`TestRefusedContinuesWithSpacingAndNeverMoves`](../internal/decide/restricted_test.go)
- **Agent defined in configuration:** partial, evidence fixture. A definition may map its agent's own codes or exit codes to restricted or refused, and HTTP 403 in http_status_field is restricted; either in any field wins over a limit and over a matching finished rule, and restricted wins over refused. Validation refuses a definition that maps a code keepgoing itself reads as a refusal or restriction to a class a loop may fail over on. What the agent does not report in a declared field stays unknown or looks like a finished turn. [`TestDefinitionDeclaredRefusalsAndRestrictions`](../internal/adapter/runner/definition_test.go) [`TestDeclinedTurnIsNotFinished`](../internal/adapter/runner/definition_test.go) [`TestCustomDefinitionCannotRelabelARefusalAsALimit`](../internal/config/custom_test.go)

### Stale retry-notice expiry

Stop honouring a provider's "retrying" notice when nothing fresh stands behind it.

- **BB thread:** yes, evidence live. The age-based expiry is running on one live loop. Dropping a fresh notice at once when BB's retry plugin has declined the failure and has nothing pending is fixture-tested only. [operations.md#bb-retry-and-stall-handling](operations.md#bb-retry-and-stall-handling) [`TestStaleRetryNoticeOnErroredThreadExpires`](../internal/adapter/bb/stale_retry_test.go) [`TestDeclinedRetryNoticeIsDroppedAtOnce`](../internal/adapter/bb/retry_plugin_test.go)
- **Claude Code headless:** n/a. A headless turn is one process that is running, finished or gone. The CLI retries inside that process and reports no retry notice that could go stale.
- **Codex headless:** n/a. A headless turn is one process that is running, finished or gone. The CLI retries inside that process and reports no retry notice that could go stale.
- **Cursor headless:** n/a. A headless turn is one process that is running, finished or gone. The CLI retries inside that process and reports no retry notice that could go stale.
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** n/a. The status contract has no retry notice.
- **Agent defined in configuration:** n/a. A turn is one process that is running, finished or gone, as for the built-in headless agents.

### Recover a failed turn: re-prompt

After an error, send the task prompt again, with backoff, up to errors.max_reprompts, then ask for a person.

- **BB thread:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. A real retry exhaustion and explicit resume were observed once. [`TestErrorRepromptsThenAwaitsUser`](../internal/supervise/supervise_test.go) [integrations.md#agent-providers](integrations.md#agent-providers)
- **Claude Code headless:** yes, evidence fixture. Run end to end with a stand-in CLI. [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go)
- **Codex headless:** yes, evidence fixture. Run end to end with a stand-in CLI. [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go)
- **Cursor headless:** yes, evidence live. A failed turn is re-prompted in its verified session. Live once after a usage-limit failure and after a stop; connection loss is fixture-only. [cursor.md](cursor.md) [`TestCursorRecoversInitializedSessionAfterWrapperCrash`](../internal/adapter/runner/cursor_recovery_test.go) [`TestCursorConnectionLossKeepsSessionAndStaysUnknown`](../internal/adapter/runner/cursor_test.go)
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestErrorRepromptsThenAwaitsUser`](../internal/supervise/supervise_test.go)
- **Agent defined in configuration:** yes, evidence fixture. Run end to end with a stand-in agent. [`TestDefinedAgentJourney`](../internal/scenario/defined_agent_test.go)

### Recover a failed turn: exact session resume

Continue the same conversation by its saved identifier, never "the most recent".

- **BB thread:** n/a. The configured thread is the conversation; every message goes to that thread ID.
- **Claude Code headless:** yes, evidence fixture. --resume with the saved session ID. If no turn ever reported one, the next turn starts a new session. [`TestClaudeTurnLifecycle`](../internal/adapter/runner/runner_test.go) [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go)
- **Codex headless:** yes, evidence fixture. exec resume with the saved thread ID. If no turn ever reported one, the next turn starts a new session. [`TestCodexResumeArgs`](../internal/adapter/runner/runner_test.go) [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go)
- **Cursor headless:** yes, evidence live. --resume with the saved session ID, live once across three turns. A new conversation is refused when the previous turn's identity is unverified, unless that turn exited 1 having written nothing on a CLI version whose start order is verified. [cursor.md](cursor.md) [`TestCursorSessionSafety`](../internal/adapter/runner/cursor_test.go) [`TestCursorFreshStartNeedsExactShapeAndVerifiedVersion`](../internal/adapter/runner/cursor_test.go)
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** n/a. The resume command owns session identity.
- **Agent defined in configuration:** partial, evidence fixture. With exact_resume: the resume command with the saved session identifier, read from a declared field or file. A saved identifier that is no longer usable holds the loop for a person; no new conversation is started in its place. Without exact_resume every turn starts a new session. [`TestDefinedAgentTurnLifecycle`](../internal/adapter/runner/definition_unix_test.go) [`TestDefinedAgentLostSessionHoldsInsteadOfStartingAnother`](../internal/adapter/runner/definition_unix_test.go) [`TestDefinitionSessionFile`](../internal/adapter/runner/definition_test.go)

### Recover a failed turn: retry the turn

Ask the provider to retry the failed turn itself, instead of sending a new prompt.

- **BB thread:** yes, evidence fixture. A re-prompt of a thread in a network, overloaded or unknown error runs `bb thread retry`, which re-submits the failed turn by reference with no new prompt, under the re-prompt cap, backoff and send allowance. The prompt is sent only when BB reports no failed turn or has no such command. No live thread has been retried by keepgoing. [`TestRetryFailedTurnReadsOnlyTheStructuredResult`](../internal/adapter/bb/turn_retry_test.go) [`TestFailedTurnIsRetriedByReferenceNotPromptedAgain`](../internal/supervise/turnretry_test.go) [`TestTurnRetryAfterAnOutageOfAnyLength`](../internal/supervise/turnretry_test.go) [`TestTickRetriesAFailedBBTurnByReference`](../cmd/keepgoing/turn_retry_test.go) [operations.md#recovering-a-failed-bb-turn-by-retry](operations.md#recovering-a-failed-bb-turn-by-retry)
- **Claude Code headless:** missing, evidence none. A re-prompt is always a new turn in the resumed session.
- **Codex headless:** missing, evidence none. A re-prompt is always a new turn in the resumed session.
- **Cursor headless:** missing, evidence none. A re-prompt is always a new turn in the resumed session.
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** missing, evidence none. The contract has a resume command only.
- **Agent defined in configuration:** missing, evidence none. A re-prompt is always a new turn.

### Connection loss and the network check

Tell an outage from a provider failure, wait it out without spending re-prompts or probes, and act on the first check after it returns.

- **BB thread:** yes, evidence fixture. The supervisor checks connectivity before a re-prompt, handoff, hold for an errored thread or probe, and waits while it fails. Run for outages of 20 minutes to 3 weeks with a stand-in bb. A turn whose log (Turn) shows it silent since before an outage or a sleep has the nudges due to it held back, not sent; a log that cannot be read (TurnUnknown) changes nothing. [`TestOfflineForAnyLengthThenRecoversOnce`](../internal/supervise/offline_scenario_unix_test.go) [`TestHungTurnActsAsTheStallRuleExceptForTheNudges`](../internal/supervise/hungturn_vs_main_test.go) [operations.md#network-outages](operations.md#network-outages) [operations.md#a-turn-that-hung-through-an-outage](operations.md#a-turn-that-hung-through-an-outage)
- **Claude Code headless:** yes, evidence fixture. As for BB. The wake that ran into the outage is spent; nothing else is, and no hold is raised. Run end to end with a stand-in CLI. [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go) [operations.md#network-outages](operations.md#network-outages)
- **Codex headless:** yes, evidence fixture. As for Claude Code. [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go) [operations.md#network-outages](operations.md#network-outages)
- **Cursor headless:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestOutageDoesNotSpendRepromptsOrProbes`](../internal/supervise/network_test.go) [operations.md#network-outages](operations.md#network-outages)
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestOutageDoesNotSpendRepromptsOrProbes`](../internal/supervise/network_test.go) [operations.md#network-outages](operations.md#network-outages)
- **Agent defined in configuration:** yes, evidence fixture. As for the built-in headless agents. The turn that ran into the outage is spent; nothing else is sent and no hold is raised until the network returns. Run end to end with a stand-in agent. [`TestDefinedAgentOutage`](../internal/scenario/defined_agent_test.go) [operations.md#network-outages](operations.md#network-outages)

### Child-work awareness

Hold a parent that is not in error while conversations it started are still working, and say which. A parent in error is recovered regardless.

- **BB thread:** yes, evidence live. Direct child threads are listed and verified on each check. [run-discovery.md#live-check-waiting-on](run-discovery.md#live-check-waiting-on) [`TestObserveActiveChildSuppressesParentUntilFinished`](../internal/adapter/bb/children_test.go)
- **Claude Code headless:** n/a. A headless turn has no child conversations outside its own process. Subagents the CLI starts live and end inside the turn.
- **Codex headless:** n/a. A headless turn has no child conversations outside its own process. Subagents the CLI starts live and end inside the turn.
- **Cursor headless:** n/a. A headless turn has no child conversations outside its own process. Subagents the CLI starts live and end inside the turn.
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** partial, evidence fixture. A background count in the status JSON holds the loop. No child identities are reported. [`TestObserveJSON`](../internal/adapter/process/process_test.go)
- **Agent defined in configuration:** n/a. A definition cannot declare work that outlives a turn, so none is assumed.

### Child failure notice and report

Tell the parent conversation, and optionally the owner, when a child fails.

- **BB thread:** yes, evidence fixture. Running on one live loop; a notice has not been captured there. [`TestChildWorkListsErroredChildren`](../internal/adapter/bb/children_test.go) [operations.md](operations.md)
- **Claude Code headless:** n/a. No child conversations.
- **Codex headless:** n/a. No child conversations.
- **Cursor headless:** n/a. No child conversations.
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** n/a. No child identities are reported.
- **Agent defined in configuration:** n/a. No child conversations.

### Lane recovery: retry a failed child's turn

Ask the provider to retry the failed turn of a lane (a direct child conversation) that was seen working and then failed with a connection, overload or unknown error.

- **BB thread:** yes, evidence fixture. Opt-in with `bb.children.retry_errored`, off by default. Runs `bb thread retry` on the lane, by reference and with no message, at most 5 lanes a check, 3 attempts a lane 10 minutes apart and 20 retries a failure (all configurable). To tell what failed, the typed fields of the lane's event log are read (event type, error category and codes, will-retry flag); message text is never decoded or stored. Lane recovery never messages, stops, creates or moves a lane (only an explicit `stop --lanes` stops one), leaves another loop's lane alone, and one waiting on a person, rate limited or needing a login is left alone. No live lane has been retried by keepgoing. [`TestInspectLaneEstablishesEachFact`](../internal/adapter/bb/lanes_test.go) [`TestLaneMessageTextIsNeverDecodedOrStored`](../internal/adapter/bb/lanes_test.go) [`TestLaneRetriesAreBoundedPerCheckPerLaneAndReportedOnce`](../internal/supervise/lanes_test.go) [`TestLanesWaitOutAnOutageOfAnyLength`](../internal/supervise/lanes_test.go) [`TestEndToEndFailedLaneIsRetriedByReferenceWhenOptedIn`](../cmd/keepgoing/lane_recovery_test.go) [`TestScenarioOutage`](../internal/scenario/outage_test.go) [operations.md#lane-recovery](operations.md#lane-recovery)
- **Claude Code headless:** missing, evidence none. No child conversations to recover.
- **Codex headless:** missing, evidence none. No child conversations to recover.
- **Cursor headless:** missing, evidence none. No child conversations to recover.
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** missing, evidence none. No child identities are reported.
- **Agent defined in configuration:** missing, evidence none. A defined agent reports no child conversations to recover.

### Handoff away from it

When it is the active target and hits a configured error class, move the loop to a confirmed fallback.

- **BB thread:** yes, evidence live. A rate-limited primary handed off on a live loop. [run-discovery.md#live-check](run-discovery.md#live-check) [`TestHandoffToFirstCandidatePassingExactProbe`](../internal/supervise/supervise_test.go)
- **Claude Code headless:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestHandoffToFirstCandidatePassingExactProbe`](../internal/supervise/supervise_test.go)
- **Codex headless:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestHandoffToFirstCandidatePassingExactProbe`](../internal/supervise/supervise_test.go)
- **Cursor headless:** missing, evidence none. Configuration refuses fallbacks for a Cursor loop, and every Cursor failure is unknown, which never fails over. [cursor.md](cursor.md)
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestHandoffToFirstCandidatePassingExactProbe`](../internal/supervise/supervise_test.go)
- **Agent defined in configuration:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestHandoffToFirstCandidatePassingExactProbe`](../internal/supervise/supervise_test.go)

### Handoff to it

Be a fallback candidate that takes the work over after an exact probe.

- **BB thread:** yes, evidence live. An existing thread; its model can be switched before the brief. [run-discovery.md#live-check](run-discovery.md#live-check) [`TestSendStopActivate`](../internal/adapter/bb/bb_test.go)
- **Claude Code headless:** yes, evidence fixture. Guided setup creates it; run only against stand-in CLIs. [fallbacks.md](fallbacks.md)
- **Codex headless:** yes, evidence fixture. Guided setup creates it; run only against stand-in CLIs. [fallbacks.md](fallbacks.md)
- **Cursor headless:** missing, evidence none. Cursor fallback candidates are refused: there is no account and model probe contract. [cursor.md](cursor.md)
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestHandoffToFirstCandidatePassingExactProbe`](../internal/supervise/supervise_test.go)
- **Agent defined in configuration:** partial, evidence fixture. With a probe in the definition, or a command probe. Guided setup does not create it; the candidate is written by hand. [`TestCustomAgentDefinition`](../internal/config/custom_test.go) [configuration.md#an-agent-defined-in-configuration](configuration.md#an-agent-defined-in-configuration)

### Switch-back

Return to the primary once its limit has lifted, when enabled.

- **BB thread:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestSwitchBackAfterThePrimaryResets`](../internal/supervise/supervise_test.go)
- **Claude Code headless:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestSwitchBackAfterThePrimaryResets`](../internal/supervise/supervise_test.go)
- **Codex headless:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestSwitchBackAfterThePrimaryResets`](../internal/supervise/supervise_test.go)
- **Cursor headless:** missing, evidence none. Refused with fallbacks. [cursor.md](cursor.md)
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestSwitchBackAfterThePrimaryResets`](../internal/supervise/supervise_test.go)
- **Agent defined in configuration:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestSwitchBackAfterThePrimaryResets`](../internal/supervise/supervise_test.go)

### Probes

Check a candidate's account and model with a small real request before relying on it.

- **BB thread:** yes, evidence live. A BB candidate is probed through its provider's CLI (Claude Code or Codex), not through BB. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestClaudeProbe`](../internal/probe/probe_test.go)
- **Claude Code headless:** yes, evidence live. One real healthy probe. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestClaudeProbe`](../internal/probe/probe_test.go)
- **Codex headless:** yes, evidence live. A real probe, and a real probe on a limited account. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestCodexProbe`](../internal/probe/probe_test.go)
- **Cursor headless:** missing, evidence none. No probe contract. [cursor.md](cursor.md)
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. A command probe. [`TestCommandProbe`](../internal/probe/probe_test.go)
- **Agent defined in configuration:** partial, evidence fixture. With a probe in the definition: one small request with the prompt passed as a turn's is, judged by the finished and error rules. [`TestCustomProbe`](../internal/probe/custom_test.go)

### Quota readings

Record how much of a usage window is used, from structured fields a probe already returns.

- **BB thread:** partial, evidence live. Readings come from the candidate's provider probe. BB's own rate-limit events supply reset times only. [fallbacks.md](fallbacks.md) [integrations.md#agent-providers](integrations.md#agent-providers)
- **Claude Code headless:** yes, evidence live. One real manual probe recorded a reading. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestClaudeProbeJudgesStreamAndPlainOutputAlike`](../internal/probe/probe_test.go)
- **Codex headless:** partial, evidence live. Structured usage is collected after successful probes and unknown failures, without another model call. Successful-probe collection is unreleased and fixture-tested; the failed-probe limit lookup has live evidence. [`TestCodexProbeRecordsUsageFromSuccessfulAndUnknownProbes`](../internal/probe/probe_test.go)
- **Cursor headless:** missing, evidence none. No probe contract.
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** missing, evidence none. A command probe reports health only.
- **Agent defined in configuration:** missing, evidence none. No usage reading is taken. A limit lookup, when defined, only says whether an unclassified failure is a usage limit and when it resets. [`TestDefinedAgentLimitLookup`](../internal/adapter/runner/definition_unix_test.go)

### Handoff into a new conversation

Take the work over in a conversation that did not exist before the handoff.

- **BB thread:** yes, evidence fixture. Opt-in: a candidate with bb.new_thread has keepgoing create the thread, within loop.limits.max_new_threads. [`TestHandoffToANewThreadCreatesItOnce`](../internal/supervise/newthread_test.go) [fallbacks.md](fallbacks.md)
- **Claude Code headless:** yes, evidence fixture. A headless candidate has no saved session, so the handoff brief starts one. [`TestClaudeTurnLifecycle`](../internal/adapter/runner/runner_test.go)
- **Codex headless:** yes, evidence fixture. A headless candidate has no saved session, so the handoff brief starts one. [`TestCodexTurn`](../internal/adapter/runner/runner_test.go)
- **Cursor headless:** missing, evidence none. Not a fallback candidate.
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** n/a. The resume command decides which conversation receives the brief.
- **Agent defined in configuration:** yes, evidence fixture. A candidate has no saved session, so the handoff brief starts one. [`TestDefinedAgentTurnLifecycle`](../internal/adapter/runner/definition_unix_test.go)

### Run graph and keepgoing runs

Record runs, sends and the conversation reference, and show them offline.

- **BB thread:** yes, evidence live. The configured thread, labelled configured. [run-discovery.md#live-check](run-discovery.md#live-check) [`TestTickRecordsRunIntentBeforeSendSessionAndGoal`](../internal/supervise/graph_test.go)
- **Claude Code headless:** yes, evidence fixture. The runner's saved resume ID, labelled observed. [`TestHeadlessTargetRecordsItsSavedSession`](../internal/supervise/graph_test.go)
- **Codex headless:** yes, evidence fixture. The runner's saved resume ID, labelled observed. [`TestHeadlessTargetRecordsItsSavedSession`](../internal/supervise/graph_test.go)
- **Cursor headless:** yes, evidence fixture. The runner's saved resume ID, labelled observed. [`TestHeadlessTargetRecordsItsSavedSession`](../internal/supervise/graph_test.go)
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** partial, evidence fixture. Runs and sends are recorded. No conversation reference exists to record. [`TestHeadlessTargetRecordsNoChildListing`](../internal/supervise/graph_test.go)
- **Agent defined in configuration:** partial, evidence fixture. Runs and sends are recorded. The session identifier is the agent's own shape and is not shown. [`TestDefinitionBecomesASpecification`](../internal/adapter/runner/definition_test.go)

### Stop and owned cancellation

Stop a running turn on request, signalling only a process keepgoing itself started and still owns.

- **BB thread:** yes, evidence live. bb thread stop on the configured thread. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestSendStopActivate`](../internal/adapter/bb/bb_test.go)
- **Claude Code headless:** yes, evidence fixture. The live turn wrapper stops its own process group on a private per-turn request. A saved PID never authorizes a signal. [`TestStopKillsTurnAndCrashIsUnknown`](../internal/adapter/runner/runner_test.go) [testing.md#lifecycle-and-platform-review](testing.md#lifecycle-and-platform-review)
- **Codex headless:** yes, evidence fixture. The same wrapper as Claude Code. [`TestStopKillsTurnAndCrashIsUnknown`](../internal/adapter/runner/runner_test.go)
- **Cursor headless:** yes, evidence live. The same wrapper. Live once on macOS; a helper process Cursor leaves running per project is not managed. [cursor.md](cursor.md) [`TestCursorOwnedStopOnThisPlatform`](../internal/adapter/runner/cursor_test.go)
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** partial, evidence fixture. Only if a stop command is configured; keepgoing owns no process to cancel. [`TestStopUnsupported`](../internal/adapter/process/process_test.go)
- **Agent defined in configuration:** partial, evidence fixture. With can_be_stopped: the same wrapper as the built-in headless agents. Without it a stop is refused and nothing is signalled. [`TestDefinedAgentStop`](../internal/adapter/runner/definition_unix_test.go)

### Stop the lanes too

On a person's request (keepgoing stop --lanes), stop the conversations the supervised one started, and theirs, along with it.

- **BB thread:** yes, evidence fixture. Verified descendants by parentThreadId, 3 levels and 50 threads at most; running ones and those in error with a retry queued are stopped with `bb thread stop`, a queued provider retry is cancelled with `bb provider-retry cancel`, only with `--drop-queued`, the messages queued in a lane that has not started or is idle are deleted with `bb thread queue delete`, and another loop's lane is left to that loop. Each stop is recorded first. Run against a stand-in bb only. [`TestStopWithLanesStopsTheRunningLanesOnce`](../internal/supervise/lanestop_test.go) [`TestLaneStopRecoversAfterACheckKilledMidStop`](../internal/supervise/lanestop_test.go) [`TestScenarioStopLanesNow`](../internal/scenario/stop_lanes_test.go) [operations.md#stopping-the-lanes-too](operations.md#stopping-the-lanes-too)
- **Claude Code headless:** n/a. Not applicable: a headless turn has no child conversations outside its own process. The stop goes ahead and its report says lanes do not apply.
- **Codex headless:** n/a. Not applicable: a headless turn has no child conversations outside its own process. The stop goes ahead and its report says lanes do not apply.
- **Cursor headless:** n/a. Not applicable: a headless turn has no child conversations outside its own process. The stop goes ahead and its report says lanes do not apply.
- **Antigravity:** missing, evidence none. No adapter is registered, so a loop cannot use it. [antigravity.md](antigravity.md)
- **Generic process:** n/a. Not applicable: no child identities are reported. The stop goes ahead and its report says lanes do not apply.
- **Agent defined in configuration:** n/a. Not applicable: a defined agent reports no child conversations. The stop goes ahead and its report says lanes do not apply.

### Permission prompts

Notice that the agent is waiting for a person to approve something, hold, and report it once.

- **BB thread:** yes, evidence live. From the interaction list; shapes come from real lifecycle events. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestPendingInteractionMeansWaitingOnAPerson`](../internal/adapter/bb/bb_test.go)
- **Claude Code headless:** missing, evidence none. permission_mode is passed to the CLI. A headless turn cannot wait for approval and reports no typed permission state. [configuration.md](configuration.md)
- **Codex headless:** missing, evidence none. The sandbox setting is passed to the CLI. No typed permission state is read. [configuration.md](configuration.md)
- **Cursor headless:** missing, evidence none. No force or approval flag is added, and print mode has no typed permission-wait signal. A rejection has not been observed live. [cursor.md](cursor.md)
- **Antigravity:** missing, evidence fixture. An internal ACP gate cancels permission requests and saves a hold, and an unregistered runner reports that hold once its worker has exited, with stand-in fixtures only. No adapter is registered. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. awaitingInput in the status JSON. [`TestObserveAwaitingInput`](../internal/adapter/process/process_test.go)
- **Agent defined in configuration:** partial, evidence fixture. With structured_waiting: a turn that ends on the declared waiting rule raises one awaiting-user hold. Nothing is approved or answered. [`TestDefinedAgentWaitingRaisesOneHold`](../internal/adapter/runner/definition_unix_test.go)
<!-- capability-matrix:end -->

## The offline scenario on headless Claude Code and Codex

The scenario: the network drops while a turn is running; the host may sleep; the outage lasts twenty minutes, four hours, three days or three weeks; and the network returns behind a captive portal. It was run with stand-in executables in [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go), through the real commands, with the check clock moved forward. Nothing here was run against the real CLIs, and how the real CLIs print a network failure is an assumption: the stand-ins print the documented result shape with an error and no recognised code, print a stream-disconnect code (Codex), print nothing, or print an HTML page.

What happens, with default settings and the connectivity check on (v0.2.0). Before the check existed, three re-prompts went into the outage ten minutes apart, the loop was on an awaiting-user hold within half an hour, and nothing brought it back:

| Moment | What keepgoing does |
| --- | --- |
| The CLI exits on the network failure | The runner records the exit code and the CLI's output for that turn. The saved session ID is not changed. |
| Next check | The turn is an error. Before re-prompting, keepgoing checks connectivity; it fails, so nothing is sent. The check records `network down; waiting for connectivity`. |
| Still offline, at 20 minutes, four hours, three days, three weeks | The same, at every check. No re-prompt is spent and no hold is raised. About once an hour the provider is asked directly with one probe, which does not get through. |
| The network returns behind a captive portal | Still `network down`: the check target does not answer as itself. |
| The portal is cleared | The first check sends one re-prompt, which resumes the saved session, and the loop carries on. |

The idle wake that ran into the outage is still sent: an idle wake does not trigger the connectivity check. See [network outages](operations.md#network-outages).

The four questions:

- **What does the runner record when the CLI exits on a network failure?** The exit code, standard output and standard error of that turn, in the target's private directory. No network state is recorded: an outage is not told apart from any other failure unless the CLI prints a recognised code.
- **Is the saved session ID reused exactly?** Yes. Every send after the first passed the saved ID (`--resume <id>` for Claude Code, `exec resume … <id>` for Codex), including three weeks later. A failed turn does not change it.
- **Can a re-prompt duplicate work?** Yes, in two ways. Every re-prompt carries the whole task prompt again, into the same conversation, so the agent is asked to do the task again and it is the agent that must notice what is done. And if the very first turn fails before the CLI reports a session ID, nothing is saved, so each re-prompt starts a new conversation with the whole task: up to four conversations for one task ([`TestScenarioHeadlessFirstTurnFails`](../internal/scenario/headless_outage_test.go)). Files the earlier turns changed are still on disk either way.
- **What does a weeks-old session do on resume?** keepgoing always resumes the saved ID and never falls back to a new session. If the provider no longer has that session, the resume fails like any other error, three re-prompts are spent, and the loop holds. Whether the real CLIs keep a session for weeks was not tested.

Not exercised: the host sleeping. A sleeping host suspends the turn's process with everything else, so a check after wake-up sees the turn still running until the CLI exits; this is reasoning from the code, not a test.

A reboot, or anything else that ends the turn's wrapper before it records an exit code, is read as a crashed turn, which a check treats as an unknown error and re-prompts. A turn is "running" only while its wrapper holds the turn lock, which the operating system releases when the wrapper ends. The saved PID is not consulted, so an unrelated process that later gets that PID cannot make a dead turn look alive ([`TestKilledWrapperIsCrashedEvenWhenItsPIDIsReused`](../internal/adapter/runner/liveness_unix_test.go)). The one exception is a turn started by a build from before this change, which holds no lock and is still judged by its PID until it ends, unless the machine has restarted since it began ([upgrading](operations.md#owned-headless-cancellation)).

## Gaps this exposed

Unless a row says otherwise, none of these is fixed. Each needs a decision or is not small.

| Gap | Affects | Evidence |
| --- | --- | --- |
| **Closed in v0.2.0: an outage ended in a hold that needed a person.** Re-prompts were spent into a dead network, and nothing acted when it returned. The connectivity check now waits the outage out. What remains: the idle wake that runs into the outage is still sent, and a provider outage with a working network is still re-prompted into. | Every adapter | The scenario above; [network outages](operations.md#network-outages) |
| **Closed in v0.2.0: `resume` did not recover an error hold on a headless loop.** The spent re-prompts were kept, so the loop held again at once and the way out was `retarget primary`, then `resume`. An explicit `resume` now starts a new error episode. | Headless Claude Code, Codex, Cursor | [`TestScenarioHeadlessFirstTurnFails`](../internal/scenario/headless_outage_test.go); [what `resume` starts over](operations.md#what-resume-starts-over) |
| **A first turn that fails before a session is reported starts a new conversation on every re-prompt.** | Headless Claude Code, Codex | [`TestScenarioHeadlessFirstTurnFails`](../internal/scenario/headless_outage_test.go) |
| **An idle headless session is woken at every check**, with the whole task prompt, until it declares completion or a limit stops it. | Headless Claude Code, Codex, Cursor | The scenario above; [operations](operations.md#bounds-on-repeated-work) |
| **No typed permission state.** A headless turn cannot wait for approval, and a denial is not reported as waiting on a person. | Headless Claude Code, Codex, Cursor | Matrix, permission prompts |
| **A session the provider no longer has is not recognised.** It fails as `unknown` until the re-prompts are spent. | Headless Claude Code, Codex, Cursor | Reasoning from the code; not tested against a real expired session |
| **Cursor reports every failure as `unknown`**, so it has no limits, no handoff and no probes. | Cursor | Matrix |
| **No live evidence for a headless loop** beyond probes and one Codex limit: no real multi-turn run, resume or stop. | Headless Claude Code, Codex, Cursor | [integrations](integrations.md#agent-providers) |

## Chats already open in an IDE

A different question from the adapters above: can an outside process observe and continue a chat that is already open in an IDE window, through a documented interface? The answers below come only from the vendors' published documentation, read on 2026-11-13. Nothing was reverse engineered and no application storage was read. The pages were read through a fetching tool that summarises long pages; the sentences quoted were each checked a second time against the page, except where marked.

**Short answer: no, for all five.** No vendor documents an interface that lets a separate program attach to the chat open in a local window, read its state on demand and send it a message at a time of the program's choosing. Three vendors document hooks, which the IDE itself runs at fixed points; one documents an open protocol that reaches a different host process, not the open window.

| IDE | Observe an open chat from outside | Continue it from outside | What is documented instead |
| --- | --- | --- | --- |
| Cursor | No documented interface. Hooks only. | No documented interface. Hooks only. | CLI, ACP, SDK and Cloud Agents API, each for its own sessions |
| Antigravity | No documented interface. Hooks only. | No documented interface. Hooks only. | Headless CLI and SDK for their own sessions; a browser remote control for people |
| VS Code agent chat | Not for the open window. | Not for the open window. | An open protocol for a standalone or remote Agent Host |
| Windsurf (now Devin Desktop) | No documented interface. Hooks only. | No documented interface. Hooks only. | Devin CLI and ACP server for their own sessions |
| Zed | No documented interface. | No documented interface. | Zed runs other agents over ACP; nothing drives Zed's own thread |

**Cursor.** The [hooks reference](https://cursor.com/docs/hooks) says "Hooks are spawned processes that communicate over stdio using JSON in both directions." They are started by Cursor, from a `hooks.json` file, at points such as session start, before a tool runs and when the agent stops. The `stop` hook's input has a `status` of `completed`, `aborted` or `error`, and the page says an optional `followup_message` is submitted as the next user message (this last sentence was read through the summarising tool only). So a script Cursor runs can learn that a turn ended and add one message at that moment. That is not an interface a supervisor can query or send through at another time, and no "waiting for approval" event is documented. The [CLI](https://cursor.com/docs/cli/overview), [ACP mode](https://cursor.com/docs/cli/acp), [SDK](https://cursor.com/docs/sdk/typescript) and [Cloud Agents API](https://cursor.com/docs/cloud-agent/api/endpoints) each start or resume their own sessions; no page says an editor chat's ID can be passed to them. [Deeplinks](https://cursor.com/docs/reference/deeplinks) need the user to confirm. A [remote control](https://cursor.com/docs/cloud-agent/mobile) for local agents exists as a phone app for a person, with no documented programmatic access.

**Antigravity.** The [hooks page](https://antigravity.google/docs/hooks/) says "Hooks allow you to run custom scripts or shell commands at specific points during Antigravity's execution loop" and lists the App, the CLI and the IDE under "Available on". A hook can return `injectSteps`, a "List of steps to inject into the conversation trajectory", including a user message. As with Cursor, this runs only at hook time, inside Antigravity's own loop. The [headless CLI](https://antigravity.google/docs/cli/headless/) and the [SDK](https://antigravity.google/docs/sdk/overview/) run their own conversations; no page says an IDE conversation is reachable from them. [Remote control](https://antigravity.google/docs/remote-control/) is a browser interface for a person and lists the App and CLI, not the IDE. The ACP pages for the CLI and IDE extensions were not read.

**VS Code agent chat.** VS Code documents the most. The [Agent Host page](https://code.visualstudio.com/docs/agents/concepts/agent-host) says "The Agent Host is a dedicated process that runs agent harnesses and manages their sessions" and "multiple clients can observe and control the same session, staying in sync." The [Agent Host Protocol](https://microsoft.github.io/agent-host-protocol/) is published with client libraries, and says "A client sends a message by issuing `chat/turnStarted` as a `dispatchAction`". But the documented way in for another client is a host you start yourself: "To run your own standalone Agent Host, use `code agent host`. By default, the command starts a server on localhost and protects it with a connection token." The page does not say that a third-party client can connect to the host behind an editor window that is already open, and gives no address or token for one. So a supervisor could, on the documentation, run and drive its own Agent Host session; it could not attach to the chat a person already has open. The [extension API](https://code.visualstudio.com/api/references/vscode-api) gives an extension only its own participant's messages in the current session (the part of the reference that was read), and [`code chat`](https://code.visualstudio.com/docs/configure/command-line) opens a new session.

**Windsurf.** The Windsurf documentation now redirects to Devin Desktop, whose [Cascade page](https://docs.devin.ai/desktop/cascade.md) says "Cascade is the legacy agent in Devin Desktop. Devin Desktop no longer starts new Cascade conversations and no longer opens old ones." For Devin Desktop's current agent, [lifecycle hooks](https://docs.devin.ai/cli/extensibility/hooks/lifecycle-hooks.md) fire in the desktop app, the CLI and cloud sessions, including a permission-request event and a stop event that can block with a reason. They are hooks, as above. The [Devin CLI](https://docs.devin.ai/cli/reference/commands.md) has print mode, `--resume` and an ACP server for its own sessions; no page says it can resume a desktop session. The hooks and CLI pages were read through the summarising tool only.

**Zed.** [External agents](https://zed.dev/docs/ai/external-agents) "run as separate processes that communicate with Zed over ACP": Zed drives other agents, and nothing documented drives Zed. The [CLI reference](https://zed.dev/docs/reference/cli) lists only file and window flags; none concerns the agent panel, a thread or a prompt. Zed's extension API was not read, so "an extension cannot reach a thread" is unverified.

What this means for keepgoing: supervising the chat a person has open in one of these IDEs is not possible today through a documented interface. What is possible is supervising a session keepgoing starts itself through each vendor's CLI or protocol, which is what the headless adapters do, and, for the three vendors with hooks, a hook the user installs that reports turn ends to keepgoing. The second is a design option, not something implemented or tested.
