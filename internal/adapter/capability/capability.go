// Package capability is the single statement of what each adapter supports.
// docs/adapters.md is rendered from Matrix, and the package's tests compare
// the checkable claims with the adapters' code, so a behaviour cannot be
// added to one adapter, or a new decision input introduced, without this
// table and the document changing with it.
package capability

import (
	"fmt"
	"strings"
)

// Status of one behaviour for one adapter.
type Status string

const (
	Yes     Status = "yes"     // implemented
	Partial Status = "partial" // implemented in part; the note says which part
	Missing Status = "missing" // not implemented
	NA      Status = "n/a"     // cannot apply; the note says why
)

// Evidence is the strongest evidence that the behaviour works.
type Evidence string

const (
	Live    Evidence = "live"    // observed with the real provider
	Fixture Evidence = "fixture" // tests with stand-ins or fakes only
	None    Evidence = "none"    // nothing exercises it
)

// Ref points at a test or a document, relative to the repository root.
type Ref struct {
	Path string // file path, optionally with a #fragment for documents
	Test string // test function name in Path, when Path is a test file
}

// Cell is one adapter's answer for one behaviour.
type Cell struct {
	Status   Status
	Evidence Evidence
	Note     string
	Refs     []Ref
}

// Adapter describes one adapter kind.
type Adapter struct {
	ID    string
	Title string
	// Registered says the adapter factory builds it. An unregistered adapter
	// cannot be configured, whatever internal code exists.
	Registered bool
	// Files are the non-test source files that implement it, for the check
	// of which decision inputs it can produce.
	Files []string
	// Inputs are the decide.Observation fields the adapter's code refers to.
	// For an agent on the runner contract the shared runner (spec.go) sets
	// the fields, so its Inputs are what its specification can produce.
	// The decision engine can only act on what an adapter can report.
	Inputs []string
	// Activator and WakePoller say the adapter implements those optional
	// interfaces.
	Activator, WakePoller bool
	// TurnRetrier says the adapter can ask its provider to re-run a failed
	// turn by reference (adapter.TurnRetrier).
	TurnRetrier bool
	// SessionEvidence says a check records a chat/session reference for it.
	SessionEvidence bool
}

// Behaviour is one row of the matrix.
type Behaviour struct {
	ID    string
	Title string
	What  string
	Cells map[string]Cell // by adapter ID
}

// Adapters in column order.
var Adapters = []Adapter{
	{ID: "bb", Title: "BB thread", Registered: true, Activator: true, WakePoller: true, TurnRetrier: true, SessionEvidence: true,
		Files: []string{"internal/adapter/bb/bb.go", "internal/adapter/bb/children.go", "internal/adapter/bb/cursor.go", "internal/adapter/bb/wake.go"},
		Inputs: []string{"ActiveChildIDs", "ActiveChildren", "AwaitingChildren", "AwaitingInput", "Background", "ChildrenUnverified", "ErrorClass", "ErroredChildren", "Evidence",
			"Gone", "HealthyChildren", "Queued", "ResetsAt", "RetryDeclined", "RetryExpired", "RetryKnown", "RetryUnverified", "Retrying", "State"}},
	{ID: "claude", Title: "Claude Code headless", Registered: true, SessionEvidence: true,
		Files:  []string{"internal/adapter/runner/claude.go", "internal/adapter/runner/spec.go"},
		Inputs: []string{"ErrorClass", "Evidence", "State"}},
	{ID: "codex", Title: "Codex headless", Registered: true, SessionEvidence: true,
		Files:  []string{"internal/adapter/runner/codex.go", "internal/adapter/runner/spec.go"},
		Inputs: []string{"ErrorClass", "Evidence", "ResetsAt", "State"}},
	{ID: "cursor", Title: "Cursor headless", Registered: true, SessionEvidence: true,
		Files:  []string{"internal/adapter/runner/cursor.go"},
		Inputs: []string{"ErrorClass", "Evidence", "State"}},
	{ID: "antigravity", Title: "Antigravity", Registered: false,
		Files: []string{"internal/adapter/runner/antigravity_protocol.go", "internal/adapter/runner/antigravity_acp_protocol.go", "internal/adapter/runner/antigravity_acp_exchange.go", "internal/adapter/runner/antigravity_acp_hold.go",
			"internal/adapter/runner/antigravity_runner.go", "internal/adapter/runner/antigravity_worker.go"}},
	{ID: "process", Title: "Generic process", Registered: true,
		Files:  []string{"internal/adapter/process/process.go"},
		Inputs: []string{"AwaitingInput", "Background", "ErrorClass", "Evidence", "Gone", "Queued", "State"}},
	// An agent defined in configuration. Its Inputs are the most a
	// definition can produce; a definition that declares less produces less.
	{ID: "custom", Title: "Agent defined in configuration", Registered: true,
		Files:  []string{"internal/adapter/runner/definition.go", "internal/adapter/runner/spec.go"},
		Inputs: []string{"DeferredInteraction", "ErrorClass", "Evidence", "Gone", "ResetsAt", "State"}},
}

