# Changelog

## Unreleased

- The daemon now checks control-file contents as well as metadata between passes, so same-size pause/stop edits with unchanged modification times request a fresh check. The private digest is never logged; existing ownership, holds and configuration checks still apply. Deterministic native fixtures cover in-place and atomic replacements, changes during polling, and unchanged-file waits.
- Add an internal Antigravity streaming-input encoder and conservative lifecycle parser with fictional offline fixtures. No adapter or setup option is registered; permission signaling, runtime integration and live verification remain open. See the [integration foundation](docs/antigravity.md).

## 0.1.5 (2026-11-12)

- CLI v0.1.5 / BB plugin v0.2.0 include version-specific notes and a repository plugin collection for explicit tagged installs. The package smoke check now covers prompt-only and goal-aware setup for all three supported agents, with zero provider calls. See [release verification](docs/release-verification.md) for publication, package and live-acceptance evidence.

- Repeated control writes advance the loaded timestamp even when the wall clock repeats or moves backwards. This prevents a repeated pause from leaving identical evidence for an older registered-resume ticket. Deterministic clock fixtures and the existing resume regressions cover the correction.

- The separately rebuilt BB plugin offers an explicit goal checkbox in setup, binds previews to that choice, and shows recorded goal identity/revision in setup and expanded status without private task text. Goal creation requires CLI v0.1.5; older builds must use the explicitly deselected task-only path. The preview now correctly distinguishes idle checks from stall-nudge spacing. SDK/host/DOM fixtures only; live deployment and first-loop acceptance remain open.

- Guided setup offers an explicit goal choice; scripted setup can use `init --with-goal`. It saves one private task snapshot referenced by the existing goal store and shares the current revision through stable wake instructions. Creation remains disabled; previews write nothing and ordinary output omits task text. Existing noninteractive callers keep prompt-only setup. Offline fixtures only; a live first-loop walkthrough remains unfinished.

- Ordinary status now shows the latest recorded automatic prompt attempt’s goal ID/revision, target alias, mode and adapter return result. Metadata comes from the same composition as the prompt and persists with completed ticks. Offline fixtures cover revisions, queued nudges, failures, holds and restart reads; delivery/execution, crash-complete history and source tracking remain unverified or unfinished.

- Diagnostic records retain BB's generic `thread_command_failed` code, while its decision classification stays `unknown`. Offline CLI fixtures cover unreadable status, interrupted turns, queued work, bounded re-prompts and preserved holds. This does not identify or restart a disconnected host; previously redacted records remain unavailable.

- Goal subcommand help (`create|revise|show --help` or `-h`) now exits successfully without loading configuration or changing state. Invalid arguments still return usage errors. v0.1.4 returns exit code 2 for this help; v0.1.5 includes the correction.

## 0.1.4 (2026-11-12)

- Headless stop now asks a private per-turn wrapper to terminate its own Unix group or Windows job instead of signalling a saved PID. Legacy turns without the key are refused. Native subprocess fixtures cover stale records and descendants; live provider cancellation and abrupt-death descendant cleanup remain unverified.

[v0.1.4 notes](docs/releases/v0.1.4.md) describe the changes and upgrade limits. Publication, package installation and live-provider evidence are tracked separately in [release verification](docs/release-verification.md).

- Incident history now retains numeric release versions with development commit-hash or numeric release-candidate suffixes. Arbitrary suffixes remain redacted; old unavailable build labels cannot be recovered. Offline fixtures only.

- Added opt-in `loop.prompt_goal` to send the latest private goal revision with automatic prompts and handoff briefs, instead of requiring copied goal text in wake policies. Missing or invalid goals refuse sends; existing loops remain unchanged. Offline fixtures only; semantic drift detection and guided goal setup remain planned.

- Added an optional read-only native macOS menu preview for explicitly registered local loops, using the shared CLI contract. It shows holds, timestamped activity, configured targets, optional private goal text and daemon evidence, without starting supervision or a wake timer. Developer build only; controls, installation, signing and live UI verification remain planned.

