# Adapter capabilities

looptender is meant to supervise every popular coding agent, not only BB threads. This page says what each adapter can do today, how strong the evidence is, and what is missing. It documents the source on `main`; see [integrations](integrations.md) for what is in a published release.

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
- An adapter gains or loses the optional activate, wake-poll or context window reading interface, or a check records a session reference for a different set of adapters than the table says.
- A cell is empty, an implemented cell has no evidence or no reference, or a reference names a file or test that does not exist.
- An agent on the runner contract, built in or defined in configuration, sets a field the table does not list for it: its real runner is run over sample turns and the fields it set are compared with the table. For defined agents a definition that declares everything must produce exactly the column's inputs, and one that declares nothing must produce only state, error class and evidence.
- This document's matrix differs from the table.

What the test cannot check is whether a cell's status is true where no code difference would show it, such as `live` evidence or a behaviour that is the same code for every adapter. Those rest on the linked tests and records.

To change the matrix, edit the table and run `KEEPGOING_UPDATE_ADAPTERS_DOC=1 go test ./internal/adapter/capability -run TestDocumentMatchesTheTable`.

## Matrix

<!-- capability-matrix:begin -->
| Behaviour | BB thread | Claude Code headless | Codex headless | Cursor headless | Antigravity ACP | Generic process | Agent defined in configuration |
| --- | --- | --- | --- | --- | --- | --- | --- |
| [Explicit whole-objective completion guard](#explicit-whole-objective-completion-guard) | yes · fixture | n/a | n/a | n/a | missing · none | n/a | n/a |
| [Observe state](#observe-state) | yes · live | yes · fixture | yes · fixture | yes · live | missing · none | yes · fixture | yes · fixture |
| [Structured error classes](#structured-error-classes) | yes · live | yes · fixture | yes · live | missing · live | missing · none | yes · fixture | partial · fixture |
| [Refusals continue, restrictions hold](#refusals-continue-restrictions-hold) | partial · fixture | yes · fixture | partial · fixture | missing · none | missing · none | yes · fixture | partial · fixture |
| [Stale retry-notice expiry](#stale-retry-notice-expiry) | yes · live | n/a | n/a | n/a | missing · none | n/a | n/a |
| [Recover a failed turn: re-prompt](#recover-a-failed-turn-re-prompt) | yes · fixture | yes · fixture | yes · fixture | yes · live | missing · none | yes · fixture | yes · fixture |
| [Recover a failed turn: exact session resume](#recover-a-failed-turn-exact-session-resume) | n/a | yes · fixture | yes · fixture | yes · live | missing · none | n/a | partial · fixture |
| [Recover a failed turn: retry the turn](#recover-a-failed-turn-retry-the-turn) | yes · fixture | missing · none | missing · none | missing · none | missing · none | missing · none | missing · none |
| [Connection loss and the network check](#connection-loss-and-the-network-check) | yes · fixture | yes · fixture | yes · fixture | yes · fixture | missing · none | yes · fixture | yes · fixture |
| [Child-work awareness](#child-work-awareness) | yes · live | n/a | n/a | n/a | missing · none | partial · fixture | n/a |
| [Explicit independent-work coordinator wake](#explicit-independent-work-coordinator-wake) | yes · fixture | n/a | n/a | n/a | missing · none | n/a | n/a |
| [Child failure notice and report](#child-failure-notice-and-report) | yes · fixture | n/a | n/a | n/a | missing · none | n/a | n/a |
| [Lane recovery: retry a failed child's turn](#lane-recovery-retry-a-failed-childs-turn) | yes · fixture | missing · none | missing · none | missing · none | missing · none | missing · none | missing · none |
| [Handoff away from it](#handoff-away-from-it) | yes · live | yes · fixture | yes · fixture | missing · none | missing · none | yes · fixture | yes · fixture |
| [Handoff to it](#handoff-to-it) | yes · live | yes · fixture | yes · fixture | missing · none | missing · none | yes · fixture | partial · fixture |
| [Switch-back](#switch-back) | yes · fixture | yes · fixture | yes · fixture | missing · none | missing · none | yes · fixture | yes · fixture |
| [Probes](#probes) | yes · live | yes · live | yes · live | missing · none | missing · none | yes · fixture | partial · fixture |
| [Quota readings](#quota-readings) | partial · live | yes · live | partial · live | missing · none | missing · none | missing · none | missing · none |
| [Context window reading](#context-window-reading) | yes · fixture | missing · none | missing · none | missing · none | missing · none | missing · none | missing · none |
| [Supervision context in sends](#supervision-context-in-sends) | yes · fixture | yes · fixture | yes · fixture | yes · fixture | missing · none | yes · fixture | yes · fixture |
| [Turn usage reading](#turn-usage-reading) | missing · none | missing · none | yes · fixture | missing · none | missing · none | missing · none | missing · none |
| [Handoff into a new conversation](#handoff-into-a-new-conversation) | yes · fixture | yes · fixture | yes · fixture | missing · none | missing · none | n/a | yes · fixture |
| [Run graph and keepgoing runs](#run-graph-and-keepgoing-runs) | yes · live | yes · fixture | yes · fixture | yes · fixture | missing · none | partial · fixture | partial · fixture |
| [Stop and owned cancellation](#stop-and-owned-cancellation) | yes · live | yes · fixture | yes · fixture | yes · live | missing · none | partial · fixture | partial · fixture |
| [Stop the lanes too](#stop-the-lanes-too) | yes · fixture | n/a | n/a | n/a | missing · none | n/a | n/a |
| [Permission prompts](#permission-prompts) | yes · live | missing · none | missing · none | missing · none | yes · fixture | yes · fixture | partial · fixture |

### Explicit whole-objective completion guard

Separately opt in to current goal/review-bound attestation and completed acknowledged direct-child obligations; never infer whole-objective acceptance from idle or missing workers.

- **BB thread:** yes, evidence fixture. Unreleased command-layer guard only. Fresh bounded structured inventory/interaction checks, owner/target/source revalidation, reviewed historical completed absence and supervised hold-clear refusal. No new schema, automatic completion or installed/live claim. [`TestAssignmentCompletionJourneyAndHistoricalAbsence`](../cmd/looptender/assignment_completion_test.go) [`TestAssignmentCompletionPublicationRevalidation`](../cmd/looptender/assignment_completion_test.go) [assignments.md#whole-objective-completion-review-unreleased](assignments.md#whole-objective-completion-review-unreleased)
- **Claude Code headless:** n/a. Requires a BB primary and explicit assignment lifecycle.
- **Codex headless:** n/a. Requires a BB primary and explicit assignment lifecycle.
- **Cursor headless:** n/a. Requires a BB primary and explicit assignment lifecycle.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** n/a. Requires a BB primary and explicit assignment lifecycle.
- **Agent defined in configuration:** n/a. Requires a BB primary and explicit assignment lifecycle.

### Observe state

Report idle, active, error or unknown from structured status, never from transcript text.

- **BB thread:** yes, evidence live. Thread status, log events and the interaction list. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestObserveIdle`](../internal/adapter/bb/bb_test.go)
- **Claude Code headless:** yes, evidence fixture. The turn's process state, exit code and result object. [`TestClaudeTurnLifecycle`](../internal/adapter/runner/runner_test.go)
- **Codex headless:** yes, evidence fixture. The turn's process state, exit code and event stream. [`TestCodexTurn`](../internal/adapter/runner/runner_test.go)
- **Cursor headless:** yes, evidence live. The turn's process state, exit code and bounded event stream. Live once, on one CLI version on macOS. [cursor.md](cursor.md) [`TestCursorLifecycleOnThisPlatform`](../internal/adapter/runner/cursor_test.go)
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. The JSON the status command prints. [`TestObserveJSON`](../internal/adapter/process/process_test.go)
- **Agent defined in configuration:** yes, evidence fixture. The turn's process state, its exit code and the JSON fields the definition names. Output over the definition's limit is not read. [`TestDefinedAgentTurnLifecycle`](../internal/adapter/runner/definition_unix_test.go) [`TestDefinedAgentOutputBound`](../internal/adapter/runner/definition_unix_test.go)

### Structured error classes

Classify a failure as rate_limit, auth, restricted, refused, overloaded or network from structured fields. Anything else is unknown, and unknown never fails over.

- **BB thread:** yes, evidence live. From real provider error and rate-limit events. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestObserveStructuredRateLimit`](../internal/adapter/bb/bb_test.go)
- **Claude Code headless:** yes, evidence fixture. From api_error_status, terminal_reason and subtype. A real usage-limit result has not been captured. [`TestClaudeErrorClassification`](../internal/adapter/runner/runner_test.go)
- **Codex headless:** yes, evidence live. Live for a usage limit, read from the app-server's structured snapshot because the event stream carries it only as prose. Other classes are fixture-only. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestCodexTurnLimitClassifiedFromAppServerNotProse`](../internal/adapter/runner/runner_test.go)
- **Cursor headless:** missing, evidence live. Every failure is unknown: the CLI has no structured error contract. A live usage limit and an untrusted directory were both an exit code and stderr text. Connection records add a diagnostic category only. [cursor.md](cursor.md) [`TestCursorFailuresRemainUnknown`](../internal/adapter/runner/cursor_test.go) [`TestCursorConnectionRecordsAreDiagnosticOnly`](../internal/adapter/runner/cursor_test.go)
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. From errorClass in the status JSON or the exit_codes map. [`TestObserveExitCodeMap`](../internal/adapter/process/process_test.go)
- **Agent defined in configuration:** partial, evidence fixture. With structured_errors: the codes, HTTP status field and exit codes the definition maps. Everything else, and every failure without that declaration, is unknown. [`TestDefinedAgentErrorClassification`](../internal/adapter/runner/definition_unix_test.go) [`TestDefinitionReadings`](../internal/adapter/runner/definition_test.go)

### Refusals continue, restrictions hold

Recognise, from a structured field, a provider denying permission (restricted) or a model or provider declining the turn (refused). A restriction holds for a person at once and a person's resume asks for exactly one more try. A refusal is continued in the same session with spacing that grows to 10 minutes, and after refusal.ask_after refusals in a row asks a person; until a turn finishes without a refusal, no error on that route moves the work. A restriction outranks a refusal. Neither ever causes a handoff. A refusal no structured field reports stays unknown or looks like a finished turn.

- **BB thread:** partial, evidence fixture. Restricted: a provider error with HTTP 403, a permission code or oauth_org_not_allowed, in any field, including BB's category "unauthorized" with status 403. Refused: BB's category "policy". Shapes read from BB 0.48.1's bundled providers, not captured live. A model refusal that ends a turn normally is not reported by BB in a structured form and looks idle. When the provider answers with another model after a refusal (provider/modelFallback with reason refusal), that is a timeline note only: no hold and no continue. An errored turn is named by its thread and the log position of its start, so a refused turn is counted once. [`TestObserveForbiddenIsRestricted`](../internal/adapter/bb/bb_test.go) [`TestObserveNamesTheErroredTurn`](../internal/adapter/bb/bb_test.go) [`TestObserveModelFallbackAfterARefusal`](../internal/adapter/bb/bb_test.go) [`TestObservePolicyBlockIsRefused`](../internal/adapter/bb/bb_test.go) [`TestRestrictedHoldsAndNeverMoves`](../internal/decide/restricted_test.go) [`TestRefusedContinuesWithSpacingAndNeverMoves`](../internal/decide/restricted_test.go)
- **Claude Code headless:** yes, evidence fixture. Restricted: HTTP 403 in api_error_status, or a permission code in any result field. Refused: stop_reason "refusal", which Claude Code documents for a declined turn, whether or not the result is marked as an error. Neither has been captured from the real CLI. [`TestClaudeErrorClassification`](../internal/adapter/runner/runner_test.go) [`TestRestrictedHoldsAndNeverMoves`](../internal/decide/restricted_test.go) [`TestRefusedContinuesWithSpacingAndNeverMoves`](../internal/decide/restricted_test.go)
- **Codex headless:** partial, evidence fixture. Restricted: an event carrying HTTP 403 or a permission code. Refused: the codes cyberPolicy and misalignmentPolicyViolation, or the type policy. Whether codex exec prints those codes was not captured; a failure carried only as a message is unknown or looks like a finished turn. [`TestParseCodexEventsRestricted`](../internal/adapter/runner/runner_test.go) [`TestRestrictedHoldsAndNeverMoves`](../internal/decide/restricted_test.go) [`TestRefusedContinuesWithSpacingAndNeverMoves`](../internal/decide/restricted_test.go)
- **Cursor headless:** missing, evidence none. Every failure is unknown.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. When the status command reports errorClass restricted or refused, or an exit code is mapped to either. [`TestObserveRestricted`](../internal/adapter/process/process_test.go) [`TestRestrictedHoldsAndNeverMoves`](../internal/decide/restricted_test.go) [`TestRefusedContinuesWithSpacingAndNeverMoves`](../internal/decide/restricted_test.go)
- **Agent defined in configuration:** partial, evidence fixture. A definition may map its agent's own codes or exit codes to restricted or refused, and HTTP 403 in http_status_field is restricted; either in any field wins over a limit and over a matching finished rule, and restricted wins over refused. Validation refuses a definition that maps a code looptender itself reads as a refusal or restriction to a class a loop may fail over on. What the agent does not report in a declared field stays unknown or looks like a finished turn. [`TestDefinitionDeclaredRefusalsAndRestrictions`](../internal/adapter/runner/definition_test.go) [`TestDeclinedTurnIsNotFinished`](../internal/adapter/runner/definition_test.go) [`TestCustomDefinitionCannotRelabelARefusalAsALimit`](../internal/config/custom_test.go)

### Stale retry-notice expiry

Stop honouring a provider's "retrying" notice when nothing fresh stands behind it.

- **BB thread:** yes, evidence live. The age-based expiry is running on one live loop. Dropping a fresh notice at once when BB's retry plugin has declined the failure and has nothing pending is fixture-tested only. [operations.md#bb-retry-and-stall-handling](operations.md#bb-retry-and-stall-handling) [`TestStaleRetryNoticeOnErroredThreadExpires`](../internal/adapter/bb/stale_retry_test.go) [`TestDeclinedRetryNoticeIsDroppedAtOnce`](../internal/adapter/bb/retry_plugin_test.go)
- **Claude Code headless:** n/a. A headless turn is one process that is running, finished or gone. The CLI retries inside that process and reports no retry notice that could go stale.
- **Codex headless:** n/a. A headless turn is one process that is running, finished or gone. The CLI retries inside that process and reports no retry notice that could go stale.
- **Cursor headless:** n/a. A headless turn is one process that is running, finished or gone. The CLI retries inside that process and reports no retry notice that could go stale.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** n/a. The status contract has no retry notice.
- **Agent defined in configuration:** n/a. A turn is one process that is running, finished or gone, as for the built-in headless agents.

### Recover a failed turn: re-prompt

After an error, send the task prompt again, with backoff, up to errors.max_reprompts, then ask for a person.

- **BB thread:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. A real retry exhaustion and explicit resume were observed once. [`TestErrorRepromptsThenAwaitsUser`](../internal/supervise/supervise_test.go) [integrations.md#agent-providers](integrations.md#agent-providers)
- **Claude Code headless:** yes, evidence fixture. Run end to end with a stand-in CLI. [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go)
- **Codex headless:** yes, evidence fixture. Run end to end with a stand-in CLI. [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go)
- **Cursor headless:** yes, evidence live. A failed turn is re-prompted in its verified session. Live once after a usage-limit failure and after a stop; connection loss is fixture-only. [cursor.md](cursor.md) [`TestCursorRecoversInitializedSessionAfterWrapperCrash`](../internal/adapter/runner/cursor_recovery_test.go) [`TestCursorConnectionLossKeepsSessionAndStaysUnknown`](../internal/adapter/runner/cursor_test.go)
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestErrorRepromptsThenAwaitsUser`](../internal/supervise/supervise_test.go)
- **Agent defined in configuration:** yes, evidence fixture. Run end to end with a stand-in agent. [`TestDefinedAgentJourney`](../internal/scenario/defined_agent_test.go)

### Recover a failed turn: exact session resume

Continue the same conversation by its saved identifier, never "the most recent".

- **BB thread:** n/a. The configured thread is the conversation; every message goes to that thread ID.
- **Claude Code headless:** yes, evidence fixture. --resume with the saved session ID. If no turn ever reported one, the next turn starts a new session. [`TestClaudeTurnLifecycle`](../internal/adapter/runner/runner_test.go) [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go)
- **Codex headless:** yes, evidence fixture. exec resume with the saved thread ID. If no turn ever reported one, the next turn starts a new session. [`TestCodexResumeArgs`](../internal/adapter/runner/runner_test.go) [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go)
- **Cursor headless:** yes, evidence live. --resume with the saved session ID, live once across three turns. A new conversation is refused when the previous turn's identity is unverified, unless that turn exited 1 having written nothing on a CLI version whose start order is verified. [cursor.md](cursor.md) [`TestCursorSessionSafety`](../internal/adapter/runner/cursor_test.go) [`TestCursorFreshStartNeedsExactShapeAndVerifiedVersion`](../internal/adapter/runner/cursor_test.go)
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** n/a. The resume command owns session identity.
- **Agent defined in configuration:** partial, evidence fixture. With exact_resume: the resume command with the saved session identifier, read from a declared field or file. A saved identifier that is no longer usable holds the loop for a person; no new conversation is started in its place until someone runs looptender session reset. Without exact_resume every turn starts a new session. [`TestDefinedAgentTurnLifecycle`](../internal/adapter/runner/definition_unix_test.go) [`TestDefinedAgentLostSessionHoldsInsteadOfStartingAnother`](../internal/adapter/runner/definition_unix_test.go) [`TestDefinitionSessionFile`](../internal/adapter/runner/definition_test.go)

### Recover a failed turn: retry the turn

Ask the provider to retry the failed turn itself, instead of sending a new prompt.

- **BB thread:** yes, evidence fixture. A re-prompt of a thread in a network, overloaded or unknown error runs `bb thread retry`, which re-submits the failed turn by reference with no new prompt, under the re-prompt cap, backoff and send allowance. The prompt is sent only when BB reports no failed turn or has no such command. No live thread has been retried by looptender. [`TestRetryFailedTurnReadsOnlyTheStructuredResult`](../internal/adapter/bb/turn_retry_test.go) [`TestFailedTurnIsRetriedByReferenceNotPromptedAgain`](../internal/supervise/turnretry_test.go) [`TestTurnRetryAfterAnOutageOfAnyLength`](../internal/supervise/turnretry_test.go) [`TestTickRetriesAFailedBBTurnByReference`](../cmd/looptender/turn_retry_test.go) [operations.md#recovering-a-failed-bb-turn-by-retry](operations.md#recovering-a-failed-bb-turn-by-retry)
- **Claude Code headless:** missing, evidence none. A re-prompt is always a new turn in the resumed session.
- **Codex headless:** missing, evidence none. A re-prompt is always a new turn in the resumed session.
- **Cursor headless:** missing, evidence none. A re-prompt is always a new turn in the resumed session.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** missing, evidence none. The contract has a resume command only.
- **Agent defined in configuration:** missing, evidence none. A re-prompt is always a new turn.

### Connection loss and the network check

Tell an outage from a provider failure, wait it out without spending re-prompts or probes, and act on the first check after it returns.

- **BB thread:** yes, evidence fixture. The supervisor checks connectivity before a re-prompt, handoff, hold for an errored thread or probe, and waits while it fails. Run for outages of 20 minutes to 3 weeks with a stand-in bb. A turn whose log (Turn) shows it silent since before an outage or a sleep has the nudges due to it held back, not sent; a log that cannot be read (TurnUnknown) changes nothing. [`TestOfflineForAnyLengthThenRecoversOnce`](../internal/supervise/offline_scenario_unix_test.go) [`TestHungTurnActsAsTheStallRuleExceptForTheNudges`](../internal/supervise/hungturn_vs_main_test.go) [operations.md#network-outages](operations.md#network-outages) [operations.md#a-turn-that-hung-through-an-outage](operations.md#a-turn-that-hung-through-an-outage)
- **Claude Code headless:** yes, evidence fixture. As for BB. The wake that ran into the outage is spent; nothing else is, and no hold is raised. Run end to end with a stand-in CLI. [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go) [operations.md#network-outages](operations.md#network-outages)
- **Codex headless:** yes, evidence fixture. As for Claude Code. [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go) [operations.md#network-outages](operations.md#network-outages)
- **Cursor headless:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestOutageDoesNotSpendRepromptsOrProbes`](../internal/supervise/network_test.go) [operations.md#network-outages](operations.md#network-outages)
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestOutageDoesNotSpendRepromptsOrProbes`](../internal/supervise/network_test.go) [operations.md#network-outages](operations.md#network-outages)
- **Agent defined in configuration:** yes, evidence fixture. As for the built-in headless agents. The turn that ran into the outage is spent; nothing else is sent and no hold is raised until the network returns. Run end to end with a stand-in agent. [`TestDefinedAgentOutage`](../internal/scenario/defined_agent_test.go) [operations.md#network-outages](operations.md#network-outages)

### Child-work awareness

Normally hold a parent that is not in error while conversations it started are still working, and say which. A parent in error is recovered regardless.

- **BB thread:** yes, evidence live. Direct child threads are listed and verified on each check. [run-discovery.md#live-check-waiting-on](run-discovery.md#live-check-waiting-on) [`TestObserveActiveChildSuppressesParentUntilFinished`](../internal/adapter/bb/children_test.go)
- **Claude Code headless:** n/a. A headless turn has no child conversations outside its own process. Subagents the CLI starts live and end inside the turn.
- **Codex headless:** n/a. A headless turn has no child conversations outside its own process. Subagents the CLI starts live and end inside the turn.
- **Cursor headless:** n/a. A headless turn has no child conversations outside its own process. Subagents the CLI starts live and end inside the turn.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** partial, evidence fixture. A background count in the status JSON holds the loop. No child identities are reported. [`TestObserveJSON`](../internal/adapter/process/process_test.go)
- **Agent defined in configuration:** n/a. A definition cannot declare work that outlives a turn, so none is assumed.

### Explicit independent-work coordinator wake

Permit one existing coordinator wake alongside healthy busy direct children, only for explicitly declared independent assignments with current typed idle evidence.

- **BB thread:** yes, evidence fixture. Unreleased separate assignment_coordination opt-in. Exact revision-bound readiness, complete current direct-child evidence and one bounded optional revalidation; existing holds, cadence and send allowances retain precedence. Durable pre-delivery attempt consumption prevents automatic replay; no child dispatch, self-resume, authenticated handoff or mandatory turn-end hook. No installed/live acceptance. [`TestAssignmentSchedulingFullJourney`](../cmd/looptender/assignment_scheduling_test.go) [`TestAssignmentObservationTimeCountsTowardGrantAge`](../internal/supervise/assignment_age_test.go) [assignments.md](assignments.md)
- **Claude Code headless:** n/a. This BB direct-child assignment contract is not supported by this adapter.
- **Codex headless:** n/a. This BB direct-child assignment contract is not supported by this adapter.
- **Cursor headless:** n/a. This BB direct-child assignment contract is not supported by this adapter.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** n/a. This BB direct-child assignment contract is not supported by this adapter.
- **Agent defined in configuration:** n/a. This BB direct-child assignment contract is not supported by this adapter.

### Child failure notice and report

Tell the parent conversation, and optionally the owner, when a child fails.

- **BB thread:** yes, evidence fixture. Running on one live loop; a notice has not been captured there. [`TestChildWorkListsErroredChildren`](../internal/adapter/bb/children_test.go) [operations.md](operations.md)
- **Claude Code headless:** n/a. No child conversations.
- **Codex headless:** n/a. No child conversations.
- **Cursor headless:** n/a. No child conversations.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** n/a. No child identities are reported.
- **Agent defined in configuration:** n/a. No child conversations.

### Lane recovery: retry a failed child's turn

Ask the provider to retry the failed turn of a lane (a direct child conversation) that was seen working and then failed with a connection, overload or unknown error.

- **BB thread:** yes, evidence fixture. Opt-in with `bb.children.retry_errored`, off by default. Runs `bb thread retry` on the lane, by reference and with no message, at most 5 lanes a check, 3 attempts a lane 10 minutes apart and 20 retries a failure (all configurable). To tell what failed, the typed fields of the lane's event log are read (event type, error category and codes, will-retry flag); message text is never decoded or stored. Lane recovery never messages, stops, creates or moves a lane (only an explicit `stop --lanes` stops one), leaves another loop's lane alone, and one waiting on a person, rate limited or needing a login is left alone. No live lane has been retried by looptender. [`TestInspectLaneEstablishesEachFact`](../internal/adapter/bb/lanes_test.go) [`TestLaneMessageTextIsNeverDecodedOrStored`](../internal/adapter/bb/lanes_test.go) [`TestLaneRetriesAreBoundedPerCheckPerLaneAndReportedOnce`](../internal/supervise/lanes_test.go) [`TestLanesWaitOutAnOutageOfAnyLength`](../internal/supervise/lanes_test.go) [`TestEndToEndFailedLaneIsRetriedByReferenceWhenOptedIn`](../cmd/looptender/lane_recovery_test.go) [`TestScenarioOutage`](../internal/scenario/outage_test.go) [operations.md#lane-recovery](operations.md#lane-recovery)
- **Claude Code headless:** missing, evidence none. No child conversations to recover.
- **Codex headless:** missing, evidence none. No child conversations to recover.
- **Cursor headless:** missing, evidence none. No child conversations to recover.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** missing, evidence none. No child identities are reported.
- **Agent defined in configuration:** missing, evidence none. A defined agent reports no child conversations to recover.

### Handoff away from it

When it is the active target and hits a configured error class, move the loop to a confirmed fallback.

- **BB thread:** yes, evidence live. A rate-limited primary handed off on a live loop. [run-discovery.md#live-check](run-discovery.md#live-check) [`TestHandoffToFirstCandidatePassingExactProbe`](../internal/supervise/supervise_test.go)
- **Claude Code headless:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestHandoffToFirstCandidatePassingExactProbe`](../internal/supervise/supervise_test.go)
- **Codex headless:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestHandoffToFirstCandidatePassingExactProbe`](../internal/supervise/supervise_test.go)
- **Cursor headless:** missing, evidence none. Configuration refuses fallbacks for a Cursor loop, and every Cursor failure is unknown, which never fails over. [cursor.md](cursor.md)
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestHandoffToFirstCandidatePassingExactProbe`](../internal/supervise/supervise_test.go)
- **Agent defined in configuration:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestHandoffToFirstCandidatePassingExactProbe`](../internal/supervise/supervise_test.go)

### Handoff to it

Be a fallback candidate that takes the work over after an exact probe.

- **BB thread:** yes, evidence live. An existing thread; its model can be switched before the brief. [run-discovery.md#live-check](run-discovery.md#live-check) [`TestSendStopActivate`](../internal/adapter/bb/bb_test.go)
- **Claude Code headless:** yes, evidence fixture. Guided setup creates it; run only against stand-in CLIs. [fallbacks.md](fallbacks.md)
- **Codex headless:** yes, evidence fixture. Guided setup creates it; run only against stand-in CLIs. [fallbacks.md](fallbacks.md)
- **Cursor headless:** missing, evidence none. Cursor fallback candidates are refused: there is no account and model probe contract. [cursor.md](cursor.md)
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestHandoffToFirstCandidatePassingExactProbe`](../internal/supervise/supervise_test.go)
- **Agent defined in configuration:** partial, evidence fixture. With a probe in the definition, or a command probe. Guided setup does not create it; the candidate is written by hand. [`TestCustomAgentDefinition`](../internal/config/custom_test.go) [configuration.md#an-agent-defined-in-configuration](configuration.md#an-agent-defined-in-configuration)

### Switch-back

Return to the primary once its limit has lifted, when enabled.

- **BB thread:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestSwitchBackAfterThePrimaryResets`](../internal/supervise/supervise_test.go)
- **Claude Code headless:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestSwitchBackAfterThePrimaryResets`](../internal/supervise/supervise_test.go)
- **Codex headless:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestSwitchBackAfterThePrimaryResets`](../internal/supervise/supervise_test.go)
- **Cursor headless:** missing, evidence none. Refused with fallbacks. [cursor.md](cursor.md)
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestSwitchBackAfterThePrimaryResets`](../internal/supervise/supervise_test.go)
- **Agent defined in configuration:** yes, evidence fixture. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestSwitchBackAfterThePrimaryResets`](../internal/supervise/supervise_test.go)

### Probes

Check a candidate's account and model with a small real request before relying on it.

- **BB thread:** yes, evidence live. A BB candidate is probed through its provider's CLI (Claude Code or Codex), not through BB. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestClaudeProbe`](../internal/probe/probe_test.go)
- **Claude Code headless:** yes, evidence live. One real healthy probe. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestClaudeProbe`](../internal/probe/probe_test.go)
- **Codex headless:** yes, evidence live. A real probe, and a real probe on a limited account. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestCodexProbe`](../internal/probe/probe_test.go)
- **Cursor headless:** missing, evidence none. No probe contract. [cursor.md](cursor.md)
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. A command probe. [`TestCommandProbe`](../internal/probe/probe_test.go)
- **Agent defined in configuration:** partial, evidence fixture. With a probe in the definition: one small request with the prompt passed as a turn's is, judged by the finished and error rules. [`TestCustomProbe`](../internal/probe/custom_test.go)

### Quota readings

Record how much of a usage window is used, from structured fields a probe already returns.

- **BB thread:** partial, evidence live. Readings come from the candidate's provider probe. BB's own rate-limit events supply reset times only. [fallbacks.md](fallbacks.md) [integrations.md#agent-providers](integrations.md#agent-providers)
- **Claude Code headless:** yes, evidence live. One real manual probe recorded a reading. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestClaudeProbeJudgesStreamAndPlainOutputAlike`](../internal/probe/probe_test.go)
- **Codex headless:** partial, evidence live. Structured usage is collected after successful probes and unknown failures, without another model call. Successful-probe collection is unreleased and fixture-tested; the failed-probe limit lookup has live evidence. [`TestCodexProbeRecordsUsageFromSuccessfulAndUnknownProbes`](../internal/probe/probe_test.go)
- **Cursor headless:** missing, evidence none. No probe contract.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** missing, evidence none. A command probe reports health only.
- **Agent defined in configuration:** missing, evidence none. No usage reading is taken. A limit lookup, when defined, only says whether an unclassified failure is a usage limit and when it resets. [`TestDefinedAgentLimitLookup`](../internal/adapter/runner/definition_unix_test.go)

### Context window reading

Record how much of the model's context window the supervised conversation uses, for status only. No decision, send, allowance or hold reads it.

- **BB thread:** yes, evidence fixture. `bb thread context <thread> --json` for the supervised thread only, never a lane: used tokens, the window and BB's estimated flag. Read after a check's wake, re-prompt or nudge, or when it first sees a turn ended, within what is left of the check's budget; anything malformed, slow or missing (older bb) is unavailable. The shape was seen on one bb build; the parser runs against fixtures only. [`TestReadContextAcceptsOnlyTheKnownShape`](../internal/adapter/bb/context_test.go) [`TestContextReadingChangesNoDecision`](../internal/supervise/context_test.go) [operations.md#context-window-reading](operations.md#context-window-reading)
- **Claude Code headless:** missing, evidence none. Unavailable: no reading is taken.
- **Codex headless:** missing, evidence none. Unavailable. The per-turn token counts in the event stream are recorded as turn usage, which is not a context window size.
- **Cursor headless:** missing, evidence none. Unavailable: no reading is taken.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** missing, evidence none. Unavailable: no reading is taken.
- **Agent defined in configuration:** missing, evidence none. Unavailable: a definition declares no context window reading.

### Supervision context in sends

With `loop.prompt_context` (off by default), append the loop's supervision context, as `context` shows it, to wakes, nudges, re-prompts and handoff briefs: on the first send after the key is turned on, after a control save (enable, resume, a change of owner), with every handoff and switch-back brief, and when its content changes. Never to the notice-only lane-error wake. With the key off every sent byte is unchanged.

- **BB thread:** yes, evidence fixture. Last in the text `bb thread tell` sends, after the fixed wake header and the whole message; in a thread looptender creates for a handoff, last in its first message. BB has no session reset, so only the events above send it again. Run against a stand-in bb only. [`TestScenarioPromptContextBB`](../internal/scenario/prompt_context_test.go) [`TestPromptContextOnlyWhereDue`](../internal/supervise/promptcontext_test.go) [`TestPromptContextInANewThreadsFirstMessage`](../internal/supervise/promptcontext_test.go) [`TestPromptContextOffSendsMainBytes`](../internal/supervise/sent_bytes_test.go) [operations.md#supervision-context-in-sends](operations.md#supervision-context-in-sends)
- **Claude Code headless:** yes, evidence fixture. Last in the turn's prompt, on standard input or in the private prompt file, with no wake header. The turn that a `session reset` starts, a new conversation, carries it again. Never in a probe's request. Run against a stand-in CLI only. [`TestScenarioPromptContextHeadless`](../internal/scenario/prompt_context_test.go) [`TestPromptContextAfterASessionReset`](../internal/supervise/promptcontext_test.go) [operations.md#supervision-context-in-sends](operations.md#supervision-context-in-sends)
- **Codex headless:** yes, evidence fixture. As for Claude Code, through the same runner. The scenario runs with Claude Code's stand-in only. [`TestPromptContextAfterASessionReset`](../internal/supervise/promptcontext_test.go) [operations.md#supervision-context-in-sends](operations.md#supervision-context-in-sends)
- **Cursor headless:** yes, evidence fixture. Last in the prompt Cursor's runner sends. `session reset` is not available for Cursor and looptender cannot see a new Cursor conversation, so only the events above send it again. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestPromptContextResendRules`](../internal/supervise/promptcontext_test.go) [operations.md#supervision-context-in-sends](operations.md#supervision-context-in-sends)
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. Last on the resume command's standard input, after the prompt. The status, stop and probe commands never receive it. looptender cannot see which conversation the command reaches, so a new one there does not send it again; a pause and resume does. [`TestPromptContextOnAProcessStandIn`](../internal/supervise/promptcontext_process_unix_test.go) [operations.md#supervision-context-in-sends](operations.md#supervision-context-in-sends)
- **Agent defined in configuration:** yes, evidence fixture. Last in the prompt, passed as the definition declares, with no wake header. With exact_resume, the turn a `session reset` starts carries it again; without it every turn starts a new session, so every turn carries it. The decision is the same for every adapter and is tested with a fake adapter, not with this one. [`TestPromptContextAfterASessionReset`](../internal/supervise/promptcontext_test.go) [operations.md#supervision-context-in-sends](operations.md#supervision-context-in-sends)

### Turn usage reading

Record the token counts the agent reports for each finished turn, for status only. No decision, send, allowance or hold reads it, and no figure the agent did not report is estimated.

- **BB thread:** missing, evidence none. Unavailable: the agent reports no per-turn token counts that are read.
- **Claude Code headless:** missing, evidence none. Not captured: no Claude Code result object with usage fields has been recorded, so none is read. [`TestClaudeReportsNoTurnUsage`](../internal/adapter/runner/turnusage_test.go)
- **Codex headless:** yes, evidence fixture. The typed usage object of `turn.completed` in `codex exec --json`: input, cached input, cache write input, output and reasoning output tokens. Only plain integers up to ten billion are kept; anything else is absent. Each turn is counted once, by its run record. Unreleased; the parser runs against fixtures only. [`TestCodexTurnUsageReadsOnlyTypedCounts`](../internal/adapter/runner/turnusage_test.go) [`TestTurnUsageChangesNoDecision`](../internal/supervise/turnusage_test.go) [operations.md#turn-usage-reading](operations.md#turn-usage-reading)
- **Cursor headless:** missing, evidence none. Unavailable: the agent reports no per-turn token counts that are read.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** missing, evidence none. Unavailable: the agent reports no per-turn token counts that are read.
- **Agent defined in configuration:** missing, evidence none. Unavailable: a definition declares no per-turn token counts.

### Handoff into a new conversation

Take the work over in a conversation that did not exist before the handoff.

- **BB thread:** yes, evidence fixture. Opt-in: a candidate with bb.new_thread has looptender create the thread, within loop.limits.max_new_threads. [`TestHandoffToANewThreadCreatesItOnce`](../internal/supervise/newthread_test.go) [fallbacks.md](fallbacks.md)
- **Claude Code headless:** yes, evidence fixture. A headless candidate has no saved session, so the handoff brief starts one. [`TestClaudeTurnLifecycle`](../internal/adapter/runner/runner_test.go)
- **Codex headless:** yes, evidence fixture. A headless candidate has no saved session, so the handoff brief starts one. [`TestCodexTurn`](../internal/adapter/runner/runner_test.go)
- **Cursor headless:** missing, evidence none. Not a fallback candidate.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** n/a. The resume command decides which conversation receives the brief.
- **Agent defined in configuration:** yes, evidence fixture. A candidate has no saved session, so the handoff brief starts one. [`TestDefinedAgentTurnLifecycle`](../internal/adapter/runner/definition_unix_test.go)

### Run graph and keepgoing runs

Record runs, sends and the conversation reference, and show them offline.

- **BB thread:** yes, evidence live. The configured thread, labelled configured. [run-discovery.md#live-check](run-discovery.md#live-check) [`TestTickRecordsRunIntentBeforeSendSessionAndGoal`](../internal/supervise/graph_test.go)
- **Claude Code headless:** yes, evidence fixture. The runner's saved resume ID, labelled observed. [`TestHeadlessTargetRecordsItsSavedSession`](../internal/supervise/graph_test.go)
- **Codex headless:** yes, evidence fixture. The runner's saved resume ID, labelled observed. [`TestHeadlessTargetRecordsItsSavedSession`](../internal/supervise/graph_test.go)
- **Cursor headless:** yes, evidence fixture. The runner's saved resume ID, labelled observed. [`TestHeadlessTargetRecordsItsSavedSession`](../internal/supervise/graph_test.go)
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** partial, evidence fixture. Runs and sends are recorded. No conversation reference exists to record. [`TestHeadlessTargetRecordsNoChildListing`](../internal/supervise/graph_test.go)
- **Agent defined in configuration:** partial, evidence fixture. Runs and sends are recorded. The session identifier is the agent's own shape and is not shown. [`TestDefinitionBecomesASpecification`](../internal/adapter/runner/definition_test.go)

### Stop and owned cancellation

Stop a running turn on request, signalling only a process looptender itself started and still owns.

- **BB thread:** yes, evidence live. bb thread stop on the configured thread. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestSendStopActivate`](../internal/adapter/bb/bb_test.go)
- **Claude Code headless:** yes, evidence fixture. The live turn wrapper stops its own process group on a private per-turn request. A saved PID never authorizes a signal. [`TestStopKillsTurnAndCrashIsUnknown`](../internal/adapter/runner/runner_test.go) [testing.md#lifecycle-and-platform-review](testing.md#lifecycle-and-platform-review)
- **Codex headless:** yes, evidence fixture. The same wrapper as Claude Code. [`TestStopKillsTurnAndCrashIsUnknown`](../internal/adapter/runner/runner_test.go)
- **Cursor headless:** yes, evidence live. The same wrapper. Live once on macOS; a helper process Cursor leaves running per project is not managed. [cursor.md](cursor.md) [`TestCursorOwnedStopOnThisPlatform`](../internal/adapter/runner/cursor_test.go)
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** partial, evidence fixture. Only if a stop command is configured; looptender owns no process to cancel. [`TestStopUnsupported`](../internal/adapter/process/process_test.go)
- **Agent defined in configuration:** partial, evidence fixture. With can_be_stopped: the same wrapper as the built-in headless agents. Without it a stop is refused and nothing is signalled. [`TestDefinedAgentStop`](../internal/adapter/runner/definition_unix_test.go)

### Stop the lanes too

On a person's request (`keepgoing stop --lanes`), stop the conversations the supervised one started, and theirs, along with it.

- **BB thread:** yes, evidence fixture. Verified descendants by parentThreadId, 3 levels and 50 threads at most; running ones and those in error with a retry queued are stopped with `bb thread stop`, a queued provider retry is cancelled with `bb provider-retry cancel`, only with `--drop-queued`, the messages queued in a lane that has not started or is idle are deleted with `bb thread queue delete`, and another loop's lane is left to that loop. Each stop is recorded first. Run against a stand-in bb only. [`TestStopWithLanesStopsTheRunningLanesOnce`](../internal/supervise/lanestop_test.go) [`TestLaneStopRecoversAfterACheckKilledMidStop`](../internal/supervise/lanestop_test.go) [`TestScenarioStopLanesNow`](../internal/scenario/stop_lanes_test.go) [operations.md#stopping-the-lanes-too](operations.md#stopping-the-lanes-too)
- **Claude Code headless:** n/a. Not applicable: a headless turn has no child conversations outside its own process. The stop goes ahead and its report says lanes do not apply.
- **Codex headless:** n/a. Not applicable: a headless turn has no child conversations outside its own process. The stop goes ahead and its report says lanes do not apply.
- **Cursor headless:** n/a. Not applicable: a headless turn has no child conversations outside its own process. The stop goes ahead and its report says lanes do not apply.
- **Antigravity ACP:** missing, evidence none. Not supported by the primary-only Antigravity ACP adapter. [antigravity.md](antigravity.md)
- **Generic process:** n/a. Not applicable: no child identities are reported. The stop goes ahead and its report says lanes do not apply.
- **Agent defined in configuration:** n/a. Not applicable: a defined agent reports no child conversations. The stop goes ahead and its report says lanes do not apply.

### Permission prompts

Notice that the agent is waiting for a person to approve something, hold, and report it once.

- **BB thread:** yes, evidence live. From the interaction list; shapes come from real lifecycle events. [integrations.md#agent-providers](integrations.md#agent-providers) [`TestPendingInteractionMeansWaitingOnAPerson`](../internal/adapter/bb/bb_test.go)
- **Claude Code headless:** missing, evidence none. permission_mode is passed to the CLI. A headless turn cannot wait for approval and reports no typed permission state. [configuration.md](configuration.md)
- **Codex headless:** missing, evidence none. The sandbox setting is passed to the CLI. No typed permission state is read. [configuration.md](configuration.md)
- **Cursor headless:** missing, evidence none. No force or approval flag is added, and print mode has no typed permission-wait signal. A rejection has not been observed live. [cursor.md](cursor.md)
- **Antigravity ACP:** yes, evidence fixture. ACP gate cancels permission requests and saves a durable DeferredInteraction hold, reported once its worker exits (stand-in fixtures). Chat-only questions without ACP elicitation and silent permission rules remain open. [antigravity.md](antigravity.md)
- **Generic process:** yes, evidence fixture. awaitingInput in the status JSON. [`TestObserveAwaitingInput`](../internal/adapter/process/process_test.go)
- **Agent defined in configuration:** partial, evidence fixture. With structured_waiting: a turn that ends on the declared waiting rule raises one awaiting-user hold. Nothing is approved or answered. [`TestDefinedAgentWaitingRaisesOneHold`](../internal/adapter/runner/definition_unix_test.go)
<!-- capability-matrix:end -->

## The offline scenario on headless Claude Code and Codex

The scenario: the network drops while a turn is running; the host may sleep; the outage lasts twenty minutes, four hours, three days or three weeks; and the network returns behind a captive portal. It was run with stand-in executables in [`TestScenarioHeadlessOutage`](../internal/scenario/headless_outage_test.go), through the real commands, with the check clock moved forward. Nothing here was run against the real CLIs, and how the real CLIs print a network failure is an assumption: the stand-ins print the documented result shape with an error and no recognised code, print a stream-disconnect code (Codex), print nothing, or print an HTML page.

What happens, with default settings and the connectivity check on (v0.2.0). Before the check existed, three re-prompts went into the outage ten minutes apart, the loop was on an awaiting-user hold within half an hour, and nothing brought it back:

| Moment | What looptender does |
| --- | --- |
| The CLI exits on the network failure | The runner records the exit code and the CLI's output for that turn. The saved session ID is not changed. |
| Next check | The turn is an error. Before re-prompting, looptender checks connectivity; it fails, so nothing is sent. The check records `network down; waiting for connectivity`. |
| Still offline, at 20 minutes, four hours, three days, three weeks | The same, at every check. No re-prompt is spent and no hold is raised. About once an hour the provider is asked directly with one probe, which does not get through. |
| The network returns behind a captive portal | Still `network down`: the check target does not answer as itself. |
| The portal is cleared | The first check sends one re-prompt, which resumes the saved session, and the loop carries on. |

The idle wake that ran into the outage is still sent: an idle wake does not trigger the connectivity check. See [network outages](operations.md#network-outages).

The four questions:

- **What does the runner record when the CLI exits on a network failure?** The exit code, standard output and standard error of that turn, in the target's private directory. No network state is recorded: an outage is not told apart from any other failure unless the CLI prints a recognised code.
- **Is the saved session ID reused exactly?** Yes. Every send after the first passed the saved ID (`--resume <id>` for Claude Code, `exec resume … <id>` for Codex), including three weeks later. A failed turn does not change it.
- **Can a re-prompt duplicate work?** Yes, in two ways. Every re-prompt carries the whole task prompt again, into the same conversation, so the agent is asked to do the task again and it is the agent that must notice what is done. And if the very first turn fails before the CLI reports a session ID, nothing is saved, so each re-prompt starts a new conversation with the whole task: up to four conversations for one task ([`TestScenarioHeadlessFirstTurnFails`](../internal/scenario/headless_outage_test.go)). Files the earlier turns changed are still on disk either way.
- **What does a weeks-old session do on resume?** looptender always resumes the saved ID and never falls back to a new session. If the provider no longer has that session, the resume fails like any other error, three re-prompts are spent, and the loop holds. Whether the real CLIs keep a session for weeks was not tested.

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

## The adapter contract

The matrix says what each adapter supports. The contract says what every adapter must do, whichever agent it drives, and is written as cases that run against any of them. `keepgoing adapter check --list` prints it:

| Case | Contract |
| --- | --- |
| `one-state` | A status read returns exactly one of idle, active, pending, stopping, error or unknown, inside the call limit. An error carries `rate_limit`, `auth`, `restricted`, `refused`, `overloaded` or `network` only when something structured says so. |
| `known-situations` | An idle agent reads as idle and a working agent as active. |
| `never-guess` | Output that is not the agent's status format reads as unknown, never as idle, active or a specific error class. |
| `waiting-on-person` | An agent blocked on a person is reported as awaiting input, so the supervisor holds and does not send to it. |
| `structured-errors-only` | A failure is classified from a machine-readable code. Prose on standard output or standard error, even prose that names a limit, is `unknown`, and so is a code that maps to no class. Neither can cause a handoff. |
| `failed-is-not-healthy` | A command that exits with a failure, or a turn that ends without leaving a result, is a failure of class unknown whatever it printed. Headless fixtures also cover exit zero after a valid stream prefix without a terminal event. |
| `send-once` | One send delivers the prompt exactly once, unchanged, on standard input or in a file only the owner can read, never in a command's arguments or environment. Reading status again or restarting the supervisor does not deliver it again. |
| `send-while-working` | A send while the agent is working is refused or reaches that same agent. It never starts a second agent beside the first. |
| `observe-during-send` | Status can be read while a send is in progress: every read is one valid state and the prompt is still delivered once. |
| `state-unwritable` | When the state directory cannot be written, a send fails and says so or still delivers once. It never reports success for a prompt that did not arrive. |
| `exact-resume` | The session the agent reported is saved in the state directory, and after the supervisor is stopped and started the next send continues exactly that session. |
| `hang-is-cut` | A status read returns inside the call limit when the agent's command never returns, and does not report idle or a specific class. |
| `stop-own-work` | Stop ends a turn this supervisor started and leaves alone anything it did not start. |
| `bounded-output` | Output far larger than any real status is not read as healthy. |
| `writes-stay-inside` | Nothing is created, changed or removed in the project, the home directory or the temporary directory. |
| `declared-matches` | Every capability the adapter declares (exact resume, can be stopped, structured errors, structured waiting) passed its case. |

The cases live in `internal/adapter/conformance`. The test suite runs all of them against the process adapter and the headless Claude Code and Codex adapters, each built by the real factory and driven by a stand-in executable that is put into each situation in turn. The Codex stand-in fails the way the recorded Codex limit does: the turn's output carries only prose, and the limit is told from any other failure by what the stand-in's app-server answers. A second test breaks an in-memory adapter in twenty-seven ways and requires each fault to fail its own case with a message that says what to change.

Current results, with stand-ins only:

| Adapter | Result |
| --- | --- |
| Generic process | Passes the staged cases, including `bounded-output` in unreleased source (1 MiB retained stdout limit). `exact-resume` does not apply: the wrapper owns its session. A turn that ends without a result is not staged: the adapter starts no turn. |
| Headless Claude Code | Passes the staged cases, including `bounded-output` in unreleased source (8 MiB observation limit; private output files are not capped). `waiting-on-person` does not apply: it has no such state. |
| Headless Codex | The same as Claude Code. |
| BB, Cursor | Not run. Neither has a stand-in for the kit yet; their own fixtures are the evidence in the matrix. |

`bounded-output` is a known gap, listed above. The suite records it with the words of the failure it expects and fails when the case passes, is skipped, or fails for any other reason, so the record cannot go stale.

What the kit does not check:

- It checks an adapter, not a check. That a session awaiting input is never sent to and that an unknown failure never causes a handoff are decided in `internal/decide`; that a check ends inside its time limit, in `internal/supervise`; the offline scenario, in `internal/scenario`.
- `writes-stay-inside` watches the project, the home directory and the temporary directory. A write to some other absolute path is not seen.
- `state-unwritable` and the file-mode part of `send-once` do not run on Windows or as root.
- A send that times out is not exercised.
- The four declarations about what an agent does for itself (retries, provider failover, scheduling, lane work) are not yet compared with behaviour.

### Checking your own wrapper

```sh
keepgoing adapter check -c /private/example-loop/looptender.yaml
```

Use the generated configuration path with `-c`. Without `-c`, the command uses the [usual configuration name rules](configuration.md); an explicitly empty path or two distinct default files is refused. Loaded configuration sources are checked again before the status command starts; this takes no lock and cannot fence external edits.

For a loop on the process adapter this attempts one invocation of the configured status command and reports its observation, so a wrapper that always answers unknown is visible. Construction or launch failure can prevent execution. It fails when the read takes longer than `--limit` (default 30s) or returns something that is not one valid state. It invokes no resume or stop command, takes no lock and does not itself write loop state. The configured status program is not sandboxed: its writes and other external behavior belong to that program. `--target ID` checks a fallback candidate. `--json` prints a versioned report (`schemaVersion` 1). The exit code is 1 when a case fails. The report never prints the task prompt, a command, a path or the wrapper's output.

The cases that need the agent put into a situation are listed as skipped: they cannot be staged against a real agent, and the kit is not yet something a wrapper's author can run against their own stand-in.

It is refused for every other adapter, before anything is built or started, because their status reads are not free of side effects: a BB read advances the loop's saved position in the thread's log and takes notices a check is waiting for, a Codex read of a failed turn starts the Codex app-server, and a Claude Code, Codex or Cursor read saves the session a finished turn reported. The conformance suite covers Claude Code and Codex; BB and Cursor rely on their separate adapter fixtures and are not run by this suite.

The command never invokes the configured resume or stop program. Recognizing `restricted` or `refused` does not authorize failover; the existing decision rules keep both on the current route. Prose alone remains unknown. A way to send one test prompt to a real agent would have to pass the same ownership, pause and stop rules as a check, count against the send limit and appear in the timeline; that is a [roadmap](roadmap.md) item, not part of this command.

## Chats already open in an IDE

A different question from the adapters above: can an outside process observe and continue a chat that is already open in an IDE window, through a documented interface? The answers below come only from the vendors' published documentation, read on 2026-11-13. Nothing was reverse engineered and no application storage was read. The pages were read through a fetching tool that summarises long pages; the sentences quoted were each checked a second time against the page, except where marked.

**At the review dates, no documented open-window interface was found for any of the thirteen entries** (the first five reviewed on 2026-11-13, eight more on 2026-11-14). None of the vendor pages reviewed on those dates documented an interface that let a separate program attach to the chat open in a local window, read its state on demand and send it a message at a time of the program's choosing. Three vendors document hooks, which the IDE itself runs at fixed points; one documents an open protocol that reaches a different host process, not the open window; three document handing a conversation over to their own CLI.

| IDE | Observe an open chat from outside | Continue it from outside | What is documented instead |
| --- | --- | --- | --- |
| Cursor | No documented interface. Hooks only. | No documented interface. Hooks only. | CLI, ACP, SDK and Cloud Agents API, each for its own sessions |
| Antigravity | No documented interface. Hooks only. | No documented interface. Hooks only. | Headless CLI and SDK for their own sessions; a browser remote control for people |
| VS Code agent chat | Not for the open window. | Not for the open window. | An open protocol for a standalone or remote Agent Host |
| Windsurf (now Devin Desktop) | No documented interface. Hooks only. | No documented interface. Hooks only. | Devin CLI and ACP server for their own sessions |
| Zed | No documented interface. | No documented interface. | Zed runs other agents over ACP; nothing drives Zed's own thread |
| Claude Code (VS Code, JetBrains) | No live interface. | Handover to the CLI: `claude --resume` | The extension and the CLI share conversation history; the docs warn against using one conversation in two places at once |
| Kiro | No documented interface. | Handover to the CLI (experimental) | "Open with Kiro CLI" resumes an IDE session in Kiro CLI; a session is active in one process at a time |
| Devin Desktop | No live interface. | Handover to the CLI: `devin -r` | Sessions are shared between Devin Desktop and the Devin CLI; a session open elsewhere can be force-opened |
| Codex IDE extension | No documented interface for the open window. | Not documented. | The app-server powers the VS Code extension and lists its threads; attaching to the server an open editor runs is not documented |
| Cline | Not documented for the shipped extension. | Not documented for the shipped extension. | Its SDK's hub lets several clients attach to one session; the docs do not say the VS Code extension uses it |
| JetBrains AI Assistant and Junie | No documented interface. | No documented interface. | ACP lets the IDE start outside agents; Junie's remote mode covers CLI sessions only |
| Visual Studio (Copilot chat) | No documented interface. | No documented interface. | An in-IDE history panel, and MCP servers the chat can call |
| Xcode | No documented interface. | No documented interface. | Outside agents can use Xcode's tools over MCP; nothing reaches its chats |

**Cursor.** The [hooks reference](https://cursor.com/docs/hooks) says "Hooks are spawned processes that communicate over stdio using JSON in both directions." They are started by Cursor, from a `hooks.json` file, at points such as session start, before a tool runs and when the agent stops. The `stop` hook's input has a `status` of `completed`, `aborted` or `error`, and the page says an optional `followup_message` is submitted as the next user message (this last sentence was read through the summarising tool only). So a script Cursor runs can learn that a turn ended and add one message at that moment. That is not an interface a supervisor can query or send through at another time, and no "waiting for approval" event is documented. The [CLI](https://cursor.com/docs/cli/overview), [ACP mode](https://cursor.com/docs/cli/acp), [SDK](https://cursor.com/docs/sdk/typescript) and [Cloud Agents API](https://cursor.com/docs/cloud-agent/api/endpoints) each start or resume their own sessions; no page says an editor chat's ID can be passed to them. [Deeplinks](https://cursor.com/docs/reference/deeplinks) need the user to confirm. A [remote control](https://cursor.com/docs/cloud-agent/mobile) for local agents exists as a phone app for a person, with no documented programmatic access.

**Antigravity.** The [hooks page](https://antigravity.google/docs/hooks/) says "Hooks allow you to run custom scripts or shell commands at specific points during Antigravity's execution loop" and lists the App, the CLI and the IDE under "Available on". A hook can return `injectSteps`, a "List of steps to inject into the conversation trajectory", including a user message. As with Cursor, this runs only at hook time, inside Antigravity's own loop. The [headless CLI](https://antigravity.google/docs/cli/headless/) and the [SDK](https://antigravity.google/docs/sdk/overview/) run their own conversations; no page says an IDE conversation is reachable from them. [Remote control](https://antigravity.google/docs/remote-control/) is a browser interface for a person and lists the App and CLI, not the IDE. The ACP pages for the CLI and IDE extensions were not read.

**VS Code agent chat.** VS Code documents the most. The [Agent Host page](https://code.visualstudio.com/docs/agents/concepts/agent-host) says "The Agent Host is a dedicated process that runs agent harnesses and manages their sessions" and "multiple clients can observe and control the same session, staying in sync." The [Agent Host Protocol](https://microsoft.github.io/agent-host-protocol/) is published with client libraries, and says "A client sends a message by issuing `chat/turnStarted` as a `dispatchAction`". But the documented way in for another client is a host you start yourself: "To run your own standalone Agent Host, use `code agent host`. By default, the command starts a server on localhost and protects it with a connection token." The page does not say that a third-party client can connect to the host behind an editor window that is already open, and gives no address or token for one. So a supervisor could, on the documentation, run and drive its own Agent Host session; it could not attach to the chat a person already has open. The [extension API](https://code.visualstudio.com/api/references/vscode-api) gives an extension only its own participant's messages in the current session (the part of the reference that was read), and [`code chat`](https://code.visualstudio.com/docs/configure/command-line) opens a new session.

**Windsurf.** The Windsurf documentation now redirects to Devin Desktop, whose [Cascade page](https://docs.devin.ai/desktop/cascade.md) says "Cascade is the legacy agent in Devin Desktop. Devin Desktop no longer starts new Cascade conversations and no longer opens old ones." For Devin Desktop's current agent, [lifecycle hooks](https://docs.devin.ai/cli/extensibility/hooks/lifecycle-hooks.md) fire in the desktop app, the CLI and cloud sessions, including a permission-request event and a stop event that can block with a reason. They are hooks, as above. The [Devin CLI](https://docs.devin.ai/cli/reference/commands.md) has print mode, `--resume` and an ACP server for its own sessions; no page says it can resume a desktop session. The hooks and CLI pages were read through the summarising tool only.

**Zed.** [External agents](https://zed.dev/docs/ai/external-agents) "run as separate processes that communicate with Zed over ACP": Zed drives other agents, and nothing documented drives Zed. The [CLI reference](https://zed.dev/docs/reference/cli) lists only file and window flags; none concerns the agent panel, a thread or a prompt. Zed's extension API was not read, so "an extension cannot reach a thread" is unverified.

### Review of 2026-11-14

This preserves the documentation review made on that date, not a new compatibility check. The eight additional entries include Devin Desktop separately from its legacy Windsurf/Cascade entry. Vendor handover support does not establish looptender adoption support.

Eight more products were checked from their vendors' documentation, and the gaps above were rechecked. Each sentence quoted below was confirmed against the page's own text, not only through a summarising tool.

**Handover, not attachment.** Three vendors document a way to move a conversation from the IDE to their own CLI. None of them is a way to watch or drive the chat while it stays open in the window.

- **Claude Code.** The [VS Code page](https://code.claude.com/docs/en/vs-code) says "The extension and CLI share the same conversation history. To continue an extension conversation in the CLI, run `claude --resume` in the terminal." It also warns: "Using it in two places at once can mix up its messages." The [JetBrains plugin](https://code.claude.com/docs/en/jetbrains) runs the `claude` command in the IDE's terminal, so its conversation is an ordinary CLI session.
- **Kiro.** The [Agent Focus Mode page](https://kiro.dev/docs/ide/experimental/focus-mode/), marked experimental, says "Select Open with Kiro CLI on a session to resume that conversation in Kiro CLI." The [CLI's session page](https://kiro.dev/docs/cli/chat/session-management/) says sessions are active in one process at a time.
- **Devin Desktop.** Its [changelog](https://docs.devin.ai/desktop/changelog) says "sessions can be accessed from both Windsurf and the CLI", and the [CLI changelog](https://docs.devin.ai/cli/changelog/stable) adds a way to force open a session held elsewhere.

**Closer, but not documented for the open window.**

- **Codex.** The [app-server page](https://developers.openai.com/codex/app-server) says "app-server is the interface Codex uses to power rich clients (for example, the Codex VS Code extension)". Nothing says another program can attach to the server an open editor is running, or that `codex resume` reaches an extension thread.
- **Cline.** Its SDK's [hub](https://docs.cline.bot/sdk/architecture/hub-spoke) says "Another client can attach to the same session and pick up the event stream mid-flight." The docs do not say the shipped VS Code extension runs on it.
- **Antigravity.** The [IDE extension's ACP page](https://antigravity.google/docs/ide/extensions/acp/) lists loading, resuming, listing and closing sessions, but for agents the editor starts, not the IDE's own conversations. [Remote control](https://antigravity.google/docs/remote-control) still covers the desktop app and the CLI, not the IDE.

**No documented interface:** [JetBrains AI Assistant's ACP support](https://www.jetbrains.com/help/ai-assistant/acp.html) lets the IDE start outside agents, and [Junie's remote mode](https://junie.jetbrains.com/docs/junie-cli-remote-mode.html) covers CLI sessions only. [Visual Studio's Copilot chat](https://learn.microsoft.com/en-us/visualstudio/ide/visual-studio-github-copilot-chat?view=visualstudio) resumes conversations from its own history panel. [Xcode](https://developer.apple.com/documentation/xcode/giving-external-agents-access-to-xcode) gives outside agents its tools, not its chats. [Zed's extensions](https://zed.dev/docs/extensions/capabilities) have three capabilities (running processes, downloading files, installing npm packages), none of which reaches the agent panel, and its [ACP extensions are deprecated](https://zed.dev/docs/extensions/agent-servers).

**Still unverified:** the Junie plugin's Marketplace page did not render; a search result claiming that the Codex desktop app and IDE "share active chats" was not found on any vendor page; whether Kiro's IDE can stay on a conversation handed to its CLI is not documented. For Cursor, VS Code's Agent Host and Zed, "unchanged since 2026-11-13" was checked against the current pages, not against a saved copy (VS Code's page is dated 2026-11-11).

What this means for looptender: the dated review found no documented way to supervise a chat while it remains open in one of these IDEs. The currently supported headless adapters supervise sessions looptender starts through their supported command-line interfaces. For the three vendors whose reviewed pages documented hooks, a user-installed hook reporting turn ends to looptender is a design option, not something implemented or tested. A third option follows from the handover above: for Claude Code, a person could close an extension conversation and hand it to a looptender loop, which resumes sessions by identifier already. Neither v0.2.0 nor this source revision has a command to adopt a session it did not start; `looptender session adopt` is planned for a headless Claude Code loop. Kiro's and Devin Desktop's handovers are documented by those vendors and not supported by looptender.

## Sessionpipe reporting boundary (unreleased)

[Sessionpipe reporting](sessionpipe.md) is an OUT report format, not a registered supervision adapter or a capability-matrix column. `adapter: sessionpipe` is rejected. No Observe, Send, Stop, activation, retry, session ownership or remote control capability is exposed. A persistent POST-401 report pause and explicit `report resume` affect future reports only; they grant no agent or loop resume capability. IN needs typed event intake and an observe-only-loop decision; BACK is held by #233.