// ObservationFields is every field of decide.Observation. A new field must be
// added here, and to the Inputs of each adapter that sets it.
var ObservationFields = []string{"ActiveChildIDs", "ActiveChildren", "AwaitingChildren", "AwaitingInput", "Background", "ChildrenUnverified", "DeferredInteraction", "ErrorClass",
	"ErroredChildren", "Evidence", "Gone", "HealthyChildren", "Queued", "ResetsAt", "RetryDeclined", "RetryExpired", "RetryKnown", "RetryUnverified", "Retrying", "State"}

func t(path, test string) Ref { return Ref{Path: path, Test: test} }
func d(path string) Ref       { return Ref{Path: path} }

var (
	notRegistered = Cell{Status: Missing, Evidence: None, Note: "No adapter is registered, so a loop cannot use it.", Refs: []Ref{d("docs/antigravity.md")}}
	headless      = []string{"claude", "codex", "cursor"}
)

// same gives several adapters one cell.
func same(c Cell, ids ...string) map[string]Cell {
	m := map[string]Cell{}
	for _, id := range ids {
		m[id] = c
	}
	return m
}

// merge combines cell maps and fills antigravity when it is not given.
func merge(maps ...map[string]Cell) map[string]Cell {
	out := map[string]Cell{"antigravity": notRegistered}
	for _, m := range maps {
		for id, c := range m {
			out[id] = c
		}
	}
	return out
}

const (
	bbTest        = "internal/adapter/bb/bb_test.go"
	runnerTest    = "internal/adapter/runner/runner_test.go"
	cursorTest    = "internal/adapter/runner/cursor_test.go"
	processTest   = "internal/adapter/process/process_test.go"
	superTest     = "internal/supervise/supervise_test.go"
	graphTest     = "internal/supervise/graph_test.go"
	offlineTest   = "internal/scenario/headless_outage_test.go"
	offlineBBTest = "internal/supervise/offline_scenario_unix_test.go"
	networkTest   = "internal/supervise/network_test.go"
	networkDoc    = "docs/operations.md#network-outages"
	providers     = "docs/integrations.md#agent-providers"
	waitingLive   = "docs/run-discovery.md#live-check-waiting-on"
	runsLive      = "docs/run-discovery.md#live-check"
	fakeDecision  = "The decision is the same for every adapter and is tested with a fake adapter, not with this one."
)