- Fixed known rate-limit cooldowns with `errors.failover_on: []`: ordinary checks continue without spending recovery retries or entering an automatic user hold before the reported reset. Earlier structured recovery is observed on the next check. Explicit holds and loop-wide bounds remain authoritative. Offline decision/supervisor fixtures only.

- Added `client prepare-resume` and `client resume`: a five-minute, single-use private ticket binds reviewed config/fallback sources, registration, caller/owner, controls, state and goal evidence. Changed evidence rejects resume; consuming before save prevents automatic replay after a crash or lost response. Preparation never clears holds; resume preserves disabled state, ownership, goals and allowances. Offline fixtures only; native UI/live verification remain pending.

- Added registered `client pause|stop` with binding/source checks, short registry/control locking, preserved holds/ownership and versioned save evidence. Diagnostic failures remain distinct from failed or unconfirmed saves. Offline fixtures only; native controls and installation remain planned.

- Added schema-1 read-only `client status` for registered loops: checked config/runtime bindings, explicit holds and declared completion, configured target, historical action/freshness, optional private goal display and bounded daemon evidence. Unknown/corrupt evidence and changed bindings remain explicit. Offline fixtures and a local authenticated daemon test only; the native companion remains planned.

- Added explicit local loop discovery through `registry add|list|remove`, with private versioned references, stable IDs, binding-conflict checks, bounded storage and independent locking. Registration never changes loop controls or runs providers. Offline fixtures only; the client status interface is described above; the menu-bar app is a separate read-only developer preview.

- Expanded the BB banner/card status with declared completion, actual target thread, timestamped daemon/check evidence, and historical action outcomes. Failed refreshes show a warning and hide controls/setup until refreshed. External schedulers remain uninspected. SDK/DOM fixtures, typechecking and bundle builds only; no live deployment.

- Added the BB plugin’s "Move supervision here" flow for plugin-created loops in the same environment/provider: preview source/destination and holds, explicitly apply a held move, and reconcile or recover interrupted requests after reload. Registered-host routing and durable private references keep paths/commands out of browser control; resume/start remain separate. SDK/controller/DOM fixtures and bundle builds only; no live transfer or deployment.

- Added explicit same-environment BB `transfer apply` and roll-forward `transfer recover`, with a durable pending fence, preserved holds/goals/allowances, target-specific observation reset, atomic config replacement and a permanent target binding against stale config copies. Moves stay awaiting-user and advance owner generation. Fixture-tested, including a real subprocess exit with fake BB; no live transfer or model/account verification is claimed.

- Added read-only `transfer preview` for a single BB primary: versioned JSON/text compare thread/provider/environment metadata, display redacted holds/ownership/goal identity and flag unavailable or incompatible inputs. Offline mode skips BB queries. Fixture-tested only; model/account identity remains unverified; the plugin flow is described above.

- `goal create|revise|show` records private goal identity and specification revisions independently of loop controls. Expected ID/revision checks prevent stale edits; ordinary status withholds titles/references, with explicit `show --private` for full history. Offline fixtures cover ownership, locks, holds, corruption, limits and privacy. Goal acceptance, verification and review-stage bindings remain unimplemented.

## 0.1.3 (2026-11-11)

