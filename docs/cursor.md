# Cursor CLI supervision

The Cursor adapter is **unreleased source**, with offline fixture coverage. Published v0.1.5 does not contain it. Guided `init` selection is implemented in source; a supported-version range, live provider acceptance, fallback probes, automatic limit/reset recovery and existing Cursor IDE chat attachment remain unfinished.

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

The `cursor` block accepts `cli` (default `agent`), required `workdir`, optional `model`, and optional `env` overrides. Paths follow the normal [configuration rules](configuration.md). Credentials can come from the existing Cursor login or inherited `CURSOR_API_KEY`; keep secrets outside the repository and prefer the launching environment over storing credentials in YAML. No login, account creation, credential migration or independent account-directory scheme is performed. Doctor resolves the executable and may read its version; account identity, login health and quota state remain unavailable.

## Turns, identity and controls

Each send launches `agent -p --output-format stream-json` through keepgoing's existing detached wrapper. Task text travels through private stdin, never command arguments. A configured model is passed explicitly. Later sends use only the saved session ID with `--resume=<id>`, never the latest conversation. A running turn cannot receive a second queued or steering prompt.

Completion requires a valid initialization, a matching terminal success record with an explicit false `is_error`, and a known zero process exit. This is **turn completion**, not task completion: the loop continues until an explicit control hold or `keepgoing complete` stops further wakes. Message and tool payloads never establish lifecycle or error state.

Initialized identity is preserved after a partial turn when the stream remains well formed. Conflicting IDs, duplicate top-level keys, invalid metadata and unavailable session storage refuse automatic resumption. A previously started turn without a verified session ID cannot silently start a new conversation. Output is limited to 8 MiB per finished/crashed turn and 2 MiB per event; exceeding either bound fails closed. The wrapper's raw output files are private and are not size-rotated by this parser limit.

Stop uses the existing [owned cancellation channel](operations.md#owned-headless-cancellation), not a saved PID. Pause, awaiting-user and completed holds prevent further sends; pause does not cancel a running turn. Cancellation of a real Cursor provider and its descendants has not been verified.

## Permissions and unsupported recovery

keepgoing never adds `--force`, `--yolo`, `--approve-mcps`, or `--trust`. Cursor's existing workspace trust, permissions and approval configuration still apply. Print mode can edit permitted workspace files and run permitted commands; it rejects actions that require interactive approval. Existing unrestricted settings can allow more actions. This adapter does not offer an interactive permission bridge or infer a permission wait from response prose. See [Cursor's headless permissions](https://cursor.com/docs/cli/headless).

Cursor documents failures that emit only a nonzero exit and stderr, potentially without terminal JSON. All failures therefore remain `unknown`; quota, reset, authentication and network text are not classified. Unknown errors use keepgoing's bounded retry policy and never trigger failover. A Cursor loop cannot configure fallbacks/switch-back, and Cursor is rejected as a fallback candidate until exact account/model probes have a verified contract. See [Cursor's output schema](https://cursor.com/docs/cli/reference/output-format).

## Evidence and next gates

Official documentation provides [print/resume/model parameters](https://cursor.com/docs/cli/reference/parameters) and a separate [ACP interface](https://cursor.com/docs/cli/acp). ACP provides structured permissions and cancellation for custom clients; it is not implemented here and does not prove attachment to existing IDE chats.

Read-only local inspection of Cursor CLI `2026.09.18-9a7762b` on macOS confirmed the version/help interface. Isolated evaluation of its installed prompt-building function with fictional Latin/CJK input confirmed stdin-only UTF-8 input, internal newlines and trimming of boundary whitespace. No provider request or full chat startup was run. This is transport evidence for that installed version, not a supported-version range or live lifecycle proof.

Offline subprocess fixtures cover exact session reuse after reconstruction, independent targets, Unicode paths/stdin, active-send rejection, owned stop, missing output, nonzero and malformed results, ambiguity, output bounds and common holds. A combined command journey reloads config/control/state and reconstructs the adapter on every invocation: active pause/await-user/complete/disable holds, completion while held, stop precedence, exact resumption after stop, retry backoff and retry-budget exhaustion all use a native stand-in executable. A separate fixture kills its own retained wrapper process handle after initialization, waits for the disposable child to exit, then reconstructs and resumes the verified session. Neither fixture restarts an actual daemon process or service. Live two-turn resume, restart, permissions, cancellation and outage behavior still need an authorized scratch walkthrough. Setup fixtures also verify CLI discovery, existing default selection, disabled task/goal creation, private stdin/JSON previews, rejection before writes and no provider invocation. Native platform execution and release evidence must be recorded separately before recommending this path for unattended work.