// Matrix is every behaviour in row order.
var Matrix = []Behaviour{
	{ID: "observe", Title: "Observe state", What: "Report idle, active, error or unknown from structured status, never from transcript text.",
		Cells: merge(map[string]Cell{
			"bb":      {Yes, Live, "Thread status, log events and the interaction list.", []Ref{d(providers), t(bbTest, "TestObserveIdle")}},
			"claude":  {Yes, Fixture, "The turn's process state, exit code and result object.", []Ref{t(runnerTest, "TestClaudeTurnLifecycle")}},
			"codex":   {Yes, Fixture, "The turn's process state, exit code and event stream.", []Ref{t(runnerTest, "TestCodexTurn")}},
			"cursor":  {Yes, Live, "The turn's process state, exit code and bounded event stream. Live once, on one CLI version on macOS.", []Ref{d("docs/cursor.md"), t(cursorTest, "TestCursorLifecycleOnThisPlatform")}},
			"process": {Yes, Fixture, "The JSON the status command prints.", []Ref{t(processTest, "TestObserveJSON")}},
		})},
	{ID: "error_classes", Title: "Structured error classes", What: "Classify a failure as rate_limit, auth, restricted, refused, overloaded or network from structured fields. Anything else is unknown, and unknown never fails over.",
		Cells: merge(map[string]Cell{
			"bb":      {Yes, Live, "From real provider error and rate-limit events.", []Ref{d(providers), t(bbTest, "TestObserveStructuredRateLimit")}},
			"claude":  {Yes, Fixture, "From api_error_status, terminal_reason and subtype. A real usage-limit result has not been captured.", []Ref{t(runnerTest, "TestClaudeErrorClassification")}},
			"codex":   {Yes, Live, "Live for a usage limit, read from the app-server's structured snapshot because the event stream carries it only as prose. Other classes are fixture-only.", []Ref{d(providers), t(runnerTest, "TestCodexTurnLimitClassifiedFromAppServerNotProse")}},
			"cursor":  {Missing, Live, "Every failure is unknown: the CLI has no structured error contract. A live usage limit and an untrusted directory were both an exit code and stderr text. Connection records add a diagnostic category only.", []Ref{d("docs/cursor.md"), t(cursorTest, "TestCursorFailuresRemainUnknown"), t(cursorTest, "TestCursorConnectionRecordsAreDiagnosticOnly")}},
			"process": {Yes, Fixture, "From errorClass in the status JSON or the exit_codes map.", []Ref{t(processTest, "TestObserveExitCodeMap")}},
		})},
	{ID: "restricted", Title: "Refusals and restrictions hold", What: "Recognise, from a structured field, a provider denying permission (restricted) or a model or provider declining the turn (refused). Either holds for a person at once and never causes a handoff; a person's resume asks for exactly one more try. A refusal no structured field reports stays unknown or looks like a finished turn.",
		Cells: merge(map[string]Cell{
			"bb":      {Partial, Fixture, "Restricted: a provider error with HTTP 403, a permission code or oauth_org_not_allowed, in any field, including BB's category \"unauthorized\" with status 403. Refused: BB's category \"policy\". Shapes read from BB 0.48.1's bundled providers, not captured live. A model refusal that ends a turn normally is not reported by BB in a structured form and looks idle.", []Ref{t("internal/adapter/bb/bb_test.go", "TestObserveForbiddenIsRestricted"), t("internal/adapter/bb/bb_test.go", "TestObservePolicyBlockIsRefused"), t("internal/decide/restricted_test.go", "TestDeclinedHoldsAndNeverMoves")}},
			"claude":  {Yes, Fixture, "Restricted: HTTP 403 in api_error_status, or a permission code in any result field. Refused: stop_reason \"refusal\", which Claude Code documents for a declined turn, whether or not the result is marked as an error. Neither has been captured from the real CLI.", []Ref{t(runnerTest, "TestClaudeErrorClassification"), t("internal/decide/restricted_test.go", "TestDeclinedHoldsAndNeverMoves")}},
			"codex":   {Partial, Fixture, "Restricted: an event carrying HTTP 403 or a permission code. Refused: the codes cyberPolicy and misalignmentPolicyViolation, or the type policy. Whether codex exec prints those codes was not captured; a failure carried only as a message is unknown or looks like a finished turn.", []Ref{t(runnerTest, "TestParseCodexEventsRestricted"), t("internal/decide/restricted_test.go", "TestDeclinedHoldsAndNeverMoves")}},
			"cursor":  {Missing, None, "Every failure is unknown.", nil},
			"process": {Yes, Fixture, "When the status command reports errorClass restricted or refused, or an exit code is mapped to either.", []Ref{t(processTest, "TestObserveRestricted"), t("internal/decide/restricted_test.go", "TestDeclinedHoldsAndNeverMoves")}},
		})},
	{ID: "retry_expiry", Title: "Stale retry-notice expiry", What: "Stop honouring a provider's \"retrying\" notice when nothing fresh stands behind it.",
		Cells: merge(map[string]Cell{
			"bb": {Yes, Live, "The age-based expiry is running on one live loop. Dropping a fresh notice at once when BB's retry plugin has declined the failure and has nothing pending is fixture-tested only.", []Ref{d("docs/operations.md#bb-retry-and-stall-handling"), t("internal/adapter/bb/stale_retry_test.go", "TestStaleRetryNoticeOnErroredThreadExpires"), t("internal/adapter/bb/retry_plugin_test.go", "TestDeclinedRetryNoticeIsDroppedAtOnce")}},
		}, same(Cell{NA, None, "A headless turn is one process that is running, finished or gone. The CLI retries inside that process and reports no retry notice that could go stale.", nil}, headless...),
			same(Cell{NA, None, "The status contract has no retry notice.", nil}, "process"))},
	{ID: "reprompt", Title: "Recover a failed turn: re-prompt", What: "After an error, send the task prompt again, with backoff, up to errors.max_reprompts, then ask for a person.",
		Cells: merge(map[string]Cell{
			"bb":      {Yes, Fixture, fakeDecision + " A real retry exhaustion and explicit resume were observed once.", []Ref{t(superTest, "TestErrorRepromptsThenAwaitsUser"), d(providers)}},
			"claude":  {Yes, Fixture, "Run end to end with a stand-in CLI.", []Ref{t(offlineTest, "TestScenarioHeadlessOutage")}},
			"codex":   {Yes, Fixture, "Run end to end with a stand-in CLI.", []Ref{t(offlineTest, "TestScenarioHeadlessOutage")}},
			"cursor":  {Yes, Live, "A failed turn is re-prompted in its verified session. Live once after a usage-limit failure and after a stop; connection loss is fixture-only.", []Ref{d("docs/cursor.md"), t("internal/adapter/runner/cursor_recovery_test.go", "TestCursorRecoversInitializedSessionAfterWrapperCrash"), t(cursorTest, "TestCursorConnectionLossKeepsSessionAndStaysUnknown")}},
			"process": {Yes, Fixture, fakeDecision, []Ref{t(superTest, "TestErrorRepromptsThenAwaitsUser")}},
		})},
	{ID: "session_resume", Title: "Recover a failed turn: exact session resume", What: "Continue the same conversation by its saved identifier, never \"the most recent\".",
		Cells: merge(map[string]Cell{
			"bb":      {NA, None, "The configured thread is the conversation; every message goes to that thread ID.", nil},
			"claude":  {Yes, Fixture, "--resume with the saved session ID. If no turn ever reported one, the next turn starts a new session.", []Ref{t(runnerTest, "TestClaudeTurnLifecycle"), t(offlineTest, "TestScenarioHeadlessOutage")}},
			"codex":   {Yes, Fixture, "exec resume with the saved thread ID. If no turn ever reported one, the next turn starts a new session.", []Ref{t(runnerTest, "TestCodexResumeArgs"), t(offlineTest, "TestScenarioHeadlessOutage")}},
			"cursor":  {Yes, Live, "--resume with the saved session ID, live once across three turns. A new conversation is refused when the previous turn's identity is unverified, unless that turn exited 1 having written nothing on a CLI version whose start order is verified.", []Ref{d("docs/cursor.md"), t(cursorTest, "TestCursorSessionSafety"), t(cursorTest, "TestCursorFreshStartNeedsExactShapeAndVerifiedVersion")}},
			"process": {NA, None, "The resume command owns session identity.", nil},
		})},
	{ID: "turn_retry", Title: "Recover a failed turn: retry the turn", What: "Ask the provider to retry the failed turn itself, instead of sending a new prompt.",
		Cells: merge(map[string]Cell{
			"bb": {Yes, Fixture, "A re-prompt of a thread in a network, overloaded or unknown error runs `bb thread retry`, which re-submits the failed turn by reference with no new prompt, under the re-prompt cap, backoff and send allowance. The prompt is sent only when BB reports no failed turn or has no such command. No live thread has been retried by keepgoing.",
				[]Ref{t("internal/adapter/bb/turn_retry_test.go", "TestRetryFailedTurnReadsOnlyTheStructuredResult"), t("internal/supervise/turnretry_test.go", "TestFailedTurnIsRetriedByReferenceNotPromptedAgain"), t("internal/supervise/turnretry_test.go", "TestTurnRetryAfterAnOutageOfAnyLength"), t("cmd/keepgoing/turn_retry_test.go", "TestTickRetriesAFailedBBTurnByReference"), d("docs/operations.md#recovering-a-failed-bb-turn-by-retry")}},
		}, same(Cell{Missing, None, "A re-prompt is always a new turn in the resumed session.", nil}, headless...),
			same(Cell{Missing, None, "The contract has a resume command only.", nil}, "process"))},
	{ID: "network", Title: "Connection loss and the network check", What: "Tell an outage from a provider failure, wait it out without spending re-prompts or probes, and act on the first check after it returns.",
		Cells: merge(map[string]Cell{
			"bb":      {Yes, Fixture, "The supervisor checks connectivity before a re-prompt, handoff, hold for an errored thread or probe, and waits while it fails. Run for outages of 20 minutes to 3 weeks with a stand-in bb.", []Ref{t(offlineBBTest, "TestOfflineForAnyLengthThenRecoversOnce"), d(networkDoc)}},
			"claude":  {Yes, Fixture, "As for BB. The wake that ran into the outage is spent; nothing else is, and no hold is raised. Run end to end with a stand-in CLI.", []Ref{t(offlineTest, "TestScenarioHeadlessOutage"), d(networkDoc)}},
			"codex":   {Yes, Fixture, "As for Claude Code.", []Ref{t(offlineTest, "TestScenarioHeadlessOutage"), d(networkDoc)}},
			"cursor":  {Yes, Fixture, fakeDecision, []Ref{t(networkTest, "TestOutageDoesNotSpendRepromptsOrProbes"), d(networkDoc)}},
			"process": {Yes, Fixture, fakeDecision, []Ref{t(networkTest, "TestOutageDoesNotSpendRepromptsOrProbes"), d(networkDoc)}},
		})},
	{ID: "child_awareness", Title: "Child-work awareness", What: "Hold a parent that is not in error while conversations it started are still working, and say which. A parent in error is recovered regardless.",
		Cells: merge(map[string]Cell{
			"bb": {Yes, Live, "Direct child threads are listed and verified on each check.", []Ref{d(waitingLive), t("internal/adapter/bb/children_test.go", "TestObserveActiveChildSuppressesParentUntilFinished")}},
		}, same(Cell{NA, None, "A headless turn has no child conversations outside its own process. Subagents the CLI starts live and end inside the turn.", nil}, headless...),
			map[string]Cell{"process": {Partial, Fixture, "A background count in the status JSON holds the loop. No child identities are reported.", []Ref{t(processTest, "TestObserveJSON")}}})},
	{ID: "child_notice", Title: "Child failure notice and report", What: "Tell the parent conversation, and optionally the owner, when a child fails.",
		Cells: merge(map[string]Cell{
			"bb": {Yes, Fixture, "Running on one live loop; a notice has not been captured there.", []Ref{t("internal/adapter/bb/children_test.go", "TestChildWorkListsErroredChildren"), d("docs/operations.md")}},
		}, same(Cell{NA, None, "No child conversations.", nil}, headless...), same(Cell{NA, None, "No child identities are reported.", nil}, "process"))},
	{ID: "handoff_source", Title: "Handoff away from it", What: "When it is the active target and hits a configured error class, move the loop to a confirmed fallback.",
		Cells: merge(map[string]Cell{
			"bb":      {Yes, Live, "A rate-limited primary handed off on a live loop.", []Ref{d(runsLive), t(superTest, "TestHandoffToFirstCandidatePassingExactProbe")}},
			"claude":  {Yes, Fixture, fakeDecision, []Ref{t(superTest, "TestHandoffToFirstCandidatePassingExactProbe")}},
			"codex":   {Yes, Fixture, fakeDecision, []Ref{t(superTest, "TestHandoffToFirstCandidatePassingExactProbe")}},
			"cursor":  {Missing, None, "Configuration refuses fallbacks for a Cursor loop, and every Cursor failure is unknown, which never fails over.", []Ref{d("docs/cursor.md")}},
			"process": {Yes, Fixture, fakeDecision, []Ref{t(superTest, "TestHandoffToFirstCandidatePassingExactProbe")}},
		})},
	{ID: "handoff_target", Title: "Handoff to it", What: "Be a fallback candidate that takes the work over after an exact probe.",
		Cells: merge(map[string]Cell{
			"bb":      {Yes, Live, "An existing thread; its model can be switched before the brief.", []Ref{d(runsLive), t(bbTest, "TestSendStopActivate")}},
			"claude":  {Yes, Fixture, "Guided setup creates it; run only against stand-in CLIs.", []Ref{d("docs/fallbacks.md")}},
			"codex":   {Yes, Fixture, "Guided setup creates it; run only against stand-in CLIs.", []Ref{d("docs/fallbacks.md")}},
			"cursor":  {Missing, None, "Cursor fallback candidates are refused: there is no account and model probe contract.", []Ref{d("docs/cursor.md")}},
			"process": {Yes, Fixture, fakeDecision, []Ref{t(superTest, "TestHandoffToFirstCandidatePassingExactProbe")}},
		})},
	{ID: "switch_back", Title: "Switch-back", What: "Return to the primary once its limit has lifted, when enabled.",
		Cells: merge(map[string]Cell{
			"bb":      {Yes, Fixture, fakeDecision, []Ref{t(superTest, "TestSwitchBackAfterThePrimaryResets")}},
			"claude":  {Yes, Fixture, fakeDecision, []Ref{t(superTest, "TestSwitchBackAfterThePrimaryResets")}},
			"codex":   {Yes, Fixture, fakeDecision, []Ref{t(superTest, "TestSwitchBackAfterThePrimaryResets")}},
			"cursor":  {Missing, None, "Refused with fallbacks.", []Ref{d("docs/cursor.md")}},
			"process": {Yes, Fixture, fakeDecision, []Ref{t(superTest, "TestSwitchBackAfterThePrimaryResets")}},
		})},
	{ID: "probes", Title: "Probes", What: "Check a candidate's account and model with a small real request before relying on it.",
		Cells: merge(map[string]Cell{
			"bb":      {Yes, Live, "A BB candidate is probed through its provider's CLI (Claude Code or Codex), not through BB.", []Ref{d(providers), t("internal/probe/probe_test.go", "TestClaudeProbe")}},
			"claude":  {Yes, Live, "One real healthy probe.", []Ref{d(providers), t("internal/probe/probe_test.go", "TestClaudeProbe")}},
			"codex":   {Yes, Live, "A real probe, and a real probe on a limited account.", []Ref{d(providers), t("internal/probe/probe_test.go", "TestCodexProbe")}},
			"cursor":  {Missing, None, "No probe contract.", []Ref{d("docs/cursor.md")}},
			"process": {Yes, Fixture, "A command probe.", []Ref{t("internal/probe/probe_test.go", "TestCommandProbe")}},
		})},
	{ID: "quota", Title: "Quota readings", What: "Record how much of a usage window is used, from structured fields a probe already returns.",
		Cells: merge(map[string]Cell{
			"bb":      {Partial, Live, "Readings come from the candidate's provider probe. BB's own rate-limit events supply reset times only.", []Ref{d("docs/fallbacks.md"), d(providers)}},
			"claude":  {Yes, Live, "One real manual probe recorded a reading.", []Ref{d(providers), t("internal/probe/probe_test.go", "TestClaudeProbeJudgesStreamAndPlainOutputAlike")}},
			"codex":   {Partial, Live, "A reading only when the existing limit read runs; none on a healthy probe.", []Ref{t("internal/probe/probe_test.go", "TestCodexProbeRecordsUsageOnlyFromTheExistingLimitRead")}},
			"cursor":  {Missing, None, "No probe contract.", nil},
			"process": {Missing, None, "A command probe reports health only.", nil},
		})},
	{ID: "new_conversation", Title: "Handoff into a new conversation", What: "Take the work over in a conversation that did not exist before the handoff.",
		Cells: merge(map[string]Cell{
			"bb":      {Yes, Fixture, "Opt-in: a candidate with bb.new_thread has keepgoing create the thread, within loop.limits.max_new_threads.", []Ref{t("internal/supervise/newthread_test.go", "TestHandoffToANewThreadCreatesItOnce"), d("docs/fallbacks.md")}},
			"claude":  {Yes, Fixture, "A headless candidate has no saved session, so the handoff brief starts one.", []Ref{t(runnerTest, "TestClaudeTurnLifecycle")}},
			"codex":   {Yes, Fixture, "A headless candidate has no saved session, so the handoff brief starts one.", []Ref{t(runnerTest, "TestCodexTurn")}},
			"cursor":  {Missing, None, "Not a fallback candidate.", nil},
			"process": {NA, None, "The resume command decides which conversation receives the brief.", nil},
		})},
	{ID: "run_graph", Title: "Run graph and keepgoing runs", What: "Record runs, sends and the conversation reference, and show them offline.",
		Cells: merge(map[string]Cell{
			"bb": {Yes, Live, "The configured thread, labelled configured.", []Ref{d(runsLive), t(graphTest, "TestTickRecordsRunIntentBeforeSendSessionAndGoal")}},
		}, same(Cell{Yes, Fixture, "The runner's saved resume ID, labelled observed.", []Ref{t(graphTest, "TestHeadlessTargetRecordsItsSavedSession")}}, headless...),
			map[string]Cell{"process": {Partial, Fixture, "Runs and sends are recorded. No conversation reference exists to record.", []Ref{t(graphTest, "TestHeadlessTargetRecordsNoChildListing")}}})},
	{ID: "stop", Title: "Stop and owned cancellation", What: "Stop a running turn on request, signalling only a process keepgoing itself started and still owns.",
		Cells: merge(map[string]Cell{
			"bb":      {Yes, Live, "bb thread stop on the configured thread.", []Ref{d(providers), t(bbTest, "TestSendStopActivate")}},
			"claude":  {Yes, Fixture, "The live turn wrapper stops its own process group on a private per-turn request. A saved PID never authorizes a signal.", []Ref{t(runnerTest, "TestStopKillsTurnAndCrashIsUnknown"), d("docs/testing.md#lifecycle-and-platform-review")}},
			"codex":   {Yes, Fixture, "The same wrapper as Claude Code.", []Ref{t(runnerTest, "TestStopKillsTurnAndCrashIsUnknown")}},
			"cursor":  {Yes, Live, "The same wrapper. Live once on macOS; a helper process Cursor leaves running per project is not managed.", []Ref{d("docs/cursor.md"), t(cursorTest, "TestCursorOwnedStopOnThisPlatform")}},
			"process": {Partial, Fixture, "Only if a stop command is configured; keepgoing owns no process to cancel.", []Ref{t(processTest, "TestStopUnsupported")}},
		})},
	{ID: "permissions", Title: "Permission prompts", What: "Notice that the agent is waiting for a person to approve something, hold, and report it once.",
		Cells: merge(map[string]Cell{
			"bb":      {Yes, Live, "From the interaction list; shapes come from real lifecycle events.", []Ref{d(providers), t(bbTest, "TestPendingInteractionMeansWaitingOnAPerson")}},
			"claude":  {Missing, None, "permission_mode is passed to the CLI. A headless turn cannot wait for approval and reports no typed permission state.", []Ref{d("docs/configuration.md")}},
			"codex":   {Missing, None, "The sandbox setting is passed to the CLI. No typed permission state is read.", []Ref{d("docs/configuration.md")}},
			"cursor":  {Missing, None, "No force or approval flag is added, and print mode has no typed permission-wait signal. A rejection has not been observed live.", []Ref{d("docs/cursor.md")}},
			"process": {Yes, Fixture, "awaitingInput in the status JSON.", []Ref{t(processTest, "TestObserveAwaitingInput")}},
		}, map[string]Cell{"antigravity": {Missing, Fixture, "An internal ACP gate cancels permission requests and saves a hold, and an unregistered runner reports that hold once its worker has exited, with stand-in fixtures only. No adapter is registered.", []Ref{d("docs/antigravity.md")}}})},
}

