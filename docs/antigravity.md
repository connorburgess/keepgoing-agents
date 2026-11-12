# Antigravity integration foundation

**Internal, unreleased, offline fixtures only.** Keepgoing has no registered Antigravity adapter or guided-setup choice. This foundation adds a headless prompt encoder/lifecycle parser, an internal ACP permission gate and stream exchange, and a saved interaction hold for future integration. No production code path calls them: nothing starts an agent, reads credentials, attaches to an IDE chat, or changes permissions.

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

The [headless permission contract](https://antigravity.google/docs/cli/headless/#permissions-in-headless-mode) allows a run to return successfully after a tool action is denied because approval is unavailable. It describes notices on stderr. Keepgoing does not parse those notices. A successful result therefore proves neither that all requested tools ran nor that the task finished.

This foundation grants no permissions and makes no sandbox guarantee. A future adapter must establish structured permission evidence or a supported interaction bridge before ordinary unattended setup is exposed. The official [ACP guide](https://antigravity.google/docs/ide/extensions/acp) documents permission requests, cancellation, steering and saved-session operations, making it a candidate for further investigation. That interface does not establish keepgoing ownership or safe attachment to another already-running IDE conversation.

## Internal ACP permission gate

The official [provider ACP guide](https://antigravity.google/docs/ide/extensions/acp) identifies a structured interaction path: permission requests pause a turn, cancellation abandons them, and sessions can be resumed. Existing permission rules still apply without prompting. Clients without form support receive questions in chat. These facts make ACP a candidate; they do not establish an unattended adapter or clearance of every requested action.

`AntigravityACPGate` is a pure in-memory protocol component. It starts no process and reads no credentials or runtime files. It encodes one [ACP v1 initialization](https://agentclientprotocol.com/protocol/v1/initialization), declaring keepgoing's identity and no optional client capabilities. It requires the agent's [exact-resume capability](https://agentclientprotocol.com/protocol/v1/session-setup) before either fresh creation or resume. Saved opaque IDs are preserved exactly; a failed resume never becomes a fresh session. Session setup must return before one prompt is encoded. No second prompt can be encoded on that gate.

A correlated [permission request](https://agentclientprotocol.com/protocol/v1/tool-calls) latches `NeedsUser`, emits `session/cancel` once, and answers each pending permission request with a cancelled outcome. It never selects an allow or deny option. Ordinary pending tool updates and message content do not count as requests. The latch survives subsequent completion, cancellation, malformed evidence or disconnection. The gate itself keeps this evidence in memory only; the [saved interaction hold](#saved-interaction-hold) is what carries it across a restart.

Only a matching prompt response with `end_turn` and no latched interaction marks a returned turn. Other documented [stop reasons](https://agentclientprotocol.com/protocol/v1/prompt-turn) do not grant return eligibility; unknown reasons fail closed. Neither return nor process success establishes task completion. Unsupported client requests, wrong sessions, duplicate/ambiguous control fields and unmatched responses fail closed without a failover classification. Message, tool and error prose never determine an outcome.

Incoming and encoded frames are capped at 1 MiB, incoming connection evidence at 4 MiB/4096 frames, and permission requests at 64. IDs have a 256-byte bound and reject control characters and lossy Unicode escapes. Permission options are bounded and validated, but never selected. The gate consumes complete frames; the internal exchange below bounds framing before calling it. Other transports must apply the same read bounds. It is not a general ACP client and does not accept arbitrary extensions or replay streams.

Unattended operation remains unavailable. A future owned worker must connect the exchange to private stdin/stdout and implement bounded draining and termination, exact send/session reconciliation and turn rollover on top of the saved hold. Structured [elicitation](https://agentclientprotocol.com/protocol/v1/elicitation) needs a real user handoff; the gate advertises none. Modes where questions fall back to chat must refuse unattended setup until a typed alternative exists. No transcript parser, automatic permission grant or invented event can fill that gap.

## Internal stream exchange

`ExchangeAntigravityACP` accepts an injected reader/writer and drives a single handshake, session setup and prompt. Its [ACP v1 framing](https://agentclientprotocol.com/protocol/v1/transports) requires UTF-8 JSON messages ending in LF; stderr is never protocol input. Fragmented reads are assembled within the gate's size/count limits, using a fixed-size read buffer. An incomplete frame, EOF before the prompt result, malformed evidence or a short/failed write fails closed. Writes are never replayed, including when a prefix might already have reached the peer. Local session/cwd/prompt validation occurs before initialization; new-session prompt preflight reserves the maximum bounded session-ID encoding overhead.

A required checkpoint callback runs before the prompt and before each batch of permission cancellation/reply frames. The first record contains exact session identity and a pending prompt attempt, not proof of delivery. Subsequent records assert human-input evidence without clearing earlier reservations or holds. The caller must bind records to its target/turn and return only after durable persistence; callback failure prevents subsequent writes. The exchange stores nothing itself; the saved interaction hold below is the one implementation of this callback, and no runner calls it yet.

The function is synchronous, creates no goroutines, and owns no process or stream closure. Context is checked between operations; the caller must use deadlines or close its transport to interrupt blocked reads/writes and must bound its checkpoint callback. This is a byte/count bound, not a wall-time guarantee. The caller must close or terminate on failure and must not automatically replay an ambiguous attempt. The exclusive connection is not reusable through this API because buffered data may extend past the terminal response. A returned turn establishes neither process exit nor task acceptance.

## Saved interaction hold

`Dir.AntigravityACPHoldCheckpoint` implements the exchange's checkpoint callback for one target directory and one caller-supplied turn identity. Each fact is its own small file beside the other turn files: `acp-prompt-reserved.json` when a prompt write is reserved and `acp-needs-user.json` when a permission request was cancelled for a person. Each holds only a version and the exact turn and session identities: no prompt text, permission option, tool detail or provider output. A file is written and synced under a private temporary name, then published with an exclusive hard link, so it is complete before it is visible and exists before the prompt frame or any cancellation frame leaves the process. That survives process exit and restart. It is not a power-loss or storage guarantee, the sync call is not fault-tested, and a filesystem without hard links fails closed.

Nothing replaces or removes a record. A reader therefore never blocks a writer, including on Windows, and no update can lose another. Because the reservation is an exclusive create, at most one exchange for a target is told it may write a prompt, whether the others are restarts or still-running duplicates. A prompt after human-input evidence, another turn, another session, and any unreadable, oversized, duplicated, non-regular, unknown-version or mutually inconsistent record are refused and left as they are. A refused restart has still sent `initialize` and session setup before reaching the checkpoint; a runner should read the record first. There is deliberately no rollover to a later turn yet: that needs the send/session reconciliation a runner does not have, so this is one exchange per target, not a usable loop. A reservation whose prompt was never written looks the same as one that was, and blocks the target equally.

An adapter that reads these records reports `DeferredInteraction` while the evidence is unadopted. Unlike a still-pending provider interaction, which the decision engine waits on, this maps to an awaiting-user hold with a fixed reason. On a tick that observes the target, the supervisor saves that hold in `control.json` before scheduled probes, redundancy, the progress fingerprint and recovery, as it does for pending child input. The earlier tick gates still come first: an existing hold, the deadline, the send allowance, a configured no-progress check and a busy yield can each end a tick before observation, without a send and without this hold. After saving it, the supervisor calls the adapter's optional `AdoptInteraction`, which publishes `acp-hold-adopted.json` for the same turn and session.

Adoption is what separates an explicit resume from an automatic restart. While the hold stands, restarts neither observe nor send. If the supervisor fails after saving the hold but before adopting it, the evidence raises the hold once more after the operator resumes; the cost is a second resume, never a lost hold or a send. Once adopted, a resume is not held again by the same evidence. Resuming approves nothing and does not make the cancelled turn eligible to continue. Stop and pause are handled first and leave the evidence unadopted, so it still holds when they are lifted. A hold owned by another rule, such as pending child input or a missing target, does not adopt it. An adoption file without the evidence it names is treated as unreadable rather than trusted.

Human-input evidence is recorded once per turn. Further permission requests while the same cancelled turn drains add nothing, also after adoption and resume; a runner must keep reporting that turn as running until its worker has exited. Handoff and switch-back send to a target without consulting these records, so a future adapter must stay primary-only or check them first. A deferred interaction does not request an early daemon check; it is held on the next ordinary tick.

Only fixtures reach this path: a real stream exchange writing the real records, and supervisor ticks through a stand-in adapter that reads them. No registered adapter reports the observation, and incident records classify the decision cause as `other` until a runtime adapter needs a typed one.

## Remaining acceptance gates

- Verify the installed CLI version/platform and its structured permission behavior. No installed `agy` executable or live provider was exercised for this foundation.
- Add a runner using the existing owned cancellation path, with exact-session storage, one turn per send, restart reconciliation of an unconfirmed prompt reservation, turn rollover, controls and no duplicate sends. Fixture coverage must include real local stand-in processes before live checks.
- Establish typed authentication, quota/reset and exact account/model probe evidence before claiming those capabilities. Unknown errors must remain unknown. No stderr or model-response matching may fill a missing interface.
- Add capability-aware setup and diagnostics only after the runtime contract is implemented. Inherited login is not evidence of separate-account isolation.
- Run an authorized scratch-project two-turn/resume/stop walkthrough, including permission failure and holds. Do not manufacture a quota limit or change existing logins/timers. Existing graphical-chat attachment remains a separate investigation.

## Verification

`go test -race ./internal/adapter/runner -run '^TestAntigravity'` exercises fictional streams and stdin envelopes: non-ASCII/control-character round trips, encoded-size bounds, exact identity, partial output, every documented terminal state, conflicting exits, malformed/duplicate output, and typed-looking model/tool text that must not influence classification. These tests do not execute Antigravity or prove provider/platform compatibility.

`go test -race ./internal/adapter/runner -run '^TestAntigravityACP'` covers fictional handshake/new/resume exchanges, string/numeric request correlation, cancellation and sticky input evidence, stop reasons, malformed/duplicate/wrong-session evidence, Unicode identities, frame/connection/request bounds and model-text isolation. It executes no ACP subprocess and verifies no live provider, persisted hold or restart behavior.

`go test -race ./internal/adapter/runner -run '^TestAntigravityACPExchange|^TestAntigravityACPFrameReader'` additionally exercises fragmented streams, a two-way in-memory pipe peer, prompt/hold checkpoint ordering and failures, short writes without replay, EOF/truncation, local-input preflight and connection limits. These are injected transport/peer fixtures, not an Antigravity subprocess or restart/durability proof.

`go test -race ./internal/adapter/runner -run '^TestAntigravityACPHold'`, `go test -race ./internal/decide -run '^TestDeferredInteraction'` and `go test -race ./internal/supervise -run '^TestDeferredInteraction'` cover the saved hold. A fictional permission exchange must find the records on disk before its prompt and cancellation frames; reconstructed readers and exchanges then verify refusal of a second prompt, another turn or session, unchanged file contents after every refusal, bounded Unicode identities, malformed input in each of the three files, inconsistent files and turn-bound adoption. Eight concurrent reservations admit exactly one prompt, and evidence and adoption are saved while every existing record is held open by a reader. Supervisor fixtures rebuild the adapter on every tick and cover dry runs, the saved control hold and report, repeated restarts without observation or sends, explicit resume, a simulated failure between hold and adoption, an unsaved hold, stop/pause precedence and holds owned by other rules. The adapter in those ticks is a stand-in; no ACP subprocess, owned process lifecycle, process kill, question handoff or live provider is exercised.

The README's current availability table remains accurate: no built-in Antigravity adapter or IDE-chat attachment is available. The integration plan and this document identify the narrower implementation; the full roadmap item remains open.