Reliability release collecting the changes after v0.1.2. Publication, package installation and live-provider evidence are tracked separately in [release verification](docs/release-verification.md#v013-reliability-release-evidence). See the [release notes](docs/releases/v0.1.3.md) for the user-facing changes and upgrade limits.

- Native macOS/Linux CI and the unpacked Linux release gate now run a recovery smoke check with stand-in BB/process/Claude/Codex providers, rejected local notifications, held daemon restarts, explicit recovery and completion without replay. This adds offline artifact evidence, not live provider or deployment certification.

- Added `timeline`, an offline text/JSON view combining bounded incident/delivery records with redacted current control flags. It exposes local timestamp/source evidence, historical hold and activity summaries, failed alerts, retention and evidence gaps without provider/scheduler/daemon/CI calls or writes. Missing/corrupt sources remain explicit; `--state-dir` supports broken configs and copied records. Acceptance is fixture-only; publication does not install the code or establish live verification.

- Notification delivery evidence now persists in a bounded private ledger. Status and doctor show historical failed/unconfirmed reports across held checks, with attempt intent, numeric HTTP outcomes and explicitly unverified receipt. Diagnostic failures preserve holds and original transport behavior; completion remains single-attempt without redirects or replay. Local webhook/transport fixtures only; not installed or live-verified.

- Combined incident-history acceptance now passes through CLI fixtures for BB, process, Claude and Codex: failure, bounded recovery attempts, hold, explicit resume and recovered observations are reconstructed from an isolated journal alone. Headless stand-ins run as real local processes; no live provider or installed loop was used. Unsupported source timing/retry evidence stays unavailable.

- Incident schema 4 records saved control changes from standalone commands, startup and daemon takeover, with locked before/after gate snapshots and no private notes or owner IDs. History failure preserves the save and existing report processing while returning an explicit error; startup stops before running a daemon if its control history fails. Legacy records upgrade as ticks without fabricated transitions. Offline fixtures only.

- Incident journal schema 3 preserves ordered decision-engine inputs and proposed actions, including early bounds and re-evaluation after failed fallback probes. Captured empty histories distinguish skipped engine calls from unavailable legacy evidence. Snapshots exclude private child IDs and prose, preserve signed timing values, and upgrade versions 1/2 on append. Offline fixtures only.

- Incident journal schema 2 adds diagnostic source labels, recognized provider error/category tokens and available numeric HTTP/exit statuses across BB, process and headless adapters. Version-1 history upgrades without backfilling evidence. Unknown strings are discarded, source event time remains unavailable, and diagnostic metadata never changes decision policy. Offline fixtures only.

- Ticks now write a separate bounded, versioned private incident history with normalized observations, retry availability, decision causes, hold snapshots and build metadata. Retention is capped at 256 records/512 KiB. Journal failure surfaces an error after actions without undoing controls. Verification is offline only; delivery receipt and live recovery certification remain unavailable.

- BB active provider retries now suppress stall nudges and false nudge-cap holds. Queued input does not imply recovery; matching structured provider activity does. An observed retry followed by active recovery gets one ordinary stall interval without resetting spent nudges, Git evidence, holds or loop-wide limits. Incomplete retry scans suppress prompts and target moves. Offline fixtures cover recovery, restart and legacy cursor migration; live deployment is not yet verified.

### Added

- Subscribed `complete` reports now describe a new explicit completion declaration without private notes or claims of independently verified success. The persisted completion flag deduplicates commands and restarts; explicit resume permits a later declaration. Completion delivery uses one attempt without retries/redirects, with notification-loss limitations documented. Covered by offline fixtures only.

- Optional `loop.limits.no_progress_timeout` / `init --no-progress-timeout` holds future automatic work when verified Git metadata stays unchanged for the configured elapsed time. A separate persisted baseline survives turns, recovery, handoffs and restart; resume does not reset it. Setup/status expose the limit and historical evidence. Covered by offline fixtures; this does not measure useful work or spend.

- Optional `loop.limits.max_sends` / `init --max-sends` caps automatic send attempts across wakes, recovery and target changes. Reservations persist before delivery, including failed/uncertain sends; exhaustion holds future automatic work. Status exposes the allowance, and explicit resume does not reset it. Covered by offline fixtures only; monetary bounds remain unimplemented.

- Optional `loop.limits.deadline` and `init --deadline` hold future automatic supervision at the next check at/after an RFC3339 cutoff. The awaiting-user hold persists across restarts and is visible in status; stop and existing holds retain precedence. Child-outcome polls skip providers after the cutoff. This does not cancel running turns, cap spend, or restrict explicit manual operations. Covered by offline fixtures, not live providers.

### Fixed

- macOS archive builds omit AppleDouble metadata files so local filesystem attributes are not packaged alongside the executable and documentation.

- Git progress fingerprinting now treats inaccessible tracked-file metadata as unknown rather than a deletion. A deterministic symlink-cycle fixture verifies that metadata errors cannot replenish the no-progress timer.

- Setup now describes `--wake-every` as stall-nudge spacing; idle wakes follow the check cadence. No idle scheduling behavior changed, and per-turn nudge counts remain distinct from loop-wide work limits.

## 0.1.2 (2026-11-10)

Maintenance release collecting the changes since v0.1.1. See [release verification](docs/release-verification.md) for artifact checks and outstanding live walkthroughs. Unless explicitly recorded in [integrations](docs/integrations.md), provider and service behavior is fixture-tested rather than live-verified.

### Added

- Guided `init` creates a private disabled loop outside Git checkouts, with single-agent defaults, existing headless account directories, ordered fallbacks, operating limits and generated control instructions. Noninteractive flags, versioned JSON preview/create output and bounded stdin task input support integrations without placing task text in arguments.
- `complete --note` persists an explicit completion hold across restarts. Enable does not clear it; explicit resume reopens the task. Completion is a declaration, never inferred from transcripts, and does not yet send a completion report event.
- `doctor` provides read-only text/JSON diagnostics for setup, executable discovery, structured BB identity, headless login status, saved resume metadata, runtime access and ACL metadata on macOS/Linux/Windows. Unsupported checks stay unavailable; ordinary checks make no model calls and `--offline` skips external CLI queries. Full ACL privacy evaluation and live headless session identity remain unverified.
- Status includes configured target/account-group metadata, control holds, recorded decisions/outcomes and history freshness. Built-in daemon diagnostics use a private local challenge for responsiveness, current phase, next normal check and build-label comparison. Explicit BB automation diagnostics show structured registration and run metadata without changing timers; they do not prove scheduler liveness or script-to-loop binding.
- BB child awareness suppresses wakes, recovery and target moves while a verified direct child is active, and waits conservatively when discovery cannot be verified. The daemon checks structured child outcome notices ahead of its normal interval with persisted deduplication. Verified pending child input creates an awaiting-user hold and identifies the child in a report without prompting the parent or answering the child.
- Persistent BB ownership records and explicit `--take-over` fence conflicting owners and stale daemons. Claims remain reserved when an owner's status is unknown or idle. Identity-free schedulers preserve ownership; restrictive controls remain available. Ownership takeover does not transfer a supervised thread or create threads.
- `start` validates local setup and launches foreground or acknowledged detached supervision, preserving existing holds and refusing duplicate daemons. It does not install a service or detect/replace external timers.
- `service install|status|remove` manages macOS LaunchAgents and Linux systemd user units with private recovery records, exact-definition checks, ownership protections and unchanged loop controls. Status reports registration, health and on-disk executable comparisons; Linux adds structured execution details and daemon binding. macOS identity/startup/exit diagnostics are explicitly unsupported. Live installed-service lifecycle verification and Windows service implementation remain outstanding.
- The BB plugin's `Supervise this thread` flow separates preview, disabled creation, diagnostics and explicit background start, using the shared CLI setup contract. Covered by SDK/controller/DOM fixtures; the plugin is built and installed separately from the core binary and its live setup walkthrough remains outstanding.
- Headless `claude.env` and `codex.env` select existing account environments consistently for fresh/resumed turns, probes and Codex limit snapshots. The three-subscription example uses separate quota groups; real multi-account handoff remains unverified.
- Manual probe previews (`fallbacks check --dry-run`) describe candidates, models, timeouts and invocation counts without calling providers or changing runtime state. Expected dollar cost remains unknown; manual checks are outside the scheduled daily cap.
- Codex usage limits are classified as `rate_limit` with a reset time. `codex exec --json` reports a limit only as prose, so when a Codex probe or headless turn fails without a recognized code, keepgoing reads the Codex app-server's structured `account/rateLimits/read` snapshot.

- Headless Claude Code and Codex runners on Windows. Each turn runs through a detached copy of the keepgoing binary acting as the wrapper. Stop ends the turn's process tree with `taskkill`.

- `switch_back`, off by default, returns the loop to its primary between the fallback's turns. It waits until the primary's reported reset time, or `min_after` when none was reported, and until the primary passes an exact probe. It sends a `switch_back` report. `loop.probe` sets how the primary is probed.
- A failed probe saves its raw output to `probe-failures/<candidate>/` in the state directory, keeping the newest five, so unrecognised error shapes such as a provider's usage-limit event can be mapped later. It is never read for a decision.
- A native-binary smoke check for macOS/Linux verifies guided setup, offline diagnostics, persisted holds/completion and owner conflicts using fictional loops with provider invocation traps. The release guide separates this evidence from real two-turn/resume, account handoff and installed-service acceptance.
- Publication verifies all archive checksums and runs that smoke check against the unpacked Linux amd64 archive before uploading release assets.

### Fixed

- Loaded configuration and fallback sources are checked under the tick lock before provider work. Stale, missing or unreadable snapshots withhold ticks, probes, retargeting and provider stop execution until reload/repair; diagnostic status remains available. External writers that ignore the lock and changes to referenced files are outside this guard.
- Child-input precedence fixtures no longer require a real subprocess inspection chain to finish within a 500 ms poll under race instrumentation. Separate timing and cancellation tests retain coverage without changing production deadlines or at-most-once event consumption.
- Installation instructions now distinguish v0.1.1 artifacts and matching examples from the v0.1.2 quick start, with an explicit revision-labeled development build path.

## 0.1.1 (2026-11-10)

### Added

- `keepgoing status --json` includes `primaryAgent` and `targetAgent`, with the adapter, provider, model, and BB thread or working directory, so tools can match a loop to the session it supervises.
- A thin BB plugin in `integrations/bb-plugin-keepgoing`. It shows keepgoing's state in a supervised thread's context banner or context card, with Pause and Resume, and adds `bb keepgoing status`, `pause`, `resume`, and `stop`.

## 0.1.0 (2026-11-10)

### Fixed

- The BB adapter read the oldest 60 log events instead of the newest, because `bb thread log --limit` returns events oldest first. On a long thread it missed current errors, rate limits, and interactions. It now keeps a cursor and reads only new events after a one-time full pass.
- A stop request is now honored while the loop is paused, disabled, or awaiting a person, and when state files cannot be read.
- The daemon now acts on a stop or pause written during a pass, instead of waiting a full interval.
- Relative `state_dir` and command paths now resolve against the config file, so headless runners and `./script.sh` commands work from any directory.
- `retarget` now applies the target agent's model and reasoning level.
- A handoff brief that cannot be written no longer leaves a candidate's model switched.
- A failed manual or exact probe no longer delays the next scheduled probe. The active target's quota group is no longer probed on schedule.
- A gone target now marks the loop awaiting user instead of staying silent.
- Time zones now load on Windows, and the network probe command works there.

### Added

- BB threads with a pending permission prompt, question, or form count as waiting on a person. keepgoing sends nothing to them and reports `await_user` once.
- `keepgoing await-user` sends an `await_user` report at once, with the note.
- The process adapter's status object accepts `awaitingInput`.
- `progress.max_nudges`, default 3: a session that stays stalled through that many nudges marks the loop awaiting user instead of being nudged forever.
- `docs/agent-chats.md` and `examples/chat-loop-prompt.md` for supervising a chat thread.
- `probe.env` for probing a candidate that uses a different account.
- `docs/integrations.md` and `docs/testing.md`, and a test bundle for machines without Go.
- Supervision of one loop through BB threads, generic processes, and headless Claude Code or Codex sessions.
- A decision engine that wakes idle sessions, nudges stalled ones, and re-prompts errors with backoff, then waits for a person.
- `fallbacks.yaml` with scheduled, exact, and manual probes, a health registry, redundancy rules, and handoff to confirmed fallbacks.
- Structured BB limit detection from provider-error and rate-limit events, including waiting out a reset that is near.
- The daemon mode, the control commands, `retarget`, and `status`.
- Signed webhook reports in Standard Webhooks or bearer format.
- The `ci-wait` and `net-retry` helpers.
- JSON Schemas, examples, and scheduler setups.