const (
	definedTest     = "internal/adapter/runner/definition_unix_test.go"
	definedReadTest = "internal/adapter/runner/definition_test.go"
	definedScenario = "internal/scenario/defined_agent_test.go"
	definedDoc      = "docs/configuration.md#an-agent-defined-in-configuration"
)

// customCells is the column for an agent defined in configuration. What it
// can do depends on what its definition declares, so most cells are partial:
// the note says which declaration the behaviour needs. Every fixture uses a
// stand-in; no real agent has been run from a definition.
var customCells = map[string]Cell{
	"observe":          {Yes, Fixture, "The turn's process state, its exit code and the JSON fields the definition names. Output over the definition's limit is not read.", []Ref{t(definedTest, "TestDefinedAgentTurnLifecycle"), t(definedTest, "TestDefinedAgentOutputBound")}},
	"error_classes":    {Partial, Fixture, "With structured_errors: the codes, HTTP status field and exit codes the definition maps. Everything else, and every failure without that declaration, is unknown.", []Ref{t(definedTest, "TestDefinedAgentErrorClassification"), t(definedReadTest, "TestDefinitionReadings")}},
	"retry_expiry":     {NA, None, "A turn is one process that is running, finished or gone, as for the built-in headless agents.", nil},
	"reprompt":         {Yes, Fixture, "Run end to end with a stand-in agent.", []Ref{t(definedScenario, "TestDefinedAgentJourney")}},
	"session_resume":   {Partial, Fixture, "With exact_resume: the resume command with the saved session identifier, read from a declared field or file. A saved identifier that is no longer usable holds the loop for a person; no new conversation is started in its place. Without exact_resume every turn starts a new session.", []Ref{t(definedTest, "TestDefinedAgentTurnLifecycle"), t(definedTest, "TestDefinedAgentLostSessionHoldsInsteadOfStartingAnother"), t(definedReadTest, "TestDefinitionSessionFile")}},
	"turn_retry":       {Missing, None, "A re-prompt is always a new turn.", nil},
	"network":          {Yes, Fixture, "As for the built-in headless agents. The turn that ran into the outage is spent; nothing else is sent and no hold is raised until the network returns. Run end to end with a stand-in agent.", []Ref{t(definedScenario, "TestDefinedAgentOutage"), d(networkDoc)}},
	"child_awareness":  {NA, None, "A definition cannot declare work that outlives a turn, so none is assumed.", nil},
	"child_notice":     {NA, None, "No child conversations.", nil},
	"handoff_source":   {Yes, Fixture, fakeDecision, []Ref{t(superTest, "TestHandoffToFirstCandidatePassingExactProbe")}},
	"handoff_target":   {Partial, Fixture, "With a probe in the definition, or a command probe. Guided setup does not create it; the candidate is written by hand.", []Ref{t("internal/config/custom_test.go", "TestCustomAgentDefinition"), d(definedDoc)}},
	"switch_back":      {Yes, Fixture, fakeDecision, []Ref{t(superTest, "TestSwitchBackAfterThePrimaryResets")}},
	"probes":           {Partial, Fixture, "With a probe in the definition: one small request with the prompt passed as a turn's is, judged by the finished and error rules.", []Ref{t("internal/probe/custom_test.go", "TestCustomProbe")}},
	"quota":            {Missing, None, "No usage reading is taken. A limit lookup, when defined, only says whether an unclassified failure is a usage limit and when it resets.", []Ref{t(definedTest, "TestDefinedAgentLimitLookup")}},
	"new_conversation": {Yes, Fixture, "A candidate has no saved session, so the handoff brief starts one.", []Ref{t(definedTest, "TestDefinedAgentTurnLifecycle")}},
	"run_graph":        {Partial, Fixture, "Runs and sends are recorded. The session identifier is the agent's own shape and is not shown.", []Ref{t(definedReadTest, "TestDefinitionBecomesASpecification")}},
	"stop":             {Partial, Fixture, "With can_be_stopped: the same wrapper as the built-in headless agents. Without it a stop is refused and nothing is signalled.", []Ref{t(definedTest, "TestDefinedAgentStop")}},
	"permissions":      {Partial, Fixture, "With structured_waiting: a turn that ends on the declared waiting rule raises one awaiting-user hold. Nothing is approved or answered.", []Ref{t(definedTest, "TestDefinedAgentWaitingRaisesOneHold")}},
}

