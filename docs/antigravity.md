# Antigravity integration foundation

**Internal, unreleased, offline fixtures only.** Keepgoing has no registered Antigravity adapter or guided-setup choice. This foundation adds a headless prompt encoder/lifecycle parser and an internal ACP permission gate for future integration. It does not start an agent, read credentials, attach to an IDE chat, save a session, or change permissions.

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

A correlated [permission request](https://agentclientprotocol.com/protocol/v1/tool-calls) latches `NeedsUser`, emits `session/cancel` once, and answers each pending permission request with a cancelled outcome. It never selects an allow or deny option. Ordinary pending tool updates and message content do not count as requests. The latch survives subsequent completion, cancellation, malformed evidence or disconnection. This is in-memory evidence only: nothing yet persists an awaiting-user hold in the supervisor.

Only a matching prompt response with `end_turn` and no latched interaction marks a returned turn. Other documented [stop reasons](https://agentclientprotocol.com/protocol/v1/prompt-turn) do not grant return eligibility; unknown reasons fail closed. Neither return nor process success establishes task completion. Unsupported client requests, wrong sessions, duplicate/ambiguous control fields and unmatched responses fail closed without a failover classification. Message, tool and error prose never determine an outcome.

Incoming and encoded frames are capped at 1 MiB, incoming connection evidence at 4 MiB/4096 frames, and permission requests at 64. IDs have a 256-byte bound and reject control characters and lossy Unicode escapes. Permission options are bounded and validated, but never selected. The gate consumes complete frames; a future transport must bound its reads before allocating frames. It is not a general ACP client and does not accept arbitrary extensions or replay streams.

Unattended operation remains unavailable. A future owned worker must implement two-way private stdin/stdout, bounded draining and termination, durable interaction holds across restart and exact send/session reconciliation. Structured [elicitation](https://agentclientprotocol.com/protocol/v1/elicitation) needs a real user handoff; the gate advertises none. Modes where questions fall back to chat must refuse unattended setup until a typed alternative exists. No transcript parser, automatic permission grant or invented event can fill that gap.

## Remaining acceptance gates

- Verify the installed CLI version/platform and its structured permission behavior. No installed `agy` executable or live provider was exercised for this foundation.
- Add a runner using the existing owned cancellation path, with exact-session storage, one turn per send, restart reconciliation, controls and no duplicate sends. Fixture coverage must include real local stand-in processes before live checks.
- Establish typed authentication, quota/reset and exact account/model probe evidence before claiming those capabilities. Unknown errors must remain unknown. No stderr or model-response matching may fill a missing interface.
- Add capability-aware setup and diagnostics only after the runtime contract is implemented. Inherited login is not evidence of separate-account isolation.
- Run an authorized scratch-project two-turn/resume/stop walkthrough, including permission failure and holds. Do not manufacture a quota limit or change existing logins/timers. Existing graphical-chat attachment remains a separate investigation.

## Verification

`go test -race ./internal/adapter/runner -run '^TestAntigravity'` exercises fictional streams and stdin envelopes: non-ASCII/control-character round trips, encoded-size bounds, exact identity, partial output, every documented terminal state, conflicting exits, malformed/duplicate output, and typed-looking model/tool text that must not influence classification. These tests do not execute Antigravity or prove provider/platform compatibility.

`go test -race ./internal/adapter/runner -run '^TestAntigravityACP'` covers fictional handshake/new/resume exchanges, string/numeric request correlation, cancellation and sticky input evidence, stop reasons, malformed/duplicate/wrong-session evidence, Unicode identities, frame/connection/request bounds and model-text isolation. It executes no ACP subprocess and verifies no live provider, persisted hold or restart behavior.

The README's current availability table remains accurate: no built-in Antigravity adapter or IDE-chat attachment is available. The integration plan and this document identify the narrower implementation; the full roadmap item remains open.
