# Cursor CLI supervision

The Cursor adapter is **unreleased source**, with offline fixture coverage. Published v0.1.5 does not contain it. Guided `init` selection and capability-aware `doctor` checks are implemented in source. One CLI version has been inspected on one platform; live provider acceptance, fallback probes, automatic limit/reset recovery and existing Cursor IDE chat attachment remain unfinished. A first live attempt reached only Cursor's workspace-trust refusal; see [evidence](#evidence-and-next-gates).

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

`doctor` resolves the executable, reads its version and says whether that is an [inspected version](#what-the-inspected-cli-exposes). It runs `agent status --format json` and reports a confirmed login only when Cursor returns both an authenticated status and an account identity; the identity is never displayed. Stored credentials without an identity are reported as unconfirmed. When `CURSOR_API_KEY` or `CURSOR_AUTH_TOKEN` is present in the loop or launching environment the status command is not run, because it describes the stored login rather than that credential. The model catalog, the configured model, quota, workspace trust and permissions are always shown as unavailable: the CLI offers no structured form of them. `doctor --offline` runs nothing.

## Turns, identity and controls

Each send launches `agent -p --output-format stream-json` through keepgoing's existing detached wrapper. Task text travels through private stdin, never command arguments. A configured model is passed explicitly. Later sends use only the saved session ID with `--resume=<id>`, never the latest conversation. A running turn cannot receive a second queued or steering prompt.

Completion requires a valid initialization, a matching terminal success record with an explicit false `is_error`, and a known zero process exit. This is **turn completion**, not task completion: the loop continues until an explicit control hold or `keepgoing complete` stops further wakes. Message and tool payloads never establish lifecycle or error state.

Initialized identity is preserved after a partial turn when the stream remains well formed. Conflicting IDs, duplicate top-level keys, invalid metadata and unavailable session storage refuse automatic resumption. A previously started turn without a verified session ID cannot silently start a new conversation, with one exception: a turn that finished with a known nonzero exit and wrote nothing at all to stdout began no conversation, so the next bounded re-prompt may start one. That is the shape of a refusal before startup, such as an untrusted workspace. A crashed wrapper, an unknown exit, any output, or a missing output file keeps the refusal. Output is limited to 8 MiB per finished/crashed turn and 2 MiB per event; exceeding either bound fails closed. The wrapper's raw output files are private and are not size-rotated by this parser limit.

Stop uses the existing [owned cancellation channel](operations.md#owned-headless-cancellation), not a saved PID. Pause, awaiting-user and completed holds prevent further sends; pause does not cancel a running turn. Cancellation of a real Cursor provider and its descendants has not been verified.

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
| Did a turn succeed? | One `result` record with `subtype: "success"`, `is_error: false` and the session ID, then exit 0. The source emits no other `result` shape. | Turn completion, with the matching initialization and a known zero exit. |
| Why did a turn fail? | Nothing structured. The source prints the error text to stderr and exits 1 (130 when interrupted). No error record, code or class. | Every failure is `unknown`. Stderr is never read. |
| Rate limit, quota, reset time? | Nothing structured, in the stream or in any command. | Unavailable. Never inferred. |
| Connection trouble inside a turn? | Unpublished `connection` records (`reconnecting` with an attempt number, `reconnected`) and `retry` records (`starting`, `resuming`) in `stream-json` output. | Diagnostic only; see [connection loss](#connection-loss-during-a-turn). |
| Workspace trust, permissions? | Nothing structured. An untrusted directory prints a notice to stderr and exits 1 with empty stdout (observed live). | Unavailable. The empty-output shape allows a later fresh start. |

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

Read-only local inspection of Cursor CLI `2026.09.18-9a7762b` on macOS confirmed the version/help interface. Isolated evaluation of its installed prompt-building function with fictional Latin/CJK input confirmed stdin-only UTF-8 input, internal newlines and trimming of boundary whitespace. No provider request or full chat startup was run. This is transport evidence for that installed version, not a supported-version range or live lifecycle proof.

Offline subprocess fixtures cover exact session reuse after reconstruction, independent targets, Unicode paths/stdin, active-send rejection, owned stop, missing output, nonzero and malformed results, ambiguity, output bounds and common holds. A combined command journey reloads config/control/state and reconstructs the adapter on every invocation: active pause/await-user/complete/disable holds, completion while held, stop precedence, exact resumption after stop, retry backoff and retry-budget exhaustion all use a native stand-in executable. A separate fixture kills its own retained wrapper process handle after initialization, waits for the disposable child to exit, then reconstructs and resumes the verified session. Those Go fixtures reconstruct command/adapter state; a separate [binary recovery smoke](release-verification.md#check-recovery-with-local-stand-ins), enabled with `--include-cursor`, starts actual temporary daemons, kills held supervisors and restarts them against the same state. It verifies persistent awaiting-user/completed holds, stale endpoint replacement, unknown-error history, unchanged ownership/send accounting and no alert/completion replay. Explicit resume then reuses the exact session UUID across successful stand-in turns. No service is installed and the provider remains fictional. Live two-turn resume, restart, permissions, cancellation and outage behavior are still unverified; see the [walkthrough](#live-walkthrough-c4). Setup fixtures also verify CLI discovery, existing default selection, disabled task/goal creation, private stdin/JSON previews, rejection before writes and no provider invocation. Native platform execution and release evidence must be recorded separately before recommending this path for unattended work.

## Live walkthrough (C4)

**Attempted once, not completed.** With the owner's authorization, a build of this branch was pointed at a new empty scratch directory with CLI `2026.09.18-9a7762b` on macOS 26.4 (arm64), a scratch state directory and `max_sends: 5`. The first turn exited 1 after writing nothing to stdout and a workspace-trust notice to stderr. No session was created and no model request was made. keepgoing recorded an `unknown` error and, after the fix described above, re-prompted within its bounds and failed the same way. The walkthrough stopped there: trusting the directory changes Cursor's stored state, which the authorization did not cover. The scratch directories were removed.

What that run verified: the pre-start failure shape (exit 1, empty stdout, stderr only), that keepgoing passes no trust flag, that the failure stays `unknown` with no handoff or probe, and that `doctor` reports the installed version and a confirmed login without printing the account. Everything below is still unverified.

Procedure for a completed run, once trusting the scratch directory is authorized:

1. Create a new empty directory outside every repository, and a separate scratch state directory. Never use the default configuration or state directories.
2. In the scratch directory, run `agent` once interactively, accept the workspace-trust question and exit without sending a prompt. Record that this was done; it is the only Cursor state the walkthrough changes.
3. Write a configuration with `adapter: cursor`, the scratch `workdir`, the cheapest listed model, a task file asking for one line to be created in or appended to a single file, and `limits: {max_sends: 5}`. Run `keepgoing validate` and `keepgoing doctor`.
4. Turn 1: `keepgoing enable`, then `keepgoing tick`. Wait for the turn to finish. Record the record types and keys of the stream, the exit code, and that a session ID was saved. Do not record the ID, the account or the text.
5. Turn 2: `keepgoing tick` again. Confirm from the private `run.json` that the command line carried `--resume=` with the saved ID and that the session ID in the new stream is the same.
6. Stop: `keepgoing tick` to start a third turn, then `keepgoing stop` and `keepgoing tick` while it runs. Record the wrapper's exit record, whether the CLI and its children exited, and what the stream contains.
7. Permission rejection: `keepgoing resume`, change the task file to ask for one shell command that Cursor's current settings do not allow without approval, and `keepgoing tick`. Record whether the turn ends in a success record, an error exit or neither, and that keepgoing neither approves nor classifies it. This depends on the account's own permission settings; record them as allowing or not allowing the command, nothing more.
8. `keepgoing stop`, remove both scratch directories, and write the results here and in the roadmap with the CLI version and platform. Stop early if any turn runs long or the send limit is reached.