func init() {
	for i := range Matrix {
		if c, ok := customCells[Matrix[i].ID]; ok {
			Matrix[i].Cells["custom"] = c
		}
	}
}

// Render returns the generated part of docs/adapters.md.
func Render() string {
	var b strings.Builder
	b.WriteString("| Behaviour |")
	for _, a := range Adapters {
		fmt.Fprintf(&b, " %s |", a.Title)
	}
	b.WriteString("\n| --- |")
	for range Adapters {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")
	for _, row := range Matrix {
		fmt.Fprintf(&b, "| [%s](#%s) |", row.Title, anchor(row.Title))
		for _, a := range Adapters {
			c := row.Cells[a.ID]
			if c.Status == NA {
				b.WriteString(" n/a |")
			} else {
				fmt.Fprintf(&b, " %s · %s |", c.Status, c.Evidence)
			}
		}
		b.WriteString("\n")
	}
	for _, row := range Matrix {
		fmt.Fprintf(&b, "\n### %s\n\n%s\n\n", row.Title, row.What)
		for _, a := range Adapters {
			c := row.Cells[a.ID]
			fmt.Fprintf(&b, "- **%s:** %s", a.Title, c.Status)
			if c.Status != NA {
				fmt.Fprintf(&b, ", evidence %s", c.Evidence)
			}
			fmt.Fprintf(&b, ". %s", c.Note)
			for _, r := range c.Refs {
				name := r.Path[strings.LastIndex(r.Path, "/")+1:]
				link := "../" + r.Path
				if strings.HasPrefix(r.Path, "docs/") {
					link = strings.TrimPrefix(r.Path, "docs/")
				}
				if r.Test != "" {
					fmt.Fprintf(&b, " [`%s`](%s)", r.Test, link)
				} else {
					fmt.Fprintf(&b, " [%s](%s)", name, link)
				}
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func anchor(title string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			out.WriteRune(r)
		case r == ' ':
			out.WriteRune('-')
		}
	}
	return out.String()
}
