# Cursor CLI supervision

The Cursor adapter is **unreleased source**, with offline fixture coverage. Published v0.1.5 does not contain it. Guided `init` selection and capability-aware `doctor` checks are implemented in source. One CLI version has been inspected on one platform; live provider acceptance, fallback probes, automatic limit/reset recovery and existing Cursor IDE chat attachment remain unfinished. One authorized [live walkthrough](#live-walkthrough-c4) covered first turn, exact resume and stop on that version; permission rejection was not observable.

This adapter starts a separate Cursor CLI conversation in a chosen project and resumes that exact conversation on later turns. Open the project in any editor; this does not attach to an existing editor chat. keepgoing supplies the repeated task and common controls; no separate loop skill is required.

## Configure a primary loop

With a build containing this adapter, use [guided setup](setup.md#cursor-setup-unreleased-source) with `init --agent cursor`, or place the configuration and runtime directory outside the checkout:

```yaml
version: 1
loop:
  name: cursor-example
  state_dir: ~/.local/state/keepgoing/cursor-example
  adapter: cursor
  cursor:
    cli: agent
    workdir: ~/src/example-app
    model: example-model
  prompt_file: ./task.md
  limits: {max_sends: 10}
```

Replace `example-model` with a model available to your account, or omit the model field to use Cursor's configured default. keepgoing does not refresh the model catalog or select a replacement automatically. Use the existing `doctor`, `start`, `status`, `pause`, `stop`, `await-user` and `complete` commands with this configuration. Setup starts disabled until explicitly enabled or started. Guided setup discovers the `agent` executable, never the desktop `cursor` launcher. It supports private task import, optional `--with-goal`, `--model`, bounds and previews without invoking Cursor. It rejects `--account-dir` and `--fallback`; the wizard skips unsupported account/fallback questions.

The `cursor` block accepts `cli` (default `agent`), required `workdir`, optional `model`, and optional `env` overrides. Paths follow the normal [configuration rules](configuration.md). Credentials can come from the existing Cursor login or inherited `CURSOR_API_KEY`; keep secrets outside the repository and prefer the launching environment over storing credentials in YAML. No login, account creation, credential migration or independent account-directory scheme is performed. Before the first turn, open the project once with Cursor's own CLI and answer its workspace-trust question: in a directory Cursor does not trust, every print-mode turn exits with an error before it starts, and keepgoing never passes `--trust`.

`doctor` resolves the executable, reads its version and says whether that is an [inspected version](#what-the-inspected-cli-exposes). It runs `agent status --format json` and reports a confirmed login only when Cursor returns an authenticated status, all three token flags and an account identity (an email string or a numeric user ID); the identity is never displayed. Stored credentials without an identity, and the signed-out and partially signed-in replies, are reported as unavailable: the first because the CLI answers `authenticated` from stored tokens even when its account lookup fails, the others because those reply shapes were read in the CLI's source and never observed. When `CURSOR_API_KEY` or `CURSOR_AUTH_TOKEN` is present in the loop or launching environment the status command is not run, because it describes the stored login rather than that credential. The model catalog, the configured model, quota, workspace trust and permissions are always shown as unavailable: the CLI offers no structured form of them. `doctor --offline` runs nothing.

## Turns, identity and controls

Each send launches `agent -p --output-format stream-json` through keepgoing's existing detached wrapper. Task text travels through private stdin, never command arguments. A configured model is passed explicitly. Later sends use only the saved session ID with `--resume=<id>`, never the latest conversation. A running turn cannot receive a second queued or steering prompt.

Completion requires a valid initialization, a matching terminal success record with an explicit false `is_error`, and a known zero process exit. This is **turn completion**, not task completion: the loop continues until an explicit control hold or `keepgoing complete` stops further wakes. Message and tool payloads never establish lifecycle or error state.

Initialized identity is preserved after a partial turn when the stream remains well formed. Conflicting IDs, duplicate top-level keys, invalid metadata and unavailable session storage refuse automatic resumption. A previously started turn without a verified session ID cannot silently start a new conversation, with one exception. When the previous turn finished with exit code 1, its output file exists and is empty, and the installed CLI is a version whose start order is verified, the next bounded re-prompt starts a conversation. That is the observed shape of a refusal before startup, such as an untrusted workspace. The verified versions are the [inspected ones](#what-the-inspected-cli-exposes), in which the CLI writes its initialization and user records before it submits the prompt, plus any listed in `cursor.fresh_start_versions` by an operator who has checked that for their version. The version that counts is the one that ran the failed turn: keepgoing runs `agent --version` as it starts each turn and saves the answer beside the turn's records (`cursor-version`), so a CLI that updates itself before the re-prompt does not change the verdict. A turn with no such record, including one started by an earlier build, is refused.

This rests on keepgoing's own record of the turn, not on a statement from Cursor that no conversation began; the CLI offers none (no exit code specific to the refusal, no structured session listing). A `cursor.cli` that points at a wrapper which discards output and exits 1 would look the same, and would be given the task again. Everything else keeps the refusal: any other exit code, including 127, 130 and 143; any output; a missing output file; a crashed wrapper; a turn whose CLI version is unverified, unreadable or was not recorded. In those cases each re-prompt fails with the reason, and after `errors.max_reprompts` the loop holds for a person.

Once that hold is reached, `keepgoing resume` alone holds again, as for every adapter: the re-prompt count of an error episode restarts only when a turn ends without an error. After fixing the cause, raise `errors.max_reprompts` by one and resume.

Stop uses the existing [owned cancellation channel](operations.md#owned-headless-cancellation), not a saved PID. Pause, awaiting-user and completed holds prevent further sends; pause does not cancel a running turn. One live stop ended the real CLI and the processes in the wrapper's group on macOS; a separate helper process that Cursor leaves running per project is outside that group and is not managed.

## Permissions and unsupported recovery

keepgoing never adds `--force`, `--yolo`, `--approve-mcps`, or `--trust`. Cursor's existing workspace trust, permissions and approval configuration still apply. Print mode can edit permitted workspace files and run permitted commands; it rejects actions that require interactive approval. Existing unrestricted settings can allow more actions. This adapter does not offer an interactive permission bridge or infer a permission wait from response prose. See [Cursor's headless permissions](https://cursor.com/docs/cli/headless).

Cursor documents failures that emit only a nonzero exit and stderr, potentially without terminal JSON. All failures therefore remain `unknown`; quota, reset, authentication and network text are not classified. Unknown errors use keepgoing's bounded retry policy and never trigger failover. A Cursor loop cannot configure fallbacks/switch-back, and Cursor is rejected as a fallback candidate. See [Cursor's output schema](https://cursor.com/docs/cli/reference/output-format) and [what is missing for a probe](#why-cursor-is-not-a-fallback-candidate).

## What the inspected CLI exposes

Read-only inspection of Cursor CLI `2026.09.18-9a7762b` on macOS 26.4 (arm64): its help, its `status`, `about` and `models` commands, and its bundled source. No model request was made for this table. Other versions and platforms are unverified.

| Question | Structured evidence in this version | How keepgoing uses it |
| --- | --- | --- |
| Is there a login? | `agent status --format json`: `status` (`authenticated`, `partially-authenticated`, `unauthenticated`), `isAuthenticated`, `hasAccessToken`, `hasRefreshToken`, and `userInfo` when the account lookup succeeded. The source answers `authenticated` from stored tokens even when that lookup fails. | `doctor` only. Confirmed needs the status, all three flags and an identity. |
| Which account? | `userInfo.email`, `userInfo.userId` in the same reply; `about --format json` has `userEmail` and `subscriptionTier`. | Presence only, never the value. Not tied to a turn: a print-mode stream names no account, only `apiKeySource`. |
| Which CLI version? | `--version` prints one token; `about --format json` has `cliVersion`, `latestStatus`, `latestVersion`. | `doctor` reads `--version` and compares it with the inspected versions. |
| Which models? | `agent models` and `--list-models` print human-readable `id - Name` lines. There is no JSON form (`models --format json` is rejected). | Not parsed. Shown as unavailable. |
| Which model ran a turn? | The stream's `system`/`init` record has `model`, which is the display name, not the configured ID. | Not used: an ID cannot be matched to a display name without the text catalog. |
| Did a turn succeed? | One `result` record with `subtype: "success"`, `is_error: false` and the session ID, then exit 0 (observed live). The source emits no other `result` shape. | Turn completion, with the matching initialization and a known zero exit. |
| Why did a turn fail? | Nothing structured. The source prints the error text to stderr and exits 1 (130 when interrupted). No error record, code or class. Observed live for an untrusted directory and for a usage limit. | Every failure is `unknown`. Stderr is never read. |
| Rate limit, quota, reset time? | Nothing structured, in the stream or in any command. A live usage-limit failure was one stderr line after `init` and `user` records. | Unavailable. Never inferred. |
| Connection trouble inside a turn? | Unpublished `connection` records (`reconnecting` with an attempt number, `reconnected`) and `retry` records (`starting`, `resuming`) in `stream-json` output. | Diagnostic only; see [connection loss](#connection-loss-during-a-turn). |
| A turn whose wrapper never started | keepgoing's own record: the turn is marked abandoned under the turn lock, so no process ran it. | The next send starts normally, as after a turn that was never started. |
| Workspace trust, permissions? | Nothing structured. An untrusted directory prints a notice to stderr and exits 1 with empty stdout (observed live). | Unavailable. Exit 1 with empty output on a verified version allows a later fresh start. |

## Why Cursor is not a fallback candidate

A fallback is only used after an exact probe has confirmed that this account and this model work now, and the loop only leaves a target on a classified error. Cursor's CLI does not provide that contract:

1. **No structured model identity.** A probe turn would report a display name. The only mapping from the configured model ID to that name is the human-readable catalog, which keepgoing does not parse, so a probe cannot confirm it exercised the configured model.
2. **No account bound to a turn.** `status` can name the stored login, but a print-mode turn reports only `apiKeySource`, and there is no documented way to run two Cursor accounts side by side (keepgoing's other fallbacks rely on a separate account directory per candidate).
3. **No failure classes.** A failed probe or turn is an exit code and stderr text. Rate limit, authentication, overload and network cannot be told apart, so a Cursor target could never justify a handoff away from it either.
4. **No check without a model request.** `status` confirms a login, not model access or remaining quota.

Until Cursor exposes these, `fallbacks.yaml` and guided `fallbacks add` keep refusing Cursor, and a Cursor loop keeps refusing fallbacks.

## Connection loss during a turn

From the inspected source, not from a live outage: while it tries to restore a dropped connection the CLI writes `connection`/`reconnecting` records and may write `retry` records; if it gives up it prints the error to stderr and exits 1 without a `result` record. If it reconnects it writes `connection`/`reconnected` and the turn can still end with a success record.

keepgoing treats these records as follows. They are not part of Cursor's published schema, so they never invalidate a stream, establish identity, mark success or change the error class. A failed turn whose last connection record for its session is `reconnecting` gets the diagnostic category `connection_error`; its class stays `unknown`, so it is retried within `errors.max_reprompts` and `errors.backoff` and can never trigger a handoff. Each re-prompt is one new turn resuming the saved session with `--resume=<id>`; nothing is sent to a turn that is still running. A turn that reconnects and succeeds is an ordinary completed turn.

Offline fixtures drive this through the real commands with a native stand-in. What only a live outage can confirm: that the CLI emits these records at all when the network drops, how long it retries before exiting, its exit code in that case, and whether the resumed session then contains the interrupted prompt once or twice. keepgoing sends one prompt per re-prompt decision; it cannot see whether Cursor recorded the interrupted one.

## Evidence and next gates

Official documentation provides [print/resume/model parameters](https://cursor.com/docs/cli/reference/parameters) and a separate [ACP interface](https://cursor.com/docs/cli/acp). ACP provides structured permissions and cancellation for custom clients; it is not implemented here and does not prove attachment to existing IDE chats.

Read-only local inspection of Cursor CLI `2026.09.18-9a7762b` on macOS confirmed the version/help interface. Isolated evaluation of its installed prompt-building function with fictional Latin/CJK input confirmed stdin-only UTF-8 input, internal newlines and trimming of boundary whitespace. That inspection made no provider request. It is transport evidence for that installed version, not a supported-version range; the live lifecycle evidence is the [walkthrough](#live-walkthrough-c4) below.

Offline subprocess fixtures cover exact session reuse after reconstruction, independent targets, Unicode paths/stdin, active-send rejection, owned stop, missing output, nonzero and malformed results, ambiguity, output bounds and common holds. A combined command journey reloads config/control/state and reconstructs the adapter on every invocation: active pause/await-user/complete/disable holds, completion while held, stop precedence, exact resumption after stop, retry backoff and retry-budget exhaustion all use a native stand-in executable. A separate fixture kills its own retained wrapper process handle after initialization, waits for the disposable child to exit, then reconstructs and resumes the verified session. Those Go fixtures reconstruct command/adapter state; a separate [binary recovery smoke](release-verification.md#check-recovery-with-local-stand-ins), enabled with `--include-cursor`, starts actual temporary daemons, kills held supervisors and restarts them against the same state. It verifies persistent awaiting-user/completed holds, stale endpoint replacement, unknown-error history, unchanged ownership/send accounting and no alert/completion replay. Explicit resume then reuses the exact session UUID across successful stand-in turns. No service is installed and the provider remains fictional. Live first turn, exact resume and stop are recorded in the [walkthrough](#live-walkthrough-c4); supervisor restart during a live turn, permission rejection and outage behavior are still unverified. Setup fixtures also verify CLI discovery, existing default selection, disabled task/goal creation, private stdin/JSON previews, rejection before writes and no provider invocation. Native platform execution and release evidence must be recorded separately before recommending this path for unattended work.

## Live walkthrough (C4)

Run once on 2026-11-13 with the owner's authorization: a build of this branch, Cursor CLI `2026.09.18-9a7762b`, macOS 26.4 (arm64), the existing stored login, a new empty scratch project outside every repository, a scratch state directory and `limits: {max_sends: 5}`. The task asked for one line to be created in, or appended to, one file. Paths, the account and the conversation are not recorded here. Seven CLI turns were started: two in a directory Cursor did not yet trust, which made no request, and five counted sends afterwards, three of which completed a model turn.

| Step | What Cursor did | What keepgoing did |
| --- | --- | --- |
| First turn, directory not trusted | Exit 1, nothing on stdout, a workspace-trust notice on stderr. No session, no model request. | `error`, class `unknown`. Before the rule above existed, every later send was refused; with it, the next re-prompt started a turn, which failed the same way. |
| Trust | `agent` was run once in the scratch directory and its trust question accepted, with no prompt sent. Cursor created a per-project folder under its own data directory holding a `.workspace-trusted` file (`trustedAt`, `workspacePath`). | Nothing; keepgoing never passes `--trust`. |
| Turn with a model over its usage limit | `system`/`init` and `user` records, then exit 1 with one line on stderr saying the usage limit was reached and when it resets. No `result` record, no error record, no code. | `error`, class `unknown`; the session ID from `init` was saved. No handoff, no probe, no reset time. This is the real shape of a Cursor limit: nothing structured. |
| Re-prompt on Cursor's default model | The same session resumed: `init` (`apiKeySource: "login"`, `permissionMode: "default"`, `model` as a display name), `user`, `thinking` records, `assistant`, `tool_call` started/completed, then `result` with `subtype: "success"`, `is_error: false`, `usage` token counts and a request ID; exit 0. The file was created. | Bounded re-prompt with `--resume=<saved id>`; the stream's session ID matched the saved one; `idle` afterwards. |
| Second completed turn | Same record shapes, exit 0, one line appended. | Ordinary idle wake. Every record in the stream carried the saved session ID, and the saved ID did not change. |
| Stop during a turn | Five seconds into the turn the CLI had written nothing to stdout. After the stop, the wrapper and the three processes in its group were gone; no exit record, empty stdout and stderr. The file was not changed. | `stop` on the next check, within a second; a matching stop acknowledgement was written; later checks reported the stop and sent nothing. |
| Resume after the stop | Same session again, exit 0, one more line appended (three in total). | `keepgoing resume`, then a re-prompt, because the stopped turn left no exit record and is observed as an unknown error; `--resume=<saved id>`. |
| Send limit | Not contacted. | The sixth check held the loop as awaiting-user at `max_sends: 5`. |

What this establishes for that version and platform: first turn, exact resume across three later turns including after a failed turn and after a stop, owned stop of the real CLI, the success record's fields, and that both an untrusted directory and a usage limit are stderr-only failures.

What it does not establish:

- **Permission rejection was not observed.** In print mode the CLI ran its shell tool without asking for approval under this account's existing settings, so there was nothing to reject. Observing a rejection needs an account or project whose settings require approval; that was not changed.
- Whether the stopped turn's prompt had reached Cursor. Nothing was written before the stop, and the resumed turn showed no sign of it.
- A network outage, the signed-out and API-key states, other CLI versions, Linux and Windows.

Two things to know when operating it. The model named `auto` was used after the cheapest listed model was refused for usage; the choice of model is the user's. After the last turn exited, a Cursor `worker-server` helper for the project was still running; keepgoing does not start, track or stop it.

Cleanup: the scratch project and state directories were deleted, the leftover helper for the scratch project was stopped, and Cursor's per-project folder for the scratch directory, including its `.workspace-trusted` file, was removed. Nothing else in Cursor's configuration was changed. The conversation itself remains in Cursor's own chat store.

To repeat it: create a new empty directory and a scratch state directory; run `agent` once there and accept the trust question without sending a prompt; write a configuration with `adapter: cursor`, the scratch `workdir`, a model the account can use, a one-line file task and `limits: {max_sends: 5}`; run `keepgoing validate` and `keepgoing doctor`; then `enable` and `tick` for the first turn, `tick` again for the resume, `tick` then `stop` then `tick` for the stop, and `resume` then `tick` for the turn after it. After each turn record the record types and keys, the exit code and whether the stream's session ID equals the saved one, never the ID, the account or the text. Finish by deleting both directories and Cursor's per-project folder for the scratch directory.
