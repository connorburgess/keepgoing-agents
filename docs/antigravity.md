# Antigravity integration

**Primary-only standalone adapter (unreleased source).** `adapter: antigravity` runs `agy acp --stdio` through the detached ACP runner (`runner.Antigravity`) with checkpointed prompt reservations, exact `session/resume`, owned process-tree stops, and durable `DeferredInteraction` holds on permission requests. `init --agent antigravity` creates a disabled loop. Fallbacks, switch-back, timing probes, and IDE-chat attachment remain unavailable.

**Live CLI evidence (`agy 1.5.13`, macOS arm64):** `agy --version`, `agy acp --stdio` `initialize` (`sessionCapabilities.resume: {}`), a two-turn scratch-dir `session/new` → `session/resume` check (`stopReason: "end_turn"`, exit `0`), and `agy --print /usage --mode plan --output-format json` were verified on macOS arm64. The installed macOS two-turn/resume/stop walkthrough is recorded below; live permission-hold and installed Linux/Windows checks remain open; see [Installed testing and debugging](#installed-testing-and-debugging).

For coordinator and lane model choices and quota habits on Antigravity and Gemini 4, see [Coordinating lanes — Antigravity and Gemini 4 model tiers](coordinating-lanes.md#antigravity-and-gemini-4-model-tiers).

## Protocol contract

The official [headless guide](https://antigravity.google/docs/cli/headless/) documents NDJSON input/output and lifecycle envelopes. The encoder prepares one user message for stdin. A future caller must select both streaming formats, omit `-p`, and close stdin after that message. Task text stays out of arguments.

The parser accepts one init, optional steps, and one result with a consistent conversation UUID. A supplied saved ID must match exactly. `--conversation` is the documented exact-resume option; `--continue` selects the most recent conversation and is unsuitable for target identity. See the [CLI reference](https://antigravity.google/docs/cli/reference/).

Terminal interpretation requires known process-exit evidence:

| Evidence | Internal interpretation |
| --- | --- |
| `SUCCESS`, exit 0 | Turn returned; idle |
| `SUCCESS`, nonzero exit | Unknown error |
| `WAITING` | Pending human input |
| `ERROR`, `CANCELED`, `INTERRUPTED`, `INVALID` | Unknown error; no inferred failover class |
| `RUNNING` after exit, unknown status, incomplete output or unavailable exit | Unknown |

A valid init without a result retains its identity as incomplete evidence. Malformed input, duplicate lifecycle keys/events, mismatched IDs, unsupported event types or data after the result invalidate the outcome. Inputs/streams are capped at 4 MiB and must be valid UTF-8. The encoder checks the encoded size as well as the source text. A future runner must also bound its file reads; this parser does not implement capture or file retention.

Only transport fields affect the outcome. Response text, error prose, tool output and model-produced `structured_output` never provide provider health, limits, reset times or completion authority. An unfamiliar status is not retained as free text.

## Permissions remain a delivery gate

The [headless permission contract](https://antigravity.google/docs/cli/headless/#permissions-in-headless-mode) allows a run to return successfully after a tool action is denied because approval is unavailable. It describes notices on stderr. looptender does not parse those notices. A successful result therefore proves neither that all requested tools ran nor that the task finished.

This foundation grants no permissions and makes no sandbox guarantee. A future adapter must establish structured permission evidence or a supported interaction bridge before ordinary unattended setup is exposed. The official [ACP guide](https://antigravity.google/docs/ide/extensions/acp) documents permission requests, cancellation, steering and saved-session operations, making it a candidate for further investigation. That interface does not establish looptender ownership or safe attachment to another already-running IDE conversation.

## Internal ACP permission gate

The official [provider ACP guide](https://antigravity.google/docs/ide/extensions/acp) identifies a structured interaction path: permission requests pause a turn, cancellation abandons them, and sessions can be resumed. Existing permission rules still apply without prompting. Clients without form support receive questions in chat. These facts make ACP a candidate; they do not establish an unattended adapter or clearance of every requested action.

`AntigravityACPGate` is a pure in-memory protocol component. It starts no process and reads no credentials or runtime files. It encodes one [ACP v1 initialization](https://agentclientprotocol.com/protocol/v1/initialization), declaring looptender's identity and no optional client capabilities. It requires the agent's [exact-resume capability](https://agentclientprotocol.com/protocol/v1/session-setup) before either fresh creation or resume. Saved opaque IDs are preserved exactly; a failed resume never becomes a fresh session. Session setup must return before one prompt is encoded. No second prompt can be encoded on that gate.

A correlated [permission request](https://agentclientprotocol.com/protocol/v1/tool-calls) latches `NeedsUser`, emits `session/cancel` once, and answers each pending permission request with a cancelled outcome. It never selects an allow or deny option. Ordinary pending tool updates and message content do not count as requests. The latch survives subsequent completion, cancellation, malformed evidence or disconnection. The gate itself keeps this evidence in memory only; the [saved interaction hold](#saved-interaction-hold) is what carries it across a restart.

Only a matching prompt response with `end_turn` and no latched interaction marks a returned turn. Other documented [stop reasons](https://agentclientprotocol.com/protocol/v1/prompt-turn) do not grant return eligibility; unknown reasons fail closed. Neither return nor process success establishes task completion. Unsupported client requests, wrong sessions, duplicate/ambiguous control fields and unmatched responses fail closed without a failover classification. Message, tool and error prose never determine an outcome.

Incoming and encoded frames are capped at 1 MiB, permission requests at 64, pre-prompt updates at 256 and post-cancel drain frames at 1024; ordinary streaming updates during an active turn are bounded per message rather than across the whole turn. IDs have a 256-byte bound and reject control characters and lossy Unicode escapes. Permission options are bounded and validated, but never selected. The gate consumes complete frames; the internal exchange below bounds framing before calling it. Other transports must apply the same read bounds. It is not a general ACP client and does not accept arbitrary extensions or replay streams.

General unattended acceptance remains open. The registered [detached runner](#detached-runner) connects the exchange to private stdin/stdout with bounded setup, write, cancel-drain and exit limits, send/session reconciliation and turn rollover on top of the saved hold. The remaining permission and question limitations follow. Structured [elicitation](https://agentclientprotocol.com/protocol/v1/elicitation) needs a real user handoff; the gate advertises none. Modes where questions fall back to chat must refuse unattended setup until a typed alternative exists. No transcript parser, automatic permission grant or invented event can fill that gap.

## Internal stream exchange

`ExchangeAntigravityACP` accepts an injected reader/writer and drives a single handshake, session setup and prompt. Its [ACP v1 framing](https://agentclientprotocol.com/protocol/v1/transports) requires UTF-8 JSON messages ending in LF; stderr is never protocol input. Fragmented reads are assembled within the gate's per-frame limit, using a fixed-size read buffer. An incomplete frame, EOF before the prompt result, malformed evidence or a short/failed write fails closed. Writes are never replayed, including when a prefix might already have reached the peer. Local session/cwd/prompt validation occurs before initialization; new-session prompt preflight reserves the maximum bounded session-ID encoding overhead.

A required checkpoint callback runs before the prompt and before each batch of permission cancellation/reply frames. The first record contains exact session identity and a pending prompt attempt, not proof of delivery. Subsequent records assert human-input evidence without clearing earlier reservations or holds. The caller must bind records to its target/turn and return only after durable persistence; callback failure prevents subsequent writes. The exchange stores nothing itself; the saved interaction hold below is the one implementation of this callback, and the [detached runner](#detached-runner) is its only caller.

The function is synchronous, creates no goroutines, and owns no process or stream closure. Context is checked between operations; the caller must use deadlines or close its transport to interrupt blocked reads/writes and must bound its checkpoint callback. This is a byte/count bound, not a wall-time guarantee. The caller must close or terminate on failure and must not automatically replay an ambiguous attempt. The exclusive connection is not reusable through this API because buffered data may extend past the terminal response. A returned turn establishes neither process exit nor task acceptance.

## Saved interaction hold

`Dir.AntigravityACPHoldCheckpoint` implements the exchange's checkpoint callback for one target directory and one caller-supplied turn identity. Each fact is its own small file beside the other turn files: `acp-prompt-reserved.json` when a prompt write is reserved and `acp-needs-user.json` when a permission request was cancelled for a person. Each holds only a version and the exact turn and session identities: no prompt text, permission option, tool detail or provider output. A file is written and synced under a private temporary name, then published with an exclusive hard link, so it is complete before it is visible and exists before the prompt frame or any cancellation frame leaves the process. That survives process exit and restart. It is not a power-loss or storage guarantee, the sync call is not fault-tested, and a filesystem without hard links fails closed. The temporary name is removed afterwards with a short retry. A `.acp-hold-*` file can still remain if the process dies first, or on Windows if a reader has the new record open at that moment; it holds the same two identities as the records, is never read, and is not cleaned up, because another process's unpublished file looks the same.

Nothing replaces or removes a published record. A reader therefore never blocks a writer, including on Windows, and no update can lose another. Because the reservation is an exclusive create, at most one exchange for a target is told it may write a prompt, whether the others are restarts or still-running duplicates. Human-input evidence requires the same turn's reservation, so two exchanges cannot leave records that disagree. The checkpoint and the reader apply one consistency rule: reservation, then evidence, then adoption, all naming the same turn and session. A prompt after human-input evidence, another turn, another session, and any unreadable, oversized, duplicated, non-regular, unknown-version or inconsistent record are refused and left as they are, so no frame is released on top of records the supervisor could not read. A refused restart has still sent `initialize` and session setup before reaching the checkpoint; a runner should read the record first. The records themselves never roll over: one directory holds one exchange. The [detached runner](#detached-runner) gives each turn its own directory and reconciles an unconfirmed reservation before it starts another. A reservation whose prompt was never written looks the same as one that was, and blocks the target equally.

An adapter that reads these records reports `DeferredInteraction` while the evidence is unadopted. Unlike a still-pending provider interaction, which the decision engine waits on, this maps to an awaiting-user hold with a fixed reason. On a tick that observes the target, the supervisor saves that hold in `control.json` before scheduled probes, redundancy, the progress fingerprint and recovery, as it does for pending lane input. The earlier tick gates still come first: an existing hold, the deadline, the send allowance, a configured no-progress check and a busy yield can each end a tick before observation, without a send and without this hold. After saving it, the supervisor calls the adapter's optional `AdoptInteraction`, which publishes `acp-hold-adopted.json` for the same turn and session.

Adoption is what separates an explicit resume from an automatic restart. While the hold stands, restarts neither observe nor send. If the supervisor fails after saving the hold but before adopting it, the evidence raises the hold once more after the operator resumes; the cost is a second resume, never a lost hold or a send. Once adopted, a resume is not held again by the same evidence. Resuming approves nothing and does not make the cancelled turn eligible to continue. Stop and pause are handled first and leave the evidence unadopted, so it still holds when they are lifted. A hold owned by another rule, such as pending lane input or a missing target, does not adopt it. An adoption file without the evidence it names is treated as unreadable rather than trusted.

Human-input evidence is recorded once per turn. Further permission requests while the same cancelled turn drains add nothing, also after adoption and resume; a runner must keep reporting that turn as running until its worker has exited. Handoff and switch-back send to a target without consulting these records, so a future adapter must stay primary-only or check them first. A deferred interaction does not request an early daemon check; it is held on the next ordinary tick.

Permission-hold evidence currently comes from fixtures: the real stream exchange writes the real records, and supervisor ticks exercise their adoption and restart behavior with a stand-in agent. The registered detached runner reports the same observation for configured primary loops; the live permission-hold walkthrough remains open.

## Detached runner

`runner.Antigravity` is a primary-only adapter type for one ACP conversation. YAML configuration and the adapter factory select it with `adapter: antigravity`; guided setup exposes `init --agent antigravity`. The configured executable defaults to `agy`, invoked with `acp --stdio`. Setup creates a disabled loop and does not attach to an existing IDE chat. It has no probe, usage-limit, reset-time or fallback role.

Each send starts one detached worker through the same wrapper the other runners use, and returns without waiting. The worker starts the agent in its own process group (Unix) or nested job object (Windows) with private pipes for stdin, stdout and stderr, drives one `ExchangeAntigravityACP`, and exits:

- **Private structured stdin and bounded stderr.** The prompt is written to the private prompt file, then framed as one ACP message on the agent's stdin. It is never an argument. The protocol stream is not stored; the agent's stderr is copied into a file capped at 64 KiB that nothing reads.
- **One turn per send, with bounded turn retention.** Send records a random turn identity in `acp-turn.json` and creates `acp-turns/<turn>/`. The saved hold records live there, so they stay create-once while later turns roll over to new directories; once a new turn is recorded, older turn directories beyond the current and immediately preceding turn are removed. The worker refuses to run for any turn but the one recorded last, and checks again before it writes its prompt and before it writes the target's shared exit record, so a worker overtaken by a later turn does neither.
- **Confirmed worker and unconfirmed recovery.** Before spawning the worker, Send records the turn's per-turn stop key in `acp-pending.json`; after spawning, it publishes `acp-started.json` with that key, or `acp-not-started.json` when no process could be created. The run record must carry the confirmed key. A turn with neither confirmation record is unconfirmed, so observation is unknown and every send is refused until cleared. `Stop` can still stop a live unconfirmed worker through its recorded stop key, and `ClearUnconfirmed` stops any running unconfirmed worker, waits for exit, preserves any saved session or unadopted hold, and writes create-once `acp-cleared.json` so a later send can roll over.
- **Checkpointed prompt attempt.** The hold checkpoint publishes `acp-prompt-reserved.json` before the prompt frame is written and `acp-needs-user.json` before any permission cancellation. After the exchange the worker publishes `acp-outcome.json` (turn, conversation and three booleans: returned, needs a person, failed) and then the agent's exit status.
- **Exact conversation resume.** The conversation identity is opaque and kept byte for byte in `acp-session.json`, written once from the first reservation. Every later turn resumes it; a turn that names another conversation is refused, and a saved conversation never falls back to a new one. If no prompt was ever reserved, nothing left the worker, and the next send may start a new conversation.
- **No duplicate sends.** A running turn refuses a send. An earlier turn whose worker is gone is rolled over, never replayed: a reservation without an outcome means delivery is unknown, the observation is an unknown error, and a later send is a new prompt in the same conversation.
- **Owned stop.** Stop writes a request for the live worker's per-turn key; the worker ends both the agent's process group or nested job and its own group or job. A saved process ID is never signalled.
- **Saved hold.** After the worker has exited, unadopted human-input evidence is reported as `DeferredInteraction`, and a send is refused until the supervisor adopts it. A cancelled turn still running is reported as running until it drains or hits `CancelTimeout`. After adoption the turn is an ordinary unknown failure.
- **Fail closed.** Unreadable or disagreeing records (another turn, another conversation, a return without a reservation, a stray protocol output file) make the observation unknown with a fixed error and refuse sends.

An observation is idle only when the worker recorded exit 0, the reservation exists without human-input evidence, and the outcome says the turn returned. Everything else that is not running is an error of class `unknown`: other stop reasons, protocol failures, a killed or failed agent, a missing outcome. No structured field in this path classifies a failure, so none is classified; stderr and message text are never read.

Time limits: observing, sending and stopping inside a check do bounded file work and return; stop waits at most three seconds for the worker's acknowledgement, as for the other runners, and each honours a cancelled check. Inside the worker, four limits are enforced by terminating the agent's entire process group (Unix) or nested job object (Windows) and closing the pipes: session setup (`HandshakeTimeout`, default two minutes), individual stdin frame writes (`WriteTimeout`, default ten seconds, preventing pipe-buffer deadlock on large prompts), draining after a permission cancellation (`CancelTimeout`, default ten seconds, so a saved hold is surfaced promptly), and agent exit after the turn (`ExitTimeout`, default ten seconds); all four are configurable up to ten minutes. Active streaming turns are bounded per frame (1 MiB) and by permission-request count (64), not by a whole-turn frame or byte total.

<a id="what-blocks-registration"></a>

### Remaining permission and platform gates

Registration and guided setup are implemented. Per-message turn bounds, cancel-drain timeouts, process-group/job termination, unconfirmed-worker recovery, retention limits and write deadlines have fixture coverage. Registration does not close these remaining acceptance gaps:

1. **Questions asked in chat.** The gate advertises no elicitation, so an agent that needs an answer may ask in its message text and end the turn normally. That is indistinguishable from a returned turn, and a loop would wake it again instead of holding for a person. No typed evidence for this exists, and message text is never read.
2. **Permission rules that apply without a request.** Existing rules can allow or deny a tool with no permission request. The absence of a request therefore proves nothing about what ran, and only a request can raise the hold.
3. **Remaining installed checks.** macOS `agy 1.5.13` command, resume and stop evidence is recorded below. A live permission-hold walkthrough and installed Linux/Windows checks remain open. Native stand-in execution does not establish real provider behavior on those platforms.

Unconfirmed-worker recovery is exposed through `looptender session clear-unconfirmed -c loop.yaml`. It preserves saved session and unadopted hold evidence; clearing uncertainty is not task completion or permission approval.

## Live CLI verification (`agy 1.5.13`, macOS arm64)

Verified against an installed `agy 1.5.13` binary on macOS arm64 (2026-11-19):

- **`agy --version`** returns `1.5.13`.
- **`agy acp --stdio`** starts the ACP v1 stdio server (`Antigravity ACP Server 1.5.13`). Its `initialize` response returns `protocolVersion: 1` and advertises `agentCapabilities.sessionCapabilities.resume: {}`, matching `AntigravityACPGate`. Closing stdin exits cleanly with code `0`.
- **Two-turn exact resume (scratch temp dir):** `session/new` returned a 36-byte conversation ID and `session/prompt` returned `stopReason: "end_turn"` with exit `0`; a fresh `agy acp --stdio` process calling `session/resume` with that exact ID returned `stopReason: "end_turn"` (with cached read tokens confirming conversation continuity) and exit `0`.
- **Model-free `/usage`:** `agy --print /usage --mode plan --output-format json` returned `status: "SUCCESS"`, `num_turns: 0`, `command.name: "usage"`, buckets `gemini-weekly`, `gemini-5h`, `3p-weekly`, `3p-5h`, and exit `0`, matching the parser in `cmd/looptender/accounts_antigravity_quota.go`.

## Local and remote workspaces

The adapter starts `agy acp --stdio` on the device running looptender, using the configured working directory and that device's provider context. A remote Antigravity project is a separate execution environment: creating it or attaching a repository does not install either CLI or attach looptender to its chat. The current adapter has no remote-project selector or existing-chat attachment operation. Verify CLI availability on the intended device before setup; do not substitute a local path for a remote workspace.

## Configuration (primary-only standalone loop)

`adapter: antigravity` is registered as a primary-only standalone loop adapter (`fallbacks`, `switch_back`, and `timing` are rejected). `looptender` runs `agy acp --stdio` with optional `--sandbox`, `--agent`, and `--add-dir` flags, and **never** passes `--dangerously-skip-permissions`:

```yaml
version: 1
loop:
  name: example-antigravity
  state_dir: state
  adapter: antigravity
  antigravity:
    cli: agy           # optional; defaults to "agy"
    workdir: ./project # required
    sandbox: true      # optional; passes --sandbox
    agent: ""          # optional; passes --agent <name>
    add_dir: []        # optional; repeatable --add-dir (max 16)
    env: {}            # optional extra environment variables
  prompt: Complete the fictional example task.
```

## Installed testing and debugging

After building and installing `looptender` from `main` (for example `go install github.com/looptender/looptender/cmd/looptender@main`, making sure `$(go env GOPATH)/bin` is first on `PATH` or invoking `$(go env GOPATH)/bin/looptender`):

1. **Create a standalone loop with guided setup (or write `loop.yaml`):**
   ```bash
   looptender init --agent antigravity --project /path/to/project --directory /path/to/loop-dir
   ```
   Or create `loop.yaml` directly as shown above.
2. **Verify CLI and loop diagnostics:**
   ```bash
   looptender doctor -c /path/to/loop.yaml
   ```
   Confirm `agy --version` resolves cleanly and review the capability/account notes.
3. **Verify model-free `/usage` account quota:**
   ```bash
   looptender accounts add --label "Example Antigravity" --provider antigravity --selection inherited
   looptender accounts usage --id <ID> --json
   ```
   Confirm `state: "reported"`, `source: "agy:/usage"`, `freshness: "provider_unknown"`, and the four Gemini / Claude-GPT windows.
4. **Scratch-directory two-turn, hold, and stop walkthrough:**
   - Create a temporary scratch directory and `loop.yaml` with `adapter: antigravity` and `antigravity.workdir` pointing to the scratch directory.
   - Enable the loop (`looptender enable -c loop.yaml`), run `looptender tick -c loop.yaml` for turn 1, wait for the worker to exit, and run `looptender tick -c loop.yaml` for turn 2; confirm `acp-session.json` holds the exact conversation ID and turn 2 resumes it.
   - Test a turn that requires a tool permission: confirm the loop enters `AwaitingUser` hold (`DeferredInteractionReason`) and does not send again until `looptender resume -c loop.yaml`.
   - Test `looptender stop -c loop.yaml -immediately` during an active turn and confirm the `agy acp --stdio` process group exits cleanly.
   - **Live installed walkthrough (2026-11-20, `agy 1.5.13`, macOS arm64):** verified `looptender accounts add`/`usage` (all 4 buckets reported), `looptender init --non-interactive --agent antigravity`, `looptender doctor -c` (`0600` config/task, CLI version `1.5.13`), `looptender enable -c`, turn 1 `wake` → `active` (no duplicate send) → `idle` with `0600` `acp-session.json` and zero stderr, turn 2 `wake` resuming the saved session, and `looptender stop -c <config> -immediately` stopping an active turn cleanly (`decision: stop; outcome succeeded`). Live permission-hold walkthrough and Linux/Windows installed checks remain open.
5. **Debugging turn state on disk** (under `<state_dir>/targets/primary/`):
   - `acp-turn.json` — current turn identity.
   - `acp-session.json` — exact opaque conversation ID written once on the first prompt reservation.
   - `acp-turns/<turn>/` — per-turn records (current and immediately preceding turn are retained; older turn directories are pruned automatically):
     - `acp-pending.json` — stop key recorded before worker spawn.
     - `acp-started.json` / `acp-not-started.json` / `acp-cleared.json` — worker confirmation/recovery records.
     - `acp-prompt-reserved.json` — durable prompt reservation written before the prompt frame leaves the worker.
     - `acp-needs-user.json` / `acp-hold-adopted.json` — saved permission-request hold and supervisor adoption.
     - `acp-outcome.json` — boolean transport outcome (`returned`, `needsUser`, `failed`; never prompt or response text).
   - `stderr` — bounded 64 KiB capture from `agy acp --stdio` for manual inspection (never parsed by `looptender`).
   - If a crash left a turn unconfirmed (`acp-pending.json` without `acp-started.json` or `acp-not-started.json`), the loop fails closed with `errAntigravityUnconfirmed` until cleared with `looptender session clear-unconfirmed -c loop.yaml`.

## Remaining acceptance gates

- Complete the remaining installed scratch-project checks (live tool permission hold + `looptender resume`) and test on Linux and Windows.
- Resolve chat-only questions without ACP elicitation (currently indistinguishable from a returned `end_turn`) and permission rules that allow/deny without emitting a request.
- Establish typed authentication, exact account/model probe evidence, and separate-account isolation before enabling fallbacks or switch-back. Existing graphical IDE-chat attachment remains a separate investigation.

## Verification

`go test -race ./internal/adapter/runner -run '^TestAntigravity'` exercises fictional streams and stdin envelopes: non-ASCII/control-character round trips, encoded-size bounds, exact identity, partial output, every documented terminal state, conflicting exits, malformed/duplicate output, and typed-looking model/tool text that must not influence classification.

`go test -race ./internal/adapter/runner -run '^TestAntigravityACP'` covers fictional handshake/new/resume exchanges, string/numeric request correlation, cancellation and sticky input evidence, stop reasons, malformed/duplicate/wrong-session evidence, Unicode identities, long streaming turns alongside per-frame/permission/pre-prompt/cancel-drain bounds, and model-text isolation.

`go test -race ./internal/adapter/runner -run '^TestAntigravityACPExchange|^TestAntigravityACPFrameReader'` additionally exercises fragmented streams, a two-way in-memory pipe peer, prompt/hold checkpoint ordering and failures, short writes without replay, EOF/truncation, local-input preflight, long streaming turns and per-frame/cancel-drain limits.

`go test -race ./internal/adapter/runner -run '^TestAntigravityACPHold'`, `go test -race ./internal/decide -run '^TestDeferredInteraction'` and `go test -race ./internal/supervise -run '^TestDeferredInteraction'` cover the saved hold, concurrent reservations, adoption, and restart resilience.

`go test -race ./internal/adapter/runner -run '^TestAntigravityRunner'` runs the detached runner against a stand-in ACP agent across two-turn exact resume, permission holds, `CancelTimeout`, `WriteTimeout`, descendant process-group/job termination, 64 KiB stderr cap, `acp-turns/` pruning, `ClearUnconfirmed` recovery, and malformed record rejection on macOS, Linux, and Windows CI.

`go test -race ./internal/supervise -run '^TestAntigravityRunnerSupervise'` and `go test -race ./internal/adapter/conformance -run '^TestAntigravityRunnerKeepsTheContract'` run full supervisor ticks and the shared adapter conformance suite directly against `runner.Antigravity` with a stand-in ACP agent: two-turn exact resume across rebuilt adapters, permission-request hold/adoption/resume, unknown error non-failover, owned stop, and all applicable adapter conformance contracts (with zero known gaps, including `bounded-output`).

`go test -race ./internal/config -run '^TestAntigravity'` and `go test -race ./internal/adapter/factory -run '^TestFactoryBuildsAntigravityRunner'` verify YAML defaults, path resolution, primary-only constraints, JSON schema definitions, and factory wiring.
